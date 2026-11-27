package cups

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/sheets"
)

const queue = "Brother_HL_L3290CDW_series"

// Captured on the owner's Mac with LC_ALL=C (2026-11-25).
const (
	lpstatE = "Brother_HL_L3290CDW_series\n"
	lpstatP = "printer Brother_HL_L3290CDW_series is idle.  enabled since Tue Nov 17 23:29:55 2026\n" +
		"\tForm mounted:\n\tContent types: any\n\tPrinter types: unknown\n" +
		"\tDescription: Brother HL-L3290CDW series\n\tAlerts: none\n\tLocation:\n\tConnection: direct\n" +
		"\tInterface: /private/etc/cups/ppd/Brother_HL_L3290CDW_series.ppd\n\tOn fault: no alert\n" +
		"\tAfter fault: continue\n\tUsers allowed:\n\t\t(all)\n\tForms allowed:\n\t\t(none)\n" +
		"\tBanner required\n\tCharset sets:\n\t\t(none)\n\tDefault pitch:\n\tDefault page size:\n\tDefault port settings:\n"
	lpstatD   = "system default destination: Brother_HL_L3290CDW_series\n"
	lpoptions = "ColorModel/Color Mode: Gray *RGB\n" +
		"cupsPrintQuality/Quality: *Normal\n" +
		"Duplex/2-Sided Printing: None *DuplexNoTumble DuplexTumble\n" +
		"PageSize/Media Size: 215x345mm 3x5 A4 A5 A6 Env10 EnvC5 EnvDL EnvMonarch Executive FanFoldGermanLegal ISOB5 Legal *Letter Oficio Custom.WIDTHxHEIGHT\n" +
		"MediaType/MediaType: stationery stationery-lightweight stationery-heavyweight envelope envelope-heavyweight envelope-lightweight labels photographic-glossy stationery-bond com.brother.thicker com.brother.recycled *any\n" +
		"InputSlot/Media Source: auto manual tray-1\n"
	ppdExcerpt = "*DefaultPageSize: Letter\r\n" +
		"*PageSize Letter: \"<</PageSize[612.0000 792.0000]/ImagingBBox null/Orientation 0>>setpagedevice\"\n" +
		"*PaperDimension 215x345mm: \"609.4488 977.9528\"\n" +
		"*PaperDimension 3x5: \"216.0000 360.0000\"\n" +
		"*PaperDimension A4: \"595.2756 841.8898\"\n" +
		"*PaperDimension Letter: \"612.0000 792.0000\"\n" +
		"*PaperDimension Oficio: \"612.0000 964.8000\"\n" +
		"*ParamCustomPageSize Width: 1 points 216 612\n" +
		"*ParamCustomPageSize Height: 2 points 360 1008\n" +
		"*ParamCustomPageSize WidthOffset: 3 points 0 0\n" +
		"*ParamCustomPageSize Orientation: 5 int 0 3\n" +
		"*CustomPageSize True: \"\"\n"
	// A label printer queue, in the shape Dymo and Brother QL drivers use.
	labelLpoptions = "PageSize/Media Size: w79h252 w167h288 62x29mm *62x100mm Custom.WIDTHxHEIGHT\n"
)

// fakeRunner answers from a table keyed on the command prefix and records every call.
// It never runs a real command.
type fakeRunner struct {
	replies map[string]RunResult // key: space-joined argv prefix
	calls   [][]string
	pdfs    [][]byte
}

func (f *fakeRunner) run(_ context.Context, argv []string) (RunResult, error) {
	f.calls = append(f.calls, slices.Clone(argv))
	if argv[0] == "lp" {
		data, err := os.ReadFile(argv[len(argv)-1])
		if err != nil {
			return RunResult{}, err
		}
		f.pdfs = append(f.pdfs, data)
	}
	joined := strings.Join(argv, " ")
	best := ""
	for prefix := range f.replies {
		if (joined == prefix || strings.HasPrefix(joined, prefix+" ")) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return RunResult{Code: 1, Stderr: "unexpected command " + joined}, nil
	}
	return f.replies[best], nil
}

func ok(stdout string) RunResult { return RunResult{Stdout: stdout} }

func brotherRunner(lp *RunResult) *fakeRunner {
	r := &fakeRunner{replies: map[string]RunResult{
		"lpstat -e":                     ok(lpstatE),
		"lpstat -l -p":                  ok(lpstatP),
		"lpstat -d":                     ok(lpstatD),
		"lpoptions -p " + queue + " -l": ok(lpoptions),
		"lp":                            ok("request id is " + queue + "-17 (1 file(s))\n"),
	}}
	if lp != nil {
		r.replies["lp"] = *lp
	}
	return r
}

func ppdDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, queue+".ppd"), []byte(ppdExcerpt), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
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

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// --- parsing -------------------------------------------------------------------------------

func TestParseLpstat(t *testing.T) {
	if got := ParseLpstatE(lpstatE); !slices.Equal(got, []string{queue}) {
		t.Fatalf("-e %v", got)
	}
	if ParseLpstatD(lpstatD) != queue || ParseLpstatD("no system default destination\n") != "" {
		t.Fatal("-d")
	}
	s := ParseLpstatP(lpstatP)[queue]
	if s.State != "idle" || !s.Enabled || s.Description != "Brother HL-L3290CDW series" || len(s.Message) != 0 {
		t.Fatalf("-p %+v", s)
	}
	busy := ParseLpstatP("printer QL_820NWB now printing QL_820NWB-4.  enabled since Wed Nov 25 10:00:00 2026\n" +
		"\tPrinting page 1, 40% complete.\n" +
		"printer Zebra disabled since Wed Nov 25 09:00:00 2026 -\n\tPaused\n")
	if q := busy["QL_820NWB"]; q.State != "printing" || !q.Enabled || !slices.Equal(q.Message, []string{"Printing page 1, 40% complete."}) {
		t.Fatalf("QL %+v", q)
	}
	if z := busy["Zebra"]; z.State != "disabled" || z.Enabled || !slices.Equal(z.Message, []string{"Paused"}) {
		t.Fatalf("Zebra %+v", z)
	}
}

func TestListQueues(t *testing.T) {
	r := brotherRunner(nil)
	qs, err := (&Driver{Runner: r.run, PPDDir: ppdDir(t)}).ListQueues(context.Background())
	if err != nil || len(qs) != 1 {
		t.Fatalf("%v %v", qs, err)
	}
	q := qs[0]
	if q.Name != queue || q.Description != "Brother HL-L3290CDW series" || q.State != "idle" || !q.Enabled || q.Message != "" || !q.IsDefault {
		t.Fatalf("%+v", q)
	}
	if q.Media.Default != "Letter" || !q.Media.AcceptsCustom || q.Media.Size("Letter") == nil {
		t.Fatalf("media %+v", q.Media)
	}
	want := [][]string{{"lpstat", "-e"}, {"lpstat", "-l", "-p"}, {"lpstat", "-d"}, {"lpoptions", "-p", queue, "-l"}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls %v", r.calls)
	}
	none := &fakeRunner{replies: map[string]RunResult{"lpstat -e": {Code: 1, Stderr: "lpstat: No destinations added."}}}
	if qs, err := (&Driver{Runner: none.run}).ListQueues(context.Background()); err != nil || len(qs) != 0 {
		t.Fatalf("no printers: %v %v", qs, err)
	}
}

func TestParseLpoptions(t *testing.T) {
	o := ParseLpoptions(lpoptions)
	page := o["PageSize"]
	if page.Label != "Media Size" || page.Default != "Letter" || page.Choices[0] != "215x345mm" || page.Choices[len(page.Choices)-1] != "Custom.WIDTHxHEIGHT" {
		t.Fatalf("PageSize %+v", page)
	}
	if o["MediaType"].Default != "any" || !slices.Equal(o["InputSlot"].Choices, []string{"auto", "manual", "tray-1"}) {
		t.Fatal("other options")
	}
}

func TestParsePPD(t *testing.T) {
	p := ParsePPD(ppdExcerpt)
	l := p.PaperMM["Letter"]
	s := p.PaperMM["3x5"]
	if p.Default != "Letter" || !approx(l[0], 215.9) || !approx(l[1], 279.4) || !approx(s[0], 76.2) || !approx(s[1], 127) {
		t.Fatalf("%+v", p)
	}
	c := p.Custom
	if c == nil || !approx(c.Width[0], 76.2) || !approx(c.Width[1], 215.9) || !approx(c.Height[0], 127) || !approx(c.Height[1], 355.6) {
		t.Fatalf("custom %+v", c)
	}
}

func TestSizeFromName(t *testing.T) {
	for name, want := range map[string][2]float64{
		"62x29mm":            {62, 29},
		"215x345mm":          {215, 345},
		"4x6in":              {101.6, 152.4},
		"3x5":                {76.2, 127},
		"w288h432":           {101.6, 152.4},
		"w167h288.Fullbleed": {58.913, 101.6},
		"na_letter_8.5x11in": {215.9, 279.4},
		"iso_a4_210x297mm":   {210, 297},
		"Letter":             {215.9, 279.4},
		"A4.Borderless":      {210, 297},
		"Env10":              {104.775, 241.3},
	} {
		w, h, ok := SizeFromName(name)
		if !ok || !approx(w, want[0]) || !approx(h, want[1]) {
			t.Errorf("%s: %g×%g %v", name, w, h, ok)
		}
	}
	if _, _, ok := SizeFromName("DC03"); ok {
		t.Error("DC03 has no size")
	}
}

func brotherMedia(withPPD bool) QueueMedia {
	var ppd *PPDSizes
	if withPPD {
		p := ParsePPD(ppdExcerpt)
		ppd = &p
	}
	return QueueMediaFrom(ParseLpoptions(lpoptions), ppd)
}

func TestBestMediaOnTheBrother(t *testing.T) {
	m := brotherMedia(true)
	if m.Default != "Letter" || !m.AcceptsCustom {
		t.Fatalf("%+v", m)
	}
	for _, tc := range []struct {
		mm   [2]float64
		want string
	}{
		{[2]float64{215.9, 279.4}, "Letter"},
		{[2]float64{279.4, 215.9}, "Letter"},
		{[2]float64{210, 297}, "A4"},
		{[2]float64{210.5, 296.5}, "A4"},                   // within sheets.SizeToleranceMM
		{[2]float64{211.5, 298.5}, "Custom.211.5x298.5mm"}, // outside it
		{[2]float64{101.6, 152.4}, "Custom.101.6x152.4mm"},
		{[2]float64{50, 30}, ""}, // below the 76.2×127 mm custom minimum
	} {
		if got := BestMedia(m, tc.mm); got != tc.want {
			t.Errorf("%v: %q want %q", tc.mm, got, tc.want)
		}
	}
	if got := BestMedia(brotherMedia(false), [2]float64{50, 30}); got != "Custom.50x30mm" {
		t.Errorf("without the PPD any custom size is offered: %q", got)
	}
}

func TestBestMediaOnALabelQueue(t *testing.T) {
	m := QueueMediaFrom(ParseLpoptions(labelLpoptions), nil)
	var names []string
	for _, s := range m.Sizes {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"w79h252", "w167h288", "62x29mm", "62x100mm"}) {
		t.Fatalf("sizes %v", names)
	}
	if BestMedia(m, [2]float64{62, 29}) != "62x29mm" || BestMedia(m, [2]float64{28, 89}) != "w79h252" {
		t.Fatal("label sizes")
	}
	if CustomName(48, 30.25) != "Custom.48x30.25mm" {
		t.Fatal(CustomName(48, 30.25))
	}
}

func TestQueueMediaReadsThePPD(t *testing.T) {
	d := &Driver{Runner: brotherRunner(nil).run, PPDDir: ppdDir(t)}
	m, err := d.QueueMedia(context.Background(), queue)
	if err != nil || m.Custom == nil {
		t.Fatalf("%+v %v", m, err)
	}
	if l := m.Size("Letter"); l == nil || !approx(l.WidthMM, 215.9) || !approx(l.HeightMM, 279.4) {
		t.Fatalf("Letter %+v", l)
	}
	missing := &fakeRunner{replies: map[string]RunResult{"lpoptions": {Code: 1, Stderr: `lpoptions: Unknown printer "X".`}}}
	_, err = (&Driver{Runner: missing.run}).QueueMedia(context.Background(), "X")
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "no printer queue") {
		t.Fatalf("err %v", err)
	}
}

// --- lp command lines ----------------------------------------------------------------------

func TestLPCommand(t *testing.T) {
	argv, err := LPCommand(queue, "/tmp/x.pdf", "Letter", "Vinyl Labels", nil)
	want := []string{"lp", "-d", queue, "-t", "Vinyl Labels", "-o", "media=Letter",
		"-o", "fit-to-page=false", "-o", "scaling=100", "-o", "print-scaling=none", "/tmp/x.pdf"}
	if err != nil || !slices.Equal(argv, want) {
		t.Fatalf("%v %v", argv, err)
	}
	argv, err = LPCommand("QL", "/tmp/y.pdf", "Custom.50x30mm", "t", map[string]string{"MediaType": "labels", "Darkness": "3 4"})
	if err != nil || !slices.Equal(argv[len(argv)-5:], []string{"-o", "Darkness=3 4", "-o", "MediaType=labels", "/tmp/y.pdf"}) || !slices.Contains(argv, "media=Custom.50x30mm") {
		t.Fatalf("%v %v", argv, err)
	}
	for _, bad := range []string{"", "-x", "two words", "a#b"} {
		if _, err := LPCommand(bad, "/tmp/x.pdf", "Letter", "t", nil); err == nil {
			t.Errorf("queue %q accepted", bad)
		}
	}
	if _, err := LPCommand("Q", "/tmp/x.pdf", "Letter", "t", map[string]string{"bad key": "1"}); err == nil {
		t.Error("bad option key accepted")
	}
	if _, err := LPCommand("Q", "/tmp/x.pdf", "Letter", "t", map[string]string{"k": "a\nb"}); err == nil {
		t.Error("newline in option value accepted")
	}
	if ParseRequestID("request id is "+queue+"-17 (1 file(s))\n") != queue+"-17" || ParseRequestID("") != "" {
		t.Error("request id")
	}
}

// --- the driver ------------------------------------------------------------------------------

func send(t *testing.T, cat *printer.Catalog, r *fakeRunner, ppd, stockID string, count int, settings printer.Settings) (printer.Result, error) {
	t.Helper()
	m, err := cat.Model("cups")
	if err != nil {
		t.Fatal(err)
	}
	s, err := cat.Stock(stockID)
	if err != nil {
		t.Fatal(err)
	}
	d := &Driver{Runner: r.run, PPDDir: ppd, TempDir: t.TempDir()}
	cv := d.Canvas(m, s)
	labels := make([]*image.Gray, count)
	for i := range labels {
		labels[i] = image.NewGray(image.Rect(0, 0, cv.Width, cv.Height))
	}
	pc := &printer.PrintContext{Model: m, Stock: s, Target: &printer.Target{ID: "c1", Name: "Office laser", ModelID: "cups", Address: queue},
		Settings: printer.Merge(m.Options, settings)}
	return d.Send(context.Background(), pc, labels)
}

var mediaBoxRe = regexp.MustCompile(`/MediaBox \[ 0 0 ([\d.]+) ([\d.]+) \]`)

func mediaBoxes(pdf []byte) [][2]float64 {
	var out [][2]float64
	for _, m := range mediaBoxRe.FindAllSubmatch(pdf, -1) {
		w, _ := strconv.ParseFloat(string(m[1]), 64)
		h, _ := strconv.ParseFloat(string(m[2]), 64)
		out = append(out, [2]float64{w, h})
	}
	return out
}

func TestSendSheetStock(t *testing.T) {
	r := brotherRunner(nil)
	res, err := send(t, catalog(t, ""), r, ppdDir(t), "avery-5160", 31, printer.Settings{"job_name": "Crate A"})
	if err != nil || !res.OK || res.Printed != 31 || res.Details["job"] != queue+"-17" {
		t.Fatalf("%+v %v", res, err)
	}
	lp := r.calls[len(r.calls)-1]
	want := []string{"lp", "-d", queue, "-t", "Crate A", "-o", "media=Letter",
		"-o", "fit-to-page=false", "-o", "scaling=100", "-o", "print-scaling=none"}
	if !slices.Equal(lp[:len(lp)-1], want) || !strings.HasSuffix(lp[len(lp)-1], ".pdf") {
		t.Fatalf("lp %v", lp)
	}
	if _, err := os.Stat(lp[len(lp)-1]); !os.IsNotExist(err) {
		t.Fatal("the temp PDF was not removed")
	}
	if got := mediaBoxes(r.pdfs[0]); !reflect.DeepEqual(got, [][2]float64{{612, 792}, {612, 792}}) {
		t.Fatalf("31 labels = 2 Letter pages, got %v", got)
	}
}

func TestSendCustomSize(t *testing.T) {
	r := brotherRunner(nil)
	res, err := send(t, catalog(t, ""), r, ppdDir(t), "4x6", 2, printer.Settings{"cups_options": map[string]any{"MediaType": "labels"}})
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	lp := r.calls[len(r.calls)-1]
	if !slices.Contains(lp, "media=Custom.101.6x152.4mm") || !slices.Equal(lp[len(lp)-3:len(lp)-1], []string{"-o", "MediaType=labels"}) {
		t.Fatalf("lp %v", lp)
	}
	if got := mediaBoxes(r.pdfs[0]); len(got) != 2 || !approx(got[0][0], 288) || !approx(got[0][1], 432) {
		t.Fatalf("boxes %v", got)
	}
}

func TestSmallLabelFallsBackToTheDefaultPage(t *testing.T) {
	r := brotherRunner(nil)
	res, err := send(t, catalog(t, ""), r, ppdDir(t), "50x30", 1, nil)
	if err != nil || !res.OK || !strings.Contains(res.Message, "actual size on a Letter page") {
		t.Fatalf("%+v %v", res, err)
	}
	if !slices.Contains(r.calls[len(r.calls)-1], "media=Letter") || !reflect.DeepEqual(mediaBoxes(r.pdfs[0]), [][2]float64{{612, 792}}) {
		t.Fatalf("lp %v", r.calls[len(r.calls)-1])
	}
}

func TestSheetStockWithoutAMatchingSizeIsRefused(t *testing.T) {
	cat := catalog(t, `
[[stock]]
id = "a3-uncut"
name = "A3 uncut"
media = "sheet"
design_mm = [48, 30]
feed_mm = [297, 420]
sheet = { paper_mm = [297, 420], grid = "uncut", cell_mm = [50, 30] }
`)
	r := brotherRunner(nil)
	_, err := send(t, cat, r, ppdDir(t), "a3-uncut", 1, nil)
	if err == nil || !strings.Contains(err.Error(), "no 297×420 mm paper size") {
		t.Fatalf("err %v", err)
	}
	for _, c := range r.calls {
		if c[0] == "lp" {
			t.Fatal("lp ran")
		}
	}
}

func TestLPFailureIsAPrinterError(t *testing.T) {
	r := brotherRunner(&RunResult{Code: 1, Stderr: "lp: The printer or class does not exist."})
	_, err := send(t, catalog(t, ""), r, ppdDir(t), "avery-5163", 1, nil)
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err %v", err)
	}
}

func TestDryRunDoesNotCallLP(t *testing.T) {
	r := brotherRunner(nil)
	res, err := send(t, catalog(t, ""), r, ppdDir(t), "letter-uncut-50x30", 5, printer.Settings{"dry_run": true})
	if err != nil || !res.OK || res.Printed != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.calls) != 1 || r.calls[0][0] != "lpoptions" {
		t.Fatalf("calls %v", r.calls)
	}
	if argv := res.Details["argv"].([]string); argv[len(argv)-1] != "<pdf>" {
		t.Fatalf("argv %v", argv)
	}
}

func TestQueueMustBeAName(t *testing.T) {
	cat := catalog(t, "")
	m, _ := cat.Model("cups")
	s, _ := cat.Stock("avery-5160")
	r := brotherRunner(nil)
	pc := &printer.PrintContext{Model: m, Stock: s, Target: &printer.Target{Name: "x"}}
	_, err := (&Driver{Runner: r.run}).Send(context.Background(), pc, []*image.Gray{sheets.White(788, 300)})
	if err == nil || !strings.Contains(err.Error(), "queue name") || len(r.calls) != 0 {
		t.Fatalf("err %v calls %v", err, r.calls)
	}
}

func TestPreviewIsTheImposedPage(t *testing.T) {
	cat := catalog(t, "")
	m, _ := cat.Model("cups")
	s, _ := cat.Stock("letter-uncut-50x30")
	d := &Driver{}
	cv := d.Canvas(m, s)
	page, err := d.Preview(m, s, image.NewGray(image.Rect(0, 0, cv.Width, cv.Height)), nil)
	if err != nil || page.Bounds() != image.Rect(0, 0, 2550, 3300) || page.GrayAt(sheets.MMToPx(7.95, 300)+12, sheets.MMToPx(19.7, 300)).Y != 0 {
		t.Fatalf("%v %v", page.Bounds(), err)
	}
	s, _ = cat.Stock("50x30")
	if got, _ := d.Preview(m, s, image.NewGray(image.Rect(0, 0, 591, 354)), nil); got.Bounds() != image.Rect(0, 0, 591, 354) {
		t.Fatalf("%v", got.Bounds())
	}
}

func TestRegistered(t *testing.T) {
	d, err := printer.DriverFor("cups")
	if err != nil || d.Family() != "cups" || !slices.Equal(d.Links(), []printer.LinkKind{printer.LinkCUPS}) {
		t.Fatalf("%v %v", d, err)
	}
}

func TestExecRunnerReportsExitCodes(t *testing.T) {
	// /usr/bin/false and /usr/bin/true are harmless; no CUPS command is run.
	res, err := ExecRunner(context.Background(), []string{"/usr/bin/false"})
	if err != nil || res.Code == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := ExecRunner(context.Background(), []string{"crate-labels-no-such-command"}); err == nil {
		t.Fatal("missing command: no error")
	}
}

func TestQueueJSONMatchesTheAPI(t *testing.T) {
	b, err := json.Marshal(Queue{Name: "Q", State: "idle", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	for _, k := range []string{"name", "description", "state", "enabled", "message", "is_default"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s missing from %s", k, b)
		}
	}
}
