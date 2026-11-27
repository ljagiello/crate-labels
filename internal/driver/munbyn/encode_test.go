package munbyn

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ljagiello/crate-labels/internal/heatshrink"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/fake"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/munbyn/jobs from the current code")

// jobManifest describes one job fixture. The inputs (what, model, stock, settings,
// input, and width_dots for a raster-only job) are written by hand. Everything else is
// a snapshot of the current code, rewritten by -update together with raster.bin,
// preview.png and all-packages.bin.
type jobManifest struct {
	What     string          `json:"what"`
	Model    *modelSpec      `json:"model,omitempty"`
	Stock    *stockSpec      `json:"stock,omitempty"`
	Settings map[string]any  `json:"settings,omitempty"`
	Canvas   *printer.Canvas `json:"canvas,omitempty"`
	Input    string          `json:"input,omitempty"`
	Preview  string          `json:"preview,omitempty"`

	WidthDots       int           `json:"width_dots"`
	Paper           int           `json:"paper_type,omitempty"`
	Layout          *Layout       `json:"layout,omitempty"`
	Rows            int           `json:"rows"`
	RasterBytes     int           `json:"raster_bytes"`
	SectionRows     int           `json:"section_rows"`
	CompressedTotal int           `json:"compressed_total"`
	LowMemory       bool          `json:"low_memory"`
	Sections        []sectionSpec `json:"sections"`
}

type sectionSpec struct {
	RasterOffset int `json:"raster_offset"`
	RasterBytes  int `json:"raster_bytes"`
	Compressed   int `json:"compressed_bytes"`
	Packages     int `json:"packages"`
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

func (m *modelSpec) model() *printer.Model {
	return &printer.Model{ID: m.ID, Label: "Munbyn FM226", Family: Family, Link: printer.LinkBLE,
		DPI: m.DPI, HeadDots: m.HeadDots, Options: printer.Settings(m.Options),
		BLE: &printer.BLEProfile{ChunkBytes: m.ChunkBytes, ChunkGapMS: m.ChunkGapMS}}
}

func (s *stockSpec) stock() *printer.Stock {
	st := &printer.Stock{ID: s.ID, Name: s.ID, Media: printer.MediaKind(s.Media), DesignMM: s.DesignMM, RotateCW: s.RotateCW}
	for i, v := range s.FeedMM {
		if v != nil {
			st.FeedMM[i] = *v
		}
	}
	return st
}

type jobFixture struct {
	name string
	dir  string
	m    jobManifest
}

func readJobFixtures() ([]jobFixture, error) {
	root := filepath.Join(goldenDir, "jobs")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []jobFixture
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			return nil, err
		}
		f := jobFixture{name: e.Name(), dir: dir}
		if err := json.Unmarshal(raw, &f.m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, f)
	}
	return out, nil
}

func loadJobFixtures(t *testing.T) []jobFixture {
	t.Helper()
	out, err := readJobFixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 7 {
		t.Fatalf("found %d job fixtures, want at least 7", len(out))
	}
	return out
}

// encodeFixture runs the code under test for a fixture: from input.png when there is
// one, else from raster.bin.
func encodeFixture(f jobFixture) (*Job, error) {
	if f.m.Input == "" {
		raster, err := os.ReadFile(filepath.Join(f.dir, "raster.bin"))
		if err != nil {
			return nil, err
		}
		return JobFromRaster(PaperGap, raster, f.m.WidthDots)
	}
	input, err := readPNG(filepath.Join(f.dir, f.m.Input))
	if err != nil {
		return nil, err
	}
	model, stock := f.m.Model.model(), f.m.Stock.stock()
	return EncodeJob(model, stock, input, printer.Merge(model.Options, f.m.Settings))
}

func describe(f jobFixture, job *Job) jobManifest {
	m := f.m
	m.WidthDots, m.Rows, m.RasterBytes = job.Layout.WidthDots, job.Rows, len(job.Raster)
	m.SectionRows = SectionRows(job.Layout.WidthDots)
	m.Sections, m.CompressedTotal = nil, 0
	step := SectionBytes(job.Layout.WidthDots)
	for i, pkgs := range job.Sections {
		size := 0
		for _, p := range pkgs {
			size += len(bodyOf(p)[2].Bytes)
		}
		m.Sections = append(m.Sections, sectionSpec{RasterOffset: i * step,
			RasterBytes: min(step, len(job.Raster)-i*step), Compressed: size, Packages: len(pkgs)})
		m.CompressedTotal += size
	}
	m.LowMemory = m.CompressedTotal < lowMemoryBelow
	if f.m.Input != "" {
		m.Paper, m.Layout = int(job.Paper), &job.Layout
		c := New().Canvas(f.m.Model.model(), f.m.Stock.stock())
		m.Canvas = &c
	}
	return m
}

// bodyOf returns the DEVICE_PRINT fields of one package frame.
func bodyOf(frame []byte) map[int]Field {
	var r Reassembler
	payloads := r.Push(frame)
	if len(payloads) != 1 {
		return nil
	}
	return DecodeFields(DecodeFields(payloads[0])[5].Bytes)
}

func TestMain(m *testing.M) {
	flag.Parse()
	if *update {
		fixtures, err := readJobFixtures()
		if err != nil {
			panic(err)
		}
		for _, f := range fixtures {
			if err := rewriteJob(f); err != nil {
				panic(fmt.Sprintf("%s: %v", f.name, err))
			}
		}
	}
	os.Exit(m.Run())
}

func rewriteJob(f jobFixture) error {
	job, err := encodeFixture(f)
	if err != nil {
		return err
	}
	write := func(name string, data []byte) error { return os.WriteFile(filepath.Join(f.dir, name), data, 0o644) }
	if err := write("raster.bin", job.Raster); err != nil {
		return err
	}
	if err := write("all-packages.bin", bytes.Join(job.Packages(), nil)); err != nil {
		return err
	}
	if f.m.Preview != "" {
		var b bytes.Buffer
		if err := png.Encode(&b, job.Preview()); err != nil {
			return err
		}
		if err := write(f.m.Preview, b.Bytes()); err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(describe(f, job), "", " ")
	if err != nil {
		return err
	}
	return write("manifest.json", append(out, '\n'))
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

func readPNGFile(t *testing.T, path string) *image.Gray {
	t.Helper()
	g, err := readPNG(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
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

// checkPackages verifies a job's packages against the DEVICE_PRINT layout without any
// stored bytes. Each package is one valid frame. The fields come in order, with the
// counts the printer expects. Every section's pieces decode, through the ported library
// decoder, to exactly its rows of the raster.
func checkPackages(t *testing.T, name string, sections [][][]byte, raster []byte, widthDots int) {
	t.Helper()
	step := SectionBytes(widthDots)
	if want := (len(raster) + step - 1) / step; len(sections) != want {
		t.Fatalf("%s: %d sections, want %d", name, len(sections), want)
	}
	total := 0
	streams := make([][]byte, len(sections))
	for s, pkgs := range sections {
		for k, frame := range pkgs {
			var r Reassembler
			payloads := r.Push(frame)
			if len(payloads) != 1 {
				t.Fatalf("%s: section %d package %d is not one frame", name, s+1, k+1)
			}
			top := DecodeFields(payloads[0])
			if Event(top[1].Num) != EventDevicePrint || len(top) != 2 {
				t.Fatalf("%s: package envelope %v", name, top)
			}
			body := top[5].Bytes
			order := fieldOrder(body)
			for i := 1; i < len(order); i++ {
				if order[i] <= order[i-1] {
					t.Fatalf("%s: fields out of order: %v", name, order)
				}
			}
			f := DecodeFields(body)
			piece := f[2].Bytes
			last := k == len(pkgs)-1
			if len(piece) == 0 || len(piece) > PackageSize || (!last && len(piece) != PackageSize) {
				t.Fatalf("%s: section %d package %d carries %d bytes", name, s+1, k+1, len(piece))
			}
			wantRowBytes := uint64(0)
			if k == 0 {
				wantRowBytes = uint64(widthDots / 8)
			}
			if f[1].Num != 1 || f[3].Num != uint64(Zigzag32(int32(len(raster)))) || f[4].Num != uint64(len(pkgs)) ||
				f[5].Num != uint64(k+1) || f[6].Num != wantRowBytes || f[7].Num != uint64(len(sections)) ||
				f[8].Num != 1 || f[12].Num != uint64(s+1) {
				t.Fatalf("%s: section %d package %d fields %v", name, s+1, k+1, f)
			}
			streams[s] = append(streams[s], piece...)
		}
		total += len(streams[s])
	}
	for s, stream := range streams {
		for k, frame := range sections[s] {
			f := bodyOf(frame)
			if f[10].Num != uint64(Zigzag32(int32(len(stream)))) || (f[11].Num == 1) != (total < lowMemoryBelow) {
				t.Fatalf("%s: section %d package %d: compressed %d, low memory %d (total %d)",
					name, s+1, k+1, f[10].Num, f[11].Num, total)
			}
		}
		got, err := heatshrink.Decode(stream)
		want := raster[s*step : min((s+1)*step, len(raster))]
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: section %d decodes to %d bytes (%v), want its %d raster bytes", name, s+1, len(got), err, len(want))
		}
	}
}

func fieldOrder(buf []byte) []int {
	var order []int
	for at := 0; at < len(buf); {
		key, next, ok := varintAt(buf, at)
		if !ok {
			return order
		}
		order = append(order, int(key>>3))
		switch key & 7 {
		case wireVarint:
			_, at, _ = varintAt(buf, next)
		case wireBytes:
			n, after, _ := varintAt(buf, next)
			at = after + int(n)
		default:
			return order
		}
	}
	return order
}

// TestJobPackaging checks every fixture's raster.bin through Sections, both structurally
// and against the stored packages.
func TestJobPackaging(t *testing.T) {
	for _, f := range loadJobFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			raster := readGolden(t, "jobs", f.name, "raster.bin")
			sections, err := Sections(raster, f.m.WidthDots)
			if err != nil {
				t.Fatal(err)
			}
			checkPackages(t, f.name, sections, raster, f.m.WidthDots)
			job := &Job{Layout: Layout{WidthDots: f.m.WidthDots}, Raster: raster, Rows: len(raster) / (f.m.WidthDots / 8), Sections: sections}
			got := describe(jobFixture{name: f.name, m: jobManifest{WidthDots: f.m.WidthDots}}, job)
			if got.RasterBytes != f.m.RasterBytes || got.Rows != f.m.Rows || got.SectionRows != f.m.SectionRows ||
				got.CompressedTotal != f.m.CompressedTotal || got.LowMemory != f.m.LowMemory || len(got.Sections) != len(f.m.Sections) {
				t.Fatalf("packaging %+v differs from the manifest; rerun with -update if intended", got)
			}
			if !bytes.Equal(bytes.Join(job.Packages(), nil), readGolden(t, "jobs", f.name, "all-packages.bin")) {
				t.Fatal("packages differ from all-packages.bin; rerun with -update if intended")
			}
		})
	}
}

// TestImageJobs runs every image fixture end to end: input.png through the shared
// raster, the layout, the packages and the preview.
func TestImageJobs(t *testing.T) {
	ran := 0
	for _, f := range loadJobFixtures(t) {
		if f.m.Input == "" {
			continue
		}
		ran++
		t.Run(f.name, func(t *testing.T) {
			model, stock := f.m.Model.model(), f.m.Stock.stock()
			if c := New().Canvas(model, stock); f.m.Canvas == nil || c != *f.m.Canvas {
				t.Fatalf("Canvas = %+v, want %+v", c, f.m.Canvas)
			}
			job, err := encodeFixture(f)
			if err != nil {
				t.Fatal(err)
			}
			if f.m.Layout == nil || job.Layout != *f.m.Layout || int(job.Paper) != f.m.Paper || job.Rows != f.m.Rows {
				t.Errorf("layout %+v paper %d rows %d, manifest %+v %d %d", job.Layout, job.Paper, job.Rows, f.m.Layout, f.m.Paper, f.m.Rows)
			}
			if !bytes.Equal(job.Raster, readGolden(t, "jobs", f.name, "raster.bin")) {
				t.Fatal("raster differs from raster.bin; rerun with -update if intended")
			}
			if !bytes.Equal(bytes.Join(job.Packages(), nil), readGolden(t, "jobs", f.name, "all-packages.bin")) {
				t.Fatal("packages differ from all-packages.bin")
			}
			input := readPNGFile(t, filepath.Join(f.dir, f.m.Input))
			preview, err := New().Preview(model, stock, input, f.m.Settings)
			if err != nil {
				t.Fatal(err)
			}
			if want := readPNGFile(t, filepath.Join(f.dir, f.m.Preview)); !samePixels(preview, want) {
				t.Errorf("preview differs from %s (%v vs %v)", f.m.Preview, preview.Bounds(), want.Bounds())
			}
		})
	}
	if ran != 6 {
		t.Fatalf("ran %d image jobs, want 6", ran)
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

// TestCatalogPrintEndToEnd prints through the shipped TOML and the dispatcher.
func TestCatalogPrintEndToEnd(t *testing.T) {
	c := catalog(t)
	m, err := c.Model("munbyn-fm226")
	if err != nil {
		t.Fatal(err)
	}
	if m.Family != Family || m.BLE.Write[roleCommand] == "" || m.BLE.Write[roleImage] == "" {
		t.Fatalf("catalog model %+v lacks the command and image roles", m)
	}
	target := &printer.Target{ID: "fm", Name: "FM226", ModelID: m.ID}
	for _, stockID := range m.Stocks {
		canvas, err := c.CanvasFor(m.ID, stockID)
		if err != nil {
			t.Fatal(err)
		}
		label := image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height))
		for i := range label.Pix {
			label.Pix[i] = byte(i % 251)
		}
		preview, err := c.Preview(target, stockID, label, nil)
		if err != nil || preview.Bounds().Dx() == 0 {
			t.Fatalf("%s: Preview: %v", stockID, err)
		}
		f := &fakeFM226{t: t, paperType: 1}
		link := fake.New(128)
		link.Responder = f.respond
		res, err := c.Print(context.Background(), target, stockID, []*image.Gray{label}, printer.PrintOptions{Link: link})
		if err != nil || !res.OK || res.Printed != 1 {
			t.Fatalf("%s: Print = %+v, %v", stockID, res, err)
		}
		job, err := EncodeJob(m, mustStock(t, c, stockID), label, m.Options)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(link.Sent(roleImage), bytes.Join(job.Packages(), nil)) {
			t.Errorf("%s: printed bytes differ from EncodeJob", stockID)
		}
		checkPackages(t, stockID, job.Sections, job.Raster, job.Layout.WidthDots)
	}
}

func mustStock(t *testing.T, c *printer.Catalog, id string) *printer.Stock {
	t.Helper()
	s, err := c.Stock(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestModuleIsRegistered(t *testing.T) {
	d, err := printer.DriverFor(Family)
	if err != nil {
		t.Fatal(err)
	}
	links := d.Links()
	if len(links) != 3 || links[0] != printer.LinkBLE || links[1] != printer.LinkTCP || links[2] != printer.LinkFile {
		t.Errorf("Links = %v", links)
	}
}

func TestCalibrationSettings(t *testing.T) {
	c := catalog(t)
	m, _ := c.Model("munbyn-fm226")
	s := mustStock(t, c, "40x30")
	canvas := New().Canvas(m, s)
	label := image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height))
	for i := range label.Pix {
		label.Pix[i] = 255
	}
	label.Pix[0] = 0 // one black dot top-left of the design
	base, err := EncodeJob(m, s, label, nil)
	if err != nil {
		t.Fatal(err)
	}
	if base.Layout.WidthDots != 320 || base.Raster[0]&0x80 == 0 {
		t.Fatalf("base layout %+v, first byte %08b", base.Layout, base.Raster[0])
	}
	// offset_dots widens the raster by whole bytes and moves the dot right.
	off, err := EncodeJob(m, s, label, printer.Settings{"offset_dots": int64(16)})
	if err != nil {
		t.Fatal(err)
	}
	if off.Layout.WidthDots != 336 || off.Layout.OffsetDots != 16 || off.Raster[2]&0x80 == 0 {
		t.Errorf("offset layout %+v, row starts %08b", off.Layout, off.Raster[:3])
	}
	// extra_rotate_cw 180 moves it to the bottom-right corner.
	rot, err := EncodeJob(m, s, label, printer.Settings{"extra_rotate_cw": 180.0})
	if err != nil {
		t.Fatal(err)
	}
	if last := rot.Raster[len(rot.Raster)-1]; last&1 == 0 || rot.Raster[0] != 0 {
		t.Errorf("rotated raster ends %08b", last)
	}
	// A negative offset is clamped; nonsense values are user errors.
	if j, err := EncodeJob(m, s, label, printer.Settings{"offset_dots": -5.0}); err != nil || j.Layout.OffsetDots != 0 {
		t.Errorf("offset -5: %v, %+v", err, j)
	}
	for _, bad := range []printer.Settings{
		{"extra_rotate_cw": 45.0}, {"darkness": 30.0}, {"offset_dots": 1.5}, {"darkness": true},
	} {
		_, err := EncodeJob(m, s, label, bad)
		perr, ok := err.(*printer.Error)
		if !ok || !strings.Contains(perr.Msg, "Setting") {
			t.Errorf("%v: err = %v, want a user error", bad, err)
		}
	}
}

func TestDarknessOnlyMovesGrayTones(t *testing.T) {
	c := catalog(t)
	m, _ := c.Model("munbyn-fm226")
	s := mustStock(t, c, "50x30")
	canvas := New().Canvas(m, s)
	bw := image.NewGray(image.Rect(0, 0, canvas.Width, canvas.Height))
	for i := range bw.Pix {
		bw.Pix[i] = byte(255 * (i / 7 % 2))
	}
	a, _ := EncodeJob(m, s, bw, nil)
	b, _ := EncodeJob(m, s, bw, printer.Settings{"darkness": 15.0})
	if !bytes.Equal(a.Raster, b.Raster) {
		t.Error("darkness changed a pure black-and-white label")
	}
	gray := image.NewGray(bw.Rect)
	for i := range gray.Pix {
		gray.Pix[i] = 160
	}
	light, _ := EncodeJob(m, s, gray, printer.Settings{"darkness": 1.0})
	dark, _ := EncodeJob(m, s, gray, printer.Settings{"darkness": 15.0})
	if ones(dark.Raster) <= ones(light.Raster) {
		t.Errorf("darkness 15 burns %d dots, darkness 1 burns %d", ones(dark.Raster), ones(light.Raster))
	}
}

func ones(b []byte) int {
	n := 0
	for _, v := range b {
		for ; v != 0; v &= v - 1 {
			n++
		}
	}
	return n
}
