package tcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// listen starts a one-connection server that records what it reads and replies with
// reply after the first read.
func listen(t *testing.T, reply []byte) (addr string, got chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		if reply != nil {
			c.Write(reply)
		}
		rest, _ := io.ReadAll(c)
		got <- append(buf, rest...)
	}()
	return ln.Addr().String(), got
}

func TestRoundTrip(t *testing.T) {
	addr, got := listen(t, []byte{0x1a, 0x07, 0x02})
	ctx := context.Background()
	l, err := Dial(ctx, "tcp://"+addr)
	if err != nil {
		t.Fatal(err)
	}
	if l.MaxWrite() != MaxWrite {
		t.Errorf("MaxWrite = %d", l.MaxWrite())
	}
	if err := l.Write(ctx, []byte{1, 2, 3, 4}, "data", false); err != nil {
		t.Fatal(err)
	}
	frame, err := l.Recv(ctx, 2*time.Second)
	if err != nil || !bytes.Equal(frame, []byte{0x1a, 0x07, 0x02}) {
		t.Fatalf("recv = % x, %v", frame, err)
	}
	if _, err := l.Recv(ctx, 50*time.Millisecond); !errors.Is(err, printer.ErrTimeout) {
		t.Errorf("idle recv err = %v, want ErrTimeout", err)
	}
	if err := l.Write(ctx, []byte{5, 6}, "", true); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second close = %v", err)
	}
	if b := <-got; !bytes.Equal(b, []byte{1, 2, 3, 4, 5, 6}) {
		t.Errorf("server read % x", b)
	}
	if err := l.Write(ctx, []byte{7}, "", false); err == nil {
		t.Error("write after close must fail")
	}
}

func TestRecvSeesTheServerHangUp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	l, err := Dial(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, err = l.Recv(context.Background(), 2*time.Second)
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "closed the connection") {
		t.Errorf("err = %v", err)
	}
}

func TestRecvHonoursContext(t *testing.T) {
	addr, _ := listen(t, nil)
	l, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, err := l.Recv(ctx, 10*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Recv did not stop when the context was cancelled")
	}
}

func TestRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = Dial(context.Background(), addr)
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "Could not connect to "+addr) {
		t.Errorf("err = %v", err)
	}
	if _, err := Dial(context.Background(), " "); err == nil {
		t.Error("an empty address must fail")
	}
}

func TestAddress(t *testing.T) {
	for in, want := range map[string]string{
		"printer.local":             "printer.local:9100",
		"10.0.0.9":                  "10.0.0.9:9100",
		"10.0.0.9:9101":             "10.0.0.9:9101",
		"socket://10.0.0.9":         "10.0.0.9:9100",
		"TCP://10.0.0.9:9100/":      "10.0.0.9:9100",
		"fe80::1":                   "[fe80::1]:9100",
		"[fe80::1]:9100":            "[fe80::1]:9100",
		"  label-printer.lan:4000 ": "label-printer.lan:4000",
	} {
		if got := Address(in); got != want {
			t.Errorf("Address(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenerIsRegistered(t *testing.T) {
	addr, got := listen(t, nil)
	m := &printer.Model{ID: "x", Link: printer.LinkTCP}
	l, err := printer.OpenLink(context.Background(), &printer.Target{Address: addr}, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Write(context.Background(), []byte("ping"), "", false); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if b := <-got; string(b) != "ping" {
		t.Errorf("server read %q", b)
	}
}
