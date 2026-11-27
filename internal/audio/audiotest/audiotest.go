// Package audiotest synthesises audio files for tests: click tracks and tones written as
// WAV or AIFF in any sample format. Import it from tests only.
package audiotest

import (
	"bytes"
	"encoding/binary"
	"math"
)

// Format is a sample encoding.
type Format struct {
	Bits  int  // container bits per sample: 8, 16, 24, 32 or 64
	Float bool // IEEE float (32 or 64 bits)
	// Extensible writes a WAVE_FORMAT_EXTENSIBLE header, with ValidBits (0 = Bits).
	Extensible bool
	ValidBits  int
	// Compression makes an AIFC file ("NONE", "sowt", "fl32", "fl64", ...); "" is
	// plain AIFF. WAV ignores it.
	Compression string
}

// Tone is a sine of hz at amp, seconds long.
func Tone(rate int, seconds, hz, amp float64) []float64 {
	out := make([]float64, int(seconds*float64(rate)))
	for i := range out {
		out[i] = amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate))
	}
	return out
}

// Clicks is a click track: a short decaying 1 kHz burst on every beat at bpm.
func Clicks(rate int, seconds, bpm float64) []float64 {
	out := make([]float64, int(seconds*float64(rate)))
	period := 60 / bpm * float64(rate)
	burst := rate / 50 // 20 ms
	for beat := 0.0; int(beat) < len(out); beat += period {
		start := int(beat)
		for i := 0; i < burst && start+i < len(out); i++ {
			out[start+i] = 0.8 * math.Exp(-float64(i)/float64(burst)*5) * math.Sin(2*math.Pi*1000*float64(i)/float64(rate))
		}
	}
	return out
}

// Mono makes frames of one channel.
func Mono(x []float64) [][]float64 {
	out := make([][]float64, len(x))
	for i, v := range x {
		out[i] = []float64{v}
	}
	return out
}

// Stereo makes frames from two channels of equal length.
func Stereo(l, r []float64) [][]float64 {
	out := make([][]float64, len(l))
	for i := range l {
		out[i] = []float64{l[i], r[i]}
	}
	return out
}

// WAV16 is a mono 16-bit PCM WAV of x.
func WAV16(rate int, x []float64) []byte { return WAV(Format{Bits: 16}, rate, Mono(x)) }

// WAV encodes frames (frames[i][channel], in [-1, 1]) as a RIFF WAVE file.
func WAV(f Format, rate int, frames [][]float64) []byte {
	channels := 1
	if len(frames) > 0 {
		channels = len(frames[0])
	}
	bps := f.Bits / 8
	var data bytes.Buffer
	for _, fr := range frames {
		for _, v := range fr {
			putSample(&data, f, v, binary.LittleEndian, true)
		}
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	tag := uint16(1)
	if f.Float {
		tag = 3
	}
	fmtLen := 16
	if f.Extensible {
		fmtLen = 40
	}
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, le, uint32(fmtLen))
	if f.Extensible {
		_ = binary.Write(&b, le, uint16(0xFFFE))
	} else {
		_ = binary.Write(&b, le, tag)
	}
	_ = binary.Write(&b, le, uint16(channels))
	_ = binary.Write(&b, le, uint32(rate))
	_ = binary.Write(&b, le, uint32(rate*channels*bps))
	_ = binary.Write(&b, le, uint16(channels*bps))
	_ = binary.Write(&b, le, uint16(f.Bits))
	if f.Extensible {
		valid := f.ValidBits
		if valid == 0 {
			valid = f.Bits
		}
		_ = binary.Write(&b, le, uint16(22))
		_ = binary.Write(&b, le, uint16(valid))
		_ = binary.Write(&b, le, uint32(0))
		_ = binary.Write(&b, le, tag)
		b.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71})
	}
	// A chunk the reader must skip, as real files carry.
	b.WriteString("LIST")
	_ = binary.Write(&b, le, uint32(5))
	b.WriteString("INFOx\x00") // odd size, padded
	b.WriteString("data")
	_ = binary.Write(&b, le, uint32(data.Len()))
	b.Write(data.Bytes())
	if data.Len()%2 == 1 {
		b.WriteByte(0)
	}
	var riff bytes.Buffer
	riff.WriteString("RIFF")
	_ = binary.Write(&riff, le, uint32(b.Len()))
	riff.Write(b.Bytes())
	return riff.Bytes()
}

// AIFF encodes frames as AIFF, or AIFC when f.Compression is set.
func AIFF(f Format, rate int, frames [][]float64) []byte {
	channels := 1
	if len(frames) > 0 {
		channels = len(frames[0])
	}
	var bo binary.ByteOrder = binary.BigEndian
	if f.Compression == "sowt" {
		bo = binary.LittleEndian
	}
	var data bytes.Buffer
	for _, fr := range frames {
		for _, v := range fr {
			putSample(&data, f, v, bo, false)
		}
	}
	be := binary.BigEndian
	var comm bytes.Buffer
	_ = binary.Write(&comm, be, int16(channels))
	_ = binary.Write(&comm, be, uint32(len(frames)))
	_ = binary.Write(&comm, be, int16(f.Bits))
	comm.Write(extended(float64(rate)))
	form := "AIFF"
	if f.Compression != "" {
		form = "AIFC"
		comm.WriteString(f.Compression)
		comm.Write([]byte{0, 0}) // empty pascal string, padded to even
	}
	var body bytes.Buffer
	body.WriteString(form)
	if form == "AIFC" {
		body.WriteString("FVER")
		_ = binary.Write(&body, be, uint32(4))
		_ = binary.Write(&body, be, uint32(0xA2805140))
	}
	body.WriteString("COMM")
	_ = binary.Write(&body, be, uint32(comm.Len()))
	body.Write(comm.Bytes())
	body.WriteString("SSND")
	_ = binary.Write(&body, be, uint32(8+data.Len()))
	_ = binary.Write(&body, be, uint32(0))
	_ = binary.Write(&body, be, uint32(0))
	body.Write(data.Bytes())
	if data.Len()%2 == 1 {
		body.WriteByte(0)
	}
	var b bytes.Buffer
	b.WriteString("FORM")
	_ = binary.Write(&b, be, uint32(body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}

func putSample(w *bytes.Buffer, f Format, v float64, bo binary.ByteOrder, wav bool) {
	v = max(-1, min(1, v))
	if f.Float {
		if f.Bits == 64 {
			_ = binary.Write(w, bo, math.Float64bits(v))
		} else {
			_ = binary.Write(w, bo, math.Float32bits(float32(v)))
		}
		return
	}
	full := float64(int64(1) << (f.Bits - 1))
	q := int64(math.Round(v * full))
	q = max(-int64(full), min(int64(full)-1, q))
	switch f.Bits {
	case 8:
		if wav {
			w.WriteByte(byte(q + 128))
		} else {
			w.WriteByte(byte(int8(q)))
		}
	case 16:
		_ = binary.Write(w, bo, int16(q))
	case 24:
		u := uint32(q)
		if bo == binary.ByteOrder(binary.BigEndian) {
			w.Write([]byte{byte(u >> 16), byte(u >> 8), byte(u)})
		} else {
			w.Write([]byte{byte(u), byte(u >> 8), byte(u >> 16)})
		}
	case 32:
		_ = binary.Write(w, bo, int32(q))
	}
}

// extended encodes a positive whole number as an 80-bit IEEE extended float.
func extended(f float64) []byte {
	out := make([]byte, 10)
	if f <= 0 {
		return out
	}
	exp := int(math.Floor(math.Log2(f)))
	mant := uint64(f * math.Ldexp(1, 63-exp))
	binary.BigEndian.PutUint16(out, uint16(exp+16383))
	binary.BigEndian.PutUint64(out[2:], mant)
	return out
}
