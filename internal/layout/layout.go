// Package layout turns a record's LabelData into label images: the templates (DJ labels,
// seller tags, the 7″ centre label, OBI strips and a calibration pattern), their options,
// the bundled fonts, text fitting and QR codes.
//
// Templates lay out in millimetres on one design system (grid.go): a margin and gutter
// derived from the label's size, a modular type scale, one text-fitting primitive
// (fit.go) and one badge primitive (badge.go). Sizes come from the canvas, so one
// template serves 203.2 dpi thermal heads and 300 dpi page printers. docs/LABELS.md
// describes the system and each template.
package layout

import (
	"image"
	"image/draw"
	"math"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// Track is one row of a release's tracklist. Its JSON form is docs/API.md's Track (see
// MarshalJSON and UnmarshalJSON).
type Track struct {
	Position string `json:"position"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Duration string `json:"duration"`
	BPM      int    `json:"bpm"` // 0 = unknown
	Key      string `json:"key"` // Camelot ("8A") or musical ("Am", "F#m", "Bb"); parsed leniently
	Included bool   `json:"included"`
}

// LabelData is everything a template may print about one record. Its JSON form is
// docs/API.md's LabelData (see MarshalJSON and UnmarshalJSON).
type LabelData struct {
	Artist    string   `json:"artist"`
	Title     string   `json:"title"`
	Label     string   `json:"label"`
	Catno     string   `json:"catno"`
	Year      string   `json:"year"`
	Country   string   `json:"country"`
	Format    string   `json:"format"` // a disc size as Discogs writes it ('12"', '7"', "25cm"), "LP" or ""
	Genres    []string `json:"genres"`
	Styles    []string `json:"styles"`
	Tracks    []Track  `json:"tracks"`
	Notes     string   `json:"notes"`
	Price     string   `json:"price"`     // seller tags, e.g. "12.99", "£15", "20 EUR"
	Condition string   `json:"condition"` // seller tags, e.g. "VG+", "NM / VG+"
	QRURL     string   `json:"qr_url"`    // Discogs release URL by default; "" = no QR
	ReleaseID int      `json:"release_id"`
}

// Template draws one kind of label.
type Template interface {
	ID() string
	Name() string
	Description() string
	// Supports reports whether the template can lay out on the stock's design size.
	Supports(s *printer.Stock) bool
	// Options lists the user-adjustable options with their defaults.
	Options() []OptionSpec
	// Render draws d on a white canvas of exactly c's size, in design orientation.
	Render(d *LabelData, c printer.Canvas, s *printer.Stock, opts map[string]any) (*image.Gray, error)
}

// TrackLabel is implemented by templates that print a fixed number of track rows. The UI
// uses it to pick the template that fits a record's tracklist.
type TrackLabel interface {
	MaxTracks() int
}

// MaxTracks is how many tracks t prints, or 0 for a template that is not track-based.
func MaxTracks(t Template) int {
	if tl, ok := t.(TrackLabel); ok {
		return tl.MaxTracks()
	}
	return 0
}

// registry lists the built-in templates in menu order.
var registry = []Template{
	djSingle{},
	newTwoTrack(),
	newFourTrack(),
	djTape{},
	sellerSmall{},
	sellerLarge{},
	centerLabel{},
	obiStrip{},
	testPattern{},
}

// Templates lists every template.
func Templates() []Template { return append([]Template(nil), registry...) }

// TemplatesFor lists the templates that support s.
func TemplatesFor(s *printer.Stock) []Template {
	var out []Template
	for _, t := range registry {
		if t.Supports(s) {
			out = append(out, t)
		}
	}
	return out
}

// Get looks a template up by id.
func Get(id string) (Template, error) {
	for _, t := range registry {
		if t.ID() == id {
			return t, nil
		}
	}
	ids := make([]string, len(registry))
	for i, t := range registry {
		ids[i] = t.ID()
	}
	return nil, printer.Errorf("Unknown label template %q (known: %s).", id, strings.Join(ids, ", "))
}

// Render draws one label with template id: grey, white = 255, exactly c's pixels, in
// design orientation. Options are coerced and defaulted first.
func Render(id string, d *LabelData, c printer.Canvas, s *printer.Stock, opts map[string]any) (*image.Gray, error) {
	t, err := Get(id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, printer.Errorf("No label stock given for template %q.", t.Name())
	}
	if !t.Supports(s) {
		return nil, printer.Errorf("The %s template does not fit %s. Pick another stock or template.", t.Name(), s.Name)
	}
	if c.Width <= 0 || c.Height <= 0 || c.DPI <= 0 {
		return nil, printer.Errorf("Cannot render a %d×%d px label at %g dpi.", c.Width, c.Height, c.DPI)
	}
	if d == nil {
		d = &LabelData{}
	}
	img, err := t.Render(d, c, s, CoerceOptions(t, opts))
	if err != nil {
		return nil, err
	}
	want := image.Rect(0, 0, c.Width, c.Height)
	if img.Rect == want {
		return img, nil
	}
	fixed := newCanvas(c.Width, c.Height).img
	draw.Draw(fixed, want, img, img.Rect.Min, draw.Src)
	return fixed, nil
}

// drawer is implemented by every built-in template: it lays d out on a sheet whose grid
// matches the canvas.
type drawer interface {
	draw(sh *sheet, g grid, d *LabelData, s *printer.Stock, o opts)
}

// sketch lays d out with template t on a fresh canvas and returns the sheet, with the
// marks tests inspect. Options are coerced first.
func sketch(t Template, d *LabelData, c printer.Canvas, s *printer.Stock, given map[string]any) *sheet {
	o := opts(CoerceOptions(t, given))
	sh := newSheet(newCanvas(c.Width, c.Height), dotsPerMM(c))
	w, h := canvasMM(c)
	if dr, ok := t.(drawer); ok {
		dr.draw(sh, newGrid(w, h, o.userScale()), d, s, o)
	}
	return sh
}

// dotsPerMM is the canvas resolution in px per mm.
func dotsPerMM(c printer.Canvas) float64 { return c.DPI / printer.MMPerInch }

// canvasMM is the canvas size in mm.
func canvasMM(c printer.Canvas) (float64, float64) {
	d := dotsPerMM(c)
	return float64(c.Width) / d, float64(c.Height) / d
}

// stockShape is a stock's design size as (long side, short side, long side across).
func stockShape(s *printer.Stock) (long, short float64, wide bool) {
	w, h := s.DesignMM[0], s.DesignMM[1]
	return math.Max(w, h), math.Min(w, h), w >= h
}
