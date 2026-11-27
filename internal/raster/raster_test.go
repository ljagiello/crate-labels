package raster

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"
)

func flat(w, h int, v float32) *Gray { return NewGray(w, h, v) }

// noise is a reproducible w×h image of values in [lo, hi).
func noise(w, h int, lo, hi float32, seed uint64) *Gray {
	rng := rand.New(rand.NewPCG(seed, seed))
	g := NewGray(w, h, 0)
	for i := range g.Pix {
		g.Pix[i] = lo + (hi-lo)*rng.Float32()
	}
	return g
}

func whiteShare(b *Bits) float64 {
	n := 0
	for _, burn := range b.Pix {
		if !burn {
			n++
		}
	}
	return float64(n) / float64(len(b.Pix))
}

func TestDitherKeepsTheMeanGrey(t *testing.T) {
	for _, level := range []float32{0, 16, 51, 100, 127, 127.5, 128, 160, 200, 240, 255} {
		share := whiteShare(FloydSteinberg(flat(96, 96, level)))
		if want := float64(level) / 255; math.Abs(share-want) > 0.01 {
			t.Errorf("flat %v: %.4f of the dots are paper, want %.4f", level, share, want)
		}
	}
	// A noisy image keeps its mean too: diffusion moves error, it does not lose it.
	g := noise(128, 128, 20, 230, 7)
	var sum float64
	for _, v := range g.Pix {
		sum += float64(v)
	}
	if got, want := whiteShare(FloydSteinberg(g)), sum/float64(len(g.Pix))/255; math.Abs(got-want) > 0.01 {
		t.Errorf("noise: %.4f paper, want %.4f", got, want)
	}
}

func TestDitherExtremesAndClamping(t *testing.T) {
	for _, tc := range []struct {
		v    float32
		burn bool
	}{{0, true}, {-500, true}, {255, false}, {900, false}} {
		for i, burn := range FloydSteinberg(flat(9, 7, tc.v)).Pix {
			if burn != tc.burn {
				t.Fatalf("flat %v: dot %d burn=%v", tc.v, i, burn)
			}
		}
	}
	if FloydSteinberg(flat(1, 1, 127.5)).Pix[0] || !FloydSteinberg(flat(1, 1, 127.4)).Pix[0] {
		t.Error("a lone pixel should stay paper from mid-grey (127.5) up and burn below it")
	}
}

func TestDitherErrorFlowsRightAndDown(t *testing.T) {
	// 100 burns and passes on +100 grey: 7/16 to its right, 5/16 below. On its own,
	// 120 would burn, but either share lifts it past mid-grey.
	if got := FloydSteinberg(&Gray{W: 2, H: 1, Pix: []float32{100, 120}}).Pix; !got[0] || got[1] {
		t.Errorf("row [100 120] = %v, want [burn paper]", got)
	}
	if got := FloydSteinberg(&Gray{W: 1, H: 2, Pix: []float32{100, 120}}).Pix; !got[0] || got[1] {
		t.Errorf("column [100 120] = %v, want [burn paper]", got)
	}
	// Nothing flows left or up: changing one pixel leaves every dot before it in
	// scan order alone, and the rows below it change only from its own column's
	// neighbourhood outward.
	base := noise(40, 30, 0, 255, 3)
	ref := FloydSteinberg(base)
	for _, p := range []image.Point{{0, 0}, {17, 11}, {39, 12}, {5, 29}} {
		g := base.Clone()
		g.Set(p.X, p.Y, 255-g.At(p.X, p.Y))
		got := FloydSteinberg(g)
		for i := range p.Y*g.W + p.X {
			if got.Pix[i] != ref.Pix[i] {
				t.Fatalf("changing %v changed dot (%d, %d), which comes before it", p, i%g.W, i/g.W)
			}
		}
		if p.Y+1 < g.H {
			for x := range max(p.X-1, 0) {
				if got.At(x, p.Y+1) != ref.At(x, p.Y+1) {
					t.Fatalf("changing %v reached (%d, %d) in the next row", p, x, p.Y+1)
				}
			}
		}
	}
}

func TestDitherIsReproducible(t *testing.T) {
	g := noise(64, 40, 0, 255, 11)
	if !bytes.Equal(PackRows(FloydSteinberg(g)), PackRows(FloydSteinberg(g.Clone()))) {
		t.Error("the same image gave different dots")
	}
}

func meanOf(g *Gray) float64 {
	var s float64
	for _, v := range g.Pix {
		s += float64(v)
	}
	return s / float64(len(g.Pix))
}

func TestResampleKeepsTheMean(t *testing.T) {
	src := noise(37, 23, 0, 255, 5)
	for _, size := range [][2]int{{2, 1}, {16, 9}, {80, 50}, {11, 40}, {37, 23}, {1, 1}, {74, 46}} {
		got, err := ResampleArea(src, size[0], size[1])
		if err != nil {
			t.Fatal(err)
		}
		if got.W != size[0] || got.H != size[1] {
			t.Fatalf("size %dx%d, want %v", got.W, got.H, size)
		}
		if d := math.Abs(meanOf(got) - meanOf(src)); d > 1e-3 {
			t.Errorf("%v: mean moved by %g", size, d)
		}
	}
	for _, size := range [][2]int{{5, 3}, {41, 29}} {
		got, _ := ResampleArea(flat(13, 7, 77), size[0], size[1])
		for i, v := range got.Pix {
			if v != 77 {
				t.Fatalf("flat 77 to %v: pixel %d = %v", size, i, v)
			}
		}
	}
}

func TestResampleAveragesCoveredArea(t *testing.T) {
	for _, tc := range []struct {
		in   *Gray
		w, h int
		want []float32
	}{
		// 3 → 2: each output covers 1.5 source pixels.
		{&Gray{W: 3, H: 1, Pix: []float32{0, 90, 255}}, 2, 1, []float32{30, 200}},
		// 4×2 → 2×1: whole 2×2 blocks.
		{&Gray{W: 4, H: 2, Pix: []float32{0, 255, 100, 50, 255, 255, 0, 0}}, 2, 1, []float32{191.25, 37.5}},
		// A whole-number upscale repeats pixels.
		{&Gray{W: 2, H: 1, Pix: []float32{10, 200}}, 6, 2, []float32{10, 10, 10, 200, 200, 200, 10, 10, 10, 200, 200, 200}},
		// 2 → 3: the middle output straddles both sources equally.
		{&Gray{W: 2, H: 1, Pix: []float32{0, 90}}, 3, 1, []float32{0, 45, 90}},
	} {
		got, err := ResampleArea(tc.in, tc.w, tc.h)
		if err != nil {
			t.Fatal(err)
		}
		for i := range tc.want {
			if math.Abs(float64(got.Pix[i]-tc.want[i])) > 1e-4 {
				t.Errorf("%v → %dx%d = %v, want %v", tc.in.Pix, tc.w, tc.h, got.Pix, tc.want)
				break
			}
		}
	}
	if _, err := ResampleArea(flat(2, 2, 0), 0, 3); err == nil {
		t.Error("a zero-width target must fail")
	}
	if _, err := ResampleArea(&Gray{}, 2, 2); err == nil {
		t.Error("an empty source must fail")
	}
}

func TestToGray(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 5, 1))
	copy(img.Pix, []byte{255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255, 200, 100, 50, 255, 0, 0, 0, 0})
	got := ToGray(img).Pix
	want := []float64{0.2126 * 255, 0.7152 * 255, 0.0722 * 255, 0.2126*200 + 0.7152*100 + 0.0722*50, 255}
	for i := range want {
		if math.Abs(float64(got[i])-want[i]) > 1e-3 {
			t.Errorf("pixel %d = %v, want %v", i, got[i], want[i])
		}
	}
	// Half-transparent black over white paper is mid-grey.
	half := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	half.Pix[3] = 128
	if v := ToGray(half).At(0, 0); math.Abs(float64(v)-255*(1-128.0/255)) > 0.01 {
		t.Errorf("half-transparent black = %v", v)
	}
	// Grey images are copied as is, honouring the sub-image bounds.
	g := image.NewGray(image.Rect(0, 0, 4, 4))
	g.Pix[2*4+3] = 77
	if sub := ToGray(g.SubImage(image.Rect(2, 2, 4, 4))); sub.W != 2 || sub.H != 2 || sub.At(1, 0) != 77 {
		t.Errorf("sub-image = %+v", sub)
	}
	// Premultiplied RGBA takes the same path as straight alpha.
	rgba := image.NewRGBA(image.Rect(0, 0, 1, 1))
	rgba.Set(0, 0, color.NRGBA{200, 100, 50, 255})
	if v := ToGray(rgba).At(0, 0); math.Abs(float64(v)-want[3]) > 1e-3 {
		t.Errorf("RGBA (200,100,50) = %v, want %v", v, want[3])
	}
}

func TestGrayImageRoundsAndClamps(t *testing.T) {
	g := &Gray{W: 5, H: 1, Pix: []float32{-3, 0.4, 0.6, 254.5, 300}}
	if got := g.Image().Pix; !bytes.Equal(got, []byte{0, 0, 1, 255, 255}) {
		t.Errorf("Image() = %v", got)
	}
	nan := &Gray{W: 1, H: 1, Pix: []float32{float32(math.NaN())}}
	if nan.Image().Pix[0] != 0 {
		t.Error("NaN should become 0")
	}
}

func TestPackRows(t *testing.T) {
	// 13 dots: two bytes per row, the last three bits padding.
	b := NewBits(13, 3)
	for x := range 13 {
		b.Set(x, 0, x%2 == 0) // 1010101010101
		b.Set(x, 1, x >= 8)   // 0000000011111
	}
	b.Set(12, 2, true)
	want := []byte{0xaa, 0xa8, 0x00, 0xf8, 0x00, 0x08}
	packed := PackRows(b)
	if !bytes.Equal(packed, want) {
		t.Fatalf("PackRows = % x, want % x", packed, want)
	}
	back, err := UnpackRows(packed, 13)
	if err != nil || !equalBits(back, b) {
		t.Fatalf("UnpackRows did not invert PackRows: %v", err)
	}
	if _, err := UnpackRows([]byte{1, 2, 3}, 13); err == nil {
		t.Error("a partial row must fail")
	}
}

func equalBits(a, b *Bits) bool {
	if a.W != b.W || a.H != b.H {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

func TestRotate(t *testing.T) {
	// Rows [1 2][3 4][5 6]: a quarter turn clockwise puts the left column on top,
	// read bottom to top.
	g := &Gray{W: 2, H: 3, Pix: []float32{1, 2, 3, 4, 5, 6}}
	for _, tc := range []struct {
		deg  int
		want []float32
	}{{90, []float32{5, 3, 1, 6, 4, 2}}, {270, []float32{2, 4, 6, 1, 3, 5}}, {-90, []float32{2, 4, 6, 1, 3, 5}}, {450, []float32{5, 3, 1, 6, 4, 2}}} {
		got, err := RotateCW(g, tc.deg)
		if err != nil {
			t.Fatal(err)
		}
		if got.W != 3 || got.H != 2 || !equalPix(got.Pix, tc.want) {
			t.Errorf("rotate %d = %dx%d %v, want %v", tc.deg, got.W, got.H, got.Pix, tc.want)
		}
	}
	if half, _ := RotateCW(g, 180); !equalPix(half.Pix, []float32{6, 5, 4, 3, 2, 1}) {
		t.Errorf("rotate 180 = %v", half.Pix)
	}
	img := noise(7, 4, 0, 255, 1)
	turned := img
	for range 4 {
		turned, _ = RotateCW(turned, 90)
	}
	if !equalPix(turned.Pix, img.Pix) || turned.W != 7 {
		t.Error("four quarter turns are not the identity")
	}
	if _, err := RotateCW(g, 45); err == nil {
		t.Error("45° must fail")
	}
	b := &Bits{W: 2, H: 1, Pix: []bool{true, false}}
	if r, _ := RotateBitsCW(b, 90); r.W != 1 || r.H != 2 || !r.Pix[0] || r.Pix[1] {
		t.Errorf("bits rotate = %+v", r)
	}
}

func equalPix(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCropAndPreviewImage(t *testing.T) {
	b := NewBits(16, 2)
	b.Set(9, 1, true)
	c := b.Crop(image.Rect(8, 0, 16, 2))
	if c.W != 8 || c.H != 2 || !c.At(1, 1) || c.At(0, 0) {
		t.Errorf("crop = %+v", c)
	}
	img := BitsToImage(c)
	if img.GrayAt(1, 1).Y != 0 || img.GrayAt(0, 0).Y != 255 {
		t.Error("preview: burn must be 0 and paper 255")
	}
}
