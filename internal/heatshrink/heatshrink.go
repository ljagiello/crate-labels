// Package heatshrink reads and writes the heatshrink LZSS stream that the Munbyn FM226
// takes for raster data, with an 11-bit window and a 4-bit length field.
//
// The format is the heatshrink library's (github.com/atomicobject/heatshrink). A
// stream is a run of tokens. Each token starts with a tag bit:
//
//   - 1: a literal; the next 8 bits are the byte.
//   - 0: a back-reference; distance-1 follows in WindowBits bits, then length-1 in
//     LookaheadBits bits. It copies length bytes from distance bytes back in the
//     output, and the copy may run into the bytes it is producing.
//
// Bits are packed MSB-first, and zero bits pad the last byte. There is no header and no
// length prefix.
//
// [Encode] is our encoder. [Decoder] ports the library's decoder.
package heatshrink

// Stream parameters of the FM226 path.
const (
	WindowBits    = 11 // back-reference distance bits: distances 1..2048
	LookaheadBits = 4  // back-reference length bits: lengths 1..16
)

// Token costs and limits that follow from the parameters.
const (
	maxDistance   = 1 << WindowBits
	maxLength     = 1 << LookaheadBits
	literalBits   = 1 + 8
	referenceBits = 1 + WindowBits + LookaheadBits

	// MinLength is the shortest back-reference Encode emits. A reference is used only
	// when it is longer than referenceBits/8 bytes: the break-even rule of the heatshrink
	// library's own encoder (3 bytes for 11/4). A 2-byte reference would save 2 bits.
	// We skip it so that we emit only token shapes a stock encoder also emits.
	MinLength = referenceBits/8 + 1
)

// Encode compresses src into one self-contained stream. Each call is independent: the
// Munbyn driver encodes every section on its own.
//
// Encode picks the smallest stream that this token set allows. It first finds, for every
// position, the longest earlier match within the window. Every shorter
// prefix of that match is also a valid reference, and every token has a fixed cost, so
// the cheapest token sequence is a shortest path. We solve it backwards from the end of
// the input. A back-reference never reaches before the first input byte, even though
// the decoder's zero-filled window would make that decode correctly. So the stream does
// not depend on how a decoder initialises its window, and the first token is always a
// literal.
func Encode(src []byte) []byte {
	n := len(src)
	if n == 0 {
		return []byte{}
	}
	lengths, distances := findMatches(src)

	// bits[i] is the cost of encoding src[i:], and take[i] is the length of the first
	// token of that cheapest encoding (1 for a literal).
	bits := make([]int32, n+1)
	take := make([]uint8, n)
	for i := n - 1; i >= 0; i-- {
		best, size := int32(-1), uint8(1)
		for l := int(lengths[i]); l >= MinLength; l-- {
			if c := bits[i+l] + referenceBits; best < 0 || c < best {
				best, size = c, uint8(l)
			}
		}
		if c := bits[i+1] + literalBits; best < 0 || c < best {
			best, size = c, 1
		}
		bits[i], take[i] = best, size
	}

	p := packer{buf: make([]byte, 0, (bits[0]+7)/8)}
	for i := 0; i < n; i += int(take[i]) {
		if take[i] == 1 {
			p.put(1, 1)
			p.put(uint32(src[i]), 8)
			continue
		}
		p.put(0, 1)
		p.put(uint32(distances[i]-1), WindowBits)
		p.put(uint32(take[i]-1), LookaheadBits)
	}
	return p.buf
}

// findMatches returns, for every position, the longest match (capped at maxLength) that
// starts between 1 and maxDistance bytes earlier, and its distance. Positions with no
// match of at least MinLength bytes get length 0.
func findMatches(src []byte) (lengths []uint8, distances []uint16) {
	n := len(src)
	lengths = make([]uint8, n)
	distances = make([]uint16, n)
	f := newMatchFinder(src)
	for i := 0; i+MinLength <= n; i++ {
		lengths[i], distances[i] = f.longestAt(i)
		f.insert(i)
	}
	return lengths, distances
}

// matchFinder indexes earlier positions by a hash of their first MinLength bytes. Each
// bucket holds a chain from the newest position to older ones.
type matchFinder struct {
	src   []byte
	first [1 << hashBits]int32 // newest position per bucket, or -1
	older []int32              // next older position in the same bucket, or -1
}

const hashBits = 12

func newMatchFinder(src []byte) *matchFinder {
	f := &matchFinder{src: src, older: make([]int32, len(src))}
	for i := range f.first {
		f.first[i] = -1
	}
	return f
}

func (f *matchFinder) bucket(i int) uint32 {
	key := uint32(f.src[i])<<16 | uint32(f.src[i+1])<<8 | uint32(f.src[i+2])
	return key * 0x9e3779b1 >> (32 - hashBits)
}

func (f *matchFinder) insert(i int) {
	b := f.bucket(i)
	f.older[i] = f.first[b]
	f.first[b] = int32(i)
}

// longestAt walks the chain for i inside the window. Every candidate is checked byte by
// byte, so a hash collision only costs time.
func (f *matchFinder) longestAt(i int) (length uint8, distance uint16) {
	limit := min(maxLength, len(f.src)-i)
	want := f.src[i : i+limit]
	for c := f.first[f.bucket(i)]; c >= 0 && i-int(c) <= maxDistance; c = f.older[c] {
		// The candidate may overlap want; the bytes are all known, so a plain
		// comparison gives the length the decoder's overlapping copy reproduces.
		have := f.src[c:]
		k := 0
		for k < limit && have[k] == want[k] {
			k++
		}
		if k >= MinLength && k > int(length) {
			length, distance = uint8(k), uint16(i-int(c))
			if k == limit {
				break
			}
		}
	}
	return length, distance
}

// packer appends bit fields MSB-first. Unused low bits of the last byte stay zero,
// which is the stream's padding.
type packer struct {
	buf  []byte
	used uint // bits used in the last byte, 0..7 (0: start a new byte)
}

func (p *packer) put(v uint32, width uint) {
	for width > 0 {
		if p.used == 0 {
			p.buf = append(p.buf, 0)
		}
		free := 8 - p.used
		n := min(free, width)
		chunk := v >> (width - n) & (1<<n - 1)
		p.buf[len(p.buf)-1] |= byte(chunk << (free - n))
		width -= n
		p.used = (p.used + n) % 8
	}
}
