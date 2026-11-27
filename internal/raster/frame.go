package raster

import (
	"fmt"
	"image"
)

// Span is a run of dots across the print head: [Start, Start+Width).
type Span struct{ Start, Width int }

// End is the first dot after the span.
func (s Span) End() int { return s.Start + s.Width }

// Justify says where a lane narrower than the head sits when no offset is given.
type Justify int

const (
	// Center puts the lane in the middle of the head, rounding towards dot 0.
	Center Justify = iota
	// End puts the lane against the head's last dot (high x).
	End
)

// JustifiedLane is a lane of width dots on a head of head dots, placed by j. A lane
// wider than the head is cut to the head.
func JustifiedLane(head, width int, j Justify) Span {
	width = min(max(width, 0), head)
	switch j {
	case End:
		return Span{Start: head - width, Width: width}
	default:
		return Span{Start: (head - width) / 2, Width: width}
	}
}

// LaneAt is a lane of width dots that starts at dot start, moved as little as
// needed to stay on the head. A lane wider than the head is cut to the head.
func LaneAt(head, width, start int) Span {
	width = min(max(width, 0), head)
	return Span{Start: min(max(start, 0), head-width), Width: width}
}

// Frame describes the raster one label is printed on.
type Frame struct {
	// Width is the raster width in dots, normally the whole head. It must be a
	// positive multiple of 8 so that rows pack into whole bytes.
	Width int
	// Lane is where the label sits across the head. The zero Span means the whole
	// width.
	Lane Span
	// Rows fixes the raster length (a die-cut label). A design that is too tall is
	// scaled down to fit; a shorter one is centred along the feed. 0 lets the
	// length follow the design at lane width.
	Rows int
	// MaxRows caps the length when Rows is 0 (continuous media). A design that would
	// run longer is scaled down to fit. 0 means no cap.
	MaxRows int
}

// Layout is a design fitted into a frame.
type Layout struct {
	Width, Rows int             // raster size in dots
	Lane        Span            // the label's dots across the head
	Image       image.Rectangle // where the scaled design sits on the raster
}

// Fit scales a w×h design into f and places it. The design keeps its aspect ratio: it
// fills the lane's width unless that would make it longer than the frame allows, in
// which case it fills the length instead. It is centred in whatever room is left.
func Fit(w, h int, f Frame) (Layout, error) {
	if f.Width <= 0 || f.Width%8 != 0 {
		return Layout{}, fmt.Errorf("raster: width must be a positive multiple of 8 dots, got %d", f.Width)
	}
	if w <= 0 || h <= 0 {
		return Layout{}, fmt.Errorf("raster: the design is empty (%dx%d)", w, h)
	}
	lane := f.Lane
	if lane == (Span{}) {
		lane = Span{Width: f.Width}
	}
	if lane.Width <= 0 || lane.Start < 0 || lane.End() > f.Width {
		return Layout{}, fmt.Errorf("raster: lane %d+%d is not on a %d-dot raster", lane.Start, lane.Width, f.Width)
	}
	limit := f.MaxRows
	if f.Rows > 0 {
		limit = f.Rows
	}
	// At lane width the design is h·lane/w rows long. Compare exactly, in integers.
	sw, sh := lane.Width, divRound(h*lane.Width, w)
	if limit > 0 && h*lane.Width > limit*w {
		sw, sh = min(divRound(w*limit, h), lane.Width), limit
	}
	sw, sh = max(sw, 1), max(sh, 1)
	rows := f.Rows
	if rows <= 0 {
		rows = sh
	}
	x := lane.Start + (lane.Width-sw)/2
	y := (rows - sh) / 2
	return Layout{Width: f.Width, Rows: rows, Lane: lane, Image: image.Rect(x, y, x+sw, y+sh)}, nil
}

// divRound is a/b rounded to the nearest integer, halves up, for a ≥ 0 and b > 0.
func divRound(a, b int) int { return (2*a + b) / (2 * b) }

// Label is one label rasterised for the head.
type Label struct {
	Bits   *Bits  // Layout.Width × Layout.Rows, true = burn
	Layout Layout // where the label landed
	TurnCW int    // the clockwise turn applied to the design first: 0, 90, 180 or 270
}

// Render turns g clockwise by turnCW, scales it into f with [Fit] and dithers it into
// burn dots. Only the scaled design is dithered: the rest of the raster is paper and
// never burns.
//
// Apply any tone curve to g before calling it.
func Render(g *Gray, turnCW int, f Frame) (*Label, error) {
	turned, err := RotateCW(g, turnCW)
	if err != nil {
		return nil, err
	}
	lay, err := Fit(turned.W, turned.H, f)
	if err != nil {
		return nil, err
	}
	scaled, err := ResampleArea(turned, lay.Image.Dx(), lay.Image.Dy())
	if err != nil {
		return nil, err
	}
	dots := FloydSteinberg(scaled)
	bits := NewBits(lay.Width, lay.Rows)
	for y := range dots.H {
		copy(bits.Pix[(y+lay.Image.Min.Y)*lay.Width+lay.Image.Min.X:], dots.Pix[y*dots.W:(y+1)*dots.W])
	}
	return &Label{Bits: bits, Layout: lay, TurnCW: ((turnCW % 360) + 360) % 360}, nil
}

// Preview is the label's lane turned back to design orientation: exactly the dots that
// burn, as 0 = burn and 255 = paper.
func (l *Label) Preview() *image.Gray {
	lane := l.Layout.Lane
	box := l.Bits.Crop(image.Rect(lane.Start, 0, lane.End(), l.Bits.H))
	back, _ := RotateBitsCW(box, 360-l.TurnCW) // TurnCW is a multiple of 90 by construction
	return BitsToImage(back)
}
