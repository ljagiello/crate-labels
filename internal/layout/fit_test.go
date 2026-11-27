package layout

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func plex() faces { return familyFaces(DefaultFontFamily) }

func TestFitTakesTheLargestSizeThatFits(t *testing.T) {
	f := plex().regular
	// Fits at max: kept at max.
	b := fitText("Low Orbit", 100, 20, fitSpec{face: f, max: 5, min: 2, lines: 1})
	if b.size != 5 || len(b.lines) != 1 {
		t.Fatalf("short text: size %v lines %q", b.size, b.lines)
	}
	// Needs shrinking: the result fits and one precision step larger does not.
	w := f.width("Carrier Wave Night Mix", 4) * 0.8
	b = fitText("Carrier Wave Night Mix", w, 20, fitSpec{face: f, max: 4, min: 1.6, lines: 1})
	if len(b.lines) != 1 || b.inkWidth() > w+1e-9 {
		t.Fatalf("shrunk text overflows: %v %q", b.size, b.lines)
	}
	if f.width("Carrier Wave Night Mix", b.size+fitPrecision) <= w {
		t.Errorf("size %.3f is not the largest that fits", b.size)
	}
	// Two lines allowed: it wraps at a larger size instead of shrinking.
	two := fitText("Carrier Wave Night Mix", w, 20, fitSpec{face: f, max: 4, min: 1.6, lines: 2})
	if len(two.lines) != 2 || two.size <= b.size {
		t.Errorf("two lines: size %.2f lines %q (one line was %.2f)", two.size, two.lines, b.size)
	}
	// Height limits the size too.
	low := fitText("Ferrous", 100, blockHeight(f, 3, 1), fitSpec{face: f, max: 8, min: 1.6, lines: 1})
	if math.Abs(low.size-3) > fitPrecision || low.height() > blockHeight(f, 3, 1)+1e-9 {
		t.Errorf("height-bound size %.3f", low.size)
	}
}

func TestFitCutsWhatDoesNotFitAtTheFloor(t *testing.T) {
	f := plex().regular
	text := "Notes From A Very Long Drive Through The Northern Provinces"
	b := fitText(text, 20, 100, fitSpec{face: f, max: 3, min: 2, lines: 2})
	if b.size != 2 || len(b.lines) != 2 || !strings.HasSuffix(b.lines[1], ellipsis) {
		t.Fatalf("overflow: size %v lines %q", b.size, b.lines)
	}
	for _, l := range b.lines {
		if f.width(l, b.size) > 20+1e-9 {
			t.Errorf("line %q is wider than the lane", l)
		}
	}
	// A word longer than the lane is broken rather than left to overflow.
	long := fitText("Overunderthroughbetweenaroundandbackagain", 15, 100, fitSpec{face: f, max: 3, min: 2, lines: 3})
	for _, l := range long.lines {
		if f.width(l, long.size) > 15+1e-9 {
			t.Errorf("broken word line %q overflows", l)
		}
	}
	// The height wins over the line limit.
	short := fitText(text, 20, blockHeight(f, 2, 1), fitSpec{face: f, max: 3, min: 2, lines: 4})
	if len(short.lines) != 1 || !strings.HasSuffix(short.lines[0], ellipsis) {
		t.Errorf("height-limited overflow %q", short.lines)
	}
	if b := fitText("  ", 20, 20, fitSpec{face: f, max: 3, min: 2, lines: 1}); !b.empty() {
		t.Errorf("blank text set as %q", b.lines)
	}
}

func TestWithEllipsis(t *testing.T) {
	f := plex().regular
	if got := withEllipsis(f, "Ferrous", 3, 100); got != "Ferrous" {
		t.Errorf("fitting text cut: %q", got)
	}
	got := withEllipsis(f, "Static Bloom Extended Version", 3, f.width("Static Bloom Ext", 3))
	if !strings.HasSuffix(got, ellipsis) || f.width(got, 3) > f.width("Static Bloom Ext", 3)+1e-9 {
		t.Errorf("cut %q", got)
	}
	if got := withEllipsis(f, "Ferrous", 3, 0.1); got != ellipsis {
		t.Errorf("nothing fits: %q", got)
	}
}

func TestGridRule(t *testing.T) {
	cases := []struct{ w, h, margin, gutter float64 }{
		{40, 30, 2, 1},
		{48, 30, 2, 1},
		{80, 48, 3.5, 1.75},
		{70, 80, 5, 2.5},
		{50, 12, 1.5, 1},
		{101.6, 152.4, 6, 3},
	}
	for _, c := range cases {
		g := newGrid(c.w, c.h, 1)
		if g.margin != c.margin || g.gutter != c.gutter {
			t.Errorf("%gx%g: margin %g gutter %g, want %g %g", c.w, c.h, g.margin, g.gutter, c.margin, c.gutter)
		}
		if g.content.x != g.margin || math.Abs(g.content.right()-(c.w-g.margin)) > 1e-9 {
			t.Errorf("%gx%g: content %+v", c.w, c.h, g.content)
		}
	}
	s := newTypeScale(30, 1)
	if s.step(0) != typeBase || math.Abs(s.step(1)/s.step(0)-typeRatio) > 1e-12 {
		t.Errorf("type scale %v %v", s.step(0), s.step(1))
	}
	if s.downTo(-3) != minTextMM {
		t.Errorf("floor %v", s.downTo(-3))
	}
	if newTypeScale(30, fontScaleMax).upTo(2) <= s.upTo(2) {
		t.Error("font_scale does not raise the ceiling")
	}
}

func TestRectCuts(t *testing.T) {
	r := rect{0, 0, 40, 30}
	top, rest := r.cutTop(10, 2)
	if top != (rect{0, 0, 40, 10}) || rest != (rect{0, 12, 40, 18}) {
		t.Errorf("cutTop %+v %+v", top, rest)
	}
	right, rest := r.cutRight(10, 1)
	if right != (rect{30, 0, 10, 30}) || rest != (rect{0, 0, 29, 30}) {
		t.Errorf("cutRight %+v %+v", right, rest)
	}
	rows := r.rows(3, 0)
	if len(rows) != 3 || rows[2].bottom() != 30 {
		t.Errorf("rows %+v", rows)
	}
	if got := chordRect(0, 0, 5, -3, 0); math.Abs(got.w-8) > 1e-9 {
		t.Errorf("chord at 3 of radius 5: %+v", got)
	}
	if got := chordRect(0, 0, 5, -6, -5.5); got.w != 0 {
		t.Errorf("chord outside the circle: %+v", got)
	}
}

func TestBadgeFit(t *testing.T) {
	f := plex().bold
	texts := []string{"132", "10B"}
	c := fitBadges(f, texts, 40, 8, 1, 100)
	if c <= 0 || badgeHeightPerCap*c > 8+1e-9 || badgeRowWidth(f, texts, c, 1) > 40+1e-9 {
		t.Fatalf("badges at cap %.3f: %.2f wide", c, badgeRowWidth(f, texts, c, 1))
	}
	if narrow := fitBadges(f, texts, 4, 8, 1, 100); narrow != 0 {
		t.Errorf("badges below the floor: cap %.3f", narrow)
	}
	if got := fitBadges(f, []string{"", ""}, 40, 8, 1, 100); got != 0 {
		t.Errorf("empty badges: %v", got)
	}
	// A badge is never narrower than it is tall.
	if w := badgeWidthPerCap(f, "1"); w < badgeHeightPerCap {
		t.Errorf("one-digit badge %.2f wide per cap", w)
	}
}

// TestEveryPrintedRuneHasAGlyph checks that each family draws every character the
// templates and samples can print, from the face itself, the cover family or the drawn
// accidentals.
func TestEveryPrintedRuneHasAGlyph(t *testing.T) {
	printed := "♯♭⅓″·…→←×°€£¥ł–—’ " + "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz$&()+,-./:;'\""
	for _, n := range SampleNames {
		d := Sample(n)
		printed += d.Artist + d.Title + d.Label + d.Catno + d.Country + d.Condition + d.Price
		for _, tr := range d.Tracks {
			printed += tr.Title
		}
	}
	for code := 1; code <= 12; code++ {
		for _, l := range []string{"A", "B"} {
			printed += musicalName(strconv.Itoa(code) + l)
		}
	}
	for _, fam := range FontFamilies() {
		fc := familyFaces(fam.ID)
		for _, f := range []*face{fc.regular, fc.bold} {
			for _, r := range printed {
				if !f.has(r) {
					t.Errorf("%s has no glyph for %q", f, r)
				}
			}
			if f.advance('♯') <= 0 || len(f.outline('♭')) == 0 {
				t.Errorf("%s: drawn accidentals missing", f)
			}
		}
	}
}

func TestFontsDrawEveryFamily(t *testing.T) {
	for _, fam := range FontFamilies() {
		fc := familyFaces(fam.ID)
		for _, f := range []*face{fc.regular, fc.bold} {
			if f.capHeight <= 0.5 || f.capHeight >= 0.9 || f.ascent <= 0 || f.descent <= 0 {
				t.Errorf("%s metrics: cap %.3f ascent %.3f descent %.3f", f, f.capHeight, f.ascent, f.descent)
			}
			cv := newCanvas(200, 60)
			sh := newSheet(cv, 8)
			sh.text(textBlock{face: f, size: 4, lines: []string{"B♭m 33⅓ 12″"}}, 1, 1, "")
			if darkCount(cv.img) == 0 {
				t.Errorf("%s drew nothing", f)
			}
		}
	}
	if familyByID("no-such-font").ID != DefaultFontFamily {
		t.Error("unknown families must fall back to the default")
	}
}
