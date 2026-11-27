// Package fake is an in-memory printer.Link for tests and dry runs: it records every
// write and replays scripted replies.
package fake

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// Write is one recorded write.
type Write struct {
	Role         string
	Data         []byte
	WithResponse bool
}

// Link records writes. Replies are returned by Recv in order; Responder (if set) runs
// after every write and may queue more replies, emulating a printer that acks packets.
type Link struct {
	Max       int
	Responder func(w Write) [][]byte

	mu     sync.Mutex
	writes []Write
	inbox  chan []byte
	closed bool
}

// New returns a link with the given max write size and initial replies.
func New(maxWrite int, replies ...[]byte) *Link {
	l := &Link{Max: maxWrite, inbox: make(chan []byte, 4096)}
	for _, r := range replies {
		l.inbox <- r
	}
	return l
}

func (l *Link) MaxWrite() int { return l.Max }

func (l *Link) Write(_ context.Context, data []byte, role string, withResponse bool) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.New("link closed")
	}
	w := Write{Role: role, Data: bytes.Clone(data), WithResponse: withResponse}
	l.writes = append(l.writes, w)
	responder := l.Responder
	l.mu.Unlock()
	if responder != nil {
		for _, frame := range responder(w) {
			l.inbox <- frame
		}
	}
	return nil
}

func (l *Link) Recv(ctx context.Context, timeout time.Duration) ([]byte, error) {
	select {
	case f := <-l.inbox:
		return f, nil
	case <-time.After(timeout):
		return nil, printer.ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *Link) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return nil
}

// Writes returns a copy of every write so far.
func (l *Link) Writes() []Write {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Write(nil), l.writes...)
}

// Sent concatenates the bytes written for role ("" = all roles).
func (l *Link) Sent(role string) []byte {
	var b bytes.Buffer
	for _, w := range l.Writes() {
		if role == "" || w.Role == role {
			b.Write(w.Data)
		}
	}
	return b.Bytes()
}

// Closed reports whether Close was called.
func (l *Link) Closed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func init() {
	printer.RegisterLinkOpener(printer.LinkFile, func(context.Context, *printer.Target, *printer.Model) (printer.Link, error) {
		return New(244), nil
	})
}
