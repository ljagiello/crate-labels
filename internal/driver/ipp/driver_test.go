package ipp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	proto "github.com/ljagiello/crate-labels/internal/ipp"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/sheets"
)

// update rewrites this package's golden files under testdata/golden/ipp:
// go test ./internal/driver/ipp -update
var update = flag.Bool("update", false, "rewrite golden files from the current code")

var (
	goldenDir  = filepath.Join("..", "..", "..", "testdata", "golden", "ipp")
	brotherGPA = filepath.Join("..", "..", "..", "testdata", "ipp", "brother-hl-l3290cdw-get-printer-attributes.bin")
	brotherEP  = proto.Endpoint{Host: "198.51.100.9", Port: 631, Resource: "/ipp/print"}
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func brother(t *testing.T) *proto.Printer {
	t.Helper()
	m, err := proto.Decode(readFile(t, brotherGPA))
	if err != nil {
		t.Fatal(err)
	}
	return &proto.Printer{Response: m}
}

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

func modelStock(t *testing.T, c *printer.Catalog, modelID, stockID string) (*printer.Model, *printer.Stock) {
	t.Helper()
	m, err := c.Model(modelID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Stock(stockID)
	if err != nil {
		t.Fatal(err)
	}
	return m, s
}

func filled(w, h int, v byte) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for i := range g.Pix {
		g.Pix[i] = v
	}
	return g
}

func encode(t *testing.T, m *proto.Message) []byte {
	t.Helper()
	b, err := proto.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func inkBox(g *image.Gray) image.Rectangle {
	var r image.Rectangle
	b := g.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := g.Pix[(y-b.Min.Y)*g.Stride:]
		for x := 0; x < b.Dx(); x++ {
			if row[x] != 255 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

// golden compares got with the file at path, or writes it under -update.
func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := readFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the code's output; run go test -update after checking the change", path)
	}
}

// driverStocks are the stocks whose Brother plans are kept as golden files.
var driverStocks = []string{
	"15x50", "40x30", "4x6", "50x30", "50x80-two-track", "70x80", "center-7in",
	"avery-5160", "avery-5163", "letter-uncut-50x30", "letter-uncut-50x80",
}

// TestDriverPlansOnTheBrother keeps, for one black label of each stock on the Brother,
// the Validate-Job request the driver sends (requests/validate-job-<stock>.bin) and a
// summary of the plan (driver-plans.json): format, resolution, media, page size and
// where the ink lands.
func TestDriverPlansOnTheBrother(t *testing.T) {
	cat := catalog(t, "")
	p := brother(t)
	plans := map[string]any{}
	for _, id := range driverStocks {
		t.Run(id, func(t *testing.T) {
			m, s := modelStock(t, cat, "ipp-sheet", id)
			cv := printer.DefaultCanvas(m, s)
			plan, err := PlanJobs(p, m, s, []*image.Gray{filled(cv.Width, cv.Height, 0)}, m.Options)
			if err != nil {
				t.Fatal(err)
			}
			req := plan.Jobs[0].Request
			golden(t, filepath.Join(goldenDir, "requests", "validate-job-"+id+".bin"),
				encode(t, proto.JobRequestMessage(proto.OpValidateJob, brotherEP, 2, proto.DefaultUser, req, nil)))
			pages, err := proto.DecodeURF(plan.Jobs[0].Document)
			if err != nil || len(pages) != 1 {
				t.Fatalf("%d pages: %v", len(pages), err)
			}
			img := pages[0].Image
			r := inkBox(img)
			summary := map[string]any{
				"format": plan.DocumentFormat, "dpi": plan.DPI, "media": plan.Choice.Media.Describe(),
				"exact": plan.Choice.Exact, "turned": plan.Choice.Turned, "canvas_px": [2]int{cv.Width, cv.Height},
				"page_px": [2]int{img.Bounds().Dx(), img.Bounds().Dy()}, "page_dpi": pages[0].DPI, "page_quality": pages[0].Quality,
			}
			// Where an uncut grid's ink lands follows the catalog's grid rule, which the
			// sheets tests check; it is not pinned here.
			if s.Sheet == nil || !sheets.IsUncut(s.Sheet) {
				summary["ink_px"] = [4]int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y}
			}
			plans[id] = summary
			if pages[0].DPI != 600 || pages[0].Quality != 4 || plan.Choice.Exact != (s.Sheet != nil) {
				t.Fatalf("page dpi %d quality %d exact %v", pages[0].DPI, pages[0].Quality, plan.Choice.Exact)
			}
			// The label keeps its physical size: 300 dpi pixels become 2×2 at 600 dpi.
			if s.Sheet == nil && (r.Dx() != 2*cv.Width || r.Dy() != 2*cv.Height) && (r.Dx() != 2*cv.Height || r.Dy() != 2*cv.Width) {
				t.Fatalf("ink %v for a %dx%d design", r, cv.Width, cv.Height)
			}
		})
	}
	if !t.Failed() {
		b, _ := json.MarshalIndent(plans, "", " ")
		golden(t, filepath.Join(goldenDir, "driver-plans.json"), append(b, '\n'))
	}
}

func TestPickFormat(t *testing.T) {
	p := brother(t)
	for pref, want := range map[string]string{proto.MimeURF: proto.MimeURF, proto.MimeJPEG: proto.MimeJPEG, proto.MimePDF: proto.MimeURF, "": proto.MimeURF} {
		if got := PickFormat(p, pref); got != want {
			t.Errorf("%q: %s", pref, got)
		}
	}
	with := func(formats, urf []string) *proto.Printer {
		attrs := []proto.Attribute{}
		vs := make([]any, len(formats))
		for i, f := range formats {
			vs[i] = f
		}
		attrs = append(attrs, proto.Attr("document-format-supported", proto.TagMimeMediaType, vs...))
		if len(urf) > 0 {
			attrs = append(attrs, proto.Keyword("urf-supported", urf...))
		}
		return &proto.Printer{Response: proto.NewMessage(0, 1, proto.Group{Tag: proto.TagPrinterGroup, Attributes: attrs})}
	}
	if got := PickFormat(with([]string{proto.MimeURF, proto.MimePDF}, []string{"SRGB24"}), proto.MimeURF); got != proto.MimePDF {
		t.Errorf("URF without W8: %s", got)
	}
	if got := PickFormat(with([]string{"image/pwg-raster"}, nil), proto.MimeURF); got != "" {
		t.Errorf("PWG only: %s", got)
	}
}

func TestPlanUsesPDFWhenURFIsMissing(t *testing.T) {
	gpa, _ := proto.Decode(readFile(t, brotherGPA))
	gpa.Lookup(proto.TagPrinterGroup, "document-format-supported").Values = []proto.Value{{Tag: proto.TagMimeMediaType, Data: proto.MimePDF}}
	p := &proto.Printer{Response: gpa}
	m, s := modelStock(t, catalog(t, ""), "ipp-sheet", "avery-5163")
	plan, err := PlanJobs(p, m, s, []*image.Gray{filled(1200, 600, 0)}, m.Options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DocumentFormat != proto.MimePDF || plan.DPI != 300 {
		t.Fatalf("format %s dpi %d", plan.DocumentFormat, plan.DPI)
	}
	doc := plan.Jobs[0].Document
	if !bytes.HasPrefix(doc, []byte("%PDF")) || !bytes.Contains(doc, []byte("/MediaBox [ 0 0 612 792 ]")) {
		t.Fatalf("document %q", doc[:min(len(doc), 200)])
	}
	var names []string
	for _, a := range plan.Jobs[0].Request.JobAttributes {
		names = append(names, a.Name)
	}
	if !slices.Equal(names, []string{"media-col", "print-color-mode", "print-scaling"}) {
		t.Fatalf("attributes %v", names)
	}
}

func TestPlanJPEGCarriesJFIFDensity(t *testing.T) {
	m, s := modelStock(t, catalog(t, ""), "ipp-label", "50x30")
	cv := printer.DefaultCanvas(m, s)
	plan, err := PlanJobs(brother(t), m, s, []*image.Gray{filled(cv.Width, cv.Height, 0), filled(cv.Width, cv.Height, 0)},
		printer.Settings{"document_format": proto.MimeJPEG})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DocumentFormat != proto.MimeJPEG || len(plan.Jobs) != 2 {
		t.Fatalf("format %s, %d jobs (one page per JPEG job)", plan.DocumentFormat, len(plan.Jobs))
	}
	doc := plan.Jobs[0].Document
	want := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 1, 0x01, 0x2c, 0x01, 0x2c}
	if !bytes.HasPrefix(doc, want) {
		t.Fatalf("JPEG starts % x", doc[:20])
	}
	if plan.Jobs[0].Request.Name != "crate-labels (1 of 2)" {
		t.Fatalf("job name %q", plan.Jobs[0].Request.Name)
	}
}

func TestPreview(t *testing.T) {
	cat := catalog(t, "")
	d := &Driver{}
	m, s := modelStock(t, cat, "ipp-sheet", "letter-uncut-50x30")
	cv := d.Canvas(m, s)
	page, err := d.Preview(m, s, filled(cv.Width, cv.Height, 0), m.Options)
	if err != nil || page.Bounds() != image.Rect(0, 0, 2550, 3300) {
		t.Fatalf("%v %v", page.Bounds(), err)
	}
	if page.GrayAt(sheets.MMToPx(s.Sheet.MarginMM[0], 300)+12, sheets.MMToPx(s.Sheet.MarginMM[1], 300)).Y != 0 {
		t.Fatal("the first cell is not at the grid's origin")
	}
	m, s = modelStock(t, cat, "ipp-label", "50x30")
	label := filled(567, 354, 0)
	if got, _ := d.Preview(m, s, label, m.Options); got.Bounds() != label.Bounds() {
		t.Fatalf("single label preview %v", got.Bounds())
	}
}

func TestRegistered(t *testing.T) {
	d, err := printer.DriverFor("ipp")
	if err != nil || d.Family() != "ipp" || !slices.Equal(d.Links(), []printer.LinkKind{printer.LinkIPP}) {
		t.Fatalf("%v %v", d, err)
	}
}

// --- a fake printer over HTTP ------------------------------------------------------------

type fakePrinter struct {
	t                   *testing.T
	gpa                 []byte
	mu                  sync.Mutex
	pollStates          []int
	printStatus         int
	pollStatus          int
	pollHTTP            []int // HTTP statuses for the next polls (0 = answer normally)
	validateUnsupported []string
	reasons             []string
	reqs                []*proto.Message
	bodies              [][]byte
}

func newFake(t *testing.T) (*fakePrinter, string) {
	f := &fakePrinter{t: t, gpa: readFile(t, brotherGPA), pollStates: []int{5, 9}, reasons: []string{"none"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, strings.Replace(srv.URL, "http://", "ipp://", 1) + "/ipp/print"
}

func (f *fakePrinter) ops() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, r := range f.reqs {
		out = append(out, r.Code)
	}
	return out
}

func (f *fakePrinter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req, err := proto.Decode(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.bodies = append(f.bodies, body)
	if req.Code == proto.OpGetJobAttributes && len(f.pollHTTP) > 0 {
		code := f.pollHTTP[0]
		f.pollHTTP = f.pollHTTP[1:]
		if code != 0 {
			f.mu.Unlock()
			w.WriteHeader(code)
			return
		}
	}
	resp := f.respond(req)
	f.mu.Unlock()
	resp.RequestID = req.RequestID
	out, _ := proto.Encode(resp)
	w.Header().Set("Content-Type", proto.ContentType)
	_, _ = w.Write(out)
}

func (f *fakePrinter) respond(req *proto.Message) *proto.Message {
	ops := proto.Group{Tag: proto.TagOperationGroup, Attributes: []proto.Attribute{
		proto.Charset("attributes-charset", "utf-8"), proto.Language("attributes-natural-language", "en"),
	}}
	switch req.Code {
	case proto.OpGetPrinterAttributes:
		if !slices.Contains(req.Lookup(proto.TagOperationGroup, "requested-attributes").Strings(), "all") {
			state := proto.PrinterIdle
			if !slices.Equal(f.reasons, []string{"none"}) {
				state = proto.PrinterStopped
			}
			return proto.NewMessage(0, 0, ops, proto.Group{Tag: proto.TagPrinterGroup, Attributes: []proto.Attribute{
				proto.Enum("printer-state", state), proto.Keyword("printer-state-reasons", f.reasons...),
			}})
		}
		m, _ := proto.Decode(f.gpa)
		return m
	case proto.OpValidateJob:
		if len(f.validateUnsupported) > 0 {
			g := proto.Group{Tag: proto.TagUnsupportedGroup}
			for _, n := range f.validateUnsupported {
				g.Attributes = append(g.Attributes, proto.Keyword(n, "x"))
			}
			return proto.NewMessage(proto.StatusOKIgnoredOrSubstituted, 0, ops, g)
		}
		return proto.NewMessage(0, 0, ops)
	case proto.OpPrintJob:
		if f.printStatus != 0 {
			ops.Attributes = append(ops.Attributes, proto.Attr("status-message", proto.TagText, "no"))
			return proto.NewMessage(f.printStatus, 0, ops)
		}
		return proto.NewMessage(0, 0, ops, proto.Group{Tag: proto.TagJobGroup, Attributes: []proto.Attribute{proto.Integer("job-id", 42), proto.Enum("job-state", 3)}})
	case proto.OpCancelJob:
		return proto.NewMessage(0, 0, ops)
	case proto.OpGetJobAttributes:
		if f.pollStatus != 0 {
			return proto.NewMessage(f.pollStatus, 0, ops)
		}
		st := f.pollStates[0]
		if len(f.pollStates) > 1 {
			f.pollStates = f.pollStates[1:]
		}
		return proto.NewMessage(0, 0, ops, proto.Group{Tag: proto.TagJobGroup, Attributes: []proto.Attribute{
			proto.Enum("job-state", st), proto.Keyword("job-state-reasons", "none"), proto.Integer("job-impressions-completed", 0),
		}})
	}
	return proto.NewMessage(0x0501, 0, ops)
}

func runDriver(t *testing.T, cat *printer.Catalog, uri, modelID, stockID string, count int, settings printer.Settings) (printer.Result, error) {
	t.Helper()
	return sendWith(t, context.Background(), cat, uri, modelID, stockID, count, settings, nil)
}

// sendWith runs the driver with polls 1 ms apart; onProgress sees every report.
func sendWith(t *testing.T, ctx context.Context, cat *printer.Catalog, uri, modelID, stockID string, count int, settings printer.Settings, onProgress func(float64, string)) (printer.Result, error) {
	t.Helper()
	m, s := modelStock(t, cat, modelID, stockID)
	cv := printer.DefaultCanvas(m, s)
	labels := make([]*image.Gray, count)
	for i := range labels {
		labels[i] = filled(cv.Width, cv.Height, 0)
	}
	var progress []float64
	pc := &printer.PrintContext{
		Model: m, Stock: s,
		Target:   &printer.Target{ID: "t1", Name: "Test printer", ModelID: modelID, Address: uri},
		Settings: printer.Merge(m.Options, printer.Settings{"ipp_poll_s": 0.0}, settings),
		Progress: func(f float64, msg string) {
			progress = append(progress, f)
			if onProgress != nil {
				onProgress(f, msg)
			}
		},
	}
	res, err := (&Driver{pollFloor: time.Millisecond}).Send(ctx, pc, labels)
	for _, f := range progress {
		if f < 0 || f > 1 {
			t.Errorf("progress %v out of range", progress)
		}
	}
	return res, err
}

var gpaValidatePrintPoll = []int{proto.OpGetPrinterAttributes, proto.OpValidateJob, proto.OpPrintJob, proto.OpGetJobAttributes, proto.OpGetJobAttributes}

func TestPrintJobOverHTTP(t *testing.T) {
	f, uri := newFake(t)
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-sheet", "avery-5160", 2, nil)
	if err != nil || !res.OK || res.Printed != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if !slices.Equal(f.ops(), gpaValidatePrintPoll) {
		t.Fatalf("ops %v", f.ops())
	}
	for i, r := range f.reqs {
		if r.RequestID != i+1 {
			t.Fatalf("request ids: %d at %d", r.RequestID, i)
		}
	}
	ep, _ := proto.ParseEndpoint(uri)
	p := brother(t)
	attrs := proto.Ticket{Media: proto.ChooseMedia(p, [2]float64{215.9, 279.4}, true), Grey: true}.Attributes(p)
	header := encode(t, proto.JobRequestMessage(proto.OpPrintJob, ep, 3, proto.DefaultUser, proto.JobRequest{Name: "crate-labels", DocumentFormat: proto.MimeURF, JobAttributes: attrs}, nil))
	body := f.bodies[2]
	if !bytes.HasPrefix(body, header) {
		t.Fatal("Print-Job does not start with the expected header for this endpoint")
	}
	pages, err := proto.DecodeURF(body[len(header):])
	if err != nil || len(pages) != 1 {
		t.Fatalf("%d pages, %v", len(pages), err)
	}
	pg := pages[0]
	if pg.Image.Bounds() != image.Rect(0, 0, 5100, 6600) || pg.DPI != 600 || pg.Quality != 4 {
		t.Fatalf("page %v dpi %d q %d", pg.Image.Bounds(), pg.DPI, pg.Quality)
	}
	// Avery 5160 cells 0 and 1 at 600 dpi: cell 1 starts at 881 px at 300 dpi = 1762.
	top := pg.Image.SubImage(image.Rect(0, 0, 5100, 1000)).(*image.Gray)
	if r := inkBox(top); r != image.Rect(112, 300, 1762+1576, 900) {
		t.Fatalf("ink %v", r)
	}
	at := func(x, y int) uint8 { return pg.Image.GrayAt(x, y).Y }
	if at(112, 300) != 0 || at(111, 300) != 255 || at(1687, 300) != 0 || at(1688, 300) != 255 || at(1762, 300) != 0 || at(1761, 300) != 255 {
		t.Fatal("cell edges")
	}
	validate := encode(t, f.reqs[1])
	if !bytes.HasSuffix(validate, header[bytes.Index(header, []byte{0x02, 0x34}):]) || len(f.reqs[1].Data) != 0 {
		t.Fatal("Validate-Job carried different attributes or a document")
	}
}

func TestSingleLabelsOnASheetPrinterSaySo(t *testing.T) {
	f, uri := newFake(t)
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-label", "50x30", 3, nil)
	if err != nil || !res.OK || res.Printed != 3 || res.Details["exact"] != false || !strings.Contains(res.Message, "actual size in the middle of a 215.9×279.4 mm") {
		t.Fatalf("%+v %v", res, err)
	}
	pages, err := proto.DecodeURF(f.reqs[2].Data)
	if err != nil || len(pages) != 3 {
		t.Fatalf("%d pages %v", len(pages), err)
	}
	// The 50×30 mm page, unturned and centred on Letter; the 48×30 mm design (567×354 px
	// at 300 dpi) doubles to 1134×708.
	r := inkBox(pages[0].Image)
	if r.Dx() != 1134 || r.Dy() != 708 || abs(r.Min.X+r.Max.X-5100) > 2 || abs(r.Min.Y+r.Max.Y-6600) > 2 {
		t.Fatalf("ink %v", r)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestSheetStockOnWrongPaperIsRefused(t *testing.T) {
	f, uri := newFake(t)
	cat := catalog(t, `
[[stock]]
id = "a4-uncut-50x30"
name = "A4 sheet, 50x30 cells"
media = "sheet"
design_mm = [48, 30]
feed_mm = [210, 297]
sheet = { paper_mm = [210, 297], grid = "uncut", cell_mm = [50, 30] }
`)
	_, err := runDriver(t, cat, uri, "cups", "a4-uncut-50x30", 1, nil)
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "215.9×279.4 mm") {
		t.Fatalf("err %v", err)
	}
	if slices.Contains(f.ops(), proto.OpPrintJob) {
		t.Fatal("printed")
	}
}

func TestValidateIgnoringMediaColStopsASheetJob(t *testing.T) {
	f, uri := newFake(t)
	f.validateUnsupported = []string{"media-col"}
	_, err := runDriver(t, catalog(t, ""), uri, "ipp-sheet", "avery-5160", 1, nil)
	if err == nil || !strings.Contains(err.Error(), "would not line up") || slices.Contains(f.ops(), proto.OpPrintJob) {
		t.Fatalf("err %v ops %v", err, f.ops())
	}
}

func TestValidateIgnoringAnAttributeIsReported(t *testing.T) {
	f, uri := newFake(t)
	f.validateUnsupported = []string{"print-scaling"}
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-label", "50x30", 1, nil)
	if err != nil || !res.OK || !reflect.DeepEqual(res.Details["unsupported"], []string{"print-scaling"}) || !strings.Contains(res.Message, "would ignore: print-scaling") {
		t.Fatalf("%+v %v", res, err)
	}
	// The setting is dropped and the job checked again without it before printing.
	ops := f.ops()
	if !slices.Equal(ops[:4], []int{proto.OpGetPrinterAttributes, proto.OpValidateJob, proto.OpValidateJob, proto.OpPrintJob}) {
		t.Fatalf("ops %v", ops)
	}
	for _, r := range f.reqs[2:4] {
		if r.Lookup(proto.TagJobGroup, "print-scaling") != nil || r.Lookup(proto.TagJobGroup, "media-col") == nil {
			t.Fatalf("request %#x still has print-scaling or lost media-col", r.Code)
		}
	}
}

func TestDryRunOnlyValidates(t *testing.T) {
	f, uri := newFake(t)
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-sheet", "letter-uncut-50x30", 40, printer.Settings{"dry_run": true})
	if err != nil || !res.OK || res.Printed != 0 || !strings.Contains(res.Message, "Nothing was printed") {
		t.Fatalf("%+v %v", res, err)
	}
	if !slices.Equal(f.ops(), []int{proto.OpGetPrinterAttributes, proto.OpValidateJob}) {
		t.Fatalf("ops %v", f.ops())
	}
	// A string flag works too, as per-job options may come from JSON or a form.
	f2, uri2 := newFake(t)
	if _, err := runDriver(t, catalog(t, ""), uri2, "ipp-sheet", "avery-5160", 1, printer.Settings{"dry_run": "yes"}); err != nil || slices.Contains(f2.ops(), proto.OpPrintJob) {
		t.Fatalf("string dry_run: %v %v", err, f2.ops())
	}
}

// A job the printer forgets before saying how it ended counts as printed, but is listed
// as unconfirmed.
func TestVanishedJobCountsAsPrinted(t *testing.T) {
	f, uri := newFake(t)
	f.pollStatus = proto.StatusNotFound
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-sheet", "avery-5163", 11, nil)
	if err != nil || !res.OK || res.Printed != 11 || !reflect.DeepEqual(res.Details["unconfirmed_jobs"], []int{42}) {
		t.Fatalf("%+v %v", res, err)
	}
	n := 0
	for _, op := range f.ops() {
		if op == proto.OpPrintJob {
			n++
		}
	}
	pages, _ := proto.DecodeURF(f.reqs[2].Data)
	if n != 1 || len(pages) != 2 {
		t.Fatalf("%d jobs, %d pages", n, len(pages))
	}
}

func TestStoppedJobWaitsForTheFix(t *testing.T) {
	f, uri := newFake(t)
	f.pollStates = []int{proto.JobProcessing, proto.JobStopped, proto.JobStopped, proto.JobProcessing, proto.JobCompleted}
	f.reasons = []string{"media-empty-error"}
	var waiting []string
	res, err := sendWith(t, context.Background(), catalog(t, ""), uri, "ipp-sheet", "avery-5160", 1, nil, func(_ float64, msg string) {
		if strings.HasPrefix(msg, "Waiting on") {
			waiting = append(waiting, msg)
		}
	})
	if err != nil || !res.OK || res.Printed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(waiting) != 2 || !strings.Contains(waiting[0], "Test printer: a tray has no paper or labels left") {
		t.Fatalf("progress %q", waiting)
	}
	if slices.Contains(f.ops(), proto.OpCancelJob) {
		t.Fatal("cancelled a job nobody gave up on")
	}
}

func TestAbortedJobReportsPrinterReason(t *testing.T) {
	f, uri := newFake(t)
	f.pollStates = []int{proto.JobAborted}
	f.reasons = []string{"media-jam-error"}
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-sheet", "avery-5160", 1, nil)
	if err == nil || res.OK || res.Printed != 0 || !strings.Contains(res.Message, "the job was aborted; paper or labels are jammed") || err.Error() != res.Message {
		t.Fatalf("%+v %v", res, err)
	}
	if ops := f.ops(); ops[len(ops)-1] != proto.OpGetPrinterAttributes {
		t.Fatalf("ops %v", ops)
	}
}

// cancelOps checks that a cancelled run sent Cancel-Job for job 42 and stopped there.
func cancelOps(t *testing.T, f *fakePrinter, res printer.Result, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) || res.OK || res.Printed != 0 || res.Details["cancelled_job"] != 42 {
		t.Fatalf("%+v %v", res, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	last := f.reqs[len(f.reqs)-1]
	if last.Code != proto.OpCancelJob || last.Lookup(proto.TagOperationGroup, "job-id").First() != 42 {
		t.Fatalf("last request %#x %v", last.Code, last.Groups)
	}
}

func TestCancelWhileStoppedCancelsTheJob(t *testing.T) {
	f, uri := newFake(t)
	f.pollStates = []int{proto.JobStopped}
	f.reasons = []string{"cover-open-error"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := sendWith(t, ctx, catalog(t, ""), uri, "ipp-sheet", "avery-5160", 1, nil, func(_ float64, msg string) {
		if strings.HasPrefix(msg, "Waiting on") {
			cancel() // the user gives up instead of closing the cover
		}
	})
	cancelOps(t, f, res, err)
}

func TestCancelWhilePrintingCancelsTheJob(t *testing.T) {
	f, uri := newFake(t)
	f.pollStates = []int{proto.JobProcessing}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := sendWith(t, ctx, catalog(t, ""), uri, "ipp-sheet", "avery-5160", 1, nil, func(_ float64, msg string) {
		if strings.HasSuffix(msg, ": processing") {
			cancel()
		}
	})
	cancelOps(t, f, res, err)
}

func TestFailedPollIsRetried(t *testing.T) {
	f, uri := newFake(t)
	f.pollHTTP = []int{http.StatusServiceUnavailable, 0, http.StatusBadGateway}
	f.pollStates = []int{proto.JobProcessing, proto.JobCompleted}
	f2, uri2 := newFake(t)
	var retries int
	res, err := sendWith(t, context.Background(), catalog(t, ""), uri, "ipp-sheet", "avery-5163", 11, nil, func(_ float64, msg string) {
		if strings.Contains(msg, "asking again") {
			retries++
		}
	})
	if err != nil || !res.OK || res.Printed != 11 || retries != 2 {
		t.Fatalf("%+v %v retries %d", res, err, retries)
	}
	want := []int{proto.OpGetPrinterAttributes, proto.OpValidateJob, proto.OpPrintJob,
		proto.OpGetJobAttributes, proto.OpGetJobAttributes, proto.OpGetJobAttributes, proto.OpGetJobAttributes}
	if !slices.Equal(f.ops(), want) {
		t.Fatalf("ops %v", f.ops())
	}
	// Every job of a multi-job run is still sent after a failed poll in the first.
	f2.pollHTTP = []int{http.StatusServiceUnavailable}
	res, err = runDriver(t, catalog(t, ""), uri2, "ipp-label", "50x30", 2, printer.Settings{"document_format": proto.MimeJPEG})
	n := 0
	for _, op := range f2.ops() {
		if op == proto.OpPrintJob {
			n++
		}
	}
	if err != nil || !res.OK || res.Printed != 2 || n != 2 {
		t.Fatalf("%+v %v, %d jobs", res, err, n)
	}
}

func TestPollIntervalIsClamped(t *testing.T) {
	d := &Driver{}
	for in, want := range map[any]time.Duration{0.0: minPoll, 0.01: minPoll, -3: minPoll, 1.5: 1500 * time.Millisecond, "x": defaultPoll} {
		if got := d.pollInterval(printer.Settings{"ipp_poll_s": in}); got != want {
			t.Errorf("%v: %v", in, got)
		}
	}
	if got := d.pollInterval(nil); got != defaultPoll {
		t.Errorf("default %v", got)
	}
}

func TestRefusedPrintJob(t *testing.T) {
	f, uri := newFake(t)
	f.printStatus = 0x040b
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-sheet", "avery-5160", 1, nil)
	if err == nil || res.OK || !strings.Contains(res.Message, "rejects one of the job's settings") || !strings.Contains(res.Message, "client-error-attributes-or-values-not-supported") {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUnreachablePrinterIsAPrinterError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	uri := strings.Replace(srv.URL, "http://", "ipp://", 1)
	srv.Close()
	_, err := runDriver(t, catalog(t, ""), uri, "ipp-sheet", "avery-5160", 1, nil)
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "Could not reach Test printer") {
		t.Fatalf("err %v", err)
	}
}

// A job that shows no progress for the driver's patience is reported as sent but
// unconfirmed, and the run goes on.
func TestStalledJobIsUnconfirmed(t *testing.T) {
	f, uri := newFake(t)
	f.pollStates = []int{proto.JobProcessing}
	m, s := modelStock(t, catalog(t, ""), "ipp-label", "50x30")
	cv := printer.DefaultCanvas(m, s)
	pc := &printer.PrintContext{
		Model: m, Stock: s,
		Target:   &printer.Target{ID: "t1", Name: "Test printer", ModelID: "ipp-label", Address: uri},
		Settings: printer.Merge(m.Options, printer.Settings{"ipp_poll_s": 0.0, "document_format": proto.MimeJPEG}),
	}
	res, err := (&Driver{pollFloor: time.Millisecond, patience: 20 * time.Millisecond}).Send(context.Background(), pc,
		[]*image.Gray{filled(cv.Width, cv.Height, 0), filled(cv.Width, cv.Height, 0)})
	if err != nil || !res.OK || res.Printed != 2 || !reflect.DeepEqual(res.Details["unconfirmed_jobs"], []int{42, 42}) {
		t.Fatalf("%+v %v", res, err)
	}
	if slices.Contains(f.ops(), proto.OpCancelJob) {
		t.Fatal("cancelled a stalled job")
	}
}

// media-col carries the page size, so a label stock keeps it even when the printer says
// it may substitute it; the result reports the warning.
func TestValidateKeepsMediaColForLabels(t *testing.T) {
	f, uri := newFake(t)
	f.validateUnsupported = []string{"media-col", "print-scaling"}
	res, err := runDriver(t, catalog(t, ""), uri, "ipp-label", "50x30", 1, nil)
	if err != nil || !res.OK || !reflect.DeepEqual(res.Details["unsupported"], []string{"media-col", "print-scaling"}) ||
		!strings.Contains(res.Message, "would ignore: print-scaling.") || !strings.Contains(res.Message, "(media-col)") {
		t.Fatalf("%+v %v", res, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var printJob *proto.Message
	for _, r := range f.reqs {
		if r.Code == proto.OpPrintJob {
			printJob = r
		}
	}
	if printJob == nil || printJob.Lookup(proto.TagJobGroup, "media-col") == nil || printJob.Lookup(proto.TagJobGroup, "print-scaling") != nil {
		t.Fatalf("Print-Job %v", printJob)
	}
}
