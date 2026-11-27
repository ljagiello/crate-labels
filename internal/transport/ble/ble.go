// Package ble is the Bluetooth LE byte link shared by the thermal printer families: a
// printer.Link over GATT, chunked writes, scanning and probing.
//
// The package is pure Go. It talks to the radio through a [Backend], which the
// internal/transport/ble/corebluetooth package installs from its init(). Keeping
// tinygo.org/x/bluetooth out of this package keeps CoreBluetooth out of every test
// binary: that library allocates a CBCentralManager as soon as it is linked in.
package ble

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// GATT services the built-in printer families use. The catalog holds each model's own
// UUIDs; these name them in code and tests.
const (
	PhomemoService = "0000ff00-0000-1000-8000-00805f9b34fb"
	MunbynService  = "0000abf0-0000-1000-8000-00805f9b34fb"
	// ClabelService is the Microchip (formerly ISSC) transparent UART service.
	ClabelService = "49535343-fe7d-4ae5-8fa9-9fafd205e455"
)

// MinChunkBytes is the smallest write the transport ever uses: the payload every BLE
// link can carry, the 23-byte default ATT MTU less the 3-byte write header.
const MinChunkBytes = 20

// Host-side waits. None of them is seen by the printer.
const (
	// ConnectTimeout bounds a connection attempt; CoreBluetooth never gives up on
	// one by itself. It allows for a printer that has only just been switched on.
	ConnectTimeout = 20 * time.Second
	// HelloWait is how long a probe listens for the first notification after
	// subscribing. A printer that sends one does so at once.
	HelloWait = 2 * time.Second
	// WriteResponseWait bounds a write with response. Apple's accessory guidelines
	// cap the link supervision timeout at 6 s, so an unanswered write has been lost
	// with the link by then.
	WriteResponseWait = 6 * time.Second

	mtuSettle           = time.Second // the OS may report the 20-byte default for a moment
	mtuPoll             = 100 * time.Millisecond
	inboxFrames         = 256
	disconnectedMessage = "The printer disconnected mid-print."
)

// Advertisement is one scan result as the backend saw it.
type Advertisement struct {
	Address      string   // CoreBluetooth peripheral UUID on macOS
	Name         string   // advertised local name; often empty in the first advert
	RSSI         int      // dBm
	ServiceUUIDs []string // advertised service UUIDs, lower-case 128-bit
}

// Backend is the platform Bluetooth stack.
type Backend interface {
	// Scan reports advertisements until ctx is done, then returns. It may call found
	// from another goroutine, but never concurrently.
	Scan(ctx context.Context, found func(Advertisement)) error
	// Connect opens a GATT connection to the peripheral with this address.
	Connect(ctx context.Context, address string, timeout time.Duration) (Peripheral, error)
}

// Peripheral is a connected GATT server.
type Peripheral interface {
	// Services discovers every primary service and returns their lower-case UUIDs.
	Services(ctx context.Context) ([]string, error)
	// Characteristics discovers the characteristics of one service from Services.
	Characteristics(ctx context.Context, service string) ([]Characteristic, error)
	// Name is the peripheral's name as the OS knows it, or "".
	Name() string
	// Done is closed once the OS confirms the connection is gone: after Disconnect, or
	// when the printer drops it. The address stays reserved until then.
	Done() <-chan struct{}
	// Disconnect asks the OS to drop the connection and waits, for a bounded time, for
	// the confirmation that closes Done.
	Disconnect() error
}

// Characteristic is one GATT characteristic of a connected peripheral.
type Characteristic interface {
	// UUID is the lower-case 128-bit UUID.
	UUID() string
	// Write sends one GATT write. Without response it waits for the stack to accept
	// more data (CoreBluetooth back-pressure); with response it waits for the reply.
	Write(data []byte, withResponse bool) error
	// Subscribe enables notifications. fn may be called on another goroutine and must
	// not block; nil disables them.
	Subscribe(fn func([]byte)) error
	// MaxWrite is the longest write without response the stack accepts now.
	MaxWrite() int
}

// installed is the backend with its per-address connection gate.
type installed struct {
	b    Backend
	gate *gate
}

var (
	backendMu sync.RWMutex
	backend   *installed
)

// SetBackend installs the platform stack. The corebluetooth package calls it from
// init(); tests install fakes. Every connection through it, from Open or Probe, holds
// its address until the disconnect is confirmed (see connect).
func SetBackend(b Backend) {
	backendMu.Lock()
	defer backendMu.Unlock()
	if b == nil {
		backend = nil
		return
	}
	backend = &installed{b: b, gate: newGate()}
}

func currentBackend() (*installed, error) {
	backendMu.RLock()
	defer backendMu.RUnlock()
	if backend == nil {
		return nil, printer.Errorf("Bluetooth is not available in this build of crate-labels.")
	}
	return backend, nil
}

// short is the first 8 hex digits of a UUID, enough to tell services apart in a message.
func short(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

// normalize lower-cases a UUID and expands the 16-bit and 32-bit short forms to the
// Bluetooth base UUID.
func normalize(uuid string) string {
	u := strings.ToLower(strings.TrimSpace(uuid))
	switch len(u) {
	case 4:
		return "0000" + u + "-0000-1000-8000-00805f9b34fb"
	case 8:
		return u + "-0000-1000-8000-00805f9b34fb"
	}
	return u
}

// NormalizeAddress is the one canonical form of a peripheral address: trimmed and
// lower-case. The connection gate and the CoreBluetooth backend both key on it, so
// " AAAA-…" and "aaaa-…" name the same printer.
func NormalizeAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}
