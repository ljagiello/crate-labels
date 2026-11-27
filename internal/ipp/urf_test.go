package ipp

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/ljagiello/crate-labels/internal/sheets"
)

func loadGray(t *testing.T, path string) *image.Gray {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	g, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("%s: %T, want *image.Gray", path, img)
	}
	return g
}

func grayRows(rows ...[]byte) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, len(rows[0]), len(rows)))
	for y, r := range rows {
		copy(g.Pix[y*g.Stride:], r)
	}
	return g
}

// urfInput is one page of a golden case: a PNG under testdata, or an image built here.
type urfInput struct {
	png     string
	build   func(t *testing.T) *image.Gray
	dpi     int
	quality int
}

func (in urfInput) image(t *testing.T) *image.Gray {
	if in.build != nil {
		return in.build(t)
	}
	return loadGray(t, filepath.Join(goldenDir, "ipp", "urf", in.png))
}

// centredLabel is a 2×2 black label placed at actual size on the Brother's Letter
// paper at 600 dpi: a page that is nearly all repeated white lines.
func centredLabel(t *testing.T) *image.Gray {
	p := brother(t)
	img, err := Place(grayRows([]byte{0, 0}, []byte{0, 0}), ChooseMedia(p, [2]float64{215.9, 279.4}, true), 600, 300)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

var urfCases = []struct {
	name, note string
	pages      []urfInput
}{
	{"row-2x2-identical", "two equal lines share one line repetition count", []urfInput{{png: "row-2x2-identical.p1.png", dpi: 300, quality: 4}}},
	{"row-2x2-distinct", "two different lines", []urfInput{{png: "row-2x2-distinct.p1.png", dpi: 300, quality: 4}}},
	{"row-isolated-pixel", "one dark pixel between white ones", []urfInput{{png: "row-isolated-pixel.p1.png", dpi: 300, quality: 4}}},
	{"row-literal-then-run", "a literal followed by a run of three", []urfInput{{png: "row-literal-then-run.p1.png", dpi: 300, quality: 4}}},
	{"row-run-130", "a run longer than one repeat code holds", []urfInput{{png: "row-run-130.p1.png", dpi: 300, quality: 4}}},
	{"row-alternating-130", "a literal longer than one literal code holds", []urfInput{{png: "row-alternating-130.p1.png", dpi: 300, quality: 4}}},
	{"doc-8x2", "a small two-line page", []urfInput{{png: "doc-8x2.p1.png", dpi: 300, quality: 4}}},
	{"doc-two-pages-203dpi-q5", "two pages, 203 dpi, high quality", []urfInput{{png: "doc-two-pages-203dpi-q5.p1.png", dpi: 203, quality: 5}, {png: "doc-two-pages-203dpi-q5.p2.png", dpi: 203, quality: 5}}},
	{"line-repeat-cap-1x300", "300 equal lines need two repetition counts (256 + 44)", []urfInput{{png: "line-repeat-cap-1x300.p1.png", dpi: 300, quality: 4}}},
	{"noise-64x48", "random grey", []urfInput{{png: "noise-64x48.p1.png", dpi: 600, quality: 4}}},
	{"binary-200x50", "random black and white", []urfInput{{png: "binary-200x50.p1.png", dpi: 300, quality: 4}}},
	{"sparse-runs-300x40", "long runs mixed with short ones", []urfInput{{png: "sparse-runs-300x40.p1.png", dpi: 300, quality: 4}}},
	{"letter-600dpi-centred-label", "a 2×2 label at actual size on Letter at 600 dpi (built by Place)", []urfInput{{build: centredLabel, dpi: 600, quality: 4}}},
}

func TestURFGoldenCases(t *testing.T) {
	var index []map[string]any
	for _, c := range urfCases {
		t.Run(c.name, func(t *testing.T) {
			var pages []URFPage
			var inputs []string
			for _, in := range c.pages {
				pages = append(pages, URFPage{Image: in.image(t), DPI: in.dpi, Quality: in.quality})
				if in.png != "" {
					inputs = append(inputs, "ipp/urf/"+in.png)
				} else {
					inputs = append(inputs, "built by the test")
				}
			}
			doc := EncodeURF(pages)
			golden(t, filepath.Join(goldenDir, "ipp", "urf", c.name+".urf"), doc)
			index = append(index, map[string]any{"name": c.name, "note": c.note, "inputs": inputs, "urf": "ipp/urf/" + c.name + ".urf", "urf_length": len(doc)})
			dec, err := DecodeURF(doc)
			if err != nil {
				t.Fatal(err)
			}
			for i, p := range dec {
				if !bytes.Equal(p.Image.Pix, sheets.Normalize(pages[i].Image).Pix) || p.DPI != pages[i].DPI || p.Quality != pages[i].Quality {
					t.Fatalf("page %d does not decode back", i)
				}
			}
		})
	}
	if !t.Failed() {
		goldenJSON(t, filepath.Join(goldenDir, "ipp", "urf", "cases.json"), index)
	}
}

// The coding rules, worked by hand from PWG 5102.4 section 4.4: a code c < 128 repeats
// one pixel c+1 times, c > 128 introduces 257-c literal pixels.
func TestURFLineCoding(t *testing.T) {
	alternating := make([]byte, 129)
	for i := range alternating {
		alternating[i] = byte(i % 2)
	}
	for _, tc := range []struct {
		name     string
		px, want []byte
	}{
		{"one pixel", []byte{9}, []byte{0x00, 9}},
		{"two equal pixels alone", []byte{7, 7}, []byte{0x01, 7}},
		{"short runs stay in the literal", []byte{1, 2, 2, 3}, []byte{0xfd, 1, 2, 2, 3}},
		{"a pair at the end stays in the literal", []byte{0, 0xff, 0xff}, []byte{0xfe, 0, 0xff, 0xff}},
		{"a run of three is a repeat", []byte{0x10, 0x20, 0x30, 0x30, 0x30}, []byte{0xff, 0x10, 0x20, 0x02, 0x30}},
		{"repeat then literal", []byte{5, 5, 5, 6, 7, 7}, []byte{0x02, 5, 0xfe, 6, 7, 7}},
		{"130 equal pixels", bytes.Repeat([]byte{0}, 130), []byte{0x7f, 0, 0x01, 0}},
		{"129 different pixels", alternating, append(append([]byte{0x81}, alternating[:128]...), 0x00, alternating[128])},
	} {
		if got := appendLine(nil, tc.px); !bytes.Equal(got, tc.want) {
			t.Errorf("%s: got % x want % x", tc.name, got, tc.want)
		}
	}
}

// PWG 5102.4 section 4.4.1 encodes a 1-bit sample image whose packed octets, read as
// 8-bit pixels three to a line, are this page. The expected 21 octets are the ones the
// standard prints.
func TestURFMatchesThePWGSample(t *testing.T) {
	page := grayRows(
		[]byte{0x8f, 0x78, 0xf7}, []byte{0x76, 0x77, 0x67},
		[]byte{0x77, 0x77, 0x77}, []byte{0x77, 0x77, 0x77}, []byte{0x77, 0x77, 0x77}, []byte{0x77, 0x77, 0x77},
		[]byte{0x8e, 0x38, 0xe3}, []byte{0xff, 0xff, 0xff},
	)
	got := AppendURFPage(nil, URFPage{Image: page, DPI: 300, Quality: 4})[urfHeaderSize:]
	want := mustHex("00 fe 8f 78 f7 00 fe 76 77 67 03 02 77 00 fe 8e 38 e3 00 02 ff")
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x\nwant % x", got, want)
	}
}

func TestURFDocumentLayout(t *testing.T) {
	doc := EncodeURF([]URFPage{{Image: grayRows([]byte{0, 0xff}, []byte{0, 0xff}), DPI: 300, Quality: 4}})
	want := mustHex("55 4e 49 52 41 53 54 00 00 00 00 01" + // UNIRAST\0, one page
		" 08 00 01 04 00 00 00 00 00 00 00 00" + // 8 bpp, sGray, one-sided, normal quality
		" 00 00 00 02 00 00 00 02 00 00 01 2c 00 00 00 00 00 00 00 00" + // 2×2 at 300 dpi
		" 01 ff 00 ff") // two copies of a two-pixel literal
	if !bytes.Equal(doc, want) {
		t.Fatalf("% x", doc)
	}
	var parts [][]byte
	pages := []URFPage{{Image: grayRows([]byte{1, 2, 3}), DPI: 600, Quality: 5}, {Image: grayRows([]byte{4}), DPI: 600, Quality: 5}}
	for _, p := range pages {
		parts = append(parts, AppendURFPage(nil, p))
	}
	if !bytes.Equal(URFDocument(parts), EncodeURF(pages)) {
		t.Fatal("URFDocument and EncodeURF differ")
	}
	sub := grayRows([]byte{9, 9, 1, 2}, []byte{9, 9, 3, 4}).SubImage(image.Rect(2, 0, 4, 2)).(*image.Gray)
	if !bytes.Equal(AppendURFPage(nil, URFPage{Image: sub, DPI: 1, Quality: 4})[urfHeaderSize:], []byte{0x00, 0xff, 1, 2, 0x00, 0xff, 3, 4}) {
		t.Fatal("sub-image rows")
	}
}

func TestURFRoundTripsRandomPages(t *testing.T) {
	r := rand.New(rand.NewPCG(5102, 4))
	for n := range 200 {
		w, h := 1+r.IntN(400), 1+r.IntN(300)
		g := image.NewGray(image.Rect(0, 0, w, h))
		for i := range g.Pix {
			switch n % 4 {
			case 0:
				g.Pix[i] = byte(r.IntN(256))
			case 1:
				g.Pix[i] = byte(r.IntN(2)) * 255
			default: // mostly runs, and whole lines repeated
				if r.IntN(12) == 0 {
					g.Pix[i] = byte(r.IntN(3)) * 127
				} else if i >= w && r.IntN(2) == 0 {
					g.Pix[i] = g.Pix[i-w]
				} else if i > 0 {
					g.Pix[i] = g.Pix[i-1]
				}
			}
		}
		pages, err := DecodeURF(EncodeURF([]URFPage{{Image: g, DPI: 300, Quality: 4}}))
		if err != nil || len(pages) != 1 || !bytes.Equal(pages[0].Image.Pix, g.Pix) {
			t.Fatalf("page %d (%dx%d): %v", n, w, h, err)
		}
	}
}

func TestDecodeURFIsStrict(t *testing.T) {
	head := func(w, h int) []byte {
		return append([]byte(urfMagic+"\x00\x00\x00\x01"), urfHeader(w, h, 300, 4)...)
	}
	for name, data := range map[string][]byte{
		"no magic":             []byte("NOTURF\x00\x00\x00\x00\x00\x01"),
		"header cut short":     append([]byte(urfMagic+"\x00\x00\x00\x01"), make([]byte, 10)...),
		"colour":               append([]byte(urfMagic+"\x00\x00\x00\x01"), append([]byte{24, 1}, make([]byte, 30)...)...),
		"code 128":             append(head(3, 1), 0x00, 0x80),
		"run past the line":    append(head(3, 1), 0x00, 0x03, 0x00),
		"literal past line":    append(head(3, 1), 0x00, 0xfd, 1, 2, 3, 4),
		"lines past the page":  append(head(1, 2), 0x02, 0x00, 0x00),
		"bitmap cut short":     append(head(1, 2), 0x00, 0x00, 0x00),
		"literal cut short":    append(head(3, 1), 0x00, 0xfe, 1),
		"bytes after the page": append(head(1, 1), 0x00, 0x00, 0x00, 0x00),
	} {
		if _, err := DecodeURF(data); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if pages, err := DecodeURF(append(head(3, 1), 0x00, 0x02, 0x05)); err != nil || !bytes.Equal(pages[0].Image.Pix, []byte{5, 5, 5}) {
		t.Fatalf("valid page: %v %v", pages, err)
	}
}

func ExampleEncodeURF() {
	page := grayRows([]byte{0, 0, 0, 255, 255, 255, 255, 255})
	doc := EncodeURF([]URFPage{{Image: page, DPI: 300, Quality: 4}})
	fmt.Printf("% x\n", doc[12+urfHeaderSize:])
	// Output: 00 02 00 04 ff
}
