package ble

import (
	"context"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// Chunker writes a payload in pieces no larger than the link accepts, pausing between
// them. The model's BLE profile sets the piece size and the pause (chunk_bytes and
// chunk_gap_ms in the catalog).
type Chunker struct {
	Role         string        // write role, e.g. "data"
	WithResponse bool          // GATT write with response
	Chunk        int           // largest piece; capped by link.MaxWrite(); ≤ 0 means MaxWrite
	Gap          time.Duration // pause between pieces, none after the last
	// Sleep pauses between pieces; nil sleeps for real, honouring ctx.
	Sleep func(ctx context.Context, d time.Duration) error
	// OnChunk, when set, is called after each piece with the bytes sent so far.
	OnChunk func(sent, total int)
}

// Write sends data and returns the number of writes. A failed piece becomes a
// user-facing error that names it, counting from 1 ("Write failed at chunk 3 of 90: …").
func (c Chunker) Write(ctx context.Context, link printer.Link, data []byte) (int, error) {
	size := link.MaxWrite()
	if c.Chunk > 0 {
		size = min(size, c.Chunk)
	}
	if size <= 0 {
		return 0, printer.Errorf("The link cannot take any data (largest write %d bytes).", size)
	}
	n := (len(data) + size - 1) / size
	sleep := c.Sleep
	if sleep == nil {
		sleep = Sleep
	}
	for i := range n {
		piece := data[i*size : min((i+1)*size, len(data))]
		if err := link.Write(ctx, piece, c.Role, c.WithResponse); err != nil {
			if ctx.Err() != nil {
				return i, ctx.Err()
			}
			return i, printer.Errorf("Write failed at chunk %d of %d: %v", i+1, n, err)
		}
		if c.OnChunk != nil {
			c.OnChunk(min((i+1)*size, len(data)), len(data))
		}
		if c.Gap > 0 && i < n-1 {
			if err := sleep(ctx, c.Gap); err != nil {
				return i + 1, err
			}
		}
	}
	return n, nil
}

// WriteChunked writes data to link in pieces of at most min(chunk, link.MaxWrite())
// bytes, gap apart.
func WriteChunked(ctx context.Context, link printer.Link, data []byte, role string, withResponse bool, chunk int, gap time.Duration) error {
	_, err := Chunker{Role: role, WithResponse: withResponse, Chunk: chunk, Gap: gap}.Write(ctx, link, data)
	return err
}

// Sleep waits for d or until ctx is done. A non-positive d only checks ctx. It is the
// one sleep every thermal driver uses.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
