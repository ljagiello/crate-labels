package munbyn

import (
	"fmt"

	"github.com/ljagiello/crate-labels/internal/heatshrink"
)

// Packaging sizes the printer sees.
const (
	sectionCap     = 4096  // raster bytes per heatshrink section, at most
	stripRows      = 8     // sections hold whole strips of this many rows
	PackageSize    = 200   // compressed bytes per DEVICE_PRINT package
	lowMemoryBelow = 10240 // field 11 is set when a label compresses to fewer bytes
)

// SectionRows is how many raster rows go into one section: as many whole 8-row strips
// as fit in 4096 bytes. On the 384-dot head that is 80 rows (3840 bytes).
func SectionRows(widthDots int) int {
	stripBytes := stripRows * widthDots / 8
	return stripRows * max(1, sectionCap/stripBytes)
}

// SectionBytes is the raster bytes per section for a widthDots raster.
func SectionBytes(widthDots int) int { return SectionRows(widthDots) * widthDots / 8 }

// devicePrint is the body of one DEVICE_PRINT package (command field 5).
type devicePrint struct {
	piece          []byte // up to PackageSize compressed bytes
	rasterBytes    int    // the whole label's raster size
	pieces, number int    // packages in this section, and this one's 1-based number
	rowBytes       int    // bytes per raster row; only the first package of a section has it
	sections       int    // sections in the label
	compressed     int    // this section's compressed size
	lowMemory      bool
	section        int // 1-based section number
}

// body lays the fields out in the order the printer is sent them: 1-8, 10, 11, 12.
func (p devicePrint) body() []byte {
	var b Builder
	b.Uint(1, 1).
		Blob(2, p.piece).
		Signed(3, int32(p.rasterBytes)).
		Uint(4, uint32(p.pieces)).
		Uint(5, uint32(p.number)).
		Uint(6, uint32(p.rowBytes)).
		Uint(7, uint32(p.sections)).
		Uint(8, 1).
		Signed(10, int32(p.compressed))
	if p.lowMemory {
		b.Uint(11, 1)
	}
	return b.Uint(12, uint32(p.section)).Payload()
}

// Sections compresses a packed raster (widthDots/8 bytes per row) section by section
// and cuts each section into framed DEVICE_PRINT packages. The result is grouped by
// section, in send order.
func Sections(raster []byte, widthDots int) ([][][]byte, error) {
	if widthDots <= 0 || widthDots%8 != 0 || len(raster) == 0 || len(raster)%(widthDots/8) != 0 {
		return nil, fmt.Errorf("munbyn: %d raster bytes are not whole rows of %d dots", len(raster), widthDots)
	}
	step := SectionBytes(widthDots)
	var streams [][]byte
	total := 0
	for start := 0; start < len(raster); start += step {
		s := heatshrink.Encode(raster[start:min(start+step, len(raster))])
		streams = append(streams, s)
		total += len(s)
	}
	out := make([][][]byte, len(streams))
	for si, s := range streams {
		count := (len(s) + PackageSize - 1) / PackageSize
		for k := range count {
			p := devicePrint{
				piece:       s[k*PackageSize : min((k+1)*PackageSize, len(s))],
				rasterBytes: len(raster), pieces: count, number: k + 1,
				sections: len(streams), compressed: len(s), lowMemory: total < lowMemoryBelow,
				section: si + 1,
			}
			if k == 0 {
				p.rowBytes = widthDots / 8
			}
			frame, err := Envelope(EventDevicePrint, 0, p.body())
			if err != nil {
				return nil, err
			}
			out[si] = append(out[si], frame)
		}
	}
	return out, nil
}
