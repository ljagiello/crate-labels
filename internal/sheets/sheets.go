// Package sheets imposes label images onto pages for page printers (IPP and CUPS), and
// writes those pages as a minimal PDF.
//
// Sheet stocks (Stock.Sheet) get a grid of cells: a die-cut layout such as Avery's, or
// an uncut grid that the user cuts apart along trim marks. Each design is centred in its
// cell and only ever scaled down. Any other stock becomes one label per page of the
// stock's feed size.
package sheets

import (
	"image"
	"math"
	"slices"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// SizeToleranceMM is how far apart two paper sizes may be and still count as the same.
// One millimetre absorbs the rounding printers and drivers apply when they convert inch
// sizes to millimetres (4×6 in may be listed as 102×152 mm), while label sizes that
// really differ, such as 62 mm and 2.4 in (60.96 mm), stay apart.
const SizeToleranceMM = 1.0

// Trim marks. Each seam of an uncut grid gets a short line in the margin at both ends,
// on the seam itself, so a ruler laid across the two marks shows where to cut. A mark
// starts markGapMM outside the grid, so a cut that strays a little does not leave ink on
// a label, and runs markLengthMM outward. It is shortened so that it ends markInsetMM
// from the paper edge: office printers cannot print their outer border, commonly 4.2 to
// 4.3 mm (the Brother HL-L3290CDW in testdata reports 4.32 mm), and a mark that falls
// there would just vanish. A mark with less than markMinMM left is not drawn.
const (
	markWidthMM  = 0.25
	markGapMM    = 1.0
	markLengthMM = 3.0
	markInsetMM  = 4.5
	markMinMM    = 0.5
)

// MMToPx converts millimetres to pixels, rounding half up.
func MMToPx(mm, dpi float64) int {
	return int(math.Floor(mm*dpi/printer.MMPerInch + 0.5))
}

// PageMM is the physical page a page printer prints for s: the sheet's paper, else the
// feed size. A continuous stock's length is its design's along-feed side.
func PageMM(s *printer.Stock) [2]float64 {
	if s.Sheet != nil {
		return s.Sheet.PaperMM
	}
	w, l := s.FeedMM[0], s.FeedMM[1]
	if l == 0 {
		l = s.DesignMM[1]
		if s.RotateCW == 90 || s.RotateCW == 270 {
			l = s.DesignMM[0]
		}
	}
	return [2]float64{w, l}
}

// Capacity is the number of cells on one sheet.
func Capacity(g *printer.SheetGrid) int { return g.Rows * g.Cols }

// CellOriginMM is the top-left of cell i (row-major), in mm from the page's top-left.
func CellOriginMM(g *printer.SheetGrid, i int) [2]float64 {
	r, c := i/g.Cols, i%g.Cols
	// The explicit float64 conversions keep the compiler from fusing the multiply and
	// add (FMA), which would round differently on some CPUs.
	return [2]float64{
		g.MarginMM[0] + float64(float64(c)*g.PitchMM[0]),
		g.MarginMM[1] + float64(float64(r)*g.PitchMM[1]),
	}
}

// IsUncut reports whether the cells abut (no gutters), so the user cuts them apart.
func IsUncut(g *printer.SheetGrid) bool {
	return math.Abs(g.PitchMM[0]-g.CellMM[0]) <= 1e-6 && math.Abs(g.PitchMM[1]-g.CellMM[1]) <= 1e-6
}

// DefaultCropMarks is on only where the user cuts the sheet (an uncut grid).
func DefaultCropMarks(s *printer.Stock) bool { return s.Sheet != nil && IsUncut(s.Sheet) }

// PagesFor is the number of labels on each page when n labels are imposed on s.
func PagesFor(s *printer.Stock, n int) []int {
	per := 1
	if s.Sheet != nil {
		per = Capacity(s.Sheet)
	}
	var out []int
	for start := 0; start < n; start += per {
		out = append(out, min(per, n-start))
	}
	return out
}

// centre is the offset that centres inner in outer, rounded down; an inner one pixel
// larger than outer starts at -1.
func centre(outer, inner int) int {
	d := outer - inner
	if d < 0 {
		return -((-d + 1) / 2)
	}
	return d / 2
}

// PasteCentred copies src onto the middle of dst, clipped where src is larger.
func PasteCentred(dst, src *image.Gray) {
	db, sb := dst.Bounds(), src.Bounds()
	Paste(dst, src, db.Min.X+centre(db.Dx(), sb.Dx()), db.Min.Y+centre(db.Dy(), sb.Dy()))
}

// shrinkToFit scales g down to fit w×h, keeping its aspect ratio. One pixel of slack is
// allowed, because a design and its cell can round differently.
func shrinkToFit(g *image.Gray, w, h int) *image.Gray {
	gw, gh := g.Bounds().Dx(), g.Bounds().Dy()
	if gw <= w+1 && gh <= h+1 {
		return g
	}
	s := math.Min(float64(w)/float64(gw), float64(h)/float64(gh))
	return Resize(g, max(1, int(math.Floor(float64(gw)*s))), max(1, int(math.Floor(float64(gh)*s))))
}

func pasteInCell(page, label *image.Gray, r image.Rectangle) {
	fit := shrinkToFit(label, r.Dx(), r.Dy())
	b := fit.Bounds()
	Paste(page, fit, r.Min.X+centre(r.Dx(), b.Dx()), r.Min.Y+centre(r.Dy(), b.Dy()))
}

// cutLines lists the distinct cell edges along one axis, ascending: one per seam of an
// uncut grid, two per gutter of a die-cut one.
func cutLines(origin, pitch, cell float64, count int) []float64 {
	var edges []float64
	for i := range count {
		start := origin + float64(float64(i)*pitch)
		edges = append(edges, start, start+cell)
	}
	slices.Sort(edges)
	return slices.CompactFunc(edges, func(a, b float64) bool { return math.Abs(a-b) < 1e-6 })
}

// markSpan is where a mark beside a grid edge goes: from markGapMM out to at most
// markLengthMM further, but no closer than markInsetMM to the paper edge. margin is the
// distance from that grid edge to the paper edge. ok is false when too little is left.
func markSpan(margin float64) (near, far float64, ok bool) {
	near = markGapMM
	far = math.Min(markGapMM+markLengthMM, margin-markInsetMM)
	return near, far, far-near >= markMinMM
}

func fillRect(page *image.Gray, r image.Rectangle) {
	r = r.Canon().Intersect(page.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := page.PixOffset(r.Min.X, y)
		clear(page.Pix[o : o+r.Dx()])
	}
}

// band is the pixel range of a line markWidthMM wide centred on mm, at least one pixel.
func band(mm, dpi float64) (int, int) {
	a, b := MMToPx(mm-markWidthMM/2, dpi), MMToPx(mm+markWidthMM/2, dpi)
	return a, max(b, a+1)
}

// DrawTrimMarks draws the trim marks of grid g on page (see markSpan): above and below
// the grid on every vertical seam, left and right of it on every horizontal one.
func DrawTrimMarks(page *image.Gray, g *printer.SheetGrid, dpi float64) {
	left, top := g.MarginMM[0], g.MarginMM[1]
	right := left + float64(float64(g.Cols-1)*g.PitchMM[0]) + g.CellMM[0]
	bottom := top + float64(float64(g.Rows-1)*g.PitchMM[1]) + g.CellMM[1]
	px := func(mm float64) int { return MMToPx(mm, dpi) }

	type side struct {
		margin float64
		edge   float64 // the grid edge the mark starts from
		dir    float64 // -1 towards the top or left paper edge, +1 away from it
	}
	vertical := cutLines(left, g.PitchMM[0], g.CellMM[0], g.Cols)
	for _, s := range []side{{top, top, -1}, {g.PaperMM[1] - bottom, bottom, 1}} {
		near, far, ok := markSpan(s.margin)
		if !ok {
			continue
		}
		y0, y1 := px(s.edge+s.dir*near), px(s.edge+s.dir*far)
		for _, x := range vertical {
			x0, x1 := band(x, dpi)
			fillRect(page, image.Rect(x0, y0, x1, y1))
		}
	}
	horizontal := cutLines(top, g.PitchMM[1], g.CellMM[1], g.Rows)
	for _, s := range []side{{left, left, -1}, {g.PaperMM[0] - right, right, 1}} {
		near, far, ok := markSpan(s.margin)
		if !ok {
			continue
		}
		x0, x1 := px(s.edge+s.dir*near), px(s.edge+s.dir*far)
		for _, y := range horizontal {
			y0, y1 := band(y, dpi)
			fillRect(page, image.Rect(x0, y0, x1, y1))
		}
	}
}

// Impose lays labels out on full pages (white = 255) at dpi, in order.
//
// Labels are designs at dpi in design orientation; Stock.RotateCW turns them first.
// Sheet stocks fill each page's cells row by row, and cropMarks adds trim marks (see
// DrawTrimMarks). Any other stock gives one page of PageMM per label, with the label
// centred on it. A design is never scaled up; one that overflows its cell is scaled
// down to fit.
func Impose(labels []*image.Gray, s *printer.Stock, dpi float64, cropMarks bool) []*image.Gray {
	if len(labels) == 0 {
		return nil
	}
	paper := PageMM(s)
	pw, ph := MMToPx(paper[0], dpi), MMToPx(paper[1], dpi)
	turned := make([]*image.Gray, len(labels))
	for i, l := range labels {
		turned[i] = Rotate(l, s.RotateCW)
	}
	g := s.Sheet
	if g == nil {
		pages := make([]*image.Gray, len(turned))
		for i, l := range turned {
			pages[i] = White(pw, ph)
			pasteInCell(pages[i], l, image.Rect(0, 0, pw, ph))
		}
		return pages
	}
	per := Capacity(g)
	cw, ch := MMToPx(g.CellMM[0], dpi), MMToPx(g.CellMM[1], dpi)
	var pages []*image.Gray
	for start := 0; start < len(turned); start += per {
		page := White(pw, ph)
		for i, l := range turned[start:min(start+per, len(turned))] {
			o := CellOriginMM(g, i)
			x, y := MMToPx(o[0], dpi), MMToPx(o[1], dpi)
			pasteInCell(page, l, image.Rect(x, y, x+cw, y+ch))
		}
		if cropMarks {
			DrawTrimMarks(page, g, dpi)
		}
		pages = append(pages, page)
	}
	return pages
}
