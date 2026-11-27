package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// pcm describes uncompressed sample data.
type pcm struct {
	channels  int
	rate      int
	bytes     int  // bytes per sample (the container, not the valid bits)
	float     bool // IEEE float (bytes 4 or 8)
	bigEndian bool
	unsigned  bool // 8-bit offset binary (WAV)
}

func (p pcm) frameBytes() int { return p.bytes * p.channels }

func (p pcm) valid() bool {
	if p.channels < 1 || p.channels > 64 || p.rate < 1 || p.rate > 1_000_000 {
		return false
	}
	if p.float {
		return p.bytes == 4 || p.bytes == 8
	}
	return p.bytes >= 1 && p.bytes <= 4
}

// decodeNative reads a WAV or AIFF file with the pure-Go readers.
func decodeNative(ctx context.Context, k kind, r io.ReaderAt, size int64, rate int, maxSeconds float64) ([]float32, error) {
	var (
		p        pcm
		off, n   int64
		frameCap int64 = -1
		err      error
	)
	if k == kindWAV {
		p, off, n, err = parseWAV(r, size)
	} else {
		p, off, n, frameCap, err = parseAIFF(r, size)
	}
	if err != nil {
		return nil, err
	}
	frames := n / int64(p.frameBytes())
	if frameCap >= 0 && frameCap < frames {
		frames = frameCap
	}
	if maxSeconds > 0 {
		frames = min(frames, int64(maxSeconds*float64(p.rate)))
	}
	return readPCM(ctx, r, off, frames, p, rate)
}

const readBlock = 1 << 14 // frames per read

// readPCM decodes frames of p at off, mixes them to mono and resamples to rate. A file
// that ends early yields what it holds.
func readPCM(ctx context.Context, r io.ReaderAt, off, frames int64, p pcm, rate int) ([]float32, error) {
	fb := p.frameBytes()
	var rs *resampler
	expected := frames
	if p.rate != rate {
		rs = newResampler(p.rate, rate)
		expected = rs.outputs(frames)
	}
	out := make([]float32, 0, expected)
	if rs != nil {
		rs.out = out
	}
	raw := make([]byte, readBlock*fb)
	mono := make([]float32, readBlock)
	sample := sampleFunc(p)
	inv := 1 / float32(p.channels)
	for done := int64(0); done < frames; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		want := min(int64(readBlock), frames-done)
		got, err := r.ReadAt(raw[:want*int64(fb)], off+done*int64(fb))
		whole := got / fb
		for i := range whole {
			frame := raw[i*fb : (i+1)*fb]
			var sum float32
			for c := range p.channels {
				sum += sample(frame[c*p.bytes:])
			}
			mono[i] = sum * inv
		}
		if rs != nil {
			rs.push(mono[:whole])
		} else {
			out = append(out, mono[:whole]...)
		}
		done += int64(whole)
		if err != nil || whole < int(want) {
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
	}
	if rs != nil {
		return rs.finish(), nil
	}
	return out, nil
}

// sampleFunc returns a decoder for one sample of p, scaled to [-1, 1).
func sampleFunc(p pcm) func([]byte) float32 {
	var bo binary.ByteOrder = binary.LittleEndian
	if p.bigEndian {
		bo = binary.BigEndian
	}
	switch {
	case p.float && p.bytes == 4:
		return func(b []byte) float32 { return finite(math.Float32frombits(bo.Uint32(b))) }
	case p.float:
		return func(b []byte) float32 { return finite(float32(math.Float64frombits(bo.Uint64(b)))) }
	case p.bytes == 1 && p.unsigned:
		return func(b []byte) float32 { return (float32(b[0]) - 128) / 128 }
	case p.bytes == 1:
		return func(b []byte) float32 { return float32(int8(b[0])) / 128 }
	case p.bytes == 2:
		return func(b []byte) float32 { return float32(int16(bo.Uint16(b))) / (1 << 15) }
	case p.bytes == 3 && p.bigEndian:
		return func(b []byte) float32 {
			return float32(int32(uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8)>>8) / (1 << 23)
		}
	case p.bytes == 3:
		return func(b []byte) float32 {
			return float32(int32(uint32(b[2])<<24|uint32(b[1])<<16|uint32(b[0])<<8)>>8) / (1 << 23)
		}
	default:
		return func(b []byte) float32 { return float32(int32(bo.Uint32(b))) / (1 << 31) }
	}
}

func finite(v float32) float32 {
	if v != v || v > 1e6 || v < -1e6 {
		return 0
	}
	return v
}

// chunkAt reads a chunk header at pos: its id and body size.
func chunkAt(r io.ReaderAt, pos int64, bo binary.ByteOrder) (string, int64, bool) {
	var h [8]byte
	if _, err := r.ReadAt(h[:], pos); err != nil {
		return "", 0, false
	}
	return string(h[:4]), int64(bo.Uint32(h[4:])), true
}

// dataSpan bounds a sample data chunk. A size of 0 or one running past the end (a file
// still being written, a streamed WAV) means "to the end of the file".
func dataSpan(off, n, size int64) int64 {
	if n == 0 || n == 0xFFFFFFFF || off+n > size {
		return size - off
	}
	return n
}

// The GUID tail shared by every KSDATAFORMAT_SUBTYPE (WAVE_FORMAT_EXTENSIBLE).
var ksSubtype = []byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}

// parseWAV finds the format and the sample data of a RIFF/RF64 WAVE file.
func parseWAV(r io.ReaderAt, size int64) (p pcm, off, n int64, err error) {
	le := binary.LittleEndian
	var head [12]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return p, 0, 0, formatErr("a broken WAV header")
	}
	rf64 := string(head[:4]) != "RIFF"
	var ds64Data int64 = -1
	haveFmt, haveData := false, false
	for pos := int64(12); pos+8 <= size && !(haveFmt && haveData); {
		id, csize, ok := chunkAt(r, pos, le)
		if !ok {
			break
		}
		body := pos + 8
		switch id {
		case "fmt ":
			if csize < 16 {
				return p, 0, 0, formatErr("a broken WAV header")
			}
			buf := make([]byte, min(csize, 40))
			if _, err := r.ReadAt(buf, body); err != nil {
				return p, 0, 0, formatErr("a broken WAV header")
			}
			if p, err = wavFormat(buf); err != nil {
				return p, 0, 0, err
			}
			haveFmt = true
		case "ds64":
			var b [16]byte
			if csize >= 16 {
				if _, err := r.ReadAt(b[:], body); err == nil {
					ds64Data = int64(le.Uint64(b[8:]))
				}
			}
		case "data":
			off = body
			if rf64 && csize == 0xFFFFFFFF && ds64Data > 0 {
				csize = ds64Data
			}
			n = dataSpan(off, csize, size)
			haveData = true
			if n == size-off {
				pos = size // the data runs to the end: nothing follows it
				continue
			}
		}
		pos = body + csize + csize&1
	}
	if !haveFmt || !haveData {
		return p, 0, 0, formatErr("a broken WAV file (no format or no audio data)")
	}
	return p, off, n, nil
}

func wavFormat(b []byte) (pcm, error) {
	le := binary.LittleEndian
	tag := le.Uint16(b[0:])
	p := pcm{channels: int(le.Uint16(b[2:])), rate: int(le.Uint32(b[4:]))}
	blockAlign := int(le.Uint16(b[12:]))
	bits := int(le.Uint16(b[14:]))
	if tag == 0xFFFE {
		if len(b) < 40 || string(b[26:40]) != string(ksSubtype) {
			return p, formatErr("a WAV file in an encoding it can't read")
		}
		tag = le.Uint16(b[24:])
	}
	switch tag {
	case 1: // PCM
		p.bytes = (bits + 7) / 8
		// A wider container than the bits need (20-bit in 3 bytes, 24 in 4).
		if p.channels > 0 && blockAlign%p.channels == 0 && blockAlign/p.channels > p.bytes {
			p.bytes = blockAlign / p.channels
		}
		p.unsigned = p.bytes == 1
	case 3: // IEEE float
		p.float, p.bytes = true, bits/8
	default:
		return p, formatErr("a WAV file in an encoding it can't read (format 0x%04X)", tag)
	}
	if !p.valid() {
		return p, formatErr("a WAV file with an impossible format (%d channels, %d Hz, %d-bit)", p.channels, p.rate, bits)
	}
	return p, nil
}

// parseAIFF finds the format and the sample data of an AIFF or AIFC file. frames is the
// COMM chunk's frame count (-1 when it gives none).
func parseAIFF(r io.ReaderAt, size int64) (p pcm, off, n, frames int64, err error) {
	be := binary.BigEndian
	var head [12]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return p, 0, 0, 0, formatErr("a broken AIFF header")
	}
	aifc := string(head[8:12]) == "AIFC"
	frames = -1
	haveComm, haveData := false, false
	for pos := int64(12); pos+8 <= size && !(haveComm && haveData); {
		id, csize, ok := chunkAt(r, pos, be)
		if !ok {
			break
		}
		body := pos + 8
		switch id {
		case "COMM":
			if csize < 18 {
				return p, 0, 0, 0, formatErr("a broken AIFF header")
			}
			buf := make([]byte, min(csize, 22))
			if _, err := r.ReadAt(buf, body); err != nil {
				return p, 0, 0, 0, formatErr("a broken AIFF header")
			}
			compression := "NONE"
			if aifc && len(buf) >= 22 {
				compression = string(buf[18:22])
			}
			if p, err = aiffFormat(buf, compression); err != nil {
				return p, 0, 0, 0, err
			}
			if fc := int64(be.Uint32(buf[2:])); fc > 0 {
				frames = fc
			}
			haveComm = true
		case "SSND":
			var b [8]byte
			if _, err := r.ReadAt(b[:], body); err != nil {
				return p, 0, 0, 0, formatErr("a broken AIFF file (no audio data)")
			}
			skip := int64(be.Uint32(b[:4]))
			off = body + 8 + skip
			if csize == 0 || body+csize > size {
				n = size - off
			} else {
				n = csize - 8 - skip
			}
			if n < 0 {
				return p, 0, 0, 0, formatErr("a broken AIFF file (no audio data)")
			}
			haveData = true
		}
		pos = body + csize + csize&1
	}
	if !haveComm || !haveData {
		return p, 0, 0, 0, formatErr("a broken AIFF file (no format or no audio data)")
	}
	return p, off, n, frames, nil
}

func aiffFormat(b []byte, compression string) (pcm, error) {
	be := binary.BigEndian
	bits := int(int16(be.Uint16(b[6:])))
	p := pcm{channels: int(int16(be.Uint16(b[0:]))), rate: int(math.Round(extended(b[8:18]))), bytes: (bits + 7) / 8,
		bigEndian: true}
	switch compression {
	case "NONE", "twos":
	case "sowt":
		p.bigEndian = false
	case "raw ":
		p.unsigned = true
	case "in24":
		p.bytes = 3
	case "42ni":
		p.bytes, p.bigEndian = 3, false
	case "in32":
		p.bytes = 4
	case "23ni":
		p.bytes, p.bigEndian = 4, false
	case "fl32", "FL32":
		p.float, p.bytes = true, 4
	case "fl64", "FL64":
		p.float, p.bytes = true, 8
	default:
		return p, formatErr("an AIFF-C file in an encoding it can't read (%q)", compression)
	}
	if !p.valid() {
		return p, formatErr("an AIFF file with an impossible format (%d channels, %d Hz, %d-bit)", p.channels, p.rate, bits)
	}
	return p, nil
}

// extended converts an 80-bit IEEE 754 extended float (AIFF's sample rate).
func extended(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:2]) & 0x7FFF)
	mant := binary.BigEndian.Uint64(b[2:10])
	if mant == 0 || exp == 0x7FFF {
		return 0
	}
	f := math.Ldexp(float64(mant), exp-16383-63)
	if b[0]&0x80 != 0 {
		f = -f
	}
	return f
}
