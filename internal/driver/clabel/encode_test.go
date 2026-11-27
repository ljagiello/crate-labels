package clabel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ljagiello/crate-labels/internal/driver/clabel/tspl"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/fake"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/clabel/jobs from the current code")

// jobManifest describes one job fixture. what, model, stock, settings, input and
// preview are written by hand. Everything else is a snapshot of the current code,
// rewritten by -update together with the page rasters, the page texts and preview.png.
type jobManifest struct {
	What     string         `json:"what"`
	Model    modelSpec      `json:"model"`
	Stock    stockSpec      `json:"stock"`
	Settings map[string]any `json:"settings"`
	Input    string         `json:"input"`
	Preview  string         `json:"preview"`

	Canvas   printer.Canvas `json:"canvas"`
	Layout   Layout         `json:"layout"`
	RatedDPI int            `json:"rated_dpi"`
	Pages    []pageSpec     `json:"pages"`
}

type pageSpec struct {
	Text    string  `json:"text"`   // the TSPL with DEFLATE payloads elided
	Raster  string  `json:"raster"` // the page's packed rows
	Rows    int     `json:"rows"`
	Paper   int     `json:"paper"`
	PrintMS float64 `json:"print_ms"`
}

type modelSpec struct {
	ID         string         `json:"id"`
	DPI        float64        `json:"dpi"`
	HeadDots   int            `json:"head_dots"`
	Options    map[string]any `json:"options"`
	ChunkBytes int            `json:"chunk_bytes"`
	ChunkGapMS float64        `json:"chunk_gap_ms"`
}

type stockSpec struct {
	ID       string     `json:"id"`
	Media    string     `json:"media"`
	DesignMM [2]float64 `json:"design_mm"`
	FeedMM   []*float64 `json:"feed_mm"`
	RotateCW int        `json:"rotate_cw"`
}

type goldenJob struct {
	dir      []string // path parts under goldenDir
	manifest jobManifest
	model    *printer.Model
	stock    *printer.Stock
	settings printer.Settings // model options, then the fixture's overrides
}

func readJobs() ([]goldenJob, error) {
	var jobs []goldenJob
	for _, model := range []string{"ct230b", "ct320b"} {
		entries, err := os.ReadDir(filepath.Join(goldenDir, "jobs", model))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			dir := []string{"jobs", model, e.Name()}
			raw, err := os.ReadFile(filepath.Join(append([]string{goldenDir}, append(dir, "manifest.json")...)...))
			if err != nil {
				return nil, err
			}
			var m jobManifest
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("%v: %w", dir, err)
			}
			mod := &printer.Model{ID: m.Model.ID, Label: m.Model.ID, Family: Family, Link: printer.LinkBLE,
				DPI: m.Model.DPI, HeadDots: m.Model.HeadDots, Options: printer.Settings(m.Model.Options),
				BLE: &printer.BLEProfile{ChunkBytes: m.Model.ChunkBytes, ChunkGapMS: m.Model.ChunkGapMS}}
			st := &printer.Stock{ID: m.Stock.ID, Name: m.Stock.ID, Media: printer.MediaKind(m.Stock.Media),
				DesignMM: m.Stock.DesignMM, RotateCW: m.Stock.RotateCW}
			for i, v := range m.Stock.FeedMM {
				if v != nil {
					st.FeedMM[i] = *v
				}
			}
			jobs = append(jobs, goldenJob{dir: dir, manifest: m, model: mod, stock: st,
				settings: printer.Merge(mod.Options, m.Settings)})
		}
	}
	return jobs, nil
}

func loadJobs(t *testing.T) []goldenJob {
	t.Helper()
	jobs, err := readJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 9 {
		t.Fatalf("found %d Clabel job fixtures, want 9", len(jobs))
	}
	return jobs
}

func (j goldenJob) path(name string) string {
	return filepath.Join(append([]string{goldenDir}, append(j.dir, name)...)...)
}

func readPNG(path string) (*image.Gray, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	g, ok := img.(*image.Gray)
	if !ok {
		return nil, fmt.Errorf("%s is %T, want 8-bit gray", path, img)
	}
	return g, nil
}

func (j goldenJob) encode() (*Job, error) {
	input, err := readPNG(j.path(j.manifest.Input))
	if err != nil {
		return nil, err
	}
	return EncodeJob(j.model, j.stock, input, j.settings)
}

// pageText is a page as text with every DEFLATE payload and its length elided. The
// lengths depend on the DEFLATE encoder, not on the page.
func pageText(data []byte) (string, error) {
	p, err := tspl.Parse(data)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, h := range p.Header {
		b.WriteString(h + "\r\n")
	}
	for _, band := range p.Bands {
		fmt.Fprintf(&b, "BITMAP %d,%d,%d,%d,%d,<raw DEFLATE>\n", band.X, band.Y, band.BytesPerRow, band.Rows, band.Mode)
	}
	b.WriteString(p.Footer)
	return b.String(), nil
}

func TestMain(m *testing.M) {
	flag.Parse()
	if *update {
		jobs, err := readJobs()
		if err != nil {
			panic(err)
		}
		for _, j := range jobs {
			if err := rewriteJob(j); err != nil {
				panic(fmt.Sprintf("%v: %v", j.dir, err))
			}
		}
	}
	os.Exit(m.Run())
}

func rewriteJob(j goldenJob) error {
	job, err := j.encode()
	if err != nil {
		return err
	}
	m := j.manifest
	m.Canvas, m.Layout, m.RatedDPI, m.Pages = New().Canvas(j.model, j.stock), job.Layout, RatedDPI(j.model), nil
	bpr := job.Layout.WidthDots / 8
	top := 0
	for k, p := range job.Pages {
		pg := pageSpec{Text: fmt.Sprintf("page-%02d.txt", k+1), Raster: fmt.Sprintf("page-%02d-raster.bin", k+1),
			Rows: p.Rows, Paper: int(p.Paper), PrintMS: float64(CTPacing.PrintTime(p.Rows).Milliseconds())}
		text, err := pageText(p.Data)
		if err != nil {
			return err
		}
		if err := os.WriteFile(j.path(pg.Text), []byte(text), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(j.path(pg.Raster), job.Raster[top*bpr:(top+p.Rows)*bpr], 0o644); err != nil {
			return err
		}
		top += p.Rows
		m.Pages = append(m.Pages, pg)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, job.Preview()); err != nil {
		return err
	}
	if err := os.WriteFile(j.path(m.Preview), b.Bytes(), 0o644); err != nil {
		return err
	}
	out, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(j.path("manifest.json"), append(out, '\n'), 0o644)
}

// checkPages compares pages with a fixture: the page text, rows, paper and print time
// exactly, and the band data after inflating.
func checkPages(t *testing.T, j goldenJob, pages []Page) {
	t.Helper()
	if len(pages) != len(j.manifest.Pages) {
		t.Fatalf("%v: %d pages, want %d", j.dir, len(pages), len(j.manifest.Pages))
	}
	for k, want := range j.manifest.Pages {
		got := pages[k]
		text, err := pageText(got.Data)
		if err != nil {
			t.Fatalf("%v page %d: %v", j.dir, k+1, err)
		}
		if wantText := string(readGolden(t, append(j.dir, want.Text)...)); text != wantText {
			t.Errorf("%v page %d: text\n%q\nwant\n%q", j.dir, k+1, text, wantText)
		}
		if got.Rows != want.Rows || int(got.Paper) != want.Paper || CTPacing.PrintTime(got.Rows) != msDuration(want.PrintMS) {
			t.Errorf("%v page %d: rows %d paper %d, want %d %d", j.dir, k+1, got.Rows, got.Paper, want.Rows, want.Paper)
		}
		p, _ := tspl.Parse(got.Data)
		rows, err := p.Raster()
		if err != nil {
			t.Fatalf("%v page %d: %v", j.dir, k+1, err)
		}
		if !bytes.Equal(rows, readGolden(t, append(j.dir, want.Raster)...)) {
			t.Errorf("%v page %d: inflated rows differ from %s", j.dir, k+1, want.Raster)
		}
		for _, b := range p.Bands {
			if b.X != 0 || b.Y%bandRows != 0 || b.Rows > bandRows || b.Mode != tspl.ModeDeflate || b.BytesPerRow != j.manifest.Layout.WidthDots/8 {
				t.Errorf("%v page %d: band %+v", j.dir, k+1, b)
			}
		}
	}
}

// TestPagesFromRaster runs every job from its page rasters onward.
func TestPagesFromRaster(t *testing.T) {
	for _, j := range loadJobs(t) {
		var raster []byte
		for _, p := range j.manifest.Pages {
			raster = append(raster, readGolden(t, append(j.dir, p.Raster)...)...)
		}
		ps, err := ReadPageSettings(j.settings)
		if err != nil {
			t.Fatal(err)
		}
		pages, err := Pages(j.model, j.stock, ps, raster, j.manifest.Layout.WidthDots)
		if err != nil {
			t.Fatalf("%v: %v", j.dir, err)
		}
		checkPages(t, j, pages)
	}
}

func samePixels(a, b *image.Gray) bool {
	if a.Bounds().Size() != b.Bounds().Size() {
		return false
	}
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	for y := range h {
		ra := a.Pix[a.PixOffset(a.Bounds().Min.X, a.Bounds().Min.Y+y):][:w]
		rb := b.Pix[b.PixOffset(b.Bounds().Min.X, b.Bounds().Min.Y+y):][:w]
		if !bytes.Equal(ra, rb) {
			return false
		}
	}
	return true
}

// TestImageJobs runs every fixture end to end: input.png through the shared raster,
// the layout, the TSPL pages and the preview.
func TestImageJobs(t *testing.T) {
	for _, j := range loadJobs(t) {
		t.Run(strings.Join(j.dir[1:], "/"), func(t *testing.T) {
			if c := New().Canvas(j.model, j.stock); c != j.manifest.Canvas {
				t.Fatalf("Canvas = %+v, want %+v", c, j.manifest.Canvas)
			}
			if l, err := HeadLayout(j.model, j.stock, j.settings); err != nil || l != j.manifest.Layout {
				t.Fatalf("HeadLayout = %+v, %v; want %+v", l, err, j.manifest.Layout)
			}
			if RatedDPI(j.model) != j.manifest.RatedDPI {
				t.Errorf("RatedDPI = %d, want %d", RatedDPI(j.model), j.manifest.RatedDPI)
			}
			job, err := j.encode()
			if err != nil {
				t.Fatal(err)
			}
			var want []byte
			for _, p := range j.manifest.Pages {
				want = append(want, readGolden(t, append(j.dir, p.Raster)...)...)
			}
			if !bytes.Equal(job.Raster, want) {
				t.Fatal("raster differs from the page rasters; rerun with -update if intended")
			}
			checkPages(t, j, job.Pages)
			input, _ := readPNG(j.path(j.manifest.Input))
			preview, err := New().Preview(j.model, j.stock, input, j.manifest.Settings)
			if err != nil {
				t.Fatal(err)
			}
			if wantPNG, err := readPNG(j.path(j.manifest.Preview)); err != nil || !samePixels(preview, wantPNG) {
				t.Errorf("preview %v differs from %s (%v)", preview.Bounds(), j.manifest.Preview, err)
			}
		})
	}
}

func catalog(t *testing.T) *printer.Catalog {
	t.Helper()
	c, err := printer.LoadCatalog(t.TempDir()) // never the user's real ~/.config
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCatalogPageText pins the page text every shipped model and stock produces:
// SIZE, GAP, DENSITY, SPEED, rows and paper are what the printer reads.
func TestCatalogPageText(t *testing.T) {
	c := catalog(t)
	want := map[string]string{
		"clabel-ct230b 50x80-two-track": "SIZE 48.1 mm,80.0 mm|GAP 2 mm,0 mm|SPEED 2 rows=945 paper=0",
		"clabel-ct230b 50x30":           "SIZE 48.1 mm,30.0 mm|GAP 2 mm,0 mm|SPEED 2 rows=354 paper=0",
		"clabel-ct230b 40x30":           "SIZE 48.1 mm,30.0 mm|GAP 2 mm,0 mm|SPEED 2 rows=354 paper=0",
		"clabel-ct320b 50x80-two-track": "SIZE 72.1 mm,80.0 mm|GAP 2 mm,0 mm|SPEED 1 rows=640 paper=0",
		"clabel-ct320b 50x30":           "SIZE 72.1 mm,30.0 mm|GAP 2 mm,0 mm|SPEED 1 rows=240 paper=0",
		"clabel-ct320b 40x30":           "SIZE 72.1 mm,30.0 mm|GAP 2 mm,0 mm|SPEED 1 rows=240 paper=0",
		"clabel-ct320b 70x80":           "SIZE 72.1 mm,80.0 mm|GAP 2 mm,0 mm|SPEED 1 rows=640 paper=0",
	}
	seen := 0
	for _, id := range []string{"clabel-ct230b", "clabel-ct320b"} {
		m, _ := c.Model(id)
		for _, s := range c.StocksFor(m) {
			canvas := New().Canvas(m, s)
			job, err := EncodeJob(m, s, image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height)), m.Options)
			if err != nil {
				t.Fatalf("%s %s: %v", id, s.ID, err)
			}
			key := id + " " + s.ID
			w, ok := want[key]
			if !ok {
				t.Errorf("%s: no pinned page text; add it after checking it on the printer", key)
				continue
			}
			seen++
			p, _ := tspl.Parse(job.Pages[0].Data)
			got := fmt.Sprintf("%s|%s|%s rows=%d paper=%d", p.Header[0], p.Header[1], p.Header[5], job.Pages[0].Rows, job.Pages[0].Paper)
			if len(job.Pages) != 1 || got != w || p.Header[2] != "DIRECTION 0,0" || p.Header[3] != "REFERENCE 0,0" ||
				p.Header[4] != "DENSITY 8" || p.Header[6] != "CLS" || p.Footer != "PRINT 1,1\r\n" {
				t.Errorf("%s: %d pages, %q (%q), want %q", key, len(job.Pages), got, p.Header, w)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("checked %d of %d pinned pages", seen, len(want))
	}
}

// TestCatalogPrintEndToEnd prints through the shipped TOML for both models and every
// stock they list.
func TestCatalogPrintEndToEnd(t *testing.T) {
	c := catalog(t)
	for _, id := range []string{"clabel-ct230b", "clabel-ct320b"} {
		m, err := c.Model(id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Family != Family || m.BLE.Write[roleData] == "" {
			t.Fatalf("%s lacks the data role: %+v", id, m.BLE)
		}
		target := &printer.Target{ID: id, Name: m.Label, ModelID: id}
		for _, stockID := range m.Stocks {
			canvas, err := c.CanvasFor(id, stockID)
			if err != nil {
				t.Fatal(err)
			}
			label := image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height))
			for i := range label.Pix {
				label.Pix[i] = byte(i % 253)
			}
			if _, err := c.Preview(target, stockID, label, nil); err != nil {
				t.Fatalf("%s %s: Preview: %v", id, stockID, err)
			}
			f := &fakeClabel{t: t, values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(0)}}
			link := fake.New(244)
			link.Responder = f.respond
			// Printing through the dispatcher would wait the real print time, so use a
			// fast driver with the same PrintContext.
			s, _ := c.Stock(stockID)
			pc := &printer.PrintContext{Catalog: c, Model: m, Stock: s, Target: target, Link: link,
				Settings: printer.Merge(m.Options, printer.Settings{"ble_gap_ms": 0.0})}
			res, err := fastDriver().Send(context.Background(), pc, []*image.Gray{label})
			if err != nil || !res.OK || res.Printed != 1 {
				t.Fatalf("%s %s: Send = %+v, %v", id, stockID, res, err)
			}
			job, err := EncodeJob(m, s, label, m.Options)
			if err != nil {
				t.Fatal(err)
			}
			var want []byte
			for _, p := range job.Pages {
				want = append(want, p.Data...)
			}
			if !bytes.Equal(f.stream.Bytes(), want) {
				t.Errorf("%s %s: streamed bytes differ from EncodeJob", id, stockID)
			}
		}
	}
}

// TestCatalogDispatchesToTheRegisteredDriver checks Catalog.Print reaches this driver.
func TestCatalogDispatchesToTheRegisteredDriver(t *testing.T) {
	if testing.Short() {
		t.Skip("waits one real print time (about 2.2 s)")
	}
	c := catalog(t)
	target := &printer.Target{ID: "ct", Name: "CT-320B", ModelID: "clabel-ct320b"}
	canvas, _ := c.CanvasFor(target.ModelID, "40x30")
	label := image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height))
	f := &fakeClabel{t: t, values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(0)}}
	link := fake.New(244)
	link.Responder = f.respond
	res, err := c.Print(context.Background(), target, "40x30", []*image.Gray{label},
		printer.PrintOptions{Link: link, Settings: printer.Settings{"ble_gap_ms": 0.0}})
	if err != nil || !res.OK || res.Message != "Sent 1 label to Clabel CT-320B." || f.stream.Len() == 0 {
		t.Fatalf("Print = %+v, %v", res, err)
	}
}

func TestModuleIsRegistered(t *testing.T) {
	d, err := printer.DriverFor(Family)
	if err != nil {
		t.Fatal(err)
	}
	if links := d.Links(); !reflect.DeepEqual(links, []printer.LinkKind{printer.LinkBLE, printer.LinkTCP, printer.LinkFile}) {
		t.Errorf("Links = %v", links)
	}
}

func TestCalibrationOverrides(t *testing.T) {
	c := catalog(t)
	m, _ := c.Model("clabel-ct230b")
	s, _ := c.Stock("50x30")
	canvas := New().Canvas(m, s)
	label := image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height))
	for i := range label.Pix {
		label.Pix[i] = 255
	}
	label.Pix[0] = 0
	header := func(settings printer.Settings) (*Job, []string) {
		t.Helper()
		job, err := EncodeJob(m, s, label, printer.Merge(m.Options, settings))
		if err != nil {
			t.Fatal(err)
		}
		p, err := tspl.Parse(job.Pages[0].Data)
		if err != nil {
			t.Fatal(err)
		}
		return job, p.Header
	}
	base, h := header(nil)
	if base.Layout.OffsetDots != 23 || h[4] != "DENSITY 8" || h[5] != "SPEED 2" {
		t.Fatalf("defaults: layout %+v header %q", base.Layout, h)
	}
	job, h := header(printer.Settings{"density": int64(12), "speed": int64(3), "offset_dots": int64(0)})
	if job.Layout.OffsetDots != 0 || h[4] != "DENSITY 12" || h[5] != "SPEED 3" {
		t.Errorf("overrides: layout %+v header %q", job.Layout, h)
	}
	// "darkness" is the Phomemo/Munbyn knob; Clabel reads density.
	if _, h := header(printer.Settings{"darkness": 3.0}); h[4] != "DENSITY 8" {
		t.Errorf("darkness changed the density: %q", h)
	}
	for _, bad := range []printer.Settings{{"density": 16.0}, {"speed": 0.0}, {"extra_rotate_cw": 30.0}, {"fixed_edge_mm": -1.0}} {
		_, err := EncodeJob(m, s, label, printer.Merge(m.Options, bad))
		var perr *printer.Error
		if !errors.As(err, &perr) {
			t.Errorf("%v: err = %v, want a user error", bad, err)
		}
	}
}

func TestContinuousStockSplitsIntoEvenPages(t *testing.T) {
	c := catalog(t)
	m, _ := c.Model("clabel-ct320b")
	for _, tc := range []struct {
		lengthMM float64
		pages    []int
	}{
		{400, []int{1066, 1067, 1067}}, // 3200 rows, at most 1518 per page
		{189.75, []int{1518}},          // SIZE 189.9 mm
		{189.875, []int{759, 760}},     // 1519 rows are 190.06 mm at 203 dpi
		{100, []int{800}},
	} {
		roll := &printer.Stock{ID: "roll", Name: "roll", Media: printer.MediaContinuous,
			DesignMM: [2]float64{48, tc.lengthMM}, FeedMM: [2]float64{58, 0}}
		canvas := New().Canvas(m, roll)
		job, err := EncodeJob(m, roll, image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height)), m.Options)
		if err != nil {
			t.Fatal(err)
		}
		var rows []int
		for _, p := range job.Pages {
			rows = append(rows, p.Rows)
			if p.Paper != PaperContinuous || !bytes.Contains(p.Data, []byte("GAP 0 mm,0 mm\r\n")) {
				t.Errorf("%g mm: page is not a continuous page", tc.lengthMM)
			}
		}
		if !reflect.DeepEqual(rows, tc.pages) {
			t.Errorf("%g mm (%d rows): pages %v, want %v", tc.lengthMM, len(job.Raster)/72, rows, tc.pages)
		}
	}
}
