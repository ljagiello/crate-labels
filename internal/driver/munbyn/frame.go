package munbyn

import (
	"bytes"
	"fmt"
)

// A frame is four header bytes and the payload:
//
//	55  n&ff  n>>8 | a<<6  x&fc | b
//
// The payload length n has 14 bits. Two checks share the spare bits:
//
//   - The header check is h = 55 ^ (n&ff) ^ (n>>8). Bits 2-3 of h travel as a, in the
//     top of byte 2. Bits 4-5 travel as b, in the bottom of byte 3.
//   - The payload check is x = h ^ (the XOR of every payload byte). Only bits 2-7 of x
//     have room, in the top of byte 3.
//
// There is no escaping and no trailer. The frame has no room for the other bits of h
// and x, so no receiver can check them.
const (
	frameStart  = 0x55
	headerBytes = 4
	MaxPayload  = 1<<14 - 1 // the most the length field can say
)

func xorOf(p []byte) byte {
	var x byte
	for _, b := range p {
		x ^= b
	}
	return x
}

func headerCheck(lo, hi byte) byte { return frameStart ^ lo ^ hi }

// Wrap frames a payload.
func Wrap(payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("munbyn: a %d-byte payload does not fit the 14-bit length", len(payload))
	}
	lo, hi := byte(len(payload)), byte(len(payload)>>8)
	h := headerCheck(lo, hi)
	x := h ^ xorOf(payload)
	frame := make([]byte, headerBytes, headerBytes+len(payload))
	frame[0], frame[1] = frameStart, lo
	frame[2] = hi | (h>>2&3)<<6
	frame[3] = x&0xfc | h>>4&3
	return append(frame, payload...), nil
}

// candidate is what the bytes at a possible frame start turn out to be.
type candidate int

const (
	notAFrame  candidate = iota // the header or payload check fails
	incomplete                  // the header checks out; more bytes are needed
	whole                       // a valid frame of size bytes
)

// inspect judges the frame that would start at b[0] == frameStart.
func inspect(b []byte) (c candidate, size int) {
	if len(b) < headerBytes {
		return incomplete, 0
	}
	lo, hi := b[1], b[2]&0x3f
	h := headerCheck(lo, hi)
	if b[2]>>6 != h>>2&3 || b[3]&3 != h>>4&3 {
		return notAFrame, 0
	}
	size = headerBytes + int(lo) + int(hi)<<8
	if len(b) < size {
		return incomplete, 0
	}
	if (h^xorOf(b[headerBytes:size]))&0xfc != b[3]&0xfc {
		return notAFrame, 0
	}
	return whole, size
}

// Reassembler turns notifications back into payloads. A frame may arrive split at any
// byte, and stray bytes may come before or between frames.
//
// Every 0x55 is a possible start. A start is taken only once its whole frame is in and
// both checks pass. A start whose frame is still incomplete does not stop the scan.
// When a later frame completes, the earlier start was noise, because the printer never
// interleaves frames. The unread tail is kept from the earliest start still open after
// the last frame. It is never longer than one frame.
type Reassembler struct{ tail []byte }

// Push adds one notification and returns the payloads of the frames it completes, in
// order. An empty payload is returned as an empty slice.
func (r *Reassembler) Push(chunk []byte) [][]byte {
	buf := append(r.tail, chunk...)
	var payloads [][]byte
	open := -1 // earliest incomplete start since the last frame
	for at := 0; at < len(buf); {
		next := bytes.IndexByte(buf[at:], frameStart)
		if next < 0 {
			break
		}
		at += next
		switch c, size := inspect(buf[at:]); c {
		case whole:
			payloads = append(payloads, bytes.Clone(buf[at+headerBytes:at+size]))
			at += size
			open = -1
		case incomplete:
			if open < 0 {
				open = at
			}
			at++
		default:
			at++
		}
	}
	r.tail = nil
	if open >= 0 {
		r.tail = bytes.Clone(buf[open:])
	}
	return payloads
}
