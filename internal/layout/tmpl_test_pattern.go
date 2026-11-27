package layout

import (
	"fmt"
	"image"
	"math"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// testPattern is the calibration pattern for every stock, drawn in design orientation in
// mm. The FEED arrow points at the design edge the driver sends as raster row 0, which
// follows from the stock's rotate_cw: on a stock turned 90° clockwise, the design's left
// edge becomes row 0. If that edge does not leave the printer first, or TOP-LEFT is not
// where you expect, adjust the stock's rotation; uneven border widths show the offset;
// the rulers check the scale.
type testPattern struct{}

func (testPattern) ID() string   { return "test-pattern" }
func (testPattern) Name() string { return "Test pattern" }
func (testPattern) Description() string {
	return "Border, TOP-LEFT mark, FEED arrow, mm rulers and the stock size, for calibration."
}

func (testPattern) Supports(*printer.Stock) bool { return true }

func (testPattern) Options() []OptionSpec { return []OptionSpec{} }

func (t testPattern) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	sh := sketch(t, d, c, s, o)
	drawPattern(sh, c, s)
	return sh.image(), nil
}

func (testPattern) draw(*sheet, grid, *LabelData, *printer.Stock, opts) {}

const patternBorder = 1.0 // mm, solid, flush with the canvas edge

// feedDirection is the unit vector from the label centre towards the edge sent first.
func feedDirection(rotateCW int) (int, int) {
	switch ((rotateCW % 360) + 360) % 360 {
	case 90:
		return -1, 0
	case 180:
		return 0, 1
	case 270:
		return 1, 0
	}
	return 0, -1
}

// mmText formats a length with at most one decimal: 48 → "48", 66.675 → "66.7".
func mmText(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

// arrow fills an arrow centred on (cx, cy) pointing along (dx, dy); it returns its box.
func arrow(sh *sheet, cx, cy float64, dx, dy int, length, width float64) rect {
	fx, fy := float64(dx), float64(dy)
	nx, ny := -fy, fx
	half := length / 2
	head := math.Min(length*0.45, width*1.6)
	shaft := width * 0.38
	tail := point{cx - fx*half, cy - fy*half}
	tip := point{cx + fx*half, cy + fy*half}
	neck := point{tip.X - fx*head, tip.Y - fy*head}
	pts := []point{
		{tail.X + nx*shaft, tail.Y + ny*shaft},
		{neck.X + nx*shaft, neck.Y + ny*shaft},
		{neck.X + nx*width, neck.Y + ny*width},
		tip,
		{neck.X - nx*width, neck.Y - ny*width},
		{neck.X - nx*shaft, neck.Y - ny*shaft},
		{tail.X - nx*shaft, tail.Y - ny*shaft},
	}
	sh.polygon(pts, "")
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range pts {
		minX, minY = math.Min(minX, p.X), math.Min(minY, p.Y)
		maxX, maxY = math.Max(maxX, p.X), math.Max(maxY, p.Y)
	}
	return rect{minX, minY, maxX - minX, maxY - minY}
}

// centredLine draws one line of bold text centred on (cx, cy).
func centredLine(sh *sheet, f *face, text string, size, cx, cy float64) rect {
	b := textBlock{face: f, size: size, lines: []string{text}}
	w := f.width(text, size)
	return sh.text(b, cx-w/2, cy-b.height()/2, "")
}

// feedBlock draws the arrow and its caption right-aligned at right and centred in
// [top, bottom] (mm); it returns the block's box.
func feedBlock(sh *sheet, fc faces, dx, dy int, right, top, bottom, short, along float64) rect {
	roomH := bottom - top
	capSize := math.Max(1.4, math.Min(3.2, short*0.14))
	f := fc.bold
	lh := blockHeight(f, capSize, 1)
	arrowW := math.Min(short*0.12, 2.4)
	const gap = 0.5
	var x0, y0, blockW, blockH float64
	if dx != 0 { // horizontal feed: caption under the arrow, or beside it when there is no room
		length := math.Min(along*0.28, 16)
		caption := "FEED →"
		if dx < 0 {
			caption = "← FEED"
		}
		if 2*arrowW+gap+lh <= roomH {
			blockW = math.Max(length, f.width(caption, capSize))
			blockH = 2*arrowW + gap + lh
			x0, y0 = right-blockW, top+(roomH-blockH)/2
			arrow(sh, x0+blockW/2, y0+arrowW, dx, dy, length, arrowW)
			centredLine(sh, f, caption, capSize, x0+blockW/2, y0+2*arrowW+gap+lh/2)
		} else {
			caption = "FEED"
			cw := f.width(caption, capSize)
			blockW, blockH = length+gap+cw, math.Max(2*arrowW, lh)
			x0, y0 = right-blockW, top+(roomH-blockH)/2
			cy := y0 + blockH/2
			arrowX, textX := x0+cw+gap+length/2, x0+cw/2
			if dx < 0 {
				arrowX, textX = x0+length/2, x0+length+gap+cw/2
			}
			arrow(sh, arrowX, cy, dx, dy, length, arrowW)
			centredLine(sh, f, caption, capSize, textX, cy)
		}
	} else { // vertical feed: caption turned to run along the arrow, left of it
		length := math.Min(roomH*0.85, 16)
		blockW, blockH = lh+gap+2*arrowW, length
		x0, y0 = right-blockW, top+(roomH-blockH)/2
		arrow(sh, x0+lh+gap+arrowW, y0+length/2, dx, dy, length, arrowW)
		b := textBlock{face: f, size: capSize, lines: []string{"FEED →"}}
		sh.turnedText(b, x0+lh/2, y0+length/2, dy > 0, "")
	}
	return rect{x0, y0, blockW, blockH}
}

func drawPattern(sh *sheet, c printer.Canvas, s *printer.Stock) {
	w, h := canvasMM(c)
	fc := familyFaces(DefaultFontFamily)
	short := math.Min(w, h)
	tick := minStrokeMM

	// 1 mm border flush with the edge: an offset makes one side thinner than the other.
	for _, r := range []rect{{0, 0, w, patternBorder}, {0, h - patternBorder, w, patternBorder}, {0, 0, patternBorder, h}, {w - patternBorder, 0, patternBorder, h}} {
		sh.fill(r, "")
	}
	sh.note(mark{kind: markLine, name: "border", box: rect{0, 0, w, h}, stroke: patternBorder})

	// Asymmetric corner mark and the TOP-LEFT caption.
	corner := math.Min(4, short*0.22)
	sh.polygon([]point{{patternBorder, patternBorder}, {patternBorder + corner, patternBorder}, {patternBorder, patternBorder + corner}}, "")
	tlSize := math.Max(1.3, math.Min(2.6, short*0.13))
	tlTop := patternBorder + 0.3
	tlX := patternBorder + corner*0.55 + 0.6
	sh.text(textBlock{face: fc.bold, size: tlSize, lines: []string{"TOP-LEFT"}}, tlX, tlTop, "top_left")
	bandTop := tlTop + blockHeight(fc.bold, tlSize, 1) + 0.5

	// Rulers: 1 mm ticks, 5 mm major ticks (numbers every 10 mm when there is room).
	minor, major := math.Min(1, short*0.08), math.Min(2.2, short*0.16)
	numbers := short >= 20
	const numSize = 1.6
	bandBottom := h - patternBorder - major - 0.5
	if numbers {
		bandBottom = h - patternBorder - math.Max(major, minor+0.3+blockHeight(fc.regular, numSize, 1)) - 0.5
	}
	for mm := 1; float64(mm) < w-patternBorder; mm++ {
		length := minor
		if mm%5 == 0 {
			length = major
		}
		x := float64(mm)
		sh.vline(x-tick/2, h-patternBorder-length, h-patternBorder, tick, "")
		label := strconv.Itoa(mm)
		nb := textBlock{face: fc.regular, size: numSize, lines: []string{label}}
		if numbers && mm%10 == 0 && x+0.4+fc.regular.width(label, numSize) < w-patternBorder {
			sh.text(nb, x+0.4, h-patternBorder-minor-0.3-nb.height(), "")
		}
	}
	for mm := int(math.Ceil(patternBorder + corner)); float64(mm) < h-patternBorder-major; mm++ {
		length := minor
		if mm%5 == 0 {
			length = major
		}
		y := float64(mm)
		sh.hline(patternBorder, patternBorder+length, y-tick/2, tick, "")
	}
	sh.note(mark{kind: markLine, name: "ruler", box: rect{patternBorder, h - patternBorder - major, w - 2*patternBorder, major}})

	// Feed arrow, pointing at the edge sent as raster row 0.
	dx, dy := feedDirection(s.RotateCW)
	along := h
	if dx != 0 {
		along = w
	}
	feed := feedBlock(sh, fc, dx, dy, w-patternBorder-1.5, bandTop, bandBottom, short, along)
	sh.note(mark{kind: markShape, name: "feed_arrow", box: feed, text: fmt.Sprintf("%d,%d", dx, dy)})

	// Size captions, left of the arrow.
	left := patternBorder + major + 1
	room := feed.x - 1 - left
	feedLen := "roll"
	if s.FeedMM[1] != 0 {
		feedLen = mmText(s.FeedMM[1])
	}
	rows := []struct {
		text    string
		nominal float64
		bold    bool
	}{
		{fmt.Sprintf("%s×%s mm", mmText(s.DesignMM[0]), mmText(s.DesignMM[1])), 4, true},
		{s.ID, 2.2, false},
		{fmt.Sprintf("feed %s×%s · rot %d°", mmText(s.FeedMM[0]), feedLen, s.RotateCW), 2.2, false},
		{fmt.Sprintf("%d×%d px · %s dpi", c.Width, c.Height, strconv.FormatFloat(c.DPI, 'g', -1, 64)), 2.2, false},
	}
	y := bandTop
	for i, r := range rows {
		f := fc.pick(r.bold)
		k := 0.09
		if r.bold {
			k = 0.16
		}
		size := math.Min(r.nominal, short*k)
		size = math.Max(1.1, math.Min(size, room/math.Max(f.emWidth(r.text), 1e-6)))
		if y+blockHeight(f, size, 1) > bandBottom+1e-6 || room <= 2 {
			break
		}
		name := ""
		if i == 0 {
			name = "size"
		}
		b := textBlock{face: f, size: size, lines: []string{withEllipsis(f, r.text, size, room)}}
		sh.text(b, left, y, name)
		y += size * leading
	}

	if s.Round {
		disc := discFromStock(s, w, h)
		sh.ring(disc.cx, disc.cy, disc.d, tick, 0, 0, "disc")
		sh.ring(disc.cx, disc.cy, disc.hole, tick, 0, 0, "hole")
	}
}
