package phomemo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// The fixtures in testdata/golden/phomemo are snapshots of this driver's own output:
// the jobs it sends and the previews it shows for a set of model, stock and setting
// combinations. Rewrite them after an intended change with:
//
//	go test ./internal/driver/phomemo -run TestGolden -update
var update = flag.Bool("update", false, "rewrite testdata/golden/phomemo from the current code")

var goldenDir = filepath.Join("..", "..", "..", "testdata", "golden", "phomemo")

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(goldenDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// pgm is a binary P5 8-bit grey image.
func pgm(img *image.Gray) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "P5\n%d %d\n255\n", img.Rect.Dx(), img.Rect.Dy())
	for y := range img.Rect.Dy() {
		b.Write(img.Pix[y*img.Stride : y*img.Stride+img.Rect.Dx()])
	}
	return b.Bytes()
}

func readPGM(t *testing.T, name string) *image.Gray {
	t.Helper()
	data := readGolden(t, name)
	var w, h, maxv int
	if _, err := fmt.Fscanf(bytes.NewReader(data), "P5\n%d %d\n%d\n", &w, &h, &maxv); err != nil || maxv != 255 {
		t.Fatalf("%s: bad PGM header: %v", name, err)
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	copy(img.Pix, data[len(data)-w*h:])
	return img
}

func catalog(t *testing.T) *printer.Catalog {
	t.Helper()
	c, err := printer.LoadCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// testCard is a reproducible w×h design: a grey ramp, a dark disc, a one-dot border
// and a solid block in the top-left corner, so turns and mirror images show.
func testCard(w, h int) *image.Gray {
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

// encode is Encode for a label that fits in one job, as its bytes.
func encode(t *testing.T, m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) []byte {
	t.Helper()
	jobs, err := Encode(m, s, label, settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d jobs, want 1", len(jobs))
	}
	return jobs[0].Data
}

func firstDiff(got, want []byte) string {
	if bytes.Equal(got, want) {
		return ""
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("length %d vs %d; first difference at byte %d: got %02x, want %02x", len(got), len(want), i, got[i], want[i])
		}
	}
	return fmt.Sprintf("length %d, want %d", len(got), len(want))
}

type goldenJob struct {
	File    string `json:"file"`
	Bytes   int    `json:"bytes"`
	Rows    int    `json:"rows"`
	PauseMS int64  `json:"pause_ms"`
}

type goldenCase struct {
	Model     string         `json:"model"`
	Stock     string         `json:"stock"`
	Settings  map[string]any `json:"settings,omitempty"`
	BlockRows int            `json:"block_rows,omitempty"`
	// Results
	Canvas   [2]int      `json:"canvas"`
	TurnCW   int         `json:"turn_cw"`
	Width    int         `json:"width"`
	Lane     [2]int      `json:"lane"`  // start, width
	Image    [4]int      `json:"image"` // x0, y0, x1, y1 of the scaled design
	Rows     int         `json:"rows"`
	Media    int         `json:"media"`
	SkipFeed bool        `json:"skip_feed"`
	TailRows int         `json:"tail_rows"`
	Jobs     []goldenJob `json:"jobs"`
	Preview  string      `json:"preview"` // at model defaults (no settings)
}

var goldenCases = map[string]goldenCase{
	"phomemo-m110__50x80-two-track":         {Model: "phomemo-m110", Stock: "50x80-two-track"},
	"phomemo-m110__50x30":                   {Model: "phomemo-m110", Stock: "50x30"},
	"phomemo-m110__40x30":                   {Model: "phomemo-m110", Stock: "40x30"},
	"phomemo-m220__50x80-two-track":         {Model: "phomemo-m220", Stock: "50x80-two-track"},
	"phomemo-m220__50x30":                   {Model: "phomemo-m220", Stock: "50x30"},
	"phomemo-m220__40x30":                   {Model: "phomemo-m220", Stock: "40x30"},
	"phomemo-m220__70x80":                   {Model: "phomemo-m220", Stock: "70x80"},
	"phomemo-d30__d30-15mm-tape":            {Model: "phomemo-d30", Stock: "d30-15mm-tape"},
	"phomemo-d30__15x50":                    {Model: "phomemo-d30", Stock: "15x50"},
	"phomemo-m110__70x80__too_wide":         {Model: "phomemo-m110", Stock: "70x80"},
	"phomemo-m110__50x80-two-track__blocks": {Model: "phomemo-m110", Stock: "50x80-two-track", BlockRows: 240},
	"phomemo-m220__40x30__center":           {Model: "phomemo-m220", Stock: "40x30", Settings: map[string]any{"paper_align": "center"}},
	"phomemo-m220__50x30__offset10":         {Model: "phomemo-m220", Stock: "50x30", Settings: map[string]any{"offset_dots": 10.0}},
	"phomemo-m110__50x30__rot180":           {Model: "phomemo-m110", Stock: "50x30", Settings: map[string]any{"extra_rotate_cw": 180.0}},
	"phomemo-m110__50x30__rot90":            {Model: "phomemo-m110", Stock: "50x30", Settings: map[string]any{"extra_rotate_cw": 90.0}},
	"phomemo-m110__50x30__speed5_dark10":    {Model: "phomemo-m110", Stock: "50x30", Settings: map[string]any{"speed": 5.0, "darkness": 10.0}},
}

func TestGoldenEncode(t *testing.T) {
	cat := catalog(t)
	d := New()
	for name, c := range goldenCases {
		t.Run(name, func(t *testing.T) {
			m, err := cat.Model(c.Model)
			if err != nil {
				t.Fatal(err)
			}
			s, err := cat.Stock(c.Stock)
			if err != nil {
				t.Fatal(err)
			}
			cv := d.Canvas(m, s)
			if cv.DPI != 203.2 {
				t.Errorf("canvas DPI %v", cv.DPI)
			}
			in := testCard(cv.Width, cv.Height)
			settings := printer.Settings(c.Settings)
			geo, err := LabelGeometry(m, s, cv.Width, cv.Height, settings)
			if err != nil {
				t.Fatal(err)
			}
			jobs, err := Encode(m, s, in, settings, c.BlockRows)
			if err != nil {
				t.Fatal(err)
			}
			prev, err := d.Preview(m, s, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			l, r := geo.Layout, geo.Layout.Image
			got := c
			got.Canvas, got.TurnCW, got.Width, got.Lane = [2]int{cv.Width, cv.Height}, geo.TurnCW, l.Width, [2]int{l.Lane.Start, l.Lane.Width}
			got.Image, got.Rows, got.Media = [4]int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y}, l.Rows, int(geo.Media)
			got.SkipFeed, got.TailRows, got.Preview = geo.SkipFeed, geo.TailRows, name+".preview.pgm"
			for i, j := range jobs {
				checkJob(t, j, geo, settings)
				got.Jobs = append(got.Jobs, goldenJob{File: fmt.Sprintf("%s.job%d.bin", name, i), Bytes: len(j.Data), Rows: j.Rows, PauseMS: j.Pause().Milliseconds()})
			}
			if *update {
				data, _ := json.MarshalIndent(got, "", "  ")
				writeGolden(t, name+".json", append(data, '\n'))
				for i, j := range jobs {
					writeGolden(t, got.Jobs[i].File, j.Data)
				}
				writeGolden(t, got.Preview, pgm(prev))
				return
			}
			var want goldenCase
			if err := json.Unmarshal(readGolden(t, name+".json"), &want); err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("manifest\n got %s\nwant %s", gotJSON, wantJSON)
			}
			for i, j := range jobs {
				if i < len(want.Jobs) {
					if d := firstDiff(j.Data, readGolden(t, want.Jobs[i].File)); d != "" {
						t.Errorf("job %d: %s", i, d)
					}
				}
			}
			wantPrev := readPGM(t, want.Preview)
			if prev.Rect != wantPrev.Rect || !bytes.Equal(prev.Pix, wantPrev.Pix) {
				t.Errorf("preview %v differs from %s (%v)", prev.Rect, want.Preview, wantPrev.Rect)
			}
		})
	}
	if !*update {
		onDisk, _ := filepath.Glob(filepath.Join(goldenDir, "*.json"))
		if len(onDisk) != len(goldenCases) {
			t.Errorf("%d manifests on disk, %d cases; remove stale files or run with -update", len(onDisk), len(goldenCases))
		}
	}
}

// checkJob parses one job and checks it against the wire format: the header with the
// job's speed, darkness and media, GS v 0 blocks of the head's width, then the footer.
func checkJob(t *testing.T, j Job, geo Geometry, settings printer.Settings) {
	t.Helper()
	speed, dark := DefaultSpeed, DefaultDarkness
	if v, ok := settings["speed"].(float64); ok {
		speed = int(v)
	}
	if v, ok := settings["darkness"].(float64); ok {
		dark = int(v)
	}
	head := []byte{0x1b, 0x4e, 0x0d, byte(speed), 0x1b, 0x4e, 0x04, byte(dark), 0x1f, 0x11, geo.Media}
	if !bytes.HasPrefix(j.Data, head) {
		t.Fatalf("header % x, want % x", j.Data[:min(11, len(j.Data))], head)
	}
	_, blocks, foot := parseJob(t, j.Data)
	rows := 0
	for _, b := range blocks {
		if b.bpr != geo.Layout.Width/8 {
			t.Errorf("block of %d bytes per row on a %d-dot head", b.bpr, geo.Layout.Width)
		}
		rows += b.rows
	}
	if rows != j.Rows || rows > jobRowLimit {
		t.Errorf("blocks hold %d rows, job says %d", rows, j.Rows)
	}
	wantFoot := "1ff00500"
	if !geo.SkipFeed {
		wantFoot += "1ff00300"
	}
	if got := fmt.Sprintf("%x", foot); got != wantFoot {
		t.Errorf("footer %s, want %s", got, wantFoot)
	}
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
