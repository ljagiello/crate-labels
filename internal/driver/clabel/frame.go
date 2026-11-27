package clabel

import (
	"bytes"
	"fmt"
)

// A control frame is
//
//	dd 00 fn sub len_hi len_lo content… crc dd
//
// crc is CRC-8/SMBUS over the five bytes after the first dd and the content. There is
// no escaping: a dd inside the content goes out as it is.
const (
	delimiter  = 0xdd
	frameBytes = 8      // everything but the content
	MaxContent = 0xffff // the most the length field can say
)

// crcTable is CRC-8/SMBUS: polynomial x^8 + x^2 + x + 1 (0x07), initial value 0, MSB
// first, nothing reflected, no final XOR.
var crcTable = func() (t [256]byte) {
	for i := range t {
		c := byte(i)
		for range 8 {
			c = c<<1 ^ 0x07*(c>>7)
		}
		t[i] = c
	}
	return t
}()

// CRC8 is the control frames' checksum (CRC-8/SMBUS; "123456789" gives f4).
func CRC8(data []byte) byte {
	var c byte
	for _, b := range data {
		c = crcTable[c^b]
	}
	return c
}

// ControlFrame builds one control frame.
func ControlFrame(function, sub byte, content []byte) ([]byte, error) {
	if len(content) > MaxContent {
		return nil, fmt.Errorf("clabel: %d bytes of control frame content do not fit the 16-bit length", len(content))
	}
	f := make([]byte, 0, frameBytes+len(content))
	f = append(f, delimiter, 0x00, function, sub, byte(len(content)>>8), byte(len(content)))
	f = append(f, content...)
	return append(f, CRC8(f[1:]), delimiter), nil
}

func fixedFrame(function, sub byte, content ...byte) []byte {
	f, err := ControlFrame(function, sub, content)
	if err != nil {
		panic(err) // fixed frames are a few bytes
	}
	return f
}

// Setting is a printer setting the driver reads and, on some models, writes.
type Setting byte

// Settings the driver uses.
const (
	SettingPaperType Setting = 0x11
	SettingLanguage  Setting = 0x2b
)

// String names the setting for messages.
func (s Setting) String() string {
	switch s {
	case SettingPaperType:
		return "paper type"
	case SettingLanguage:
		return "language"
	}
	return fmt.Sprintf("setting %#02x", byte(s))
}

// LanguageTSPL is the LANGUAGE value for TSPL.
const LanguageTSPL = 0

// Paper is the value of the PAPER_TYPE setting.
type Paper byte

// Paper types.
const (
	PaperGap        Paper = 0
	PaperContinuous Paper = 1
)

// String is the paper type as a user would name it.
func (p Paper) String() string {
	if p == PaperContinuous {
		return "continuous"
	}
	return "gap"
}

// Control requests: function 5 carries the identity query (item 4) and the settings.
const (
	fnSettings   = 5
	fnStatus     = 1
	itemIdentity = 4
	itemStatus   = 1
	answerBit    = 0x80 // set in sub of every reply
)

// IdentityQuery asks for the model and firmware.
var IdentityQuery = fixedFrame(fnSettings, itemIdentity)

// ReadSetting is the frame that asks for one setting.
func ReadSetting(s Setting) []byte { return fixedFrame(fnSettings, byte(s)) }

// WriteSetting is the frame that changes one setting.
func WriteSetting(s Setting, value byte) []byte { return fixedFrame(fnSettings, byte(s), value) }

// RawFrame is one reassembled control frame.
type RawFrame struct {
	Function, Sub byte
	Content       []byte
}

type candidate int

const (
	notAFrame  candidate = iota // the trailer or the CRC is wrong
	incomplete                  // nothing wrong so far; more bytes are needed
	whole                       // a valid frame of size bytes
)

// inspect judges the frame that would start at b[0] == delimiter: the frame must end
// with dd and the CRC must match. Our frames put 00 after the first dd. The CRC covers
// that byte, but a reply is not required to have 00 there, so a printer that sends
// something else is still heard.
func inspect(b []byte) (c candidate, size int) {
	if len(b) < 6 {
		return incomplete, 0
	}
	n := int(b[4])<<8 | int(b[5])
	size = frameBytes + n
	switch {
	case len(b) < size:
		return incomplete, 0
	case b[size-1] != delimiter || CRC8(b[1:size-2]) != b[size-2]:
		return notAFrame, 0
	}
	return whole, size
}

// Reassembler turns notifications back into control frames. A frame may arrive split
// at any byte, and other bytes may come before or between frames.
//
// Every dd is a possible start, including one inside rejected bytes. A start is taken
// once its whole frame is in and checks out. A start whose frame is still incomplete
// does not stop the scan. When a later frame completes, the earlier start was noise,
// because the printer never interleaves frames. The unread tail is kept from the
// earliest start still open after the last frame. It is never longer than one frame.
type Reassembler struct{ tail []byte }

// Push adds one notification and returns the frames it completes, in order.
func (r *Reassembler) Push(chunk []byte) []RawFrame {
	buf := append(r.tail, chunk...)
	var frames []RawFrame
	open := -1
	for at := 0; at < len(buf); {
		next := bytes.IndexByte(buf[at:], delimiter)
		if next < 0 {
			break
		}
		at += next
		switch c, size := inspect(buf[at:]); c {
		case whole:
			frames = append(frames, RawFrame{Function: buf[at+2], Sub: buf[at+3],
				Content: bytes.Clone(buf[at+6 : at+size-2])})
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
	return frames
}
