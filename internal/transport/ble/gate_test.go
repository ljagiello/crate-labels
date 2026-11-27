package ble

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// dialer hands out a fresh Phomemo peripheral per connect and remembers them in order.
type dialer struct {
	mu   sync.Mutex
	made []*fakePeripheral
	hold bool // peripherals wait for drop() to confirm a disconnect
}

func (d *dialer) dial(string) *fakePeripheral {
	p, _, _ := phomemoDevice("M110")
	p.holdDisconnect = d.hold
	d.mu.Lock()
	defer d.mu.Unlock()
	d.made = append(d.made, p)
	return p
}

func (d *dialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.made)
}

func (d *dialer) nth(i int) *fakePeripheral {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.made[i]
}

type opened struct {
	l   *Link
	err error
}

func openAsync(ctx context.Context, t *testing.T, address string) <-chan opened {
	profile := phomemoProfile(t)
	ch := make(chan opened, 1)
	go func() {
		l, err := Open(ctx, address, profile)
		ch <- opened{l, err}
	}()
	return ch
}

func expectWaiting(t *testing.T, ch <-chan opened, what string) {
	t.Helper()
	select {
	case o := <-ch:
		t.Fatalf("%s did not wait: %+v", what, o)
	case <-time.After(60 * time.Millisecond):
	}
}

func expectOpened(t *testing.T, ch <-chan opened) *Link {
	t.Helper()
	select {
	case o := <-ch:
		if o.err != nil {
			t.Fatal(o.err)
		}
		return o.l
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting connection never opened")
	}
	return nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConnectionsToOneAddressAreSerialised(t *testing.T) {
	d := &dialer{}
	b := &fakeBackend{dial: d.dial}
	install(t, b)
	ctx := context.Background()
	first, err := Open(ctx, "dev", phomemoProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	second := openAsync(ctx, t, "dev")
	expectWaiting(t, second, "a second connection to the same printer")
	if n := b.connects.Load(); n != 1 {
		t.Fatalf("%d backend connects while the first link is open, want 1", n)
	}
	other, err := Open(ctx, "other", phomemoProfile(t)) // another printer is not held up
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	first.Close()
	l := expectOpened(t, second)
	defer l.Close()
	if d.nth(0).disconnectCount() != 1 || l.periph != d.nth(2) || !l.Connected() {
		t.Error("the second link must get its own connection after the first closed")
	}
}

func TestGateWaitsForTheConfirmedDisconnect(t *testing.T) {
	d := &dialer{hold: true}
	install(t, &fakeBackend{dial: d.dial})
	ctx := context.Background()
	first, err := Open(ctx, "dev", phomemoProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	first.Close() // asked to disconnect, not yet confirmed
	second := openAsync(ctx, t, "dev")
	expectWaiting(t, second, "a connection before the disconnect was confirmed")
	d.nth(0).drop() // the OS confirms
	expectOpened(t, second).Close()
}

func TestBusyAddressGivesUpWithAClearError(t *testing.T) {
	d := &dialer{}
	install(t, &fakeBackend{dial: d.dial})
	held, err := Open(context.Background(), "dev", phomemoProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = Probe(ctx, "dev", catalog(t))
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "busy with another Bluetooth connection") {
		t.Errorf("err = %v", err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := Open(cctx, "dev", phomemoProfile(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait err = %v, want context.Canceled", err)
	}
}

func TestAbandonedConnectNeverCancelsALaterOne(t *testing.T) {
	baseline := runtime.NumGoroutine()
	d := &dialer{}
	block := make(chan struct{})
	b := &fakeBackend{dial: d.dial, block: block}
	install(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	abandoned := openAsync(ctx, t, "dev")
	waitFor(t, func() bool { return b.connects.Load() == 1 })
	cancel()
	if o := <-abandoned; !errors.Is(o.err, context.Canceled) {
		t.Fatalf("abandoned open = %+v, want context.Canceled", o)
	}

	later := openAsync(context.Background(), t, "dev")
	expectWaiting(t, later, "a connection while an abandoned attempt is in flight")
	if n := b.connects.Load(); n != 1 {
		t.Fatalf("%d backend connects, want the later one to wait for the abandoned one", n)
	}
	close(block) // the abandoned attempt connects after all
	l := expectOpened(t, later)
	if d.nth(0).disconnectCount() != 1 {
		t.Error("the late connection of the abandoned attempt was not dropped")
	}
	if d.nth(1).disconnectCount() != 0 || l.periph != d.nth(1) || !l.Connected() {
		t.Error("the later connection was disturbed by the abandoned one")
	}
	l.Close()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= baseline })
}

func TestHungBackendKeepsTheAddressUntilItReturns(t *testing.T) {
	d := &dialer{}
	block := make(chan struct{})
	install(t, &fakeBackend{dial: d.dial, block: block})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := Open(ctx, "dev", phomemoProfile(t))
	if err == nil || !strings.Contains(err.Error(), "No answer from the printer") {
		t.Fatalf("err = %v, want the unreachable message", err)
	}
	bctx, bcancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer bcancel()
	if _, err := Open(bctx, "dev", phomemoProfile(t)); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err = %v, want busy while the first attempt is still in the backend", err)
	}
	close(block)
	waitFor(t, func() bool { return d.count() == 1 && d.nth(0).disconnectCount() == 1 })
	l, err := Open(context.Background(), "dev", phomemoProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestFailedConnectFreesTheAddressAtOnce(t *testing.T) {
	b := &fakeBackend{connectErr: errors.New("timeout on Connect")}
	install(t, b)
	for range 2 {
		if _, err := Open(context.Background(), "dev", phomemoProfile(t)); err == nil || !strings.Contains(err.Error(), "No answer from the printer") {
			t.Fatalf("err = %v", err)
		}
	}
	if b.connects.Load() != 2 {
		t.Errorf("connects = %d", b.connects.Load())
	}
}

func TestGateKeysOnTheNormalizedAddress(t *testing.T) {
	d := &dialer{}
	b := &fakeBackend{dial: d.dial}
	install(t, b)
	const addr = "AAAA0000-1111-2222-3333-444455556666"
	first, err := Open(context.Background(), addr, phomemoProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{strings.ToLower(addr), "  " + addr + "\n"} {
		other := openAsync(context.Background(), t, alias)
		expectWaiting(t, other, "a connection to "+strconvQuote(alias))
		first.Close()
		first = expectOpened(t, other)
	}
	first.Close()
	if n := b.connects.Load(); n != 3 {
		t.Errorf("%d backend connects, want 3, one at a time", n)
	}
	if got := NormalizeAddress(" AbC-1 "); got != "abc-1" {
		t.Errorf("NormalizeAddress = %q", got)
	}
}

func strconvQuote(s string) string { return "\"" + strings.ReplaceAll(s, "\n", `\n`) + "\"" }
