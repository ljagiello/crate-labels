package layout

import (
	"image"
	"math"

	"golang.org/x/image/vector"
)

// Ink levels on the 8-bit grey canvas.
const (
	black uint8 = 0
	white uint8 = 255
)

type point struct{ X, Y float64 }

type opKind uint8

const (
	opMove opKind = iota
	opLine
	opQuad
	opCube
)

// pathOp is one step of a closed outline; unused points are zero.
type pathOp struct {
	kind opKind
	p    [3]point
}

// path is a list of closed sub-paths. Sub-paths wind non-zero: reverse one to cut a hole.
type path []pathOp

func (p *path) moveTo(x, y float64) { *p = append(*p, pathOp{kind: opMove, p: [3]point{{x, y}}}) }
func (p *path) lineTo(x, y float64) { *p = append(*p, pathOp{kind: opLine, p: [3]point{{x, y}}}) }
func (p *path) quadTo(bx, by, cx, cy float64) {
	*p = append(*p, pathOp{kind: opQuad, p: [3]point{{bx, by}, {cx, cy}}})
}
func (p *path) cubeTo(bx, by, cx, cy, dx, dy float64) {
	*p = append(*p, pathOp{kind: opCube, p: [3]point{{bx, by}, {cx, cy}, {dx, dy}}})
}

func (op pathOp) points() []point {
	switch op.kind {
	case opQuad:
		return op.p[:2]
	case opCube:
		return op.p[:3]
	}
	return op.p[:1]
}

// polygon appends a closed polygon.
func (p *path) polygon(pts []point) {
	for i, pt := range pts {
		if i == 0 {
			p.moveTo(pt.X, pt.Y)
		} else {
			p.lineTo(pt.X, pt.Y)
		}
	}
}

// arc appends a circular arc from angle a0 to a1 (radians, clockwise on screen
// because y grows down) around (cx, cy). It starts a sub-path when start is set, else
// continues the current one with a line to the arc's first point.
func (p *path) arc(cx, cy, r, a0, a1 float64, start bool) {
	steps := int(math.Ceil(math.Abs(a1-a0) / (math.Pi / 2)))
	if steps < 1 {
		steps = 1
	}
	da := (a1 - a0) / float64(steps)
	k := 4.0 / 3.0 * math.Tan(da/4)
	x0, y0 := cx+r*math.Cos(a0), cy+r*math.Sin(a0)
	if start {
		p.moveTo(x0, y0)
	} else {
		p.lineTo(x0, y0)
	}
	for i := range steps {
		t0 := a0 + float64(i)*da
		t1 := t0 + da
		c0, s0 := math.Cos(t0), math.Sin(t0)
		c1, s1 := math.Cos(t1), math.Sin(t1)
		p.cubeTo(
			cx+r*(c0-k*s0), cy+r*(s0+k*c0),
			cx+r*(c1+k*s1), cy+r*(s1-k*c1),
			cx+r*c1, cy+r*s1,
		)
	}
}

// circle appends a full circle; clockwise unless reverse.
func (p *path) circle(cx, cy, r float64, reverse bool) {
	if reverse {
		p.arc(cx, cy, r, 2*math.Pi, 0, true)
	} else {
		p.arc(cx, cy, r, 0, 2*math.Pi, true)
	}
}

// roundRect appends a rounded rectangle [x0, x1] × [y0, y1] with corner radius r.
func (p *path) roundRect(x0, y0, x1, y1, r float64, reverse bool) {
	r = math.Max(0, math.Min(r, math.Min((x1-x0)/2, (y1-y0)/2)))
	type corner struct{ cx, cy, a0 float64 }
	corners := []corner{
		{x1 - r, y0 + r, -math.Pi / 2}, // top right
		{x1 - r, y1 - r, 0},            // bottom right
		{x0 + r, y1 - r, math.Pi / 2},  // bottom left
		{x0 + r, y0 + r, math.Pi},      // top left
	}
	if reverse {
		for i := len(corners) - 1; i >= 0; i-- {
			c := corners[i]
			p.arc(c.cx, c.cy, r, c.a0+math.Pi/2, c.a0, i == len(corners)-1)
		}
		return
	}
	for i, c := range corners {
		p.arc(c.cx, c.cy, r, c.a0, c.a0+math.Pi/2, i == 0)
	}
}

// affine maps (x, y) to (a·x + b·y + c, d·x + e·y + f).
type affine struct{ a, b, c, d, e, f float64 }

func (t affine) apply(pt point) point {
	return point{t.a*pt.X + t.b*pt.Y + t.c, t.d*pt.X + t.e*pt.Y + t.f}
}

// canvas wraps the label image with a reusable rasteriser and coverage mask.
type canvas struct {
	img  *image.Gray
	z    vector.Rasterizer
	mask image.Alpha
}

func newCanvas(w, h int) *canvas {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = white
	}
	return &canvas{img: img}
}

// fillRectPx fills whole pixels [x0, x1) × [y0, y1), clipped to the canvas.
func (cv *canvas) fillRectPx(x0, y0, x1, y1 int, ink uint8) {
	r := image.Rect(x0, y0, x1, y1).Intersect(cv.img.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := cv.img.Pix[y*cv.img.Stride:]
		for x := r.Min.X; x < r.Max.X; x++ {
			row[x] = ink
		}
	}
}

// fill rasterises p (pixel coordinates) and paints it with ink. A crisp fill sets every
// pixel at least half covered, like an aliased polygon fill; otherwise coverage blends.
func (cv *canvas) fill(p path, ink uint8, crisp bool) {
	cv.fillTransformed(p, affine{a: 1, e: 1}, ink, crisp)
}

// fillTransformed rasterises p after mapping it through t.
func (cv *canvas) fillTransformed(p path, t affine, ink uint8, crisp bool) {
	if len(p) == 0 {
		return
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, op := range p {
		for _, pt := range op.points() {
			q := t.apply(pt)
			minX, maxX = math.Min(minX, q.X), math.Max(maxX, q.X)
			minY, maxY = math.Min(minY, q.Y), math.Max(maxY, q.Y)
		}
	}
	if math.IsNaN(minX+minY+maxX+maxY) || math.IsInf(minX+minY+maxX+maxY, 0) {
		return
	}
	box := image.Rect(int(math.Floor(minX)), int(math.Floor(minY)),
		int(math.Ceil(maxX))+1, int(math.Ceil(maxY))+1).Intersect(cv.img.Rect)
	if box.Empty() {
		return
	}
	w, h := box.Dx(), box.Dy()
	cv.z.Reset(w, h)
	ox, oy := float64(box.Min.X), float64(box.Min.Y)
	open := false
	var start point
	for _, op := range p {
		q := [3]point{}
		for i, pt := range op.points() {
			v := t.apply(pt)
			q[i] = point{v.X - ox, v.Y - oy}
		}
		switch op.kind {
		case opMove:
			if open {
				cv.z.LineTo(float32(start.X), float32(start.Y))
			}
			cv.z.MoveTo(float32(q[0].X), float32(q[0].Y))
			start, open = q[0], true
		case opLine:
			cv.z.LineTo(float32(q[0].X), float32(q[0].Y))
		case opQuad:
			cv.z.QuadTo(float32(q[0].X), float32(q[0].Y), float32(q[1].X), float32(q[1].Y))
		case opCube:
			cv.z.CubeTo(float32(q[0].X), float32(q[0].Y), float32(q[1].X), float32(q[1].Y),
				float32(q[2].X), float32(q[2].Y))
		}
	}
	if open {
		cv.z.LineTo(float32(start.X), float32(start.Y))
	}
	if cap(cv.mask.Pix) < w*h {
		cv.mask.Pix = make([]uint8, w*h)
	}
	cv.mask.Pix = cv.mask.Pix[:w*h]
	clear(cv.mask.Pix)
	cv.mask.Stride = w
	cv.mask.Rect = image.Rect(0, 0, w, h)
	cv.z.Draw(&cv.mask, cv.mask.Rect, image.Opaque, image.Point{})
	cv.composite(box.Min, w, h, ink, crisp)
}

// composite blends the coverage mask into the canvas at off.
func (cv *canvas) composite(off image.Point, w, h int, ink uint8, crisp bool) {
	inkV := int(ink)
	for y := range h {
		src := cv.mask.Pix[y*w : y*w+w]
		dst := cv.img.Pix[(off.Y+y)*cv.img.Stride+off.X:]
		for x, a := range src {
			switch {
			case a == 0:
			case crisp:
				if a >= 128 {
					dst[x] = ink
				}
			case a == 255:
				dst[x] = ink
			default:
				av := int(a)
				dst[x] = uint8((int(dst[x])*(255-av) + inkV*av + 127) / 255)
			}
		}
	}
}

// blit copies src into the canvas with its top-left at (x, y), clipped.
func (cv *canvas) blit(src *image.Gray, x, y int) {
	r := src.Bounds().Add(image.Pt(x, y)).Intersect(cv.img.Rect)
	for py := r.Min.Y; py < r.Max.Y; py++ {
		sy := py - y + src.Rect.Min.Y
		sx := r.Min.X - x + src.Rect.Min.X
		copy(cv.img.Pix[py*cv.img.Stride+r.Min.X:py*cv.img.Stride+r.Max.X],
			src.Pix[sy*src.Stride+sx:])
	}
}
