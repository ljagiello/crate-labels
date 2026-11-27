package ble

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// fakeChar is an in-memory characteristic.
type fakeChar struct {
	uuid string
	mtu  []int // successive MaxWrite answers; the last one repeats

	mu       sync.Mutex
	calls    int
	writes   [][]byte
	resp     []bool
	notifyFn func([]byte)
	failWith error
}

func (c *fakeChar) UUID() string { return c.uuid }

func (c *fakeChar) Write(data []byte, withResponse bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failWith != nil {
		return c.failWith
	}
	c.writes = append(c.writes, slices.Clone(data))
	c.resp = append(c.resp, withResponse)
	return nil
}

func (c *fakeChar) Subscribe(fn func([]byte)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifyFn = fn
	return nil
}

func (c *fakeChar) MaxWrite() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.mtu) == 0 {
		return 244
	}
	i := min(c.calls, len(c.mtu)-1)
	c.calls++
	return c.mtu[i]
}

// notify delivers a notification the way the OS would, on another goroutine.
func (c *fakeChar) notify(frame []byte) {
	c.mu.Lock()
	fn := c.notifyFn
	c.mu.Unlock()
	if fn != nil {
		fn(frame)
	}
}

func (c *fakeChar) subscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.notifyFn != nil
}

// fakePeripheral serves a fixed GATT table.
type fakePeripheral struct {
	name     string
	services map[string][]*fakeChar
	order    []string

	done         chan struct{}
	once         sync.Once
	disconnected atomic.Int32
	onSubscribe  func(*fakeChar) // called when a notify subscription starts
	// holdDisconnect leaves Done open after Disconnect until the test calls drop(), like
	// an OS that has not confirmed the disconnect yet.
	holdDisconnect bool
}

func newPeripheral(name string, services ...string) *fakePeripheral {
	return &fakePeripheral{name: name, services: map[string][]*fakeChar{}, order: services, done: make(chan struct{})}
}

func (p *fakePeripheral) add(service string, chars ...*fakeChar) *fakePeripheral {
	if !slices.Contains(p.order, service) {
		p.order = append(p.order, service)
	}
	p.services[service] = append(p.services[service], chars...)
	return p
}

func (p *fakePeripheral) Services(context.Context) ([]string, error) {
	return slices.Clone(p.order), nil
}

func (p *fakePeripheral) Characteristics(_ context.Context, service string) ([]Characteristic, error) {
	var out []Characteristic
	for _, c := range p.services[service] {
		if p.onSubscribe != nil {
			out = append(out, &hookChar{fakeChar: c, hook: p.onSubscribe})
		} else {
			out = append(out, c)
		}
	}
	return out, nil
}

func (p *fakePeripheral) Name() string          { return p.name }
func (p *fakePeripheral) Done() <-chan struct{} { return p.done }
func (p *fakePeripheral) drop()                 { p.once.Do(func() { close(p.done) }) }
func (p *fakePeripheral) Disconnect() error {
	p.disconnected.Add(1)
	if !p.holdDisconnect {
		p.drop()
	}
	return nil
}
func (p *fakePeripheral) disconnectCount() int32 { return p.disconnected.Load() }

// hookChar runs a hook after a subscription, e.g. to send a handshake.
type hookChar struct {
	*fakeChar
	hook func(*fakeChar)
}

func (c *hookChar) Subscribe(fn func([]byte)) error {
	if err := c.fakeChar.Subscribe(fn); err != nil {
		return err
	}
	if fn != nil {
		go c.hook(c.fakeChar)
	}
	return nil
}

// fakeBackend connects to known peripherals and replays adverts.
type fakeBackend struct {
	peripherals map[string]*fakePeripheral
	dial        func(address string) *fakePeripheral // a fresh peripheral per connect, if set
	block       chan struct{}                        // when set, Connect waits for it to close
	connects    atomic.Int32
	adverts     []Advertisement
	scanErr     error
	connectErr  error

	mu       sync.Mutex
	timeouts []time.Duration
}

func (b *fakeBackend) Scan(ctx context.Context, found func(Advertisement)) error {
	if b.scanErr != nil {
		return b.scanErr
	}
	for _, a := range b.adverts {
		found(a)
	}
	<-ctx.Done()
	return nil
}

func (b *fakeBackend) Connect(_ context.Context, address string, timeout time.Duration) (Peripheral, error) {
	b.mu.Lock()
	b.timeouts = append(b.timeouts, timeout)
	b.mu.Unlock()
	b.connects.Add(1)
	if b.block != nil {
		<-b.block
	}
	if b.connectErr != nil {
		return nil, b.connectErr
	}
	if b.dial != nil {
		return b.dial(address), nil
	}
	p, ok := b.peripherals[address]
	if !ok {
		return nil, errors.New("no such peer")
	}
	return p, nil
}

// install puts b in place for one test.
func install(t interface{ Cleanup(func()) }, b Backend) {
	backendMu.Lock()
	prev := backend
	backendMu.Unlock()
	SetBackend(b)
	t.Cleanup(func() {
		backendMu.Lock()
		backend = prev
		backendMu.Unlock()
	})
}

// writeFile creates path and its directory.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
