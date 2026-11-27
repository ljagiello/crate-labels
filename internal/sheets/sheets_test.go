package sheets

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// update rewrites testdata/golden/sheets from the current code:
// go test ./internal/sheets -update
var update = flag.Bool("update", false, "rewrite golden files from the current code")

var goldenDir = filepath.Join("..", "..", "testdata", "golden", "sheets")

func catalog(t *testing.T, userStocks string) *printer.Catalog {
	t.Helper()
	dir := t.TempDir()
	if userStocks != "" {
		if err := os.WriteFile(filepath.Join(dir, "stocks.toml"), []byte(userStocks), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := printer.LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// testGrids are uncut sheets defined here rather than taken from the catalog, so the
// fixtures do not depend on how the catalog lays out its own uncut grids. Their margins
// let the trim marks run full length, be shortened, and be left out.
const testGrids = `
[[stock]]
id = "test-uncut-roomy"
name = "Letter, 3×6 cells of 60×40 mm"
media = "sheet"
design_mm = [60, 40]
feed_mm = [215.9, 279.4]
sheet = { paper_mm = [215.9, 279.4], rows = 6, cols = 3, margin_mm = [17.95, 19.7], pitch_mm = [60, 40], cell_mm = [60, 40] }

[[stock]]
id = "test-uncut-tight"
name = "Letter, 4×10 cells of 2×1 in"
media = "sheet"
design_mm = [50.8, 25.4]
feed_mm = [215.9, 279.4]
sheet = { paper_mm = [215.9, 279.4], rows = 10, cols = 4, margin_mm = [6.35, 12.7], pitch_mm = [50.8, 25.4], cell_mm = [50.8, 25.4] }

[[stock]]
id = "test-uncut-edge"
name = "A4, 4×11 cells of 52×26 mm"
media = "sheet"
design_mm = [52, 26]
feed_mm = [210, 297]
sheet = { paper_mm = [210, 297], rows = 11, cols = 4, margin_mm = [1, 5.5], pitch_mm = [52, 26], cell_mm = [52, 26] }
`

// fixtureStocks are imposed against golden files: every catalog label and die-cut
// sheet stock a page printer uses, plus the test grids.
var fixtureStocks = []string{
	"15x50", "40x30", "4x6", "50x30", "50x80-two-track", "70x80", "center-7in", "d30-15mm-tape",
	"avery-5160", "avery-5163", "test-uncut-roomy", "test-uncut-tight", "test-uncut-edge",
}

// goldenJSON compares v, rendered as indented JSON, with path, or writes it under
// -update.
func goldenJSON(t *testing.T, path string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the code's output; run go test -update after checking the change", path)
	}
}

// goldenPNG compares pixels with the PNG at path (so a different PNG encoder does not
// matter), or writes it under -update.
func goldenPNG(t *testing.T, path string, got *image.Gray) {
	t.Helper()
	got = Normalize(got)
	if *update {
		var buf bytes.Buffer
		if err := png.Encode(&buf, got); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("%s: %T", path, img)
	}
	if got.Bounds() != want.Bounds() {
		t.Fatalf("%s: page %v, fixture %v", path, got.Bounds(), want.Bounds())
	}
	if i := firstDiff(got.Pix, want.Pix); i >= 0 {
		w := want.Bounds().Dx()
		t.Fatalf("%s: first difference at (%d, %d): %d, want %d", path, i%w, i/w, got.Pix[i], want.Pix[i])
	}
}

func firstDiff(a, b []byte) int {
	for i := range a {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// blackLabel is a black design with two white last rows and columns, so labels in
// abutting cells stay apart.
func blackLabel(w, h int) *image.Gray {
	g := White(w, h)
	for y := 0; y < h-2; y++ {
		clear(g.Pix[y*w : y*w+w-2])
	}
	return g
}

// inkBoxes finds [x0, y0, x1, y1) of each dark rectangle, in scan order.
func inkBoxes(g *image.Gray) [][4]int {
	w, h := g.Bounds().Dx(), g.Bounds().Dy()
	dark := func(x, y int) bool { return g.Pix[y*g.Stride+x] < 128 }
	seen := make([]bool, w*h)
	var boxes [][4]int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !dark(x, y) || seen[y*w+x] {
				continue
			}
			x1 := x
			for x1 < w && dark(x1, y) {
				x1++
			}
			y1 := y
			for y1 < h && dark(x, y1) {
				y1++
			}
			boxes = append(boxes, [4]int{x, y, x1, y1})
			for yy := y; yy < y1; yy++ {
				for xx := x; xx < x1; xx++ {
					seen[yy*w+xx] = true
				}
			}
		}
	}
	return boxes
}

func labels(n int, l *image.Gray) []*image.Gray {
	out := make([]*image.Gray, n)
	for i := range out {
		out[i] = l
	}
	return out
}

func TestImposeMatchesFixtures(t *testing.T) {
	cat := catalog(t, testGrids)
	model, err := cat.Model("cups")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range fixtureStocks {
		t.Run(id, func(t *testing.T) {
			s, err := cat.Stock(id)
			if err != nil {
				t.Fatal(err)
			}
			cv := printer.DefaultCanvas(model, s)
			label := blackLabel(cv.Width, cv.Height)
			fx := map[string]any{"stock": id, "design_mm": s.DesignMM, "rotate_cw": s.RotateCW, "canvas_px": [2]int{cv.Width, cv.Height}, "page_mm": PageMM(s)}
			g := s.Sheet
			if g == nil {
				pages := Impose([]*image.Gray{label}, s, 300, false)
				fx["page_px"] = [2]int{pages[0].Bounds().Dx(), pages[0].Bounds().Dy()}
				fx["ink_boxes_px"] = inkBoxes(pages[0])
				fx["page_png"] = id + ".page1.png"
				goldenPNG(t, filepath.Join(goldenDir, id+".page1.png"), pages[0])
				goldenJSON(t, filepath.Join(goldenDir, id+".json"), fx)
				return
			}
			capacity := Capacity(g)
			full := Impose(labels(capacity+1, label), s, 300, DefaultCropMarks(s))
			marks := Impose([]*image.Gray{White(cv.Width, cv.Height)}, s, 300, true)[0]
			counts := map[string]int{}
			for _, n := range []int{1, capacity, capacity + 1, 3 * capacity} {
				counts[fmt.Sprint(n)] = len(PagesFor(s, n))
				if got := len(Impose(labels(n, label), s, 300, false)); got != counts[fmt.Sprint(n)] {
					t.Errorf("%d labels: %d pages, PagesFor says %d", n, got, counts[fmt.Sprint(n)])
				}
			}
			fx["grid"] = map[string]any{"rows": g.Rows, "cols": g.Cols, "margin_mm": g.MarginMM, "pitch_mm": g.PitchMM, "cell_mm": g.CellMM, "uncut": IsUncut(g)}
			fx["capacity"] = capacity
			fx["default_crop_marks"] = DefaultCropMarks(s)
			fx["pages_for"] = counts
			fx["page_px"] = [2]int{full[0].Bounds().Dx(), full[0].Bounds().Dy()}
			fx["page2_ink_boxes_px"] = inkBoxes(full[1])
			fx["trim_mark_boxes_px"] = inkBoxes(marks)
			fx["page_png"] = id + ".full-page.png"
			fx["marks_png"] = id + ".marks-only.png"
			goldenPNG(t, filepath.Join(goldenDir, id+".full-page.png"), full[0])
			goldenPNG(t, filepath.Join(goldenDir, id+".marks-only.png"), marks)
			goldenJSON(t, filepath.Join(goldenDir, id+".json"), fx)
		})
	}
}

// markBoxes draws only the trim marks of g at dpi and returns their boxes.
func markBoxes(g *printer.SheetGrid, dpi float64) [][4]int {
	page := White(MMToPx(g.PaperMM[0], dpi), MMToPx(g.PaperMM[1], dpi))
	DrawTrimMarks(page, g, dpi)
	return inkBoxes(page)
}

func TestTrimMarkGeometry(t *testing.T) {
	// 2×2 cells of 50 mm from (50, 50) on a 200 mm square: 50 mm margins all round, so
	// every mark is full length. At 254 dpi a millimetre is exactly 10 pixels.
	g := &printer.SheetGrid{PaperMM: [2]float64{200, 200}, Rows: 2, Cols: 2, MarginMM: [2]float64{50, 50},
		PitchMM: [2]float64{50, 50}, CellMM: [2]float64{50, 50}}
	got := markBoxes(g, 254)
	px := func(mm float64) int { return MMToPx(mm, 254) }
	var want [][4]int
	// Above the grid: x = 50, 100, 150 mm; from 4 mm to 1 mm above the top edge.
	for _, x := range []float64{50, 100, 150} {
		want = append(want, [4]int{px(x - 0.125), px(50 - 4), px(x + 0.125), px(50 - 1)})
	}
	// Left and right of each horizontal seam, in scan order.
	for _, y := range []float64{50, 100, 150} {
		want = append(want, [4]int{px(50 - 4), px(y - 0.125), px(50 - 1), px(y + 0.125)})
		want = append(want, [4]int{px(151), px(y - 0.125), px(154), px(y + 0.125)})
	}
	for _, x := range []float64{50, 100, 150} {
		want = append(want, [4]int{px(x - 0.125), px(151), px(x + 0.125), px(154)})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("marks\n%v\nwant\n%v", got, want)
	}
	// Marks are 0.25 mm wide: 3 pixels at 300 dpi, and never less than one.
	if lo, hi := band(50, 300); hi-lo != 3 {
		t.Fatalf("300 dpi band %d..%d", lo, hi)
	}
	if lo, hi := band(10, 72); hi-lo != 1 {
		t.Fatalf("72 dpi band %d..%d", lo, hi)
	}
}

func TestTrimMarksKeepClearOfThePaperEdge(t *testing.T) {
	for _, tc := range []struct {
		margin      float64
		near, far   float64
		drawn       bool
		description string
	}{
		{20, 1, 4, true, "full length"},
		{7.95, 1, 3.45, true, "ends 4.5 mm from the edge"},
		{6.0, 1, 1.5, true, "shortest drawn"},
		{5.9, 0, 0, false, "too little room"},
	} {
		near, far, ok := markSpan(tc.margin)
		if ok != tc.drawn || (ok && (math.Abs(near-tc.near) > 1e-9 || math.Abs(far-tc.far) > 1e-9)) {
			t.Errorf("%s: margin %v gives %v..%v (%v)", tc.description, tc.margin, near, far, ok)
		}
	}
}

// Every catalog stock with an uncut grid: the grid is centred, leaves at least
// printer.UncutClearMM on every side but has no room for another row or column, and its
// trim marks stay in the margins, clear of the cells and of the paper's outer border,
// on all four sides.
func TestCatalogUncutGrids(t *testing.T) {
	papers := map[string][2]float64{"letter": {215.9, 279.4}, "a4": {210, 297}, "legal": {215.9, 355.6}}
	cells := [][2]float64{{50, 30}, {80, 48}, {50, 80}, {40, 30}, {70, 80}, {29, 90}, {50.8, 25.4}, {101.6, 152.4}, {50, 50}}
	var toml string
	for name, p := range papers {
		for i, c := range cells {
			toml += fmt.Sprintf("[[stock]]\nid = \"%s-%d\"\nname = \"x\"\nmedia = \"sheet\"\ndesign_mm = [%g, %g]\nfeed_mm = [%g, %g]\nsheet = { paper_mm = [%g, %g], grid = \"uncut\", cell_mm = [%g, %g] }\n\n",
				name, i, c[0], c[1], p[0], p[1], p[0], p[1], c[0], c[1])
		}
	}
	cat := catalog(t, toml)
	for name, p := range papers {
		for i, c := range cells {
			id := fmt.Sprintf("%s-%d", name, i)
			s, err := cat.Stock(id)
			if err != nil {
				t.Fatal(err)
			}
			g := s.Sheet
			w, h := float64(g.Cols)*g.CellMM[0], float64(g.Rows)*g.CellMM[1]
			right, bottom := p[0]-g.MarginMM[0]-w, p[1]-g.MarginMM[1]-h
			switch {
			case g.Rows < 1 || g.Cols < 1 || !IsUncut(g) || Capacity(g) != g.Rows*g.Cols || g.CellMM != c:
				t.Errorf("%s: grid %+v", id, g)
			case g.MarginMM[0] < 0 || g.MarginMM[1] < 0 || right < -1e-6 || bottom < -1e-6:
				t.Errorf("%s: grid %+v runs off the paper", id, g)
			case math.Abs(g.MarginMM[0]-right) > 1e-6 || math.Abs(g.MarginMM[1]-bottom) > 1e-6:
				t.Errorf("%s: grid %+v is not centred", id, g)
			case g.MarginMM[0] < printer.UncutClearMM-1e-6 || g.MarginMM[1] < printer.UncutClearMM-1e-6:
				t.Errorf("%s: grid %+v leaves less than %g mm", id, g, printer.UncutClearMM)
			case 2*g.MarginMM[0] >= g.CellMM[0]+2*printer.UncutClearMM-1e-6 || 2*g.MarginMM[1] >= g.CellMM[1]+2*printer.UncutClearMM-1e-6:
				t.Errorf("%s: grid %+v has room for another cell", id, g)
			}
			const dpi = 300
			grid := image.Rect(MMToPx(g.MarginMM[0], dpi), MMToPx(g.MarginMM[1], dpi), MMToPx(g.MarginMM[0]+w, dpi), MMToPx(g.MarginMM[1]+h, dpi))
			clear := image.Rect(MMToPx(markInsetMM, dpi)-1, MMToPx(markInsetMM, dpi)-1, MMToPx(p[0]-markInsetMM, dpi)+1, MMToPx(p[1]-markInsetMM, dpi)+1)
			boxes := markBoxes(g, dpi)
			if want := 2*(g.Cols+1) + 2*(g.Rows+1); len(boxes) != want {
				t.Errorf("%s: %d trim marks, want %d", id, len(boxes), want)
			}
			for _, b := range boxes {
				r := image.Rect(b[0], b[1], b[2], b[3])
				if r.Overlaps(grid) || !r.In(clear) {
					t.Errorf("%s: mark %v touches the grid %v or leaves %v", id, r, grid, clear)
				}
			}
		}
	}
}

func TestNeverScaledUpButScaledDownToFit(t *testing.T) {
	s, _ := catalog(t, "").Stock("avery-5160")
	page := Impose([]*image.Gray{image.NewGray(image.Rect(0, 0, 100, 50))}, s, 300, false)[0]
	boxes := inkBoxes(page)
	cellX, cellW := MMToPx(s.Sheet.MarginMM[0], 300), MMToPx(s.Sheet.CellMM[0], 300)
	if len(boxes) != 1 || boxes[0][2]-boxes[0][0] != 100 || boxes[0][3]-boxes[0][1] != 50 || boxes[0][0] != cellX+(cellW-100)/2 {
		t.Fatalf("small design: %v", boxes)
	}
	page = Impose([]*image.Gray{image.NewGray(image.Rect(0, 0, 2*cellW, 300))}, s, 300, false)[0]
	boxes = inkBoxes(page)
	if len(boxes) != 1 || boxes[0][2]-boxes[0][0] != cellW || boxes[0][3]-boxes[0][1] != 150 {
		t.Fatalf("big design: %v", boxes)
	}
}

func TestSingleLabelsTurnClockwise(t *testing.T) {
	s, _ := catalog(t, "").Stock("50x80-two-track") // 80×48 design, 50×80 feed, 90° cw
	design := White(MMToPx(80, 300), MMToPx(48, 300))
	for y := range 10 {
		clear(design.Pix[y*design.Stride : y*design.Stride+10]) // the design's top-left corner
	}
	pages := Impose([]*image.Gray{design, design}, s, 300, true)
	if len(pages) != 2 || pages[0].Bounds() != image.Rect(0, 0, MMToPx(50, 300), MMToPx(80, 300)) {
		t.Fatalf("pages %d %v", len(pages), pages[0].Bounds())
	}
	xOff := (pages[0].Bounds().Dx() - design.Bounds().Dy()) / 2
	want := [4]int{xOff + design.Bounds().Dy() - 10, 0, xOff + design.Bounds().Dy(), 10}
	if got := inkBoxes(pages[0]); len(got) != 1 || got[0] != want {
		t.Fatalf("after a clockwise turn the top-left lands top-right: %v, want %v", got, want)
	}
}

func TestRotateAndResize(t *testing.T) {
	g := image.NewGray(image.Rect(0, 0, 3, 2))
	copy(g.Pix, []byte{1, 2, 3, 4, 5, 6})
	for deg, want := range map[int][]byte{
		90:  {4, 1, 5, 2, 6, 3},
		180: {6, 5, 4, 3, 2, 1},
		270: {3, 6, 2, 5, 1, 4},
		0:   {1, 2, 3, 4, 5, 6},
	} {
		if got := Rotate(g, deg); !reflect.DeepEqual(got.Pix, want) {
			t.Errorf("%d°: %v", deg, got.Pix)
		}
	}
	if got := Resize(g, 6, 4).Pix; !reflect.DeepEqual(got, []byte{1, 1, 2, 2, 3, 3, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 4, 4, 5, 5, 6, 6}) {
		t.Errorf("doubling: %v", got)
	}
	half := image.NewGray(image.Rect(0, 0, 4, 2))
	copy(half.Pix, []byte{0, 255, 100, 100, 0, 255, 50, 50})
	if got := Resize(half, 2, 1).Pix; !reflect.DeepEqual(got, []byte{128, 75}) {
		t.Errorf("box: %v", got)
	}
	// A third: each output pixel averages 1.5 source pixels.
	third := image.NewGray(image.Rect(0, 0, 3, 1))
	copy(third.Pix, []byte{0, 90, 180})
	if got := Resize(third, 2, 1).Pix; !reflect.DeepEqual(got, []byte{30, 150}) {
		t.Errorf("box thirds: %v", got)
	}
	sub := g.SubImage(image.Rect(1, 0, 3, 2)).(*image.Gray)
	if got := Normalize(sub).Pix; !reflect.DeepEqual(got, []byte{2, 3, 5, 6}) {
		t.Errorf("normalize: %v", got)
	}
}

func TestPasteCentred(t *testing.T) {
	dst := White(5, 3)
	PasteCentred(dst, image.NewGray(image.Rect(0, 0, 2, 1)))
	if got := inkBoxes(dst); len(got) != 1 || got[0] != [4]int{1, 1, 3, 2} {
		t.Fatalf("smaller: %v", got)
	}
	dst = White(2, 2)
	src := White(3, 3)
	src.Pix[0] = 0 // top-left corner of the larger image overhangs by one pixel
	src.Pix[4] = 0 // its middle lands on the destination's top-left
	PasteCentred(dst, src)
	if !reflect.DeepEqual(dst.Pix, []byte{0, 255, 255, 255}) {
		t.Fatalf("larger: %v", dst.Pix)
	}
}

func TestPageMMForContinuousStock(t *testing.T) {
	tape := &printer.Stock{Media: printer.MediaContinuous, DesignMM: [2]float64{50, 12}, FeedMM: [2]float64{15, 0}, RotateCW: 90}
	if got := PageMM(tape); got != [2]float64{15, 50} {
		t.Fatalf("%v", got)
	}
	grid := &printer.SheetGrid{PaperMM: [2]float64{215.9, 279.4}, Rows: 1, Cols: 1, PitchMM: [2]float64{10, 10}, CellMM: [2]float64{10, 10}}
	if got := Impose(nil, &printer.Stock{Media: printer.MediaSheet, Sheet: grid}, 300, true); got != nil {
		t.Fatalf("empty input: %v", got)
	}
}

func TestCutLines(t *testing.T) {
	// Two columns with a gutter: four edges. Three abutting columns: four seams.
	if got := cutLines(3.96875, 106.3625, 101.6, 2); len(got) != 4 || math.Abs(got[1]-105.56875) > 1e-9 || math.Abs(got[2]-110.33125) > 1e-9 {
		t.Fatalf("gutter %v", got)
	}
	if got := cutLines(10, 50, 50, 3); !reflect.DeepEqual(got, []float64{10, 60, 110, 160}) {
		t.Fatalf("uncut %v", got)
	}
}
