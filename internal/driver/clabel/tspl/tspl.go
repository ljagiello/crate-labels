// Package tspl builds TSPL label pages from raster BITMAP bands. The Clabel driver uses
// it for its pages. It is a separate package so that a plain TSPL-over-TCP family could
// reuse it without the Clabel control protocol.
package tspl

import (
	"bytes"
	"compress/flate"
	"fmt"
	"strconv"
)

// Mode is the BITMAP mode field.
type Mode int

const (
	// ModeOverwrite is standard TSPL mode 0: raw packed rows follow the header.
	ModeOverwrite Mode = 0
	// ModeDeflate is mode 3, which the Clabel printers accept: each band is raw DEFLATE
	// (RFC 1951, no zlib header) of the packed rows, and the length field counts the
	// compressed bytes.
	ModeDeflate Mode = 3
)

// DefaultBandRows is the rows per BITMAP command. The Clabel printers have only been
// driven with 40-row bands.
const DefaultBandRows = 40

// Page describes one printed page. Lengths are whole tenths of a millimetre, so the
// page text is exact and needs no float formatting.
type Page struct {
	WidthTenths     int  // SIZE width
	HeightTenths    int  // SIZE height
	GapTenths       int  // GAP distance (0 for continuous media)
	GapOffsetTenths int  // GAP offset
	Density         int  // DENSITY
	Speed           int  // SPEED
	BandRows        int  // rows per BITMAP band; 0 means DefaultBandRows
	Mode            Mode // ModeDeflate for Clabel
	// Invert flips every raster bit before it is sent. The Clabel dialect takes
	// 1 = black; standard TSPL BITMAP data uses 0 = black.
	Invert bool
}

// Encode returns the page as one byte string:
//
//	SIZE, GAP, DIRECTION 0,0, REFERENCE 0,0, DENSITY, SPEED, CLS (each CRLF-terminated),
//	then BITMAP 0,y,bytesPerRow,rows,mode,length, followed directly by the band data
//	(no CRLF), then PRINT 1,1.
//
// raster is packed rows, MSB = leftmost dot.
func (p Page) Encode(raster []byte, bytesPerRow int) ([]byte, error) {
	if bytesPerRow <= 0 || len(raster) == 0 || len(raster)%bytesPerRow != 0 {
		return nil, fmt.Errorf("tspl: %d bytes are not whole rows of %d bytes", len(raster), bytesPerRow)
	}
	if p.Mode != ModeOverwrite && p.Mode != ModeDeflate {
		return nil, fmt.Errorf("tspl: unsupported BITMAP mode %d", p.Mode)
	}
	band := p.BandRows
	if band <= 0 {
		band = DefaultBandRows
	}
	var b bytes.Buffer
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\r\n", args...) }
	line("SIZE %s mm,%s mm", FormatTenths(p.WidthTenths), FormatTenths(p.HeightTenths))
	line("GAP %s mm,%s mm", trimmedTenths(p.GapTenths), trimmedTenths(p.GapOffsetTenths))
	line("DIRECTION 0,0")
	line("REFERENCE 0,0")
	line("DENSITY %d", p.Density)
	line("SPEED %d", p.Speed)
	line("CLS")
	rowCount := len(raster) / bytesPerRow
	for y := 0; y < rowCount; y += band {
		rows := min(band, rowCount-y)
		data := raster[y*bytesPerRow : (y+rows)*bytesPerRow]
		if p.Invert {
			data = inverted(data)
		}
		if p.Mode == ModeDeflate {
			var err error
			if data, err = RawDeflate(data); err != nil {
				return nil, err
			}
		}
		fmt.Fprintf(&b, "BITMAP 0,%d,%d,%d,%d,%d,", y, bytesPerRow, rows, p.Mode, len(data))
		b.Write(data)
	}
	line("PRINT 1,1")
	return b.Bytes(), nil
}

func inverted(band []byte) []byte {
	out := make([]byte, len(band))
	for i, v := range band {
		out[i] = ^v
	}
	return out
}

// RawDeflate compresses at the best level as raw RFC 1951 DEFLATE, with no zlib header
// or Adler-32. Other DEFLATE encoders give other bytes, but every valid stream inflates
// to the same rows.
func RawDeflate(data []byte) ([]byte, error) {
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, flate.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Tenths converts dots at dpi to the nearest tenth of a millimetre, halves rounding up.
func Tenths(dots, dpi int) int {
	num, den := 2*dots*254, 2*dpi // tenths = dots * 254 / dpi
	return (num + den/2) / den
}

// FormatTenths writes tenths of a millimetre with one decimal: 721 is "72.1".
func FormatTenths(t int) string {
	return strconv.Itoa(t/10) + "." + strconv.Itoa(t%10)
}

// trimmedTenths is FormatTenths without a ".0" (GAP is written this way): 20 is "2",
// 25 is "2.5".
func trimmedTenths(t int) string {
	s := FormatTenths(t)
	if t%10 == 0 {
		s = s[:len(s)-2]
	}
	return s
}
