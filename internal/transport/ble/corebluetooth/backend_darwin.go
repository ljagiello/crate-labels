//go:build darwin

package corebluetooth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	bt "tinygo.org/x/bluetooth"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/ble"
)

func init() {
	ble.SetBackend(&backend{adapter: bt.DefaultAdapter, names: map[string]string{}, conns: map[string]*peripheral{}})
}

// backend adapts tinygo's darwin adapter to ble.Backend.
type backend struct {
	adapter *bt.Adapter

	enableMu sync.Mutex
	enabled  bool

	scanMu sync.Mutex // tinygo runs one scan at a time

	mu    sync.Mutex
	names map[string]string      // address → last non-empty advertised name
	conns map[string]*peripheral // address → live connection, for disconnect events
}

// enable powers up the CoreBluetooth central on first use, never at init.
func (b *backend) enable() error {
	b.enableMu.Lock()
	defer b.enableMu.Unlock()
	if b.enabled {
		return nil
	}
	b.adapter.SetConnectionStateHandler(b.onConnectionState)
	if err := b.adapter.Enable(); err != nil {
		return printer.Errorf("Bluetooth is not available (%v). Is it switched on, and does crate-labels have Bluetooth permission (System Settings > Privacy & Security > Bluetooth)?", err)
	}
	b.enabled = true
	return nil
}

// onConnectionState runs on CoreBluetooth's queue. The handler is adapter-wide, so
// disconnects are dispatched by address.
func (b *backend) onConnectionState(d bt.Device, s bt.ConnectionState) {
	if s.Connected {
		return
	}
	addr := ble.NormalizeAddress(d.Address.String())
	b.mu.Lock()
	p := b.conns[addr]
	delete(b.conns, addr)
	b.mu.Unlock()
	if p != nil {
		p.markDone()
	}
}

func (b *backend) Scan(ctx context.Context, found func(ble.Advertisement)) error {
	if err := b.enable(); err != nil {
		return err
	}
	b.scanMu.Lock()
	defer b.scanMu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	errc := make(chan error, 1)
	go func() {
		errc <- b.adapter.Scan(func(_ *bt.Adapter, r bt.ScanResult) {
			adv := ble.Advertisement{Address: ble.NormalizeAddress(r.Address.String()), Name: r.LocalName(), RSSI: int(r.RSSI)}
			for _, u := range r.ServiceUUIDs() {
				adv.ServiceUUIDs = append(adv.ServiceUUIDs, u.String())
			}
			if adv.Name != "" {
				b.mu.Lock()
				b.names[adv.Address] = adv.Name
				b.mu.Unlock()
			}
			found(adv)
		})
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// StopScan fails until Scan has registered itself; retry until it takes.
	for b.adapter.StopScan() != nil {
		select {
		case err := <-errc:
			return err
		case <-time.After(10 * time.Millisecond):
		}
	}
	return <-errc
}

func (b *backend) Connect(_ context.Context, address string, timeout time.Duration) (ble.Peripheral, error) {
	if err := b.enable(); err != nil {
		return nil, err
	}
	uuid, err := bt.ParseUUID(ble.NormalizeAddress(address))
	if err != nil {
		return nil, printer.Errorf("%q is not a Bluetooth device address. Scan for the printer and save it again.", address)
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	timeout = min(timeout, 40*time.Second) // ConnectionParams holds at most ~40.9 s
	var addr bt.Address
	addr.UUID = uuid
	// ble holds this address until the disconnect is confirmed, so tinygo never sees two
	// connects to one peripheral; it also handles ctx and abandons a slow attempt.
	dev, err := b.adapter.Connect(addr, bt.ConnectionParams{ConnectionTimeout: bt.NewDuration(timeout)})
	if err != nil {
		return nil, err
	}
	p := &peripheral{b: b, address: ble.NormalizeAddress(uuid.String()), dev: dev, done: make(chan struct{})}
	b.mu.Lock()
	b.conns[p.address] = p
	b.mu.Unlock()
	return p, nil
}

// disconnectWait bounds how long Disconnect waits for the OS to confirm.
const disconnectWait = 5 * time.Second

// peripheral is one live connection. Done closes only on a confirmed disconnect.
type peripheral struct {
	b       *backend
	address string
	dev     bt.Device

	mu       sync.Mutex
	services []bt.DeviceService

	done     chan struct{}
	doneOnce sync.Once
}

func (p *peripheral) markDone() { p.doneOnce.Do(func() { close(p.done) }) }

func (p *peripheral) Services(context.Context) ([]string, error) {
	svcs, err := p.dev.DiscoverServices(nil)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.services = svcs
	p.mu.Unlock()
	out := make([]string, len(svcs))
	for i, s := range svcs {
		out[i] = s.UUID().String()
	}
	return out, nil
}

func (p *peripheral) Characteristics(ctx context.Context, service string) ([]ble.Characteristic, error) {
	p.mu.Lock()
	svcs := p.services
	p.mu.Unlock()
	if svcs == nil {
		if _, err := p.Services(ctx); err != nil {
			return nil, err
		}
		p.mu.Lock()
		svcs = p.services
		p.mu.Unlock()
	}
	for _, s := range svcs {
		if strings.EqualFold(s.UUID().String(), service) {
			chars, err := s.DiscoverCharacteristics(nil)
			if err != nil {
				return nil, err
			}
			out := make([]ble.Characteristic, len(chars))
			for i, c := range chars {
				out[i] = characteristic{c}
			}
			return out, nil
		}
	}
	return nil, bt.ErrServiceNotFound
}

func (p *peripheral) Name() string {
	p.b.mu.Lock()
	defer p.b.mu.Unlock()
	return p.b.names[p.address]
}

func (p *peripheral) Done() <-chan struct{} { return p.done }

// Disconnect cancels the connection and waits up to [disconnectWait] for CoreBluetooth's
// disconnect callback, which closes Done. If the callback is late but the peripheral
// already reports itself disconnected, that state counts as the confirmation.
// Otherwise Done stays open, and ble keeps the address reserved until the callback.
func (p *peripheral) Disconnect() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.dev.Disconnect(); err != nil {
		return err
	}
	timer := time.NewTimer(disconnectWait)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	}
	if connected, _ := p.dev.Connected(); !connected {
		p.b.mu.Lock()
		if p.b.conns[p.address] == p {
			delete(p.b.conns, p.address)
		}
		p.b.mu.Unlock()
		p.markDone()
		return nil
	}
	return errors.New("the printer did not confirm the disconnect")
}

// characteristic adapts a tinygo characteristic to ble.Characteristic.
type characteristic struct{ c bt.DeviceCharacteristic }

func (c characteristic) UUID() string { return c.c.UUID().String() }

func (c characteristic) Write(data []byte, withResponse bool) error {
	var err error
	if withResponse {
		_, err = c.c.Write(data)
	} else {
		_, err = c.c.WriteWithoutResponse(data)
	}
	return err
}

func (c characteristic) Subscribe(fn func([]byte)) error {
	if fn == nil {
		return c.c.EnableNotifications(nil)
	}
	return c.c.EnableNotifications(func(buf []byte) { fn(bytes.Clone(buf)) })
}

func (c characteristic) MaxWrite() int {
	n, err := c.c.GetMTU()
	if err != nil {
		return 0
	}
	return int(n)
}
