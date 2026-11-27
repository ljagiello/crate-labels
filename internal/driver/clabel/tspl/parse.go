package tspl

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Band is one BITMAP command of a parsed page.
type Band struct {
	X, Y        int
	BytesPerRow int
	Rows        int
	Mode        Mode
	Data        []byte // as sent: compressed for ModeDeflate
}

// Packed returns the band's packed rows, inflating ModeDeflate data.
func (b Band) Packed() ([]byte, error) {
	if b.Mode != ModeDeflate {
		return b.Data, nil
	}
	rows, err := io.ReadAll(flate.NewReader(bytes.NewReader(b.Data)))
	if err != nil {
		return nil, fmt.Errorf("tspl: band at y=%d does not inflate: %w", b.Y, err)
	}
	if len(rows) != b.BytesPerRow*b.Rows {
		return nil, fmt.Errorf("tspl: band at y=%d inflates to %d bytes, want %d", b.Y, len(rows), b.BytesPerRow*b.Rows)
	}
	return rows, nil
}

// Parsed is a page split back into its parts.
type Parsed struct {
	Header []string // the CRLF-terminated commands before the first BITMAP, without CRLF
	Bands  []Band
	Footer string // everything after the last band, e.g. "PRINT 1,1\r\n"
}

// Parse splits a TSPL page with BITMAP bands, such as one from Page.Encode, into header
// lines, bands and footer. It is meant for tests and dry-run inspection.
func Parse(page []byte) (*Parsed, error) {
	const bitmap = "BITMAP "
	p := &Parsed{}
	first := bytes.Index(page, []byte(bitmap))
	if first < 0 {
		return nil, fmt.Errorf("tspl: page has no BITMAP command")
	}
	head := string(page[:first])
	if !strings.HasSuffix(head, "\r\n") {
		return nil, fmt.Errorf("tspl: header does not end with CRLF")
	}
	p.Header = strings.Split(strings.TrimSuffix(head, "\r\n"), "\r\n")
	rest := page[first:]
	for bytes.HasPrefix(rest, []byte(bitmap)) {
		rest = rest[len(bitmap):]
		var nums [6]int
		for i := range nums {
			comma := bytes.IndexByte(rest, ',')
			if comma < 0 {
				return nil, fmt.Errorf("tspl: truncated BITMAP header")
			}
			n, err := strconv.Atoi(string(rest[:comma]))
			if err != nil {
				return nil, fmt.Errorf("tspl: bad BITMAP field %q", rest[:comma])
			}
			nums[i], rest = n, rest[comma+1:]
		}
		if nums[5] < 0 || nums[5] > len(rest) {
			return nil, fmt.Errorf("tspl: BITMAP at y=%d claims %d bytes, %d left", nums[1], nums[5], len(rest))
		}
		p.Bands = append(p.Bands, Band{X: nums[0], Y: nums[1], BytesPerRow: nums[2], Rows: nums[3],
			Mode: Mode(nums[4]), Data: rest[:nums[5]]})
		rest = rest[nums[5]:]
	}
	p.Footer = string(rest)
	return p, nil
}

// Raster joins the packed rows of every band.
func (p *Parsed) Raster() ([]byte, error) {
	var out []byte
	for _, b := range p.Bands {
		rows, err := b.Packed()
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}
