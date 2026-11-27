package munbyn

import "strings"

// Payloads use the protobuf wire format. Each field is a key varint (field number << 3
// | wire type), then either a varint or a length-prefixed byte string.
const (
	wireVarint = 0
	wireBytes  = 2
)

// AppendVarint appends v as a protobuf varint: 7 bits per byte, low group first, and the
// high bit set on every byte but the last.
func AppendVarint(dst []byte, v uint64) []byte {
	for ; v > 0x7f; v >>= 7 {
		dst = append(dst, 0x80|byte(v&0x7f))
	}
	return append(dst, byte(v))
}

// Zigzag32 is protobuf's sint32 mapping: 0, -1, 1, -2 … become 0, 1, 2, 3 …
func Zigzag32(v int32) uint32 {
	if v < 0 {
		return uint32(-(int64(v)+1))<<1 | 1
	}
	return uint32(v) << 1
}

// Builder assembles a payload. Fields go out in call order. A zero number or an empty
// byte string is left out, as protobuf does for default values.
type Builder struct{ buf []byte }

func (b *Builder) key(field, wire int) {
	b.buf = AppendVarint(b.buf, uint64(field)<<3|uint64(wire))
}

// Uint adds an unsigned varint field.
func (b *Builder) Uint(field int, v uint32) *Builder {
	if v != 0 {
		b.key(field, wireVarint)
		b.buf = AppendVarint(b.buf, uint64(v))
	}
	return b
}

// Signed adds a zigzag varint field.
func (b *Builder) Signed(field int, v int32) *Builder {
	if v != 0 {
		b.key(field, wireVarint)
		b.buf = AppendVarint(b.buf, uint64(Zigzag32(v)))
	}
	return b
}

// Blob adds a length-delimited field.
func (b *Builder) Blob(field int, p []byte) *Builder {
	if len(p) != 0 {
		b.key(field, wireBytes)
		b.buf = AppendVarint(b.buf, uint64(len(p)))
		b.buf = append(b.buf, p...)
	}
	return b
}

// Payload returns the bytes built so far.
func (b *Builder) Payload() []byte { return b.buf }

// Field is one decoded field: a varint, or a byte string when IsBytes is set.
type Field struct {
	Num     uint64
	Bytes   []byte
	IsBytes bool
}

// Int32 is a varint field's low 32 bits as a signed number. A byte string gives 0.
func (f Field) Int32() int32 {
	if f.IsBytes {
		return 0
	}
	return int32(f.Num)
}

// Text is a byte-string field as UTF-8, with invalid sequences replaced. A varint gives "".
func (f Field) Text() string {
	if !f.IsBytes {
		return ""
	}
	return strings.ToValidUTF8(string(f.Bytes), "�")
}

// varintAt decodes the varint at buf[at:]. ok is false when it is cut short or needs more
// than 64 bits.
func varintAt(buf []byte, at int) (v uint64, next int, ok bool) {
	for shift := uint(0); at < len(buf); shift += 7 {
		if shift > 63 {
			return 0, 0, false
		}
		b := buf[at]
		at++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v, at, true
		}
	}
	return 0, 0, false
}

// DecodeFields maps each field number to its last value. Varints are returned raw (not
// zigzag-decoded). Decoding stops at a wire type other than varint or bytes, or at a
// value cut short. The fields read until then are kept.
func DecodeFields(buf []byte) map[int]Field {
	out := map[int]Field{}
	at := 0
	for at < len(buf) {
		key, next, ok := varintAt(buf, at)
		if !ok {
			break
		}
		field := int(key >> 3)
		switch key & 7 {
		case wireVarint:
			v, after, ok := varintAt(buf, next)
			if !ok {
				return out
			}
			out[field], at = Field{Num: v}, after
		case wireBytes:
			size, after, ok := varintAt(buf, next)
			if !ok || size > uint64(len(buf)-after) {
				return out
			}
			end := after + int(size)
			out[field], at = Field{Bytes: buf[after:end], IsBytes: true}, end
		default:
			return out
		}
	}
	return out
}
