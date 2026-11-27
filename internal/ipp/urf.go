// Apple raster ("URF", image/urf) for 8-bit grey pages.
//
// File layout, as OpenPrinting CUPS reads and writes Apple raster
// (cups/raster-stream.c): the eight octets "UNIRAST\0", a four-octet page count, then
// for each page a 32-octet header and the page bitmap. Header octets: 0 bits per pixel,
// 1 colour space (0 is sGray), 2 duplex (1 is one-sided), 3 print quality (the RFC 8011
// print-quality enum), 4 media type and 5 media position (0, automatic), 12-15 width,
// 16-19 height and 20-23 resolution in dpi, all big-endian; the rest are zero.
//
// The bitmap uses the line coding PWG 5102.4 section 4.4 defines for PWG Raster: every
// line starts with an octet holding its repetition count minus one (1-256 copies), then
// a series of codes. An octet c below 128 repeats the following pixel c+1 times; an octet
// c above 128 introduces 257-c literal pixels (2 to 128). PWG 5102.4 gives 128 no
// meaning, so the encoder never writes it and DecodeURF rejects it.

package ipp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
)

// MimeURF is the document-format of Apple raster.
const MimeURF = "image/urf"

const (
	urfMagic         = "UNIRAST\x00"
	urfHeaderSize    = 32
	urfGrey8         = 8 // bits per pixel
	urfSGray         = 0 // colour space code
	urfOneSided      = 1 // duplex code
	urfMaxCopies     = 256
	urfMaxRun        = 128
	urfMinRepeat     = 3 // shortest run written as a repeat code inside a line; see appendLine
	urfUndefinedCode = 128
)

// URFPage is one 8-bit grey page: 0 is black, 255 white, the same dpi on both axes.
type URFPage struct {
	Image   *image.Gray
	DPI     int
	Quality int // RFC 8011 print-quality: 3 draft, 4 normal, 5 high
}

func urfHeader(w, h, dpi, quality int) []byte {
	var hdr [urfHeaderSize]byte
	hdr[0] = urfGrey8
	hdr[1] = urfSGray
	hdr[2] = urfOneSided
	hdr[3] = byte(quality)
	binary.BigEndian.PutUint32(hdr[12:], uint32(w))
	binary.BigEndian.PutUint32(hdr[16:], uint32(h))
	binary.BigEndian.PutUint32(hdr[20:], uint32(dpi))
	return hdr[:]
}

// appendRepeat writes n copies of v as repeat codes of up to 128 pixels each.
func appendRepeat(dst []byte, v byte, n int) []byte {
	for n > 0 {
		k := min(n, urfMaxRun)
		dst = append(dst, byte(k-1), v)
		n -= k
	}
	return dst
}

// appendLiteral writes px as literal codes of up to 128 pixels. A piece of one pixel,
// which a literal code cannot hold, becomes a one-pixel repeat. A stretch that is just
// one or two equal pixels is written as a repeat too, the shorter form for it.
func appendLiteral(dst, px []byte) []byte {
	if len(px) <= 2 && bytes.Count(px, px[:1]) == len(px) {
		return appendRepeat(dst, px[0], len(px))
	}
	for len(px) > 0 {
		k := min(len(px), urfMaxRun)
		if k == 1 {
			return appendRepeat(dst, px[0], 1)
		}
		dst = append(dst, byte(257-k))
		dst = append(dst, px[:k]...)
		px = px[k:]
	}
	return dst
}

// appendLine codes one line. The line is read as runs of equal pixels. Runs of three
// or more become repeat codes; shorter runs are gathered into literals. A two-pixel
// repeat inside a literal costs as much as the two pixels themselves, plus a new
// literal code after it, so keeping it in the literal is never worse.
func appendLine(dst, px []byte) []byte {
	lit := 0 // start of the pixels gathered for the next literal
	for i := 0; i < len(px); {
		j := i + 1
		for j < len(px) && px[j] == px[i] {
			j++
		}
		if j-i >= urfMinRepeat {
			if lit < i {
				dst = appendLiteral(dst, px[lit:i])
			}
			dst = appendRepeat(dst, px[i], j-i)
			lit = j
		}
		i = j
	}
	if lit < len(px) {
		dst = appendLiteral(dst, px[lit:])
	}
	return dst
}

func grayLine(g *image.Gray, y int) []byte {
	b := g.Bounds()
	off := g.PixOffset(b.Min.X, y)
	return g.Pix[off : off+b.Dx()]
}

// AppendURFPage appends one page, header and bitmap, to dst. Equal neighbouring lines
// share one coded line with a repetition count.
func AppendURFPage(dst []byte, p URFPage) []byte {
	b := p.Image.Bounds()
	dst = append(dst, urfHeader(b.Dx(), b.Dy(), p.DPI, p.Quality)...)
	if b.Dy() == 0 {
		return dst
	}
	held, copies := grayLine(p.Image, b.Min.Y), 1
	for y := b.Min.Y + 1; y <= b.Max.Y; y++ {
		if y < b.Max.Y && copies < urfMaxCopies {
			if next := grayLine(p.Image, y); bytes.Equal(next, held) {
				copies++
				continue
			}
		}
		dst = append(dst, byte(copies-1))
		dst = appendLine(dst, held)
		if y < b.Max.Y {
			held, copies = grayLine(p.Image, y), 1
		}
	}
	return dst
}

// URFDocument puts pages already encoded by AppendURFPage into one document.
func URFDocument(pages [][]byte) []byte {
	size := len(urfMagic) + 4
	for _, p := range pages {
		size += len(p)
	}
	out := make([]byte, 0, size)
	out = append(out, urfMagic...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(pages)))
	for _, p := range pages {
		out = append(out, p...)
	}
	return out
}

// EncodeURF encodes pages as one document.
func EncodeURF(pages []URFPage) []byte {
	parts := make([][]byte, len(pages))
	for i, p := range pages {
		parts[i] = AppendURFPage(nil, p)
	}
	return URFDocument(parts)
}

// ErrURF wraps every DecodeURF error.
var ErrURF = errors.New("urf: malformed document")

func urfErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrURF, fmt.Sprintf(format, args...))
}

// DecodeURF reads a document of 8-bit sGray pages, for tests and debugging. It accepts
// exactly what PWG 5102.4 section 4.4 defines: every line must be filled by its codes
// without running past its end, and a repetition count must not run past the page.
func DecodeURF(data []byte) ([]URFPage, error) {
	if len(data) < len(urfMagic)+4 || !bytes.HasPrefix(data, []byte(urfMagic)) {
		return nil, urfErr("the document does not start with %q and a page count", urfMagic[:7])
	}
	r := &reader{data: data, pos: len(urfMagic)}
	cnt, _ := r.bytes(4)
	var pages []URFPage
	for n := binary.BigEndian.Uint32(cnt); n > 0; n-- {
		hdr, err := r.bytes(urfHeaderSize)
		if err != nil {
			return nil, urfErr("page %d: header cut short", len(pages)+1)
		}
		if hdr[0] != urfGrey8 || hdr[1] != urfSGray {
			return nil, urfErr("page %d: %d bits per pixel in colour space %d; only 8-bit sGray is read", len(pages)+1, hdr[0], hdr[1])
		}
		w := int(binary.BigEndian.Uint32(hdr[12:]))
		h := int(binary.BigEndian.Uint32(hdr[16:]))
		img := image.NewGray(image.Rect(0, 0, w, h))
		for y := 0; y < h; {
			copies, err := r.bytes(1)
			if err != nil {
				return nil, urfErr("page %d: bitmap ends at line %d of %d", len(pages)+1, y, h)
			}
			lines := int(copies[0]) + 1
			if y+lines > h {
				return nil, urfErr("page %d: line %d repeats past the last line", len(pages)+1, y)
			}
			line := img.Pix[y*w : (y+1)*w]
			if err := decodeLine(r, line); err != nil {
				return nil, urfErr("page %d, line %d: %v", len(pages)+1, y, err)
			}
			for k := 1; k < lines; k++ {
				copy(img.Pix[(y+k)*w:], line)
			}
			y += lines
		}
		pages = append(pages, URFPage{Image: img, DPI: int(binary.BigEndian.Uint32(hdr[20:])), Quality: int(hdr[3])})
	}
	if rest := len(data) - r.pos; rest != 0 {
		return nil, urfErr("%d octets follow the last page", rest)
	}
	return pages, nil
}

func decodeLine(r *reader, line []byte) error {
	for x := 0; x < len(line); {
		c, err := r.bytes(1)
		if err != nil {
			return errors.New("codes end before the line is full")
		}
		var px []byte
		switch code := int(c[0]); {
		case code == urfUndefinedCode:
			return errors.New("code 128 is not defined")
		case code < urfUndefinedCode:
			v, err := r.bytes(1)
			if err != nil {
				return errors.New("a repeat code has no pixel")
			}
			px = bytes.Repeat(v, code+1)
		default:
			if px, err = r.bytes(257 - code); err != nil {
				return errors.New("a literal is cut short")
			}
		}
		if x+len(px) > len(line) {
			return fmt.Errorf("a code runs %d pixels past the line end", x+len(px)-len(line))
		}
		x += copy(line[x:], px)
	}
	return nil
}
