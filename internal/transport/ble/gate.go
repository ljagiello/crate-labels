package ble

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// connectGrace is how long connect waits past the backend's own timeout before it gives
// up on an attempt that has not returned.
const connectGrace = 5 * time.Second

// gate allows one connection per address, from the start of Connect until the backend
// confirms the disconnect (Peripheral.Done closes). CoreBluetooth tracks one pending
// connect per peripheral, so two overlapping connections to one printer would cancel or
// strand each other.
type gate struct {
	mu   sync.Mutex
	held map[string]<-chan struct{} // address → closed when the address is free again
}

func newGate() *gate { return &gate{held: map[string]<-chan struct{}{}} }

// claim is one connection's hold on an address.
type claim struct {
	g       *gate
	address string
	pending chan struct{} // the address's "free" channel while the connect is in flight
	once    sync.Once
}

// acquire waits until address is free and takes it, or returns ctx's error.
func (g *gate) acquire(ctx context.Context, address string) (*claim, error) {
	for {
		g.mu.Lock()
		busy, taken := g.held[address]
		if taken {
			select {
			case <-busy:
				taken = false
			default:
			}
		}
		if !taken {
			c := &claim{g: g, address: address, pending: make(chan struct{})}
			g.held[address] = c.pending
			g.mu.Unlock()
			return c, nil
		}
		g.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// holdUntil keeps the address taken until done closes: the confirmed disconnect.
func (c *claim) holdUntil(done <-chan struct{}) {
	c.g.mu.Lock()
	c.g.held[c.address] = done
	c.g.mu.Unlock()
	c.once.Do(func() { close(c.pending) }) // waiters wake, see done, and wait on it
}

// release frees the address at once: the attempt failed and nothing is connected.
func (c *claim) release() {
	var pending <-chan struct{} = c.pending
	c.g.mu.Lock()
	if c.g.held[c.address] == pending {
		delete(c.g.held, c.address)
	}
	c.g.mu.Unlock()
	c.once.Do(func() { close(c.pending) })
}

type connectResult struct {
	p   Peripheral
	err error
}

// connect takes the address's gate, then connects within [ConnectTimeout] or ctx's
// deadline, whichever is sooner. The gate stays taken until the returned peripheral's
// disconnect is confirmed.
//
// An attempt abandoned because ctx ended keeps the gate until the backend returns. If
// it connected after all, it is disconnected, and only that confirmation frees the
// address. So an abandoned attempt can never cancel a later connection, and no goroutine
// outlives the backend call.
func connect(ctx context.Context, in *installed, address string) (Peripheral, error) {
	timeout := ConnectTimeout
	if dl, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(dl))
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	c, err := in.gate.acquire(wctx, NormalizeAddress(address))
	cancel()
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		return nil, printer.Errorf("The printer is busy with another Bluetooth connection from crate-labels (a print or a probe). Try again when it has finished.")
	}
	if dl, ok := ctx.Deadline(); ok {
		timeout = min(ConnectTimeout, time.Until(dl))
	}
	if timeout <= 0 {
		c.release()
		return nil, connectError(ctx, address, timeout, context.DeadlineExceeded)
	}
	ch := make(chan connectResult, 1)
	go func() {
		p, err := in.b.Connect(ctx, address, timeout)
		ch <- connectResult{p, err}
	}()
	timer := time.NewTimer(timeout + connectGrace)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			c.release()
			return nil, connectError(ctx, address, timeout, r.err)
		}
		c.holdUntil(r.p.Done())
		return r.p, nil
	case <-ctx.Done():
		abandon(c, ch)
		return nil, connectError(ctx, address, timeout, ctx.Err())
	case <-timer.C:
		abandon(c, ch)
		return nil, connectError(ctx, address, timeout, errors.New("the Bluetooth stack did not answer"))
	}
}

// abandon waits in the background for an attempt nobody is waiting for, and cleans up
// after it. The gate stays taken until then.
func abandon(c *claim, ch <-chan connectResult) {
	go func() {
		r := <-ch
		if r.err != nil {
			c.release()
			return
		}
		c.holdUntil(r.p.Done())
		if err := r.p.Disconnect(); err != nil {
			slog.Warn("ble: could not drop an abandoned connection", "address", c.address, "err", err)
		}
	}()
}

// connectError is ctx.Err() when the caller cancelled, else a user-facing message.
func connectError(ctx context.Context, address string, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	var pe *printer.Error
	if errors.As(err, &pe) {
		return err
	}
	slog.Debug("ble connect failed", "address", address, "err", err)
	return printer.Errorf("No answer from the printer after %s. Check that it is on and in range, and that no phone or other computer is connected to it.", fmtSeconds(timeout))
}
