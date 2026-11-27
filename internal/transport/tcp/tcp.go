// Package tcp is a raw TCP printer.Link (port 9100 "JetDirect" style) for byte-stream
// printer protocols. It registers the "tcp" link opener.
package tcp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// DefaultPort is used when an address names no port.
const DefaultPort = "9100"

// Limits and timeouts.
const (
	MaxWrite       = 65535 // a stream has no write limit; large enough that chunking is a no-op
	DialTimeout    = 10 * time.Second
	writeTimeout   = 30 * time.Second
	recvBufferSize = 4096
)

func init() {
	printer.RegisterLinkOpener(printer.LinkTCP, func(ctx context.Context, t *printer.Target, _ *printer.Model) (printer.Link, error) {
		return Dial(ctx, t.Address)
	})
}

// Link is a connected socket. Write roles and the response flag are ignored: a stream
// has one channel.
type Link struct {
	addr string
	conn net.Conn

	mu     sync.Mutex
	closed bool
}

var _ printer.Link = (*Link)(nil)

// Address turns "host", "host:port", "tcp://host:port" or "socket://host:port" into
// host:port, adding [DefaultPort] when none is given.
func Address(address string) string {
	a := strings.TrimSpace(address)
	for _, scheme := range []string{"tcp://", "socket://"} {
		if len(a) >= len(scheme) && strings.EqualFold(a[:len(scheme)], scheme) {
			a = a[len(scheme):]
		}
	}
	a = strings.TrimSuffix(a, "/")
	if _, _, err := net.SplitHostPort(a); err == nil {
		return a
	}
	return net.JoinHostPort(strings.Trim(a, "[]"), DefaultPort)
}

// Dial connects to address within [DialTimeout] or ctx's deadline.
func Dial(ctx context.Context, address string) (*Link, error) {
	addr := Address(address)
	if host, _, _ := net.SplitHostPort(addr); host == "" {
		return nil, printer.Errorf("The printer has no network address. Enter its host name or IP address.")
	}
	d := net.Dialer{Timeout: DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, printer.Errorf("Could not connect to %s within %g s. Is the printer on and on this network?", addr, DialTimeout.Seconds())
	}
	return &Link{addr: addr, conn: conn}, nil
}

// MaxWrite implements printer.Link.
func (l *Link) MaxWrite() int { return MaxWrite }

func (l *Link) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// Write sends all of data, honouring ctx.
func (l *Link) Write(ctx context.Context, data []byte, _ string, _ bool) error {
	if l.isClosed() {
		return printer.Errorf("The connection to %s is closed.", l.addr)
	}
	deadline := time.Now().Add(writeTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = l.conn.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = l.conn.SetWriteDeadline(time.Now()) })
	defer stop()
	if _, err := l.conn.Write(data); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return printer.Errorf("Sending to %s failed: %v", l.addr, err)
	}
	return nil
}

// Recv returns whatever bytes arrive next (up to 4 KiB), printer.ErrTimeout when none
// arrive in time, or an error once the printer has closed the connection.
func (l *Link) Recv(ctx context.Context, timeout time.Duration) ([]byte, error) {
	if l.isClosed() {
		return nil, printer.Errorf("The connection to %s is closed.", l.addr)
	}
	_ = l.conn.SetReadDeadline(time.Now().Add(timeout))
	stop := context.AfterFunc(ctx, func() { _ = l.conn.SetReadDeadline(time.Now()) })
	defer stop()
	buf := make([]byte, recvBufferSize)
	n, err := l.conn.Read(buf)
	if n > 0 {
		return buf[:n], nil
	}
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, os.ErrDeadlineExceeded):
		return nil, printer.ErrTimeout
	case errors.Is(err, io.EOF):
		return nil, printer.Errorf("%s closed the connection.", l.addr)
	case err != nil:
		return nil, printer.Errorf("Reading from %s failed: %v", l.addr, err)
	}
	return nil, printer.ErrTimeout
}

// Close closes the socket. It is safe to call more than once.
func (l *Link) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.conn.Close()
}
