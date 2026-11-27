package layout

import (
	"fmt"
	"math"
	"testing"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// Layout properties every template keeps on every stock it supports, in every font,
// at both ends of the text-size range, for full, stressed, blank and tempo-less records:
//
//   - content (text, badges, codes) stays inside the label's safe area;
//   - no two content elements overlap;
//   - no text is set below minTextMM;
//   - every line is at least minStrokeMM;
//   - every QR module is at least the module floor.

// propertyRecords are the samples plus a blank record and one with no tempos or keys.
func propertyRecords() map[string]*LabelData {
	out := map[string]*LabelData{"blank": {}}
	for _, n := range SampleNames {
		out[n] = Sample(n)
	}
	plain := Sample("ep12")
	for i := range plain.Tracks {
		plain.Tracks[i].BPM, plain.Tracks[i].Key = 0, ""
	}
	plain.Price, plain.Condition = "", ""
	out["plain"] = plain
	return out
}

// safeArea is where a template's content must stay: the grid's content area, except on
// the centre label (the disc and sticker set their own margins inside the canvas) and the
// OBI strip (each panel has its own margin).
func safeArea(t Template, c printer.Canvas, userScale float64) rect {
	w, h := canvasMM(c)
	switch t.ID() {
	case "center-label":
		return rect{0, 0, w, h}
	case "obi-strip":
		return rect{0, 0, w, h}.inset(marginMin, marginMin)
	}
	return newGrid(w, h, userScale).content
}

const slack = 0.05 // mm: rounding to whole pixels moves edges by up to half a pixel

func checkMarks(t *testing.T, label string, tmpl Template, c printer.Canvas, sh *sheet, userScale float64) {
	t.Helper()
	safe := safeArea(tmpl, c, userScale)
	var content []mark
	for _, m := range sh.marks {
		switch {
		case m.kind.content():
			content = append(content, m)
			if !safe.contains(m.box, slack) {
				t.Errorf("%s: %s %q at %+v leaves the safe area %+v", label, m.name, m.text, m.box, safe)
			}
		case m.kind == markLine && m.stroke > 0 && m.stroke < minStrokeMM-1e-9:
			t.Errorf("%s: line %s is %.3f mm", label, m.name, m.stroke)
		}
		if m.kind == markText || m.kind == markBadge {
			if m.size < minTextMM-1e-9 {
				t.Errorf("%s: %s %q set at %.2f mm", label, m.name, m.text, m.size)
			}
		}
		if m.kind == markCode && m.module < minModulePx(sh.dpmm) {
			t.Errorf("%s: QR module %d px", label, m.module)
		}
	}
	for i, a := range content {
		for _, b := range content[i+1:] {
			if a.box.inset(slack, slack).overlaps(b.box.inset(slack, slack)) {
				t.Errorf("%s: %s %q %+v overlaps %s %q %+v", label, a.name, a.text, a.box, b.name, b.text, b.box)
			}
		}
	}
}

func TestLayoutProperties(t *testing.T) {
	cat := testCatalog(t)
	records := propertyRecords()
	for _, tmpl := range Templates() {
		if tmpl.ID() == "test-pattern" {
			continue // a calibration print, edge to edge on purpose
		}
		for _, s := range stocks(cat) {
			if !tmpl.Supports(s) {
				continue
			}
			for _, dpi := range sampleDPIs {
				c := canvasFor(s, dpi)
				for _, fam := range FontFamilies() {
					if dpi != sampleDPIs[0] && fam.ID != DefaultFontFamily {
						continue // every family at 203.2 dpi; the default one at both
					}
					for _, scale := range []float64{fontScaleMin, 1, fontScaleMax} {
						for name, d := range records {
							o := map[string]any{"font": fam.ID, "font_scale": scale}
							label := fmt.Sprintf("%s/%s/%g/%s/%.1f/%s", tmpl.ID(), s.ID, dpi, fam.ID, scale, name)
							checkMarks(t, label, tmpl, c, sketch(tmpl, d, c, s, o), scale)
						}
					}
				}
			}
		}
	}
}

// TestLayoutOptionsKeepProperties runs the property check over each choice of every
// choice and bool option, one at a time, on the template's first stock.
func TestLayoutOptionsKeepProperties(t *testing.T) {
	cat := testCatalog(t)
	for _, tmpl := range Templates() {
		var s *printer.Stock
		for _, st := range stocks(cat) {
			if tmpl.Supports(st) {
				s = st
				break
			}
		}
		if s == nil || tmpl.ID() == "test-pattern" {
			continue
		}
		c := canvasFor(s, 203.2)
		for _, spec := range tmpl.Options() {
			var values []any
			switch spec.Type {
			case OptionChoice:
				for _, ch := range spec.Choices {
					values = append(values, ch)
				}
			case OptionBool:
				values = []any{true, false}
			case OptionText:
				values = []any{"B", "2", "Shop Name That Is Rather Long Indeed"}
			}
			for _, v := range values {
				for _, n := range SampleNames {
					label := fmt.Sprintf("%s/%s=%v/%s", tmpl.ID(), spec.ID, v, n)
					checkMarks(t, label, tmpl, c, sketch(tmpl, Sample(n), c, s, map[string]any{spec.ID: v}), 1)
				}
			}
		}
	}
}

// TestSameLinesAtBothResolutions checks that layout happens in millimetres: without a QR
// code (whose size snaps to whole modules), 203.2 and 300 dpi set the same words on the
// same lines at sizes within a pixel.
func TestSameLinesAtBothResolutions(t *testing.T) {
	cat := testCatalog(t)
	for _, tmpl := range Templates() {
		if tmpl.ID() == "test-pattern" {
			continue
		}
		for _, s := range stocks(cat) {
			if !tmpl.Supports(s) {
				continue
			}
			for _, n := range SampleNames {
				o := map[string]any{"show_qr": false}
				lo := sketch(tmpl, Sample(n), canvasFor(s, 203.2), s, o)
				hi := sketch(tmpl, Sample(n), canvasFor(s, 300), s, o)
				texts := func(sh *sheet) map[string]mark {
					out := map[string]mark{}
					for _, m := range sh.marks {
						if m.kind == markText && m.name != "" {
							out[m.name] = m
						}
					}
					return out
				}
				a, b := texts(lo), texts(hi)
				for name, m := range a {
					o, ok := b[name]
					if !ok {
						t.Errorf("%s/%s/%s: %s only at 203.2 dpi", tmpl.ID(), s.ID, n, name)
						continue
					}
					if m.text != o.text || math.Abs(m.size-o.size) > 25.4/203.2 {
						t.Errorf("%s/%s/%s: %s is %q at %.2f mm and %q at %.2f mm", tmpl.ID(), s.ID, n, name, m.text, m.size, o.text, o.size)
					}
				}
			}
		}
	}
}
