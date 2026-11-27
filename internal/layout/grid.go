package layout

import "math"

// The design system every template is built on: a millimetre grid with one margin and
// gutter rule, a modular type scale, one stroke floor, and rectangles that are cut into
// bands and columns rather than placed at absolute coordinates.

// rect is an axis-aligned area in millimetres: top-left corner and size.
type rect struct{ x, y, w, h float64 }

func (r rect) right() float64  { return r.x + r.w }
func (r rect) bottom() float64 { return r.y + r.h }
func (r rect) empty() bool     { return r.w <= 0 || r.h <= 0 }

func (r rect) inset(dx, dy float64) rect {
	return rect{r.x + dx, r.y + dy, math.Max(0, r.w-2*dx), math.Max(0, r.h-2*dy)}
}

// cutTop splits off a band of height h at the top; rest starts gap below it.
func (r rect) cutTop(h, gap float64) (band, rest rect) {
	h = math.Min(math.Max(0, h), r.h)
	band = rect{r.x, r.y, r.w, h}
	rest = rect{r.x, r.y + h + gap, r.w, math.Max(0, r.h-h-gap)}
	return band, rest
}

// cutBottom splits off a band of height h at the bottom; rest ends gap above it.
func (r rect) cutBottom(h, gap float64) (band, rest rect) {
	h = math.Min(math.Max(0, h), r.h)
	band = rect{r.x, r.bottom() - h, r.w, h}
	rest = rect{r.x, r.y, r.w, math.Max(0, r.h-h-gap)}
	return band, rest
}

// cutLeft splits off a column of width w on the left; rest starts gap to its right.
func (r rect) cutLeft(w, gap float64) (col, rest rect) {
	w = math.Min(math.Max(0, w), r.w)
	col = rect{r.x, r.y, w, r.h}
	rest = rect{r.x + w + gap, r.y, math.Max(0, r.w-w-gap), r.h}
	return col, rest
}

// cutRight splits off a column of width w on the right; rest ends gap to its left.
func (r rect) cutRight(w, gap float64) (col, rest rect) {
	w = math.Min(math.Max(0, w), r.w)
	col = rect{r.right() - w, r.y, w, r.h}
	rest = rect{r.x, r.y, math.Max(0, r.w-w-gap), r.h}
	return col, rest
}

// rows divides r into n equal bands separated by gap.
func (r rect) rows(n int, gap float64) []rect {
	if n <= 0 {
		return nil
	}
	h := (r.h - gap*float64(n-1)) / float64(n)
	out := make([]rect, n)
	for i := range out {
		out[i] = rect{r.x, r.y + float64(i)*(h+gap), r.w, math.Max(0, h)}
	}
	return out
}

func (r rect) overlaps(o rect) bool {
	return r.x < o.right() && o.x < r.right() && r.y < o.bottom() && o.y < r.bottom()
}

func (r rect) contains(o rect, slack float64) bool {
	return o.x >= r.x-slack && o.y >= r.y-slack && o.right() <= r.right()+slack && o.bottom() <= r.bottom()+slack
}

// Grid rule. The margin is 7 % of the label's short side, so the white frame keeps the
// same proportion on every stock, snapped to 0.5 mm and held between 1.5 mm (a die-cut
// label's registration error on a gap-sensing printer) and 6 mm (more is wasted paper on
// 4×6″ and sheet stocks). The gutter between columns and bands is half the margin, never
// under 1 mm.
const (
	marginShare = 0.07
	marginMin   = 1.5
	marginMax   = 6.0
	gutterMin   = 1.0
)

// grid is a label's margin, gutter, content area and type scale.
type grid struct {
	w, h    float64 // the label, mm
	margin  float64
	gutter  float64
	content rect
	scale   typeScale
}

func newGrid(w, h, userScale float64) grid {
	short := math.Min(w, h)
	m := math.Max(marginMin, math.Min(marginMax, math.Round(short*marginShare*2)/2))
	g := grid{w: w, h: h, margin: m, gutter: math.Max(gutterMin, m/2)}
	g.content = rect{m, m, w - 2*m, h - 2*m}
	g.scale = newTypeScale(short, userScale)
	return g
}

// Type scale. Sizes are em heights in mm on a modular scale with ratio 1.25 (a major
// third), so each step is visibly distinct from its neighbours without wasting space.
// Step 0 is 2 mm on labels up to 48 mm on the short side and grows with larger labels,
// so a 4×6″ tag is not set like a 30 mm sticker. Nothing is ever set below minTextMM:
// 1.6 mm puts about 9 dots in a capital at 203 dpi, the smallest size that stays legible
// once a thermal head spreads each dot.
const (
	typeRatio = 1.25
	typeBase  = 2.0
	minTextMM = 1.6
)

// typeScale maps steps to sizes. user is the font_scale option and only widens the upper
// bound a fit may reach.
type typeScale struct{ base, user float64 }

func newTypeScale(shortSide, user float64) typeScale {
	return typeScale{base: math.Max(typeBase, shortSide/24), user: user}
}

// step is the size of step n (n may be negative), before the user scale.
func (t typeScale) step(n int) float64 { return t.base * math.Pow(typeRatio, float64(n)) }

// upTo is the largest size an element at step n may grow to: the step times the user
// scale.
func (t typeScale) upTo(n int) float64 { return t.step(n) * t.user }

// downTo is the smallest size an element at step n may shrink to: two steps down, never
// under minTextMM.
func (t typeScale) downTo(n int) float64 { return math.Max(minTextMM, t.step(n-2)) }

// Strokes. Every rule, outline, guide and badge border is at least two dots of a
// 203.2 dpi (8 dots/mm) head: a 1-dot line breaks up on a thermal head.
const minStrokeMM = 2.0 / 8
