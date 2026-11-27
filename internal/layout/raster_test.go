package layout

import (
	"math"
	"testing"
)

// TestFillClipsAtTheCanvasEdge fills circles that cross every edge and checks each pixel
// against the analytic shape, so clipping never smears coverage along an edge.
func TestFillClipsAtTheCanvasEdge(t *testing.T) {
	const w, h, r = 40, 30, 12.0
	for _, c := range []point{{0, 0}, {w, 0}, {0, h}, {w, h}, {-5, 15}, {45, 15}, {20, -5}, {20, 35}} {
		cv := newCanvas(w, h)
		var p path
		p.circle(c.X, c.Y, r, false)
		cv.fill(p, black, true)
		for y := range h {
			for x := range w {
				d := math.Hypot(float64(x)+0.5-c.X, float64(y)+0.5-c.Y)
				ink := cv.img.Pix[y*w+x] == black
				if d < r-1 && !ink || d > r+1 && ink {
					t.Fatalf("circle at %v: pixel (%d, %d) at distance %.1f ink=%v", c, x, y, d, ink)
				}
			}
		}
	}
}

func TestRingHasAHole(t *testing.T) {
	cv := newCanvas(50, 50)
	var p path
	p.circle(25, 25, 20, false)
	p.circle(25, 25, 15, true)
	cv.fill(p, black, true)
	if cv.img.Pix[25*50+25] != white {
		t.Error("the ring's centre is filled")
	}
	if cv.img.Pix[25*50+(25+17)] != black {
		t.Error("the ring itself is not filled")
	}
}

// inkBox is the bounding box of pixels darker than 128 ([x0, y0, x1, y1]).
func inkBox(cv *canvas) [4]int {
	img := cv.img
	b := [4]int{img.Rect.Dx(), img.Rect.Dy(), 0, 0}
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			if img.Pix[y*img.Stride+x] < 128 {
				b = [4]int{min(b[0], x), min(b[1], y), max(b[2], x+1), max(b[3], y+1)}
			}
		}
	}
	return b
}

func TestStrokesSnapToWholePixelsAboveTheFloor(t *testing.T) {
	for _, dpi := range []float64{203.2, 300} {
		dpmm := dpi / 25.4
		cv := newCanvas(200, 200)
		sh := newSheet(cv, dpmm)
		sh.hline(1, 10, 5, 0.01, "thin")
		ink := inkBox(cv)
		rows := ink[3] - ink[1]
		if float64(rows)/dpmm < minStrokeMM-1e-9 || rows < 2 {
			t.Errorf("%g dpi: a hairline request drew %d rows", dpi, rows)
		}
		m, _ := sh.find("thin")
		if m.stroke < minStrokeMM-1e-9 {
			t.Errorf("%g dpi: recorded stroke %.3f mm", dpi, m.stroke)
		}
	}
}

func TestFrameRunsInward(t *testing.T) {
	cv := newCanvas(40, 20)
	sh := newSheet(cv, 8)
	sh.frame(rect{0.5, 0.5, 4, 2}, minStrokeMM, "")
	at := func(x, y int) uint8 { return cv.img.Pix[y*40+x] }
	if at(20, 4) != black || at(20, 5) != black || at(20, 6) != white || at(20, 3) != white {
		t.Errorf("top edge column: %d %d %d %d", at(20, 3), at(20, 4), at(20, 5), at(20, 6))
	}
	if at(20, 10) != white {
		t.Error("the frame's inside is filled")
	}
}

func TestTurnedTextStaysInItsBox(t *testing.T) {
	f := plex().bold
	for _, cw := range []bool{true, false} {
		cv := newCanvas(200, 200)
		sh := newSheet(cv, 1)
		b := textBlock{face: f, size: 20, lines: []string{"SPINE"}}
		box := sh.turnedText(b, 100, 100, cw, "")
		ink := inkBox(cv)
		if float64(ink[0]) < box.x-1 || float64(ink[1]) < box.y-1 || float64(ink[2]) > box.right()+1 || float64(ink[3]) > box.bottom()+1 {
			t.Errorf("cw=%v: ink %v outside box %+v", cw, ink, box)
		}
		if ink[2]-ink[0] >= ink[3]-ink[1] {
			t.Errorf("cw=%v: ink %v is not upright", cw, ink)
		}
	}
	// Reading direction: the heavy first letter of "W..." lands at the top when turned
	// clockwise and at the bottom when turned counter-clockwise, so the ink's centre of
	// mass sits above or below the box centre.
	for _, cw := range []bool{true, false} {
		cv := newCanvas(200, 200)
		sh := newSheet(cv, 1)
		sh.turnedText(textBlock{face: f, size: 20, lines: []string{"W...."}}, 100, 100, cw, "")
		sum, n := 0, 0
		for y := range 200 {
			for x := range 200 {
				if cv.img.Pix[y*200+x] < 128 {
					sum += y
					n++
				}
			}
		}
		if above := float64(sum)/float64(n) < 100; above != cw {
			t.Errorf("cw=%v: ink centre at y=%.1f", cw, float64(sum)/float64(n))
		}
	}
}

func TestDrawnAccidentalsHaveInk(t *testing.T) {
	for _, fam := range FontFamilies() {
		fc := familyFaces(fam.ID)
		for _, r := range []rune{sharpSign, flatSign} {
			cv := newCanvas(120, 120)
			sh := newSheet(cv, 1)
			sh.text(textBlock{face: fc.bold, size: 80, lines: []string{string(r)}}, 10, 10, "")
			ink := inkBox(cv)
			w, h := ink[2]-ink[0], ink[3]-ink[1]
			// About a capital tall, narrower than tall, and with white inside the shape.
			if h < 45 || h > 80 || w >= h || w < 15 {
				t.Errorf("%s %q ink %v", fam.ID, r, ink)
			}
		}
	}
}
