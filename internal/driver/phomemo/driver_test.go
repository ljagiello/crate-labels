package phomemo

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/raster"
	"github.com/ljagiello/crate-labels/internal/transport/fake"
)

var colorBlack = color.Gray{Y: 0}

// greeting is what a current M110 sends: its hello, then the reply to the firmware query.
var greeting = [][]byte{{0x01, 0x07}, {0x1a, 0x07, 0x02, 0x04, 0x04}}

// clock is a fake time source; sleeps advance it and are recorded.
type clock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return ctx.Err()
}

// waits are the pacing sleeps; gaps are the inter-chunk ones (≤ 1 s).
func (c *clock) split() (waits, gaps []time.Duration) {
	for _, d := range c.sleeps {
		if d > time.Second {
			waits = append(waits, d)
		} else {
			gaps = append(gaps, d)
		}
	}
	return waits, gaps
}

func testDriver(t *testing.T) (*Driver, *clock) {
	t.Helper()
	t.Setenv("CRATE_LABELS_CONFIG", t.TempDir())
	c := &clock{now: time.Unix(1_000_000, 0)}
	d := New()
	d.now = func() time.Time { return c.now }
	d.sleep = c.sleep
	d.helloWindow = 50 * time.Millisecond
	d.firmwareWindow = 50 * time.Millisecond
	return d, c
}

func white(t *testing.T, d *Driver, m *printer.Model, s *printer.Stock) *image.Gray {
	t.Helper()
	cv := d.Canvas(m, s)
	img := image.NewGray(image.Rect(0, 0, cv.Width, cv.Height))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

func pcFor(t *testing.T, cat *printer.Catalog, modelID, stockID string, link printer.Link, address, name string, settings printer.Settings) *printer.PrintContext {
	t.Helper()
	m, err := cat.Model(modelID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cat.Stock(stockID)
	if err != nil {
		t.Fatal(err)
	}
	tgt := &printer.Target{ID: "t1", Name: name, ModelID: modelID, Address: address}
	return &printer.PrintContext{Catalog: cat, Model: m, Stock: s, Target: tgt, Link: link, Settings: printer.Merge(m.Options, settings)}
}

type block struct{ bpr, rows int }

// parseJob splits one job into header, raster blocks and footer.
func parseJob(t *testing.T, job []byte) (head []byte, blocks []block, foot []byte) {
	t.Helper()
	head, rest := job[:11], job[11:]
	for len(rest) >= 8 && bytes.Equal(rest[:4], []byte{0x1d, 0x76, 0x30, 0x00}) {
		bpr := int(rest[4]) | int(rest[5])<<8
		rows := int(rest[6]) | int(rest[7])<<8
		blocks = append(blocks, block{bpr, rows})
		rest = rest[8+bpr*rows:]
	}
	return head, blocks, rest
}

func TestSendTwoLabelsPacesAndChunks(t *testing.T) {
	d, clk := testDriver(t)
	cat := catalog(t)
	link := fake.New(244, greeting...)
	var fractions []float64
	pc := pcFor(t, cat, "phomemo-m110", "50x30", link, "AA", "M110-TEST", nil)
	pc.Progress = func(f float64, _ string) { fractions = append(fractions, f) }
	label := white(t, d, pc.Model, pc.Stock)

	res, err := d.Send(context.Background(), pc, []*image.Gray{label, label})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Printed != 2 || res.Details["firmware"] != "2.4.4" || res.Details["hello"] != "0107" {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := res.Details["suspected_models"]; ok {
		t.Errorf("an M110 named M110 should not suspect another model: %v", res.Details)
	}
	writes := link.Writes()
	if !bytes.Equal(writes[0].Data, FirmwareQuery) {
		t.Fatalf("first write = % x, want the firmware query", writes[0].Data)
	}
	job := encode(t, pc.Model, pc.Stock, label, nil)
	if !bytes.Equal(link.Sent("")[3:], append(append([]byte{}, job...), job...)) {
		t.Error("the two labels' jobs were not sent back to back after the query")
	}
	for _, w := range writes[1:] {
		if len(w.Data) > 128 || w.Role != "data" || w.WithResponse {
			t.Fatalf("write of %d bytes role %q response %v", len(w.Data), w.Role, w.WithResponse)
		}
	}
	chunks := (len(job) + 127) / 128
	waits, gaps := clk.split()
	if len(gaps) != 2*(chunks-1) || gaps[0] != 20*time.Millisecond {
		t.Errorf("%d gaps (first %v), want %d of 20ms", len(gaps), gaps[0], 2*(chunks-1))
	}
	if len(waits) != 1 || waits[0] != 3*time.Second {
		t.Errorf("waits = %v, want one 3s pause before the second label", waits)
	}
	for i := 1; i < len(fractions); i++ {
		if fractions[i] < fractions[i-1] {
			t.Fatalf("progress went backwards: %v", fractions)
		}
	}
	if fractions[len(fractions)-1] != 1 {
		t.Errorf("progress ends at %v", fractions[len(fractions)-1])
	}
}

func TestPauseCarriesOverToTheNextPrint(t *testing.T) {
	d, clk := testDriver(t)
	cat := catalog(t)
	first := pcFor(t, cat, "phomemo-m110", "50x30", fake.New(244, greeting...), "BB", "M110", nil)
	if _, err := d.Send(context.Background(), first, []*image.Gray{white(t, d, first.Model, first.Stock)}); err != nil {
		t.Fatal(err)
	}
	clk.now = clk.now.Add(time.Second)
	clk.sleeps = nil
	// This time the printer sends no hello and only answers the firmware query.
	second := fake.New(244)
	second.Responder = func(w fake.Write) [][]byte {
		if bytes.Equal(w.Data, FirmwareQuery) {
			return [][]byte{greeting[1]}
		}
		return nil
	}
	pc := pcFor(t, cat, "phomemo-m110", "50x30", second, "BB", "M110", nil)
	res, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)})
	if err != nil {
		t.Fatal(err)
	}
	if waits, _ := clk.split(); len(waits) != 1 || waits[0] != 2*time.Second {
		t.Errorf("waits = %v, want the 2s left of the first job's pause", waits)
	}
	// Each connection listens for its own hello; none came this time.
	if res.Details["hello"] != nil || res.Details["firmware"] != "2.4.4" {
		t.Errorf("details = %v", res.Details)
	}
}

func TestLegacyFirmwareGetsShortBlocks(t *testing.T) {
	old := [][]byte{{0x01, 0x07}, {0x1a, 0x07, 0x01, 0x09}}
	for _, tc := range []struct {
		model, name string
		blockRows   int
		blocks      []int
	}{
		{"phomemo-m110", "M110", 240, []int{240, 240, 160}},
		{"phomemo-m220", "M220", 0, []int{640}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			d, _ := testDriver(t)
			link := fake.New(244, old...)
			pc := pcFor(t, catalog(t), tc.model, "50x80-two-track", link, "CC", tc.name, nil)
			res, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)})
			if err != nil {
				t.Fatal(err)
			}
			if res.Details["block_rows"] != tc.blockRows || res.Details["firmware"] != "1.9" {
				t.Errorf("details = %v", res.Details)
			}
			_, blocks, _ := parseJob(t, link.Sent("")[3:])
			var rows []int
			for _, b := range blocks {
				rows = append(rows, b.rows)
			}
			if !slicesEqual(rows, tc.blocks) {
				t.Errorf("block rows = %v, want %v", rows, tc.blocks)
			}
		})
	}
}

func slicesEqual(a, b []int) bool {
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

func TestSilentPrinterStillPrints(t *testing.T) {
	d, _ := testDriver(t)
	link := fake.New(244)
	pc := pcFor(t, catalog(t), "phomemo-m110", "50x30", link, "EE", "M110", nil)
	res, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Details["hello"] != nil || res.Details["firmware"] != nil || res.Details["block_rows"] != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestBLEGapSettingOverridesProfile(t *testing.T) {
	d, clk := testDriver(t)
	pc := pcFor(t, catalog(t), "phomemo-m110", "50x30", fake.New(244, greeting...), "FF", "M110", printer.Settings{"ble_gap_ms": 5.0})
	if _, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)}); err != nil {
		t.Fatal(err)
	}
	_, gaps := clk.split()
	if len(gaps) == 0 {
		t.Fatal("no gaps")
	}
	for _, g := range gaps {
		if g != 5*time.Millisecond {
			t.Fatalf("gap %v, want 5ms", g)
		}
	}
}

func TestSuspectedModelIsRecordedNotObeyed(t *testing.T) {
	d, _ := testDriver(t)
	link := fake.New(244, greeting...)
	pc := pcFor(t, catalog(t), "phomemo-d30", "15x50", link, "GG", "M110-1234", nil)
	res, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := res.Details["suspected_models"].([]string); strings.Join(got, ",") != "phomemo-m110,phomemo-m220" {
		t.Errorf("details = %v, want the models that send 01 07 or are named M110", res.Details)
	}
	if _, blocks, _ := parseJob(t, link.Sent("")[3:]); blocks[0].bpr != 12 {
		t.Errorf("bytes per row %d, want the D30's 12", blocks[0].bpr)
	}
}

func TestNonBLELinkSendsOnlyJobs(t *testing.T) {
	d, clk := testDriver(t)
	cat := catalog(t)
	base, _ := cat.Model("phomemo-m110")
	m := *base
	m.Link, m.BLE = printer.LinkTCP, nil
	s, _ := cat.Stock("50x30")
	link := fake.New(1 << 16)
	pc := &printer.PrintContext{Model: &m, Stock: s, Target: &printer.Target{Address: "10.0.0.9:9100"}, Link: link, Settings: m.Options}
	label := white(t, d, &m, s)
	if _, err := d.Send(context.Background(), pc, []*image.Gray{label}); err != nil {
		t.Fatal(err)
	}
	job := encode(t, &m, s, label, nil)
	if !bytes.Equal(link.Sent(""), job) || len(link.Writes()) != 1 {
		t.Errorf("sent %d bytes in %d writes, want the job in one write", len(link.Sent("")), len(link.Writes()))
	}
	if len(clk.sleeps) != 0 {
		t.Errorf("sleeps = %v", clk.sleeps)
	}
}

// flakyLink fails every write after the first n.
type flakyLink struct {
	*fake.Link
	n int
}

func (l *flakyLink) Write(ctx context.Context, data []byte, role string, withResponse bool) error {
	if l.n <= 0 {
		return errors.New("link closed")
	}
	l.n--
	return l.Link.Write(ctx, data, role, withResponse)
}

func TestWriteFailureMidBatchSaysHowManyWereSent(t *testing.T) {
	d, _ := testDriver(t)
	cat := catalog(t)
	pc := pcFor(t, cat, "phomemo-m110", "50x30", nil, "HH", "M110", nil)
	label := white(t, d, pc.Model, pc.Stock)
	job := encode(t, pc.Model, pc.Stock, label, nil)
	pc.Link = &flakyLink{Link: fake.New(244, greeting...), n: 1 + (len(job)+127)/128}
	res, err := d.Send(context.Background(), pc, []*image.Gray{label, label})
	var pe *printer.Error
	if res.Printed != 1 || res.OK {
		t.Errorf("result = %+v, want Printed 1 and not OK", res)
	}
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "Write failed at chunk 1 of") || !strings.Contains(err.Error(), "1 of 2 labels were sent") {
		t.Fatalf("err = %v", err)
	}
}

func TestNothingToPrint(t *testing.T) {
	d, _ := testDriver(t)
	pc := pcFor(t, catalog(t), "phomemo-m110", "50x30", fake.New(244), "II", "M110", nil)
	if _, err := d.Send(context.Background(), pc, nil); err == nil || !strings.Contains(err.Error(), "Nothing to print") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadSettingsAreUserErrors(t *testing.T) {
	cat := catalog(t)
	m, _ := cat.Model("phomemo-m110")
	s, _ := cat.Stock("50x30")
	label := white(t, New(), m, s)
	for _, bad := range []printer.Settings{
		{"speed": 300}, {"speed": true}, {"darkness": "dark"}, {"paper_align": "rod"}, {"paper_align": "left"},
		{"extra_rotate_cw": 45}, {"extra_rotate_cw": -90}, {"extra_rotate_cw": 360}, {"offset_dots": 1.5},
	} {
		_, err := Encode(m, s, label, bad, 0)
		var pe *printer.Error
		if !errors.As(err, &pe) {
			t.Errorf("%v: err = %v, want a printer.Error", bad, err)
		}
	}
	sheet, _ := cat.Stock("avery-5160")
	if _, err := Encode(m, sheet, label, nil, 0); err == nil {
		t.Error("a sheet stock must be refused")
	}
}

func TestSettingsFromJSONAndTOML(t *testing.T) {
	cat := catalog(t)
	m, _ := cat.Model("phomemo-m110")
	s, _ := cat.Stock("50x30")
	label := white(t, New(), m, s)
	for _, set := range []printer.Settings{
		{"speed": 5.0, "darkness": 10.0}, {"speed": int64(5), "darkness": int64(10)}, {"speed": "5", "darkness": 10},
	} {
		job := encode(t, m, s, label, set)
		if !bytes.Equal(job[:8], []byte{0x1b, 0x4e, 0x0d, 0x05, 0x1b, 0x4e, 0x04, 0x0a}) {
			t.Errorf("%v: header % x", set, job[:8])
		}
	}
}

func TestRodPlacementInBytes(t *testing.T) {
	cat := catalog(t)
	for _, tc := range []struct {
		model, stock string
		base         int
	}{{"phomemo-m110", "40x30", 64}, {"phomemo-m220", "50x30", 176}, {"phomemo-m220", "40x30", 240}} {
		m, _ := cat.Model(tc.model)
		s, _ := cat.Stock(tc.stock)
		black := white(t, New(), m, s)
		clear(black.Pix)
		job := encode(t, m, s, black, nil)
		_, blocks, _ := parseJob(t, job)
		bits, _ := raster.UnpackRows(job[19:19+blocks[0].bpr*blocks[0].rows], blocks[0].bpr*8)
		for y := range bits.H {
			for x := range bits.W {
				if bits.At(x, y) != (x >= tc.base) {
					t.Fatalf("%s %s: dot (%d,%d) = %v with the label at x ≥ %d", tc.model, tc.stock, x, y, bits.At(x, y), tc.base)
				}
			}
		}
	}
}

func TestTwoTrackTurnsClockwise(t *testing.T) {
	// A quarter turn clockwise: the design's left edge becomes raster row 0 and its top
	// edge lies at high x.
	cat := catalog(t)
	m, _ := cat.Model("phomemo-m110")
	s, _ := cat.Stock("50x80-two-track")
	design := white(t, New(), m, s)
	for y := 100; y <= 120; y++ {
		design.SetGray(0, y, colorBlack)
	}
	for x := 300; x <= 310; x++ {
		design.SetGray(x, 0, colorBlack)
	}
	label, _, err := renderLabel(m, s, design, nil)
	if err != nil {
		t.Fatal(err)
	}
	bits := label.Bits
	if bits.W != 384 || bits.H != 640 {
		t.Fatalf("raster %dx%d", bits.W, bits.H)
	}
	for x := 383 - 120; x <= 383-100; x++ {
		if !bits.At(x, 0) {
			t.Fatalf("row 0 x=%d not burnt", x)
		}
	}
	for y := 300; y <= 310; y++ {
		if !bits.At(383, y) {
			t.Fatalf("x=383 row %d not burnt", y)
		}
	}
}

func TestPreviewIsTheSentDotsWithCalibration(t *testing.T) {
	cat := catalog(t)
	rng := rand.New(rand.NewPCG(9, 9))
	for _, tc := range []struct {
		model, stock string
		settings     printer.Settings
	}{
		{"phomemo-m220", "50x80-two-track", nil},
		{"phomemo-m220", "50x30", printer.Settings{"offset_dots": 10.0}},
		{"phomemo-m220", "40x30", printer.Settings{"paper_align": "center"}},
		{"phomemo-m110", "50x30", printer.Settings{"extra_rotate_cw": 180}},
		{"phomemo-m110", "50x30", printer.Settings{"extra_rotate_cw": 90}},
		{"phomemo-d30", "d30-15mm-tape", nil},
	} {
		m, _ := cat.Model(tc.model)
		s, _ := cat.Stock(tc.stock)
		design := white(t, New(), m, s)
		for i := range design.Pix {
			design.Pix[i] = uint8(40 + rng.IntN(180))
		}
		job := encode(t, m, s, design, tc.settings)
		_, blocks, _ := parseJob(t, job)
		sent, _ := raster.UnpackRows(job[19:19+blocks[0].bpr*blocks[0].rows], blocks[0].bpr*8)
		geo, _ := LabelGeometry(m, s, design.Rect.Dx(), design.Rect.Dy(), tc.settings)
		lane := geo.Layout.Lane
		box := sent.Crop(image.Rect(lane.Start, 0, lane.End(), geo.Layout.Rows))
		back, _ := raster.RotateBitsCW(box, (360-geo.TurnCW)%360)
		want := raster.BitsToImage(back)
		got, err := New().Preview(m, s, design, tc.settings)
		if err != nil {
			t.Fatal(err)
		}
		if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%s %s %v: preview %v is not the sent dots %v", tc.model, tc.stock, tc.settings, got.Rect, want.Rect)
		}
	}
}

func TestLinksAndCanvas(t *testing.T) {
	cat := catalog(t)
	d, err := printer.DriverFor(Family)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Links(); len(got) != 3 {
		t.Errorf("links = %v", got)
	}
	for stock, size := range map[string][2]int{"50x80-two-track": {640, 384}, "50x30": {384, 240}, "40x30": {320, 240}, "70x80": {560, 640}, "d30-15mm-tape": {400, 96}, "15x50": {400, 96}} {
		cv, err := cat.CanvasFor("phomemo-m220", stock)
		if err != nil {
			t.Fatal(err)
		}
		if cv.Width != size[0] || cv.Height != size[1] {
			t.Errorf("%s canvas %dx%d, want %v", stock, cv.Width, cv.Height, size)
		}
	}
}

// TestCatalogPrintWritesGoldenBytes runs the registered driver end to end through
// Catalog.Print with a fake link: firmware query first, then exactly the golden job.
func TestCatalogPrintWritesGoldenBytes(t *testing.T) {
	t.Setenv("CRATE_LABELS_CONFIG", t.TempDir())
	cat := catalog(t)
	label := testCard(384, 240)
	link := fake.New(244, greeting...)
	target := &printer.Target{ID: "e2e", Name: "M110-E2E", ModelID: "phomemo-m110", Address: "e2e-phomemo-m110-50x30"}
	res, err := cat.Print(context.Background(), target, "50x30", []*image.Gray{label}, printer.PrintOptions{Link: link})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Printed != 1 {
		t.Fatalf("result = %+v", res)
	}
	want := append(append([]byte{}, FirmwareQuery...), readGolden(t, "phomemo-m110__50x30.job0.bin")...)
	if d := firstDiff(link.Sent(""), want); d != "" {
		t.Fatal(d)
	}
	if link.Closed() {
		t.Error("Print closed a link it did not open")
	}
}

func TestCancelledSendKeepsTheCountAndTheCause(t *testing.T) {
	d, clk := testDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.sleep = func(c context.Context, wait time.Duration) error {
		if wait > time.Second { // the pacing wait before the second label
			cancel()
		}
		return clk.sleep(c, wait)
	}
	pc := pcFor(t, catalog(t), "phomemo-m110", "50x30", fake.New(244, greeting...), "JJ", "M110", nil)
	label := white(t, d, pc.Model, pc.Stock)
	res, err := d.Send(ctx, pc, []*image.Gray{label, label, label})
	if !errors.Is(err, context.Canceled) || err.Error() != context.Canceled.Error() {
		t.Errorf("err = %v, want context.Canceled itself", err)
	}
	if res.Printed != 1 || res.OK {
		t.Errorf("result = %+v, want Printed 1", res)
	}
}

// cancellingLink cancels the job's context after n writes.
type cancellingLink struct {
	*fake.Link
	n      int
	cancel context.CancelFunc
}

func (l *cancellingLink) Write(ctx context.Context, data []byte, role string, withResponse bool) error {
	if l.n--; l.n < 0 {
		l.cancel()
		return ctx.Err()
	}
	return l.Link.Write(ctx, data, role, withResponse)
}

func TestCancelledWriteIsContextCanceled(t *testing.T) {
	d, _ := testDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pc := pcFor(t, catalog(t), "phomemo-m110", "50x30", nil, "KK", "M110", nil)
	pc.Link = &cancellingLink{Link: fake.New(244, greeting...), n: 5, cancel: cancel}
	res, err := d.Send(ctx, pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)})
	if !errors.Is(err, context.Canceled) || res.Printed != 0 {
		t.Errorf("result %+v, err %v", res, err)
	}
}

func TestGapSettingIsValidated(t *testing.T) {
	d, _ := testDriver(t)
	pc := pcFor(t, catalog(t), "phomemo-m110", "50x30", fake.New(244, greeting...), "LL", "M110", printer.Settings{"ble_gap_ms": 5000.0})
	_, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)})
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "ble_gap_ms") {
		t.Errorf("err = %v", err)
	}
}

func TestOffsetDotsIsClampedNotRejected(t *testing.T) {
	cat := catalog(t)
	m, _ := cat.Model("phomemo-m220")
	s, _ := cat.Stock("50x30")
	for _, tc := range []struct {
		offset any
		base   int
	}{{-5, 0}, {-200.0, 0}, {-1e9, 0}, {999.0, 176}, {int64(10), 10}, {"12", 12}, {nil, 176}} {
		geo, err := LabelGeometry(m, s, 384, 240, printer.Settings{"offset_dots": tc.offset})
		if err != nil {
			t.Fatalf("offset %v: %v", tc.offset, err)
		}
		if geo.Layout.Lane.Start != tc.base {
			t.Errorf("offset %v: lane starts at %d, want %d", tc.offset, geo.Layout.Lane.Start, tc.base)
		}
	}
}

func TestSuspectsComeFromTheJobsCatalog(t *testing.T) {
	d, _ := testDriver(t) // CRATE_LABELS_CONFIG is an empty directory
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "printers"), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := `
[[model]]
id = "phomemo-custom"
label = "Phomemo Custom"
family = "phomemo"
link = "ble"
dpi = 203.2
head_dots = 384
paper_width_mm = [20, 50]
options = { hello_frames = ["0107"] }
[model.ble]
name_patterns = ["custom"]
service = "0000ff00-0000-1000-8000-00805f9b34fb"
write = { data = "0000ff02-0000-1000-8000-00805f9b34fb" }
notify = "0000ff03-0000-1000-8000-00805f9b34fb"
`
	if err := os.WriteFile(filepath.Join(dir, "printers", "custom.toml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := printer.LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The configured D30 does not send 01 07; every model in pc.Catalog that does is a
	// suspect, the user's own included.
	pc := pcFor(t, cat, "phomemo-d30", "15x50", fake.New(244, greeting...), "MM", "", nil)
	res, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := res.Details["suspected_models"].([]string); strings.Join(got, ",") != "phomemo-m110,phomemo-m220,phomemo-custom" {
		t.Errorf("details = %v, want the senders of 01 07 from pc.Catalog", res.Details)
	}
	pc = pcFor(t, cat, "phomemo-d30", "15x50", fake.New(244, greeting...), "NN", "", nil)
	pc.Catalog = nil // only the configured model is known: nothing to report
	if res, err := d.Send(context.Background(), pc, []*image.Gray{white(t, d, pc.Model, pc.Stock)}); err != nil || res.Details["suspected_models"] != nil {
		t.Errorf("without a catalog: %v, %v", res.Details, err)
	}
}

func TestNegativeOffsetIsAcceptedAndPrintsLikeZero(t *testing.T) {
	cat := catalog(t)
	m, _ := cat.Model("phomemo-m220")
	s, _ := cat.Stock("50x30")
	label := white(t, New(), m, s)
	off, err := offsetSetting(printer.Settings{"offset_dots": -20.0})
	if err != nil || off == nil || *off != -20 {
		t.Fatalf("offset_dots -20 read as %v, %v; Phomemo keeps negative values", off, err)
	}
	neg := encode(t, m, s, label, printer.Settings{"offset_dots": -20.0})
	zero := encode(t, m, s, label, printer.Settings{"offset_dots": 0})
	if !bytes.Equal(neg, zero) {
		t.Error("a negative offset must print the same as 0: the lane is clamped onto the head")
	}
}
