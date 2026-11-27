package ble

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

func init() {
	printer.RegisterLinkOpener(printer.LinkBLE, func(ctx context.Context, t *printer.Target, m *printer.Model) (printer.Link, error) {
		if m.BLE == nil {
			return nil, printer.Errorf("%s: model %q has no [model.ble] table, so it cannot be reached over Bluetooth.", m.Source, m.ID)
		}
		if t.Address == "" {
			return nil, printer.Errorf("The printer %q has no Bluetooth address. Scan for it and save it again.", t.Name)
		}
		return Open(ctx, t.Address, m.BLE)
	})
}

// Link is a connected GATT byte pipe and a printer.Link. Write roles map to
// characteristics through BLEProfile.Write, and notifications are buffered for Recv.
type Link struct {
	address  string
	profile  *printer.BLEProfile
	periph   Peripheral
	services []string
	chars    map[string]Characteristic // write role → characteristic
	notify   Characteristic            // nil when the printer has none

	inbox chan []byte

	mu     sync.Mutex
	closed bool
}

var _ printer.Link = (*Link)(nil)

// Open connects to address with the installed backend, finds profile.Service, maps the
// write roles and subscribes to profile.Notify. It gives up after [ConnectTimeout] or
// at ctx's deadline, whichever is sooner. While the link is open, other connections to
// the same address wait; Close frees it once the disconnect is confirmed.
func Open(ctx context.Context, address string, profile *printer.BLEProfile) (*Link, error) {
	in, err := currentBackend()
	if err != nil {
		return nil, err
	}
	p, err := connect(ctx, in, address)
	if err != nil {
		return nil, err
	}
	l, err := newLink(ctx, address, profile, p)
	if err != nil {
		_ = p.Disconnect()
		return nil, err
	}
	return l, nil
}

func fmtSeconds(d time.Duration) string {
	return fmt.Sprintf("%g s", d.Round(100*time.Millisecond).Seconds())
}

func newLink(ctx context.Context, address string, profile *printer.BLEProfile, p Peripheral) (*Link, error) {
	services, err := p.Services(ctx)
	if err != nil {
		return nil, printer.Errorf("Could not list the printer's Bluetooth services: %v", err)
	}
	for i, s := range services {
		services[i] = normalize(s)
	}
	want := normalize(profile.Service)
	if !slices.Contains(services, want) {
		offered := "none"
		if len(services) > 0 {
			shorts := make([]string, len(services))
			for i, s := range services {
				shorts[i] = short(s)
			}
			offered = strings.Join(shorts, ", ")
		}
		return nil, printer.Errorf("This device does not offer the %s service its printer model needs (it offers: %s). It may be a different kind of printer.", short(want), offered)
	}
	chars, err := p.Characteristics(ctx, want)
	if err != nil {
		return nil, printer.Errorf("Could not list the printer's %s characteristics: %v", short(want), err)
	}
	byUUID := map[string]Characteristic{}
	for _, c := range chars {
		byUUID[normalize(c.UUID())] = c
	}
	l := &Link{address: address, profile: profile, periph: p, services: services,
		chars: map[string]Characteristic{}, inbox: make(chan []byte, inboxFrames)}
	roles := make([]string, 0, len(profile.Write))
	for role := range profile.Write {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		uuid := normalize(profile.Write[role])
		c, ok := byUUID[uuid]
		if !ok {
			return nil, printer.Errorf("The printer's %s service has no %s characteristic (needed for %q writes).", short(want), short(uuid), role)
		}
		l.chars[role] = c
	}
	if profile.Notify != "" {
		// Every family can print without hearing back, so a missing notify
		// characteristic is worth a warning, not a failure.
		if c, ok := byUUID[normalize(profile.Notify)]; !ok {
			slog.Warn("ble: the printer has no notify characteristic; printing without replies", "address", address, "uuid", short(profile.Notify))
		} else if err := c.Subscribe(l.push); err != nil {
			slog.Warn("ble could not subscribe", "address", address, "uuid", short(profile.Notify), "err", err)
		} else {
			l.notify = c
		}
	}
	l.settleMTU(ctx)
	return l, nil
}

// settleMTU waits up to a second for the stack to report more than the 20-byte default.
func (l *Link) settleMTU(ctx context.Context) {
	c := l.writeChar()
	if c == nil {
		return
	}
	deadline := time.Now().Add(mtuSettle)
	for c.MaxWrite() <= MinChunkBytes && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-l.periph.Done():
			return
		case <-time.After(mtuPoll):
		}
	}
}

// writeChar is the "data" characteristic, else the first role's.
func (l *Link) writeChar() Characteristic {
	if c, ok := l.chars["data"]; ok {
		return c
	}
	roles := make([]string, 0, len(l.chars))
	for r := range l.chars {
		roles = append(roles, r)
	}
	if len(roles) == 0 {
		return nil
	}
	sort.Strings(roles)
	return l.chars[roles[0]]
}

// push queues one notification, dropping the oldest when the inbox is full.
func (l *Link) push(frame []byte) {
	f := slices.Clone(frame)
	for {
		select {
		case l.inbox <- f:
			return
		default:
		}
		select {
		case old := <-l.inbox:
			slog.Warn("ble inbox full, dropped a notification", "address", l.address, "frame", fmt.Sprintf("% x", old))
		default:
		}
	}
}

// Address is the peripheral address the link was opened with.
func (l *Link) Address() string { return l.address }

// Name is the peripheral's name as the OS reports it, or "".
func (l *Link) Name() string { return l.periph.Name() }

// Services lists the services discovered on the peripheral.
func (l *Link) Services() []string { return slices.Clone(l.services) }

// MaxWrite is min(the stack's write length, profile.ChunkBytes), never below 20.
func (l *Link) MaxWrite() int {
	n := l.profile.ChunkBytes
	if c := l.writeChar(); c != nil {
		if mtu := c.MaxWrite(); mtu > 0 && (n <= 0 || mtu < n) {
			n = mtu
		}
	}
	return max(n, MinChunkBytes)
}

// Connected reports whether the link is open and the peripheral still connected.
func (l *Link) Connected() bool {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return false
	}
	select {
	case <-l.periph.Done():
		return false
	default:
		return true
	}
}

// Write sends data as one GATT write to the characteristic for role.
func (l *Link) Write(ctx context.Context, data []byte, role string, withResponse bool) error {
	c, ok := l.chars[role]
	if !ok {
		known := slices.Sorted(maps.Keys(l.chars))
		return printer.Errorf("This printer has no %q characteristic (roles: %s).", role, strings.Join(known, ", "))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.Connected() {
		return printer.Errorf(disconnectedMessage)
	}
	if !withResponse {
		if err := c.Write(data, false); err != nil {
			return l.writeError(err)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.Write(data, true) }()
	timer := time.NewTimer(WriteResponseWait)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return l.writeError(err)
		}
		return nil
	case <-timer.C:
		return printer.Errorf("The printer did not confirm a write within %s.", fmtSeconds(WriteResponseWait))
	case <-l.periph.Done():
		return printer.Errorf(disconnectedMessage)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Link) writeError(err error) error {
	if !l.Connected() {
		return printer.Errorf(disconnectedMessage)
	}
	return printer.Errorf("Bluetooth write failed: %v", err)
}

// Recv returns the next notification. It returns printer.ErrTimeout when none arrives
// in time, and a user-facing error once the printer has disconnected and every queued
// frame has been read.
func (l *Link) Recv(ctx context.Context, timeout time.Duration) ([]byte, error) {
	select {
	case f := <-l.inbox:
		return f, nil
	default:
	}
	if !l.Connected() {
		return nil, printer.Errorf("The printer disconnected.")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case f := <-l.inbox:
		return f, nil
	case <-timer.C:
		return nil, printer.ErrTimeout
	case <-l.periph.Done():
		select {
		case f := <-l.inbox:
			return f, nil
		default:
			return nil, printer.Errorf("The printer disconnected.")
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close unsubscribes and disconnects. It is safe to call more than once.
func (l *Link) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	select {
	case <-l.periph.Done():
		return nil
	default:
	}
	if l.notify != nil {
		if err := l.notify.Subscribe(nil); err != nil {
			slog.Debug("ble unsubscribe failed", "address", l.address, "err", err)
		}
	}
	return l.periph.Disconnect()
}
