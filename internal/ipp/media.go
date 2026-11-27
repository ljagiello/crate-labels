// Media: reading media-col entries, choosing one for a page, putting the page on it,
// and the media-col job attribute that asks for it.
//
// media-col and its members are PWG 5100.7; sizes are in hundredths of a millimetre. A
// dimension is either an integer or a rangeOfInteger, the latter for rolls and custom
// sizes. The job's media-col repeats the printer's own entry (see MediaCol), limited to
// the members the IANA IPP registry lists for the media-col Job Template attribute.

package ipp

import (
	"errors"
	"image"
	"math"
	"slices"
	"strconv"

	"github.com/ljagiello/crate-labels/internal/sheets"
)

const hundredthsPerInch = 2540

func hundredthsToDots(h, dpi int) int {
	return int(math.Round(float64(h) * float64(dpi) / hundredthsPerInch))
}

func mmToHundredths(mm float64) int { return int(math.Round(mm * 100)) }

// Extent is one media dimension: fixed (Min == Max) or a range the printer accepts.
type Extent struct{ Min, Max int }

// Fixed reports a single value.
func (e Extent) Fixed() bool { return e.Min == e.Max }

// nearest is the value in e closest to want.
func (e Extent) nearest(want int) int { return max(e.Min, min(e.Max, want)) }

func extentOf(v any) (Extent, bool) {
	switch d := v.(type) {
	case int:
		if d == 0 { // a roll of unstated length
			return Extent{1, math.MaxInt32}, true
		}
		return Extent{d, d}, d > 0
	case Range:
		return Extent{d.Lower, d.Upper}, d.Lower > 0 && d.Upper >= d.Lower
	}
	return Extent{}, false
}

// Media is one media-col-ready or media-col-database entry with a stated size.
type Media struct {
	Col    Collection // the entry as the printer sent it
	Loaded bool       // from media-col-ready
	X, Y   Extent     // media-size x- and y-dimension
}

// MediaFrom reads an entry; ok is false when it states no usable media-size.
func MediaFrom(c Collection, loaded bool) (*Media, bool) {
	size, isCol := c.First("media-size").(Collection)
	if !isCol {
		return nil, false
	}
	x, okX := extentOf(size.First("x-dimension"))
	y, okY := extentOf(size.First("y-dimension"))
	if !okX || !okY {
		return nil, false
	}
	return &Media{Col: c, Loaded: loaded, X: x, Y: y}, true
}

func (m *Media) text(member string) string {
	if s := m.Col.Get(member).Strings(); len(s) > 0 {
		return s[0]
	}
	return ""
}

func (m *Media) integer(member string) int {
	if n, ok := m.Col.First(member).(int); ok {
		return n
	}
	return 0
}

// Margins returns the left, top, right and bottom margins; missing ones are 0.
func (m *Media) Margins() (left, top, right, bottom int) {
	return m.integer("media-left-margin"), m.integer("media-top-margin"),
		m.integer("media-right-margin"), m.integer("media-bottom-margin")
}

func mmText(h int) string { return strconv.FormatFloat(float64(h)/100, 'f', -1, 64) }

func (e Extent) describe() string {
	if e.Fixed() {
		return mmText(e.Min)
	}
	if e.Max >= math.MaxInt32 {
		return mmText(e.Min) + "+"
	}
	return mmText(e.Min) + "–" + mmText(e.Max)
}

// Describe is a short description such as "215.9×279.4 mm in tray-1".
func (m *Media) Describe() string {
	s := m.X.describe() + "×" + m.Y.describe() + " mm"
	if name := m.text("media-size-name"); name != "" {
		s = name + " (" + s + ")"
	}
	if src := m.text("media-source"); src != "" {
		s += " in " + src
	}
	return s
}

// Choice is the media picked for a page.
type Choice struct {
	Media  *Media
	Size   [2]int // the x- and y-dimension to ask for, in hundredths of a millimetre
	Turned bool   // the page goes on the media turned 90° clockwise
	Exact  bool   // Size matches the page within sheets.SizeToleranceMM
}

// ChooseMedia picks media for a page of pageMM (width, height). Every listed entry is
// tried both ways round, and kept when the page fits on it; ranged dimensions take the
// page's own size where they can. The best is an exact size, then the one that wastes
// the least area, then the page unturned, then the printer's order. exactOnly keeps
// only exact sizes; it is forced on when the printer does not say what is loaded, so a
// job never asks for media merely because the database knows it. nil means none fits.
func ChooseMedia(p *Printer, pageMM [2]float64, exactOnly bool) *Choice {
	media, loaded := p.Media()
	return choose(media, pageMM, exactOnly || !loaded)
}

func choose(media []*Media, pageMM [2]float64, exactOnly bool) *Choice {
	tol := mmToHundredths(sheets.SizeToleranceMM)
	pw, ph := mmToHundredths(pageMM[0]), mmToHundredths(pageMM[1])
	type scored struct {
		c     Choice
		waste int64
	}
	var best *scored
	better := func(a, b *scored) bool {
		if a.c.Exact != b.c.Exact {
			return a.c.Exact
		}
		if a.waste != b.waste {
			return a.waste < b.waste
		}
		return !a.c.Turned && b.c.Turned
	}
	for _, m := range media {
		for _, turned := range []bool{false, true} {
			w, h := pw, ph
			if turned {
				w, h = ph, pw
			}
			x, y := m.X.nearest(w), m.Y.nearest(h)
			if x < w-tol || y < h-tol {
				continue
			}
			exact := abs(x-w) <= tol && abs(y-h) <= tol
			if exactOnly && !exact {
				continue
			}
			s := &scored{Choice{Media: m, Size: [2]int{x, y}, Turned: turned, Exact: exact},
				max(0, int64(x)*int64(y)-int64(w)*int64(h))}
			if best == nil || better(s, best) {
				best = s
			}
		}
	}
	if best == nil {
		return nil
	}
	return &best.c
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ErrEmptyPage reports a page with no pixels.
var ErrEmptyPage = errors.New("the page is empty")

// Place draws page, rendered at sourceDPI, on a white sheet of the chosen size at dpi,
// at actual size and centred, turned clockwise when the choice says so.
func Place(page *image.Gray, c *Choice, dpi int, sourceDPI float64) (*image.Gray, error) {
	b := page.Bounds()
	if b.Empty() {
		return nil, ErrEmptyPage
	}
	if sourceDPI <= 0 {
		sourceDPI = float64(dpi)
	}
	f := float64(dpi) / sourceDPI
	img := sheets.Resize(page, max(1, int(math.Round(float64(b.Dx())*f))), max(1, int(math.Round(float64(b.Dy())*f))))
	if c.Turned {
		img = sheets.Rotate(img, 90)
	}
	sheet := sheets.White(hundredthsToDots(c.Size[0], dpi), hundredthsToDots(c.Size[1], dpi))
	sheets.PasteCentred(sheet, img)
	return sheet, nil
}

// templateMembers are the media-col members the IANA IPP registry defines for the
// media-col Job Template attribute. Entries of media-col-ready and -database can hold
// more, such as media-source-properties, which describes a tray and is not a request.
var templateMembers = []string{
	"media-back-coating", "media-bottom-margin", "media-color", "media-front-coating",
	"media-grain", "media-hole-count", "media-info", "media-key", "media-left-margin",
	"media-order-count", "media-pre-printed", "media-recycled", "media-right-margin",
	"media-size", "media-size-name", "media-source", "media-thickness", "media-tooth",
	"media-top-margin", "media-top-offset", "media-tracking", "media-type",
	"media-weight-metric",
}

// MediaCol is the media-col job attribute for a choice: the chosen entry's own members,
// in the printer's order, keeping those that are Job Template members and that
// media-col-supported lists (all of them when it lists none). When a dimension was a
// range, media-size is rewritten with the size picked and media-size-name, which named
// the range, is left out.
func MediaCol(c *Choice, supported []string) Attribute {
	ranged := !c.Media.X.Fixed() || !c.Media.Y.Fixed()
	var members []Attribute
	for _, a := range c.Media.Col {
		switch {
		case !slices.Contains(templateMembers, a.Name),
			len(supported) > 0 && !slices.Contains(supported, a.Name),
			ranged && a.Name == "media-size-name":
			continue
		case ranged && a.Name == "media-size":
			a = NewCollection("media-size", Integer("x-dimension", c.Size[0]), Integer("y-dimension", c.Size[1]))
		}
		members = append(members, a)
	}
	return NewCollection("media-col", members...)
}
