package layout

import (
	"image"
	"math"
)

// sheet draws in millimetres on a canvas and records every element it draws, so tests
// can check the layout (margins, overlaps, sizes, stroke weights) without reading pixels.
type sheet struct {
	cv    *canvas
	dpmm  float64 // canvas pixels per mm
	marks []mark
}

func newSheet(cv *canvas, dpmm float64) *sheet { return &sheet{cv: cv, dpmm: dpmm} }

// markKind says what a mark is. Content marks (text, badges, codes) must not overlap
// each other and must stay inside the label's safe area; lines may cross anything.
type markKind uint8

const (
	markText markKind = iota
	markBadge
	markCode
	markLine // rules, outlines, guides, fold and cut lines
	markShape
)

func (k markKind) content() bool { return k == markText || k == markBadge || k == markCode }

// mark is one drawn element: its box in mm and, for text, the em size and the lines.
type mark struct {
	kind   markKind
	name   string
	box    rect
	size   float64 // text em, mm
	stroke float64 // line weight, mm
	module int     // QR module, px
	text   string
	lines  int
}

func (s *sheet) note(m mark) { s.marks = append(s.marks, m) }

// find returns the first mark with the name.
func (s *sheet) find(name string) (mark, bool) {
	for _, m := range s.marks {
		if m.name == name {
			return m, true
		}
	}
	return mark{}, false
}

func (s *sheet) px(mm float64) int { return int(math.Round(mm * s.dpmm)) }

// weight is a stroke snapped to whole pixels and never under minStrokeMM.
func (s *sheet) weight(mm float64) int {
	return max(s.px(math.Max(mm, minStrokeMM)), int(math.Ceil(minStrokeMM*s.dpmm-1e-9)))
}

// fill paints r black, snapped to whole pixels.
func (s *sheet) fill(r rect, name string) {
	x0, y0, x1, y1 := s.px(r.x), s.px(r.y), s.px(r.right()), s.px(r.bottom())
	s.cv.fillRectPx(x0, y0, x1, y1, black)
	if name != "" {
		s.note(mark{kind: markShape, name: name, box: r})
	}
}

// hline draws a horizontal rule of the given weight whose top edge is at y.
func (s *sheet) hline(x0, x1, y, weight float64, name string) {
	t := s.weight(weight)
	top := s.px(y)
	s.cv.fillRectPx(s.px(x0), top, s.px(x1), top+t, black)
	s.note(mark{kind: markLine, name: name, box: rect{x0, y, x1 - x0, float64(t) / s.dpmm}, stroke: float64(t) / s.dpmm})
}

// vline draws a vertical rule of the given weight whose left edge is at x.
func (s *sheet) vline(x, y0, y1, weight float64, name string) {
	t := s.weight(weight)
	left := s.px(x)
	s.cv.fillRectPx(left, s.px(y0), left+t, s.px(y1), black)
	s.note(mark{kind: markLine, name: name, box: rect{x, y0, float64(t) / s.dpmm, y1 - y0}, stroke: float64(t) / s.dpmm})
}

// dashH and dashV draw dashed rules: dash mm on, gap mm off, starting with a dash.
func (s *sheet) dashH(x0, x1, y, weight, dash, gap float64, name string) {
	for x := x0; x < x1; x += dash + gap {
		s.hline(x, math.Min(x+dash, x1), y, weight, "")
	}
	s.note(mark{kind: markLine, name: name, box: rect{x0, y, x1 - x0, weight}, stroke: float64(s.weight(weight)) / s.dpmm})
}

func (s *sheet) dashV(x, y0, y1, weight, dash, gap float64, name string) {
	for y := y0; y < y1; y += dash + gap {
		s.vline(x, y, math.Min(y+dash, y1), weight, "")
	}
	s.note(mark{kind: markLine, name: name, box: rect{x, y0, weight, y1 - y0}, stroke: float64(s.weight(weight)) / s.dpmm})
}

// frame strokes the inside edge of r.
func (s *sheet) frame(r rect, weight float64, name string) {
	t := float64(s.weight(weight)) / s.dpmm
	s.hline(r.x, r.right(), r.y, weight, "")
	s.hline(r.x, r.right(), r.bottom()-t, weight, "")
	s.vline(r.x, r.y, r.bottom(), weight, "")
	s.vline(r.right()-t, r.y, r.bottom(), weight, "")
	s.note(mark{kind: markLine, name: name, box: r, stroke: t})
}

// ring strokes a circle of diameter d around (cx, cy), inward from the diameter. With
// dash > 0 it is drawn as arcs of about dash mm separated by gaps of about gap mm, evenly
// spread so the pattern closes.
func (s *sheet) ring(cx, cy, d, weight, dash, gap float64, name string) {
	t := float64(s.weight(weight)) / s.dpmm
	r := d / 2
	x, y := cx*s.dpmm, cy*s.dpmm
	ro, ri := r*s.dpmm, (r-t)*s.dpmm
	var p path
	if dash <= 0 {
		p.circle(x, y, ro, false)
		p.circle(x, y, ri, true)
	} else {
		n := max(4, int(math.Round(math.Pi*d/(dash+gap))))
		step := 2 * math.Pi / float64(n)
		on := step * dash / (dash + gap)
		for i := range n {
			a0 := -math.Pi/2 + float64(i)*step
			p.arc(x, y, ro, a0, a0+on, true)
			p.arc(x, y, ri, a0+on, a0, false)
		}
	}
	s.cv.fill(p, black, true)
	s.note(mark{kind: markLine, name: name, box: rect{cx - r, cy - r, d, d}, stroke: t})
}

// text draws a block with its lane's left edge at x and its cap line at top, and
// records it. It returns the block's box.
func (s *sheet) text(b textBlock, x, top float64, name string) rect {
	if b.empty() {
		return rect{x, top, 0, 0}
	}
	pitch := b.size * leading
	minX, maxX := math.Inf(1), math.Inf(-1)
	for i, line := range b.lines {
		w := b.face.width(line, b.size)
		lx := x
		switch b.align {
		case alignCenter:
			lx = x + (b.width-w)/2
		case alignRight:
			lx = x + b.width - w
		}
		baseline := top + b.size*b.face.capHeight + float64(i)*pitch
		s.cv.drawText(b.face, line, b.size*s.dpmm, affine{a: 1, c: lx * s.dpmm, e: 1, f: baseline * s.dpmm}, black)
		minX, maxX = math.Min(minX, lx), math.Max(maxX, lx+w)
	}
	box := rect{minX, top, maxX - minX, b.height()}
	s.note(mark{kind: markText, name: name, box: box, size: b.size, text: joinLines(b.lines), lines: len(b.lines)})
	return box
}

// textIn draws b in r: horizontally by the block's own alignment, vertically at the
// top, centre or bottom of r (v = 0, 0.5 or 1).
func (s *sheet) textIn(b textBlock, r rect, v float64, name string) rect {
	b.width = r.w
	return s.text(b, r.x, r.y+(r.h-b.height())*v, name)
}

// turnedText draws a one-line block turned a quarter clockwise (reading top to bottom,
// cw true) or counter-clockwise (bottom to top), centred on (cx, cy).
func (s *sheet) turnedText(b textBlock, cx, cy float64, cw bool, name string) rect {
	if b.empty() {
		return rect{cx, cy, 0, 0}
	}
	line := b.lines[0]
	w, h := b.face.width(line, b.size), b.height()
	cap := b.size * b.face.capHeight
	// In text space, x runs along the line from the pen and y down from the baseline;
	// the block's centre is at (w/2, h/2 - cap).
	var t affine
	if cw { // text x → canvas +y, text y → canvas -x
		t = affine{a: 0, b: -1, c: (cx + h/2 - cap) * s.dpmm, d: 1, e: 0, f: (cy - w/2) * s.dpmm}
	} else { // text x → canvas -y, text y → canvas +x
		t = affine{a: 0, b: 1, c: (cx - h/2 + cap) * s.dpmm, d: -1, e: 0, f: (cy + w/2) * s.dpmm}
	}
	s.cv.drawText(b.face, line, b.size*s.dpmm, t, black)
	box := rect{cx - h/2, cy - w/2, h, w}
	s.note(mark{kind: markText, name: name, box: box, size: b.size, text: line, lines: 1})
	return box
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += " / "
		}
		out += l
	}
	return out
}

// polygon fills a polygon given in mm.
func (s *sheet) polygon(pts []point, name string) {
	var p path
	px := make([]point, len(pts))
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i, pt := range pts {
		px[i] = point{pt.X * s.dpmm, pt.Y * s.dpmm}
		minX, minY = math.Min(minX, pt.X), math.Min(minY, pt.Y)
		maxX, maxY = math.Max(maxX, pt.X), math.Max(maxY, pt.Y)
	}
	p.polygon(px)
	s.cv.fill(p, black, true)
	if name != "" {
		s.note(mark{kind: markShape, name: name, box: rect{minX, minY, maxX - minX, maxY - minY}})
	}
}

// code draws a QR code for url with its top-left at (x, y), as large as fits side mm
// with whole-pixel modules of at least the module floor. It returns the side drawn in
// mm, or 0 when no code fits.
func (s *sheet) code(url string, x, y, side float64, name string) float64 {
	img, k := qrBitmap(url, int(math.Floor(side*s.dpmm+1e-9)), minModulePx(s.dpmm))
	if img == nil {
		return 0
	}
	px, py := s.px(x), s.px(y)
	if px < 0 || py < 0 || px+img.Rect.Dx() > s.cv.img.Rect.Dx() || py+img.Rect.Dy() > s.cv.img.Rect.Dy() {
		return 0
	}
	s.cv.blit(img, px, py)
	drawn := float64(img.Rect.Dx()) / s.dpmm
	s.note(mark{kind: markCode, name: name, box: rect{float64(px) / s.dpmm, float64(py) / s.dpmm, drawn, drawn}, module: k})
	return drawn
}

// codeSide is the side code would draw in a square of side mm, without drawing it.
func (s *sheet) codeSide(url string, side float64) float64 {
	return float64(qrPixels(url, int(math.Floor(side*s.dpmm+1e-9)), minModulePx(s.dpmm))) / s.dpmm
}

// image returns the canvas image.
func (s *sheet) image() *image.Gray { return s.cv.img }
