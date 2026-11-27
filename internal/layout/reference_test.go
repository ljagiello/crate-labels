package layout

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// Reference renders live in testdata/layout-reference as 1-bit PNGs, one per template ×
// supported stock × resolution × sample. Regenerate them after an intended change with
//
//	go test ./internal/layout -run TestReferenceRenders -update
//
// Set CRATE_LABELS_CONTACT_DIR to also write one contact sheet per template there.
var update = flag.Bool("update", false, "rewrite testdata/layout-reference from the current templates")

const referenceDir = "../../testdata/layout-reference"

var sampleDPIs = []float64{203.2, 300}

func testCatalog(t testing.TB) *printer.Catalog {
	t.Helper()
	cat, err := printer.LoadCatalog(t.TempDir()) // built-in stocks only
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// stocks lists the catalog's stocks in a stable order.
func stocks(cat *printer.Catalog) []*printer.Stock {
	ids := make([]string, 0, len(cat.Stocks))
	for id := range cat.Stocks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*printer.Stock, len(ids))
	for i, id := range ids {
		out[i] = cat.Stocks[id]
	}
	return out
}

// canvasFor is the canvas a driver would ask for: design mm × dpi, rounded.
func canvasFor(s *printer.Stock, dpi float64) printer.Canvas {
	d := dpi / printer.MMPerInch
	return printer.Canvas{Width: int(s.DesignMM[0]*d + 0.5), Height: int(s.DesignMM[1]*d + 0.5), DPI: dpi}
}

type renderCase struct {
	template string
	stock    *printer.Stock
	dpi      float64
	record   string
}

func (c renderCase) name() string {
	return fmt.Sprintf("%s__%s__%s__%s", c.template, c.stock.ID, strconv.FormatFloat(c.dpi, 'g', -1, 64), c.record)
}

func renderCases(cat *printer.Catalog) []renderCase {
	var out []renderCase
	for _, t := range Templates() {
		for _, s := range stocks(cat) {
			if !t.Supports(s) {
				continue
			}
			for _, dpi := range sampleDPIs {
				records := SampleNames
				if t.ID() == "test-pattern" {
					records = []string{"pattern"}
				}
				for _, r := range records {
					out = append(out, renderCase{t.ID(), s, dpi, r})
				}
			}
		}
	}
	return out
}

func caseData(record string) *LabelData {
	if record == "pattern" {
		return &LabelData{}
	}
	return Sample(record)
}

// inkOf thresholds a render at mid-grey. The raster stage dithers instead, but the
// threshold is a stable summary of the layout that is small to store and to compare.
func inkOf(img *image.Gray) *image.Paletted {
	out := image.NewPaletted(img.Rect, color.Palette{color.Gray{255}, color.Gray{0}})
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			if img.Pix[y*img.Stride+x] < 128 {
				out.Pix[y*out.Stride+x] = 1
			}
		}
	}
	return out
}

// TestReferenceRenders renders every case and compares its ink with the reference
// file. A few differing pixels (0.2 % of the inked ones) are tolerated, because
// anti-aliased edges can land either side of the threshold on another CPU.
func TestReferenceRenders(t *testing.T) {
	cat := testCatalog(t)
	cases := renderCases(cat)
	want := map[string]bool{}
	for _, c := range cases {
		want[c.name()+".png"] = true
		img, err := Render(c.template, caseData(c.record), canvasFor(c.stock, c.dpi), c.stock, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name(), err)
		}
		ink := inkOf(img)
		path := filepath.Join(referenceDir, c.name()+".png")
		if *update {
			writePNG(t, path, ink)
			continue
		}
		ref, err := readInk(path)
		if err != nil {
			t.Errorf("%s: no reference (run with -update): %v", c.name(), err)
			continue
		}
		if ref.Rect != ink.Rect {
			t.Errorf("%s: size %v, reference %v", c.name(), ink.Rect, ref.Rect)
			continue
		}
		diff := 0
		for i := range ink.Pix {
			if ink.Pix[i] != ref.Pix[i] {
				diff++
			}
		}
		// The tolerance is a share of the ink, not of the canvas, so even a lost line of
		// small text on a large label fails.
		if inked := inkCount(ref); diff > max(8, inked/500) {
			t.Errorf("%s: %d pixels differ from the reference (%d inked; run with -update if intended)", c.name(), diff, inked)
		}
	}
	entries, err := os.ReadDir(referenceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !want[e.Name()] {
			if *update {
				if err := os.Remove(filepath.Join(referenceDir, e.Name())); err != nil {
					t.Fatal(err)
				}
				continue
			}
			t.Errorf("stale reference %s matches no template, stock and sample", e.Name())
		}
	}
	if dir := os.Getenv("CRATE_LABELS_CONTACT_DIR"); dir != "" {
		writeContactSheets(t, cat, dir)
	}
}

func inkCount(p *image.Paletted) int {
	n := 0
	for _, v := range p.Pix {
		n += int(v)
	}
	return n
}

func writePNG(t testing.TB, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func readInk(path string) (*image.Paletted, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	out := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), color.Palette{color.Gray{255}, color.Gray{0}})
	for y := range b.Dy() {
		for x := range b.Dx() {
			if g := color.GrayModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.Gray); g.Y < 128 {
				out.Pix[y*out.Stride+x] = 1
			}
		}
	}
	return out, nil
}

// writeContactSheets writes, per template, every supported stock × sample at 203.2 dpi
// in a grid on grey, each tile captioned with its stock and sample.
func writeContactSheets(t *testing.T, cat *printer.Catalog, dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const pad = 24
	fc := familyFaces(DefaultFontFamily)
	for _, tmpl := range Templates() {
		var tiles []*image.Gray
		var captions []string
		for _, s := range stocks(cat) {
			if !tmpl.Supports(s) {
				continue
			}
			records := slices.Clone(SampleNames)
			if tmpl.ID() == "test-pattern" {
				records = []string{"pattern"}
			}
			for _, r := range records {
				img, err := Render(tmpl.ID(), caseData(r), canvasFor(s, 203.2), s, nil)
				if err != nil {
					t.Fatal(err)
				}
				tiles = append(tiles, img)
				captions = append(captions, s.ID+" · "+r)
			}
		}
		cols := 3
		if tmpl.ID() == "test-pattern" {
			cols = 4
		}
		colW := make([]int, cols)
		var rowH []int
		for i, tile := range tiles {
			colW[i%cols] = max(colW[i%cols], tile.Rect.Dx())
			if i%cols == 0 {
				rowH = append(rowH, 0)
			}
			rowH[i/cols] = max(rowH[i/cols], tile.Rect.Dy())
		}
		const captionH = 40
		width, height := pad, pad
		for _, w := range colW {
			width += w + pad
		}
		for _, h := range rowH {
			height += h + captionH + pad
		}
		cv := newCanvas(width, height)
		for i := range cv.img.Pix {
			cv.img.Pix[i] = 200
		}
		sh := newSheet(cv, 8)
		y := pad
		for r, h := range rowH {
			x := pad
			for c := range cols {
				i := r*cols + c
				if i >= len(tiles) {
					break
				}
				cv.blit(tiles[i], x, y)
				b := textBlock{face: fc.regular, size: 3, lines: []string{captions[i]}}
				sh.text(b, float64(x)/8, float64(y+h+10)/8, "")
				x += colW[c] + pad
			}
			y += h + captionH + pad
		}
		writePNG(t, filepath.Join(dir, tmpl.ID()+".png"), cv.img)
	}
	t.Logf("contact sheets in %s", strings.TrimSpace(dir))
}
