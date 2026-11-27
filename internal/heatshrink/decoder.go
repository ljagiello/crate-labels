package heatshrink

// This file ports the heatshrink library's decoder, heatshrink_decoder.c from
// github.com/atomicobject/heatshrink, state for state. It shares no code with the
// encoder, so tests use it as an independent answer to "would a stock heatshrink decoder,
// such as the printer's, accept this stream?".
//
// heatshrink_decoder.c is distributed under the ISC licence:
//
//	Copyright (c) 2013-2015, Scott Vokes <vokes.s@gmail.com>
//	All rights reserved.
//
//	Permission to use, copy, modify, and/or distribute this software for any
//	purpose with or without fee is hereby granted, provided that the above
//	copyright notice and this permission notice appear in all copies.
//
//	THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
//	WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
//	MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
//	ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
//	WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
//	ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
//	OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

import (
	"errors"
	"fmt"
)

// noBits is get_bits' "suspend" marker, (uint16_t)-1 in C.
const noBits = 0xffff

type decoderState uint8

const (
	stTagBit decoderState = iota
	stYieldLiteral
	stBackrefIndexMSB
	stBackrefIndexLSB
	stBackrefCountMSB
	stBackrefCountLSB
	stYieldBackref
)

// PollResult says why [Decoder.Poll] returned.
type PollResult int

const (
	// PollEmpty means the decoder needs more input.
	PollEmpty PollResult = iota
	// PollMore means the output buffer filled up; call Poll again.
	PollMore
)

// FinishResult is the answer of [Decoder.Finish].
type FinishResult int

const (
	// FinishDone means all input was used and nothing is pending.
	FinishDone FinishResult = iota
	// FinishMore means Poll still has output to yield.
	FinishMore
)

// Decoder is the reference library's streaming decoder: a fixed input buffer that only
// refills once drained, a zero-filled circular window, and a bit reader that suspends
// before it would read past the end of the input.
type Decoder struct {
	inputBufferSize int
	windowBits      uint8
	lookaheadBits   uint8

	input       []byte
	inputSize   int
	inputIndex  int
	window      []byte
	state       decoderState
	bitIndex    uint8
	currentByte uint8
	outputCount uint16
	outputIndex uint16
	headIndex   uint16
}

// NewDecoder allocates a decoder, with heatshrink_decoder_alloc's parameter checks.
func NewDecoder(inputBufferSize int, windowBits, lookaheadBits uint8) (*Decoder, error) {
	if windowBits < 4 || windowBits > 15 || inputBufferSize <= 0 ||
		lookaheadBits < 3 || lookaheadBits >= windowBits {
		return nil, fmt.Errorf("heatshrink: invalid decoder parameters (input %d, window %d, lookahead %d)",
			inputBufferSize, windowBits, lookaheadBits)
	}
	d := &Decoder{inputBufferSize: inputBufferSize, windowBits: windowBits, lookaheadBits: lookaheadBits}
	d.Reset()
	return d, nil
}

// Reset clears all state and zero-fills the window.
func (d *Decoder) Reset() {
	d.input = make([]byte, d.inputBufferSize)
	d.window = make([]byte, 1<<d.windowBits)
	d.state = stTagBit
	d.inputSize, d.inputIndex = 0, 0
	d.bitIndex, d.currentByte = 0, 0
	d.outputCount, d.outputIndex, d.headIndex = 0, 0, 0
}

// Sink copies as much of p as fits into the input buffer and returns the count. Zero
// means the buffer is full (HSDR_SINK_FULL): poll first.
func (d *Decoder) Sink(p []byte) int {
	rem := d.inputBufferSize - d.inputSize
	if rem == 0 {
		return 0
	}
	n := copy(d.input[d.inputSize:d.inputSize+min(rem, len(p))], p)
	d.inputSize += n
	return n
}

// Poll decodes into out and returns the number of bytes written.
func (d *Decoder) Poll(out []byte) (int, PollResult) {
	o := output{buf: out}
	for {
		in := d.state
		switch in {
		case stTagBit:
			d.state = d.tagBit()
		case stYieldLiteral:
			d.state = d.yieldLiteral(&o)
		case stBackrefIndexMSB:
			d.state = d.backrefIndexMSB()
		case stBackrefIndexLSB:
			d.state = d.backrefIndexLSB()
		case stBackrefCountMSB:
			d.state = d.backrefCountMSB()
		case stBackrefCountLSB:
			d.state = d.backrefCountLSB()
		case stYieldBackref:
			d.state = d.yieldBackref(&o)
		}
		// A state that cannot advance has run out of input or output space.
		if d.state == in {
			if o.n == len(out) {
				return o.n, PollMore
			}
			return o.n, PollEmpty
		}
	}
}

// Finish reports whether the stream is complete. The zero bits padding the last byte
// look like the start of a back-reference, so the decoder is done in any header state
// once the input is used up.
func (d *Decoder) Finish() FinishResult {
	switch d.state {
	case stTagBit, stBackrefIndexLSB, stBackrefIndexMSB, stBackrefCountLSB,
		stBackrefCountMSB, stYieldLiteral:
		if d.inputSize == 0 {
			return FinishDone
		}
		return FinishMore
	}
	return FinishMore
}

type output struct {
	buf []byte
	n   int
}

func (o *output) push(c byte) {
	o.buf[o.n] = c
	o.n++
}

func (d *Decoder) mask() uint16 { return uint16(1)<<d.windowBits - 1 }

func (d *Decoder) tagBit() decoderState {
	bits := d.getBits(1)
	switch {
	case bits == noBits:
		return stTagBit
	case bits != 0:
		return stYieldLiteral
	case d.windowBits > 8:
		return stBackrefIndexMSB
	}
	d.outputIndex = 0
	return stBackrefIndexLSB
}

func (d *Decoder) yieldLiteral(o *output) decoderState {
	if o.n >= len(o.buf) {
		return stYieldLiteral
	}
	b := d.getBits(8)
	if b == noBits {
		return stYieldLiteral
	}
	c := byte(b)
	d.window[d.headIndex&d.mask()] = c
	d.headIndex++
	o.push(c)
	return stTagBit
}

func (d *Decoder) backrefIndexMSB() decoderState {
	bits := d.getBits(d.windowBits - 8)
	if bits == noBits {
		return stBackrefIndexMSB
	}
	d.outputIndex = bits << 8
	return stBackrefIndexLSB
}

func (d *Decoder) backrefIndexLSB() decoderState {
	bits := d.getBits(min(d.windowBits, 8))
	if bits == noBits {
		return stBackrefIndexLSB
	}
	d.outputIndex |= bits
	d.outputIndex++
	d.outputCount = 0
	if d.lookaheadBits > 8 {
		return stBackrefCountMSB
	}
	return stBackrefCountLSB
}

func (d *Decoder) backrefCountMSB() decoderState {
	bits := d.getBits(d.lookaheadBits - 8)
	if bits == noBits {
		return stBackrefCountMSB
	}
	d.outputCount = bits << 8
	return stBackrefCountLSB
}

func (d *Decoder) backrefCountLSB() decoderState {
	bits := d.getBits(min(d.lookaheadBits, 8))
	if bits == noBits {
		return stBackrefCountLSB
	}
	d.outputCount |= bits
	d.outputCount++
	return stYieldBackref
}

func (d *Decoder) yieldBackref(o *output) decoderState {
	count := len(o.buf) - o.n
	if count > 0 {
		count = min(count, int(d.outputCount))
		mask := d.mask()
		negOffset := d.outputIndex
		for range count {
			c := d.window[(d.headIndex-negOffset)&mask]
			o.push(c)
			d.window[d.headIndex&mask] = c
			d.headIndex++
		}
		d.outputCount -= uint16(count)
		if d.outputCount == 0 {
			return stTagBit
		}
	}
	return stYieldBackref
}

// getBits returns the next count bits MSB-first, or noBits. It suspends before reading
// when the input is exhausted and the current byte cannot supply all of them, because
// it does not remember a partial accumulator across calls.
func (d *Decoder) getBits(count uint8) uint16 {
	if count > 15 {
		return noBits
	}
	if d.inputSize == 0 && d.bitIndex < 1<<(count-1) {
		return noBits
	}
	var acc uint16
	for range count {
		if d.bitIndex == 0 {
			if d.inputSize == 0 {
				return noBits
			}
			d.currentByte = d.input[d.inputIndex]
			d.inputIndex++
			if d.inputIndex == d.inputSize {
				d.inputIndex, d.inputSize = 0, 0 // input is exhausted
			}
			d.bitIndex = 0x80
		}
		acc <<= 1
		if d.currentByte&d.bitIndex != 0 {
			acc |= 1
		}
		d.bitIndex >>= 1
	}
	return acc
}

// errNoFinish guards Decode against a decoder that never reports done.
var errNoFinish = errors.New("heatshrink: decoder never finished")

// Decode decompresses a WindowBits/LookaheadBits stream with the reference decoder,
// using the heatshrink CLI's sink/poll/finish loop.
func Decode(src []byte) ([]byte, error) {
	return decode(src, WindowBits, LookaheadBits, 256, 64, 1024)
}

func decode(src []byte, windowBits, lookaheadBits uint8, inputBuf, sinkSize, outBuf int) ([]byte, error) {
	d, err := NewDecoder(inputBuf, windowBits, lookaheadBits)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(src)*2)
	buf := make([]byte, outBuf)
	drain := func() {
		for {
			n, res := d.Poll(buf)
			out = append(out, buf[:n]...)
			if res == PollEmpty {
				return
			}
		}
	}
	for pos := 0; pos < len(src); {
		pos += d.Sink(src[pos:min(len(src), pos+sinkSize)])
		drain()
	}
	for range 1_000_000 {
		if d.Finish() == FinishDone {
			return out, nil
		}
		n, _ := d.Poll(buf)
		out = append(out, buf[:n]...)
	}
	return nil, errNoFinish
}
