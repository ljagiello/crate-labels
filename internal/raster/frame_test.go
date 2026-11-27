package raster

import (
	"image"
	"testing"
)

func TestLanes(t *testing.T) {
	for _, tc := range []struct {
		got, want Span
	}{
		{JustifiedLane(16, 8, End), Span{8, 8}},
		{JustifiedLane(16, 8, Center), Span{4, 8}},
		{JustifiedLane(17, 8, Center), Span{4, 8}}, // odd room: the extra dot goes high
		{JustifiedLane(16, 20, End), Span{0, 16}},  // cut to the head
		{LaneAt(560, 384, 10), Span{10, 384}},
		{LaneAt(560, 384, -5), Span{0, 384}},
		{LaneAt(560, 384, 999), Span{176, 384}},
		{LaneAt(96, 200, 3), Span{0, 96}},
	} {
		if tc.got != tc.want {
			t.Errorf("lane %+v, want %+v", tc.got, tc.want)
		}
	}
}

func TestFit(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
		f    Frame
		want Layout
	}{
		{"fills a fixed frame", 384, 240, Frame{Width: 384, Rows: 240},
			Layout{384, 240, Span{0, 384}, image.Rect(0, 0, 384, 240)}},
		{"lane at the end", 320, 240, Frame{Width: 384, Lane: JustifiedLane(384, 320, End), Rows: 240},
			Layout{384, 240, Span{64, 320}, image.Rect(64, 0, 384, 240)}},
		{"too tall: fills the length, centred across", 240, 384, Frame{Width: 384, Rows: 240},
			Layout{384, 240, Span{0, 384}, image.Rect(117, 0, 267, 240)}},
		{"short: centred along the feed", 40, 30, Frame{Width: 48, Lane: Span{4, 40}, Rows: 60},
			Layout{48, 60, Span{4, 40}, image.Rect(4, 15, 44, 45)}},
		{"length follows the design", 40, 30, Frame{Width: 96},
			Layout{96, 72, Span{0, 96}, image.Rect(0, 0, 96, 72)}},
		{"under the cap", 96, 400, Frame{Width: 96, MaxRows: 1100},
			Layout{96, 400, Span{0, 96}, image.Rect(0, 0, 96, 400)}},
		{"over the cap", 96, 2000, Frame{Width: 96, MaxRows: 1100},
			Layout{96, 1100, Span{0, 96}, image.Rect(21, 0, 74, 1100)}},
		{"never thinner than a dot", 1, 1000, Frame{Width: 8, Rows: 4},
			Layout{8, 4, Span{0, 8}, image.Rect(3, 0, 4, 4)}},
	} {
		got, err := Fit(tc.w, tc.h, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
	for _, f := range []Frame{{Width: 12}, {Width: 0}, {Width: 16, Lane: Span{10, 8}}, {Width: 16, Lane: Span{-1, 4}}} {
		if _, err := Fit(10, 10, f); err == nil {
			t.Errorf("Fit(%+v) should fail", f)
		}
	}
	if _, err := Fit(0, 10, Frame{Width: 8}); err == nil {
		t.Error("an empty design should fail")
	}
}

func TestRenderNeverBurnsOutsideTheDesign(t *testing.T) {
	black := flat(30, 40, 0) // too tall for the frame: it is scaled into the middle
	l, err := Render(black, 0, Frame{Width: 64, Lane: JustifiedLane(64, 40, End), Rows: 20})
	if err != nil {
		t.Fatal(err)
	}
	box := l.Layout.Image
	if box != image.Rect(36, 0, 51, 20) {
		t.Fatalf("design box %v", box)
	}
	for y := range l.Bits.H {
		for x := range l.Bits.W {
			if l.Bits.At(x, y) != image.Pt(x, y).In(box) {
				t.Fatalf("dot (%d, %d) burn=%v with the design at %v", x, y, l.Bits.At(x, y), box)
			}
		}
	}
}

func TestRenderTurnsAndPreviewTurnsBack(t *testing.T) {
	// A binary 24×8 design turned onto a lane exactly its size: no scaling, so every
	// dot is either burnt or paper and the preview must be the design again.
	design := NewGray(24, 8, 255)
	for i := range design.Pix {
		if (i*7)%5 == 0 {
			design.Pix[i] = 0
		}
	}
	for _, turn := range []int{90, 270, -90, 180} {
		f := Frame{Width: 16, Lane: JustifiedLane(16, 8, End)}
		if turn == 180 {
			f = Frame{Width: 24}
		}
		l, err := Render(design, turn, f)
		if err != nil {
			t.Fatal(err)
		}
		if turn%180 != 0 && (l.Bits.W != 16 || l.Bits.H != 24 || l.Layout.Lane.Start != 8) {
			t.Fatalf("turn %d: raster %dx%d layout %+v", turn, l.Bits.W, l.Bits.H, l.Layout)
		}
		prev := l.Preview()
		if prev.Rect.Dx() != 24 || prev.Rect.Dy() != 8 {
			t.Fatalf("turn %d: preview %v", turn, prev.Rect)
		}
		for i, v := range design.Pix {
			if float32(prev.Pix[i]) != v {
				t.Fatalf("turn %d: preview differs from the design at %d", turn, i)
			}
		}
	}
	if _, err := Render(design, 45, Frame{Width: 16}); err == nil {
		t.Error("a 45° turn must fail")
	}
}
