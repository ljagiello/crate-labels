package layout

import (
	"testing"

	"rsc.io/qr"
)

func TestDiscogsURL(t *testing.T) {
	if got := DiscogsURL(2360917, false); got != "https://www.discogs.com/release/2360917" {
		t.Errorf("release URL %q", got)
	}
	if got := DiscogsURL(4589, true); got != "https://www.discogs.com/master/4589" {
		t.Errorf("master URL %q", got)
	}
}

func TestQRModulesAreWholePixelsAboveTheFloor(t *testing.T) {
	url := DiscogsURL(3071552, false) // version 3 at level M: 29 modules
	cases := []struct{ maxSide, floor, side int }{
		{87, 2, 87},   // 3 px modules exactly
		{116, 2, 116}, // 4 px
		{90, 2, 87},   // rounds down to whole modules
		{57, 2, 0},    // under 2 px a module: no code
		{60, 3, 0},    // the 300 dpi floor is 3 px
		{87, 3, 87},
	}
	for _, c := range cases {
		if got := qrPixels(url, c.maxSide, c.floor); got != c.side {
			t.Errorf("qrPixels(%d, floor %d) = %d, want %d", c.maxSide, c.floor, got, c.side)
		}
		img, k := qrBitmap(url, c.maxSide, c.floor)
		if c.side == 0 {
			if img != nil {
				t.Errorf("maxSide %d: want no code", c.maxSide)
			}
			continue
		}
		if img == nil || img.Rect.Dx() != c.side || k*29 != c.side {
			t.Errorf("qrBitmap(%d) = %v, module %d", c.maxSide, img, k)
		}
	}
	if qrPixels("", 100, 2) != 0 {
		t.Error("an empty URL must give no code")
	}
	if minModulePx(203.2/25.4) != 2 || minModulePx(300/25.4) != 3 {
		t.Errorf("module floors %d %d", minModulePx(203.2/25.4), minModulePx(300/25.4))
	}
}

// TestQREncodesTheDiscogsURL reads the code back out of rendered labels, module by
// module, and compares it with a fresh encoding (rsc.io/qr cannot decode).
func TestQREncodesTheDiscogsURL(t *testing.T) {
	cat := testCatalog(t)
	d := Sample("ep12")
	want, err := qr.Encode(d.QRURL, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	n := want.Size
	for _, c := range []struct {
		template, stock string
		dpi             float64
		opts            map[string]any
	}{
		{"dj-single", "40x30", 203.2, nil}, {"dj-single", "avery-5160", 300, nil},
		{"dj-two-track", "50x80-two-track", 203.2, nil}, {"dj-four-track", "70x80", 203.2, nil},
		{"dj-tape", "d30-15mm-tape", 203.2, map[string]any{"show_qr": true}},
		{"seller-small", "40x30", 203.2, nil}, {"seller-large", "70x80", 203.2, nil},
		{"seller-large", "50x80-two-track", 203.2, nil},
		{"center-label", "center-7in", 300, nil}, {"obi-strip", "obi-legal", 300, nil},
	} {
		s, _ := cat.Stock(c.stock)
		tmpl, _ := Get(c.template)
		sh := sketch(tmpl, d, canvasFor(s, c.dpi), s, c.opts)
		m, ok := sh.find("qr")
		if !ok {
			t.Errorf("%s on %s: no QR drawn", c.template, c.stock)
			continue
		}
		img := sh.image()
		x0, y0 := sh.px(m.box.x), sh.px(m.box.y)
		k := m.module
		if sh.px(m.box.w) != n*k {
			t.Errorf("%s: side %d px is not %d modules of %d", c.template, sh.px(m.box.w), n, k)
			continue
		}
		for y := range n {
			for x := range n {
				px, py := x0+x*k+k/2, y0+y*k+k/2
				if (img.Pix[py*img.Stride+px] < 128) != want.Black(x, y) {
					t.Fatalf("%s: module (%d, %d) differs from the encoded URL", c.template, x, y)
				}
			}
		}
	}
}

func BenchmarkEncodeQR(b *testing.B) {
	url := DiscogsURL(2360917, false)
	for b.Loop() {
		encodeQR(url)
	}
}
