package raster

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures in testdata/golden/raster are snapshots of this package's own output, so
// that a change in the dots every thermal driver sends never goes unnoticed. Rewrite
// them after an intended change with:
//
//	go test ./internal/raster -run TestGolden -update
var update = flag.Bool("update", false, "rewrite testdata/golden/raster from the current code")

var goldenDir = filepath.Join("..", "..", "testdata", "golden", "raster")

// testDesign is a reproducible w×h test card: a left-to-right grey ramp, a dark disc
// in the middle, a one-dot black border and a solid block in the top-left corner, so
// that turns and mirror images are told apart.
func testDesign(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	cx, cy, r := w/2, h/2, min(w, h)/3
	for y := range h {
		for x := range w {
			v := x * 255 / max(w-1, 1)
			switch {
			case x == 0 || y == 0 || x == w-1 || y == h-1:
				v = 0
			case x < w/6 && y < h/5:
				v = 0
			case (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r:
				v = 40 + v/4
			}
			img.Pix[y*img.Stride+x] = uint8(v)
		}
	}
	return img
}

type goldenCase struct {
	Name    string `json:"name"`
	Design  [2]int `json:"design"` // w, h of testDesign
	TurnCW  int    `json:"turn_cw"`
	Width   int    `json:"width"`
	Lane    [2]int `json:"lane"` // start, width
	Rows    int    `json:"rows"`
	MaxRows int    `json:"max_rows"`
	// Results
	RasterRows int    `json:"raster_rows"`
	Image      [4]int `json:"image"` // x0, y0, x1, y1 of the scaled design
	File       string `json:"file"`  // packed rows, MSB-first, 1 = burn
}

var goldenCases = []goldenCase{
	{Name: "lane-end", Design: [2]int{40, 30}, Width: 64, Lane: [2]int{24, 40}, Rows: 30},
	{Name: "lane-center", Design: [2]int{40, 30}, Width: 64, Lane: [2]int{12, 40}, Rows: 30},
	{Name: "lane-offset5", Design: [2]int{40, 30}, Width: 64, Lane: [2]int{5, 40}, Rows: 30},
	{Name: "too-tall", Design: [2]int{40, 30}, Width: 64, Lane: [2]int{24, 40}, Rows: 12},
	{Name: "short", Design: [2]int{40, 30}, Width: 48, Lane: [2]int{4, 40}, Rows: 60},
	{Name: "upscale", Design: [2]int{40, 30}, Width: 96},
	{Name: "follow-design", Design: [2]int{40, 30}, Width: 40},
	{Name: "capped", Design: [2]int{40, 30}, Width: 48, MaxRows: 20},
	{Name: "turn90", Design: [2]int{48, 30}, TurnCW: 90, Width: 32, Rows: 48},
	{Name: "dither-only", Design: [2]int{64, 40}, Width: 64},
}

func TestGoldenRender(t *testing.T) {
	manifest := filepath.Join(goldenDir, "cases.json")
	var want []goldenCase
	if !*update {
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &want); err != nil {
			t.Fatal(err)
		}
		if len(want) != len(goldenCases) {
			t.Fatalf("cases.json has %d cases, the test %d; run with -update", len(want), len(goldenCases))
		}
	}
	got := make([]goldenCase, len(goldenCases))
	for i, c := range goldenCases {
		f := Frame{Width: c.Width, Lane: Span{c.Lane[0], c.Lane[1]}, Rows: c.Rows, MaxRows: c.MaxRows}
		l, err := Render(ToGray(testDesign(c.Design[0], c.Design[1])), c.TurnCW, f)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		r := l.Layout.Image
		c.RasterRows, c.Image, c.File = l.Layout.Rows, [4]int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y}, c.Name+".bin"
		got[i] = c
		packed := PackRows(l.Bits)
		path := filepath.Join(goldenDir, c.File)
		if *update {
			if err := os.WriteFile(path, packed, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if got[i] != want[i] {
			t.Errorf("%s: layout %+v, want %+v", c.Name, got[i], want[i])
		}
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if d := firstDiff(packed, onDisk); d != "" {
			t.Errorf("%s: %s", c.Name, d)
		}
	}
	if *update {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(manifest, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func firstDiff(got, want []byte) string {
	if bytes.Equal(got, want) {
		return ""
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("%d bytes vs %d; first difference at byte %d", len(got), len(want), i)
		}
	}
	return fmt.Sprintf("%d bytes, want %d", len(got), len(want))
}
