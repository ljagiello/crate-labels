package service_test

import (
	"bytes"
	"context"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/discogs"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/service/servicetest"
	"github.com/ljagiello/crate-labels/internal/store"
)

var ctx = context.Background()

func rid(n int64) *int64 { return &n }

func wait(t *testing.T, s *service.Service, id int64) store.Job {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	j, err := s.Wait(c, id)
	if err != nil {
		t.Fatalf("wait %d: %v", id, err)
	}
	return j
}

func TestPreviewIsAPNGAtCanvasSize(t *testing.T) {
	e := servicetest.New(t)
	data := service.Label{Artist: "A", Title: "T"}
	p, err := e.Service.Preview(ctx, service.PreviewRequest{Source: service.Source{Data: &data}, Template: "dj-single",
		Stock: "40x30", ModelID: servicetest.Model})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(p.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 240 || p.Canvas.Width != 320 || p.Canvas.Height != 240 {
		t.Fatalf("size = %v, canvas = %+v", b, p.Canvas)
	}
	if p.ModelID != servicetest.Model || p.Stock.ID != "40x30" || p.Canvas.DPI != 203.2 {
		t.Fatalf("preview = %+v", p)
	}
	for y := range 240 {
		for x := range 320 {
			if r, _, _, _ := img.At(x, y).RGBA(); r != 0 && r != 0xffff {
				t.Fatalf("pixel %d,%d is grey", x, y)
			}
		}
	}
}

func TestPreviewAcceptsIDsURLsMastersAndPrinters(t *testing.T) {
	e := servicetest.New(t)
	render := func(src service.Source, printerID string) service.Preview {
		t.Helper()
		p, err := e.Service.Preview(ctx, service.PreviewRequest{Source: src, Template: "dj-single", Stock: "40x30",
			PrinterID: printerID, ModelID: servicetest.Model})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	byID := render(service.Source{ReleaseID: rid(118638)}, "")
	byURL := render(service.Source{Ref: "https://www.discogs.com/release/118638-DJ-Force"}, "")
	byMaster := render(service.Source{Ref: "[m4589]"}, "")
	if !bytes.Equal(byID.PNG, byURL.PNG) || !bytes.Equal(byURL.PNG, byMaster.PNG) {
		t.Fatal("the same release rendered differently")
	}
	got := e.Layouts.Rendered()
	if got[0].Artist != "DJ Force & The Evolution" || got[0].ReleaseID != 118638 {
		t.Fatalf("rendered = %+v", got[0])
	}
	p := e.AddPrinter(t, "Bench", "", nil)
	if m := render(service.Source{ReleaseID: rid(118638)}, p.ID).ModelID; m != servicetest.Model {
		t.Fatalf("model = %s", m)
	}
}

func TestPreviewModelChoice(t *testing.T) {
	e := servicetest.New(t)
	data := service.Label{Title: "x"}
	pick := func(stock string) string {
		t.Helper()
		p, err := e.Service.Preview(ctx, service.PreviewRequest{Source: service.Source{Data: &data}, Template: "test-pattern", Stock: stock})
		if err != nil {
			t.Fatalf("%s: %v", stock, err)
		}
		return p.ModelID
	}
	// The test model takes 40x30 but is loaded last; the default printer wins.
	p := e.AddPrinter(t, "Bench", "", nil)
	if err := e.Store.SetSetting(ctx, "default_printer", p.ID); err != nil {
		t.Fatal(err)
	}
	if got := pick("40x30"); got != servicetest.Model {
		t.Errorf("default printer's model not used: %s", got)
	}
	m, _, err := e.Service.ModelFor(ctx, "", "", "70x80")
	if err != nil || m.ID == servicetest.Model {
		t.Errorf("70x80 model = %v, %v", m, err)
	}
}

func TestPreviewRejectsMisfits(t *testing.T) {
	e := servicetest.New(t)
	data := service.Label{Title: "x"}
	cases := []struct {
		req  service.PreviewRequest
		want string
	}{
		{service.PreviewRequest{Template: "dj-single", Stock: "70x80", ModelID: servicetest.Model}, "can't print on"},
		{service.PreviewRequest{Template: "big-only", Stock: "40x30", ModelID: servicetest.Model}, "does not fit"},
		{service.PreviewRequest{Template: "nope", Stock: "40x30", ModelID: servicetest.Model}, "Unknown template"},
		{service.PreviewRequest{Template: "dj-single", Stock: "40x30", PrinterID: "ghost"}, "No saved printer"},
		{service.PreviewRequest{Template: "dj-single", Stock: "zz", ModelID: servicetest.Model}, "unknown label stock"},
	}
	for _, c := range cases {
		c.req.Source = service.Source{Data: &data}
		_, err := e.Service.Preview(ctx, c.req)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.req, err)
			continue
		}
		if status, user := service.StatusOf(err); status != 400 || !user {
			t.Errorf("%v: status %d", err, status)
		}
	}
	_, err := e.Service.Preview(ctx, service.PreviewRequest{Template: "dj-single", Stock: "40x30", ModelID: servicetest.Model})
	if status, _ := service.StatusOf(err); status != 400 {
		t.Errorf("no source: %v", err)
	}
	_, err = e.Service.Preview(ctx, service.PreviewRequest{Source: service.Source{ReleaseID: rid(999)}, Template: "dj-single",
		Stock: "40x30", ModelID: servicetest.Model})
	if status, _ := service.StatusOf(err); status != 404 {
		t.Errorf("missing release: %v", err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPrintJobRunsAndRecordsHistoryPrintsAndFile(t *testing.T) {
	e := servicetest.New(t)
	out := filepath.Join(e.Dir, "out.bin")
	p := e.AddPrinter(t, "Bench", out, printer.Settings{"darkness": 10})
	events, stop := e.Service.Subscribe()
	defer stop()
	job, err := e.Service.Print(ctx, service.PrintRequest{
		Sources:  []service.Source{{ReleaseID: rid(118638)}, {Ref: "https://www.discogs.com/release/89569"}},
		Template: "dj-single", Stock: "40x30", Printer: p.ID, Copies: 2, Options: map[string]any{"show_qr": false},
		PrintOptions: map[string]any{"darkness": 12}, Origin: "ui"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.JobQueued || job.Total != 4 || len(job.Records) != 2 {
		t.Fatalf("job = %+v", job)
	}
	done := wait(t, e.Service, job.ID)
	if done.Status != store.JobDone || done.Printed != 4 || done.Progress != 1 || done.Error != "" {
		t.Fatalf("done = %+v", done)
	}
	var statuses []string
	for len(events) > 0 {
		statuses = append(statuses, (<-events).Status)
	}
	if statuses[0] != store.JobQueued || statuses[len(statuses)-1] != store.JobDone {
		t.Errorf("events = %v", statuses)
	}
	blob := readFile(t, out)
	if n := bytes.Count(blob, []byte("LBL 320x240 d=12\n")); n != 4 {
		t.Errorf("file holds %d labels at d=12 (per-job option must beat the printer setting)", n)
	}
	hist, _ := e.Store.History(ctx, 10)
	if len(hist) != 2 || hist[0].Title != "The Future Sound Of Hardcore" || hist[0].Copies != 2 || hist[1].Catno != "KF039" {
		t.Fatalf("history = %+v", hist)
	}
	if hist[0].Options["show_qr"] != false || hist[0].Printer != "Bench" {
		t.Errorf("history options = %+v", hist[0])
	}
	if st, _ := e.Store.PrintedState(ctx, 118638); !st.Printed || st.Labels != 2 || st.LastPrintedAt == nil {
		t.Errorf("printed state = %+v", st)
	}
	if n, _ := e.Store.LabelsPrinted(ctx); n != 4 {
		t.Errorf("labels printed = %d", n)
	}
	// A directory address gets one file per job.
	dir := filepath.Join(e.Dir, "jobs") + "/"
	q := e.AddPrinter(t, "Dir", dir, nil)
	second, err := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{ReleaseID: rid(118638)}},
		Template: "dj-single", Stock: "40x30", Printer: q.Name, Copies: 1})
	if err != nil {
		t.Fatal(err)
	}
	if wait(t, e.Service, second.ID).Status != store.JobDone {
		t.Fatal("second job failed")
	}
	if b := readFile(t, filepath.Join(dir, "job-"+itoa(second.ID)+".bin")); !bytes.HasPrefix(b, []byte("LBL 320x240 d=8")) {
		t.Errorf("job file = %q", b[:20])
	}
	// The printer that printed is now listed first.
	list, _ := e.Store.Printers(ctx)
	if list[0].ID != q.ID {
		t.Errorf("printers = %v", list)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func discogsParams(q string) discogs.SearchParams {
	return discogs.SearchParams{Query: q, Type: "release", Format: "Vinyl", Page: 1, PerPage: 30}
}

func collParams() discogs.CollectionParams {
	return discogs.CollectionParams{Page: 1, PerPage: 50, Sort: "added", SortOrder: "desc"}
}

func TestPartialFailureRecordsOnlyWhatPrinted(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", printer.Settings{"fail_after": 3})
	job, err := e.Service.Print(ctx, service.PrintRequest{
		Sources:  []service.Source{{ReleaseID: rid(118638)}, {ReleaseID: rid(89569)}},
		Template: "dj-single", Stock: "40x30", Printer: p.ID, Copies: 2})
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, e.Service, job.ID)
	if done.Status != store.JobFailed || done.Printed != 3 || done.Error != "Out of labels" || done.Message != "Stopped after 3 of 4" {
		t.Fatalf("done = %+v", done)
	}
	hist, _ := e.Store.History(ctx, 10)
	if len(hist) != 2 || *hist[0].ReleaseID != 89569 || hist[0].Copies != 1 || *hist[1].ReleaseID != 118638 || hist[1].Copies != 2 {
		t.Fatalf("history = %+v", hist)
	}
	if n, _ := e.Store.LabelsPrinted(ctx); n != 3 {
		t.Errorf("labels printed = %d", n)
	}
}

func TestDriverErrorWithPartialResultRecordsWhatPrinted(t *testing.T) {
	e := servicetest.New(t)
	out := filepath.Join(e.Dir, "partial.bin")
	p := e.AddPrinter(t, "Bench", out, printer.Settings{"error_after": 3})
	job, err := e.Service.Print(ctx, service.PrintRequest{
		Sources:  []service.Source{{ReleaseID: rid(118638)}, {ReleaseID: rid(89569)}},
		Template: "dj-single", Stock: "40x30", Printer: p.ID, Copies: 2})
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, e.Service, job.ID)
	if done.Status != store.JobFailed || done.Printed != 3 || done.Message != "Stopped after 3 of 4" ||
		done.Error != "The printer reports a paper jam. Clear it and retry." {
		t.Fatalf("done = %+v", done)
	}
	hist, _ := e.Store.History(ctx, 10)
	if len(hist) != 2 || *hist[0].ReleaseID != 89569 || hist[0].Copies != 1 || hist[1].Copies != 2 {
		t.Fatalf("history = %+v", hist)
	}
	for id, want := range map[int64]int{118638: 2, 89569: 1} {
		if st, _ := e.Store.PrintedState(ctx, id); !st.Printed || st.Labels != want {
			t.Errorf("release %d = %+v, want %d labels", id, st, want)
		}
	}
	if n, _ := e.Store.LabelsPrinted(ctx); n != 3 {
		t.Errorf("labels printed = %d", n)
	}
	if b := readFile(t, out); bytes.Count(b, []byte("LBL ")) != 3 {
		t.Errorf("file output holds %d labels", bytes.Count(b, []byte("LBL ")))
	}
}

func TestEveryPrintedRecordIsLoggedButOnlyReleasesGetPrinted(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", nil)
	byHand := service.Label{Title: "White label, no release"}
	seller := service.Label{Title: "For sale", ReleaseID: rid(5), Price: "12.99"}
	job, err := e.Service.Print(ctx, service.PrintRequest{
		Sources:  []service.Source{{Data: &byHand}, {Data: &seller}},
		Template: "dj-single", Stock: "40x30", Printer: p.ID, Copies: 2})
	if err != nil {
		t.Fatal(err)
	}
	if wait(t, e.Service, job.ID).Status != store.JobDone {
		t.Fatal("not done")
	}
	// A priced label is still a label on that copy, so it counts.
	if st, _ := e.Store.PrintedState(ctx, 5); !st.Printed || st.Labels != 2 {
		t.Errorf("seller tag = %+v", st)
	}
	if n, _ := e.Store.LabelsPrinted(ctx); n != 4 {
		t.Errorf("labels printed = %d (the label without a release counts in the total)", n)
	}
	test, err := e.Service.TestPrint(ctx, p.ID, "test-pattern", "40x30", nil)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, e.Service, test.ID)
	if n, _ := e.Store.LabelsPrinted(ctx); n != 5 {
		t.Errorf("labels printed after a test = %d", n)
	}
	views, err := e.Service.Printed(ctx, []int64{5, 6, 0})
	if err != nil || len(views) != 2 || !views[5].Printed || views[5].Override != "auto" || views[6].Printed || views[6].LastPrintedAt != nil {
		t.Fatalf("printed views = %+v %v", views, err)
	}
	v, err := e.Service.SetPrinted(ctx, 5, store.OverrideNotPrinted)
	if err != nil || v.Printed || v.Override != "not_printed" || v.Labels != 2 {
		t.Fatalf("override = %+v %v", v, err)
	}
	if _, err := e.Service.SetPrinted(ctx, 5, "perhaps"); err == nil {
		t.Fatal("bad override accepted")
	}
}

func TestJobsForOnePrinterRunInOrderAndCancel(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", printer.Settings{"hold_ms": 30})
	data := service.Label{Title: "x"}
	var ids []int64
	for range 3 {
		j, err := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single",
			Stock: "40x30", Printer: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	cancelled, err := e.Service.Cancel(ctx, ids[2])
	if err != nil || cancelled.Status != store.JobCancelled {
		t.Fatalf("cancel queued = %+v, %v", cancelled, err)
	}
	first, second := wait(t, e.Service, ids[0]), wait(t, e.Service, ids[1])
	if first.Status != store.JobDone || second.Status != store.JobDone || *second.StartedAt < *first.FinishedAt {
		t.Fatalf("jobs = %+v / %+v", first, second)
	}
	if j, _ := e.Store.Job(ctx, ids[2]); j.Status != store.JobCancelled || j.StartedAt != nil {
		t.Fatalf("cancelled job ran: %+v", j)
	}
	// Retry makes a new job; a finished job only.
	retried, err := e.Service.Retry(ctx, ids[2])
	if err != nil || retried.Source != "retry:"+itoa(ids[2]) || retried.ID == ids[2] {
		t.Fatalf("retry = %+v, %v", retried, err)
	}
	if wait(t, e.Service, retried.ID).Status != store.JobDone {
		t.Fatal("retry failed")
	}
}

func TestCancelAPrintingJob(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", printer.Settings{"hold_ms": 5000})
	data := service.Label{Title: "x", ReleaseID: rid(7)}
	j, err := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single",
		Stock: "40x30", Printer: p.ID, Copies: 3})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, _ := e.Store.Job(ctx, j.ID)
		if cur.Status == store.JobPrinting && strings.HasPrefix(cur.Message, "Sending") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never started: %+v", cur)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := e.Service.Retry(ctx, j.ID); err == nil {
		t.Fatal("retried a printing job")
	}
	if _, err := e.Service.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, e.Service, j.ID)
	if done.Status != store.JobCancelled || done.FinishedAt == nil {
		t.Fatalf("done = %+v", done)
	}
	if h, _ := e.Store.History(ctx, 10); len(h) != 0 {
		t.Fatalf("nothing printed but history = %+v", h)
	}
}

func TestPrintCrateInOrderWithAnEditedLabel(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", nil)
	c, _ := e.Store.CreateCrate(ctx, "Happy HC")
	y95, y96 := int64(1995), int64(1996)
	edited := service.Label{Artist: "DJ Force", Title: "Edited title", ReleaseID: rid(118638)}
	a, _ := e.Store.AddToCrate(ctx, c.ID, store.NewCrateRecord{ReleaseID: 89569, Artist: "Various", Title: "The Future Sound Of Hardcore", Year: &y96})
	_, _ = e.Store.AddToCrate(ctx, c.ID, store.NewCrateRecord{ReleaseID: 118638, Artist: "DJ Force & The Evolution", Title: "Simply Electric", Year: &y95, Data: edited.JSON()})
	job, err := e.Service.PrintCrate(ctx, service.CratePrintRequest{CrateID: c.ID, Order: service.OrderArtist, Template: "dj-single",
		Stock: "40x30", Printer: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if job.Source != "crate:Happy HC" || len(job.Records) != 2 {
		t.Fatalf("job = %+v", job)
	}
	var titles []string
	for _, raw := range job.Records {
		l, _ := service.ParseLabel(raw)
		titles = append(titles, l.Title)
	}
	if !reflect.DeepEqual(titles, []string{"Edited title", "The Future Sound Of Hardcore"}) {
		t.Fatalf("titles = %v", titles)
	}
	if wait(t, e.Service, job.ID).Status != store.JobDone {
		t.Fatal("crate job failed")
	}
	only, err := e.Service.PrintCrate(ctx, service.CratePrintRequest{CrateID: c.ID, RecordIDs: []int64{a.ID}, Template: "dj-single",
		Stock: "40x30", Printer: p.ID})
	if err != nil || len(only.Records) != 1 {
		t.Fatalf("selected records = %+v, %v", only, err)
	}
	for name, r := range map[string]service.CratePrintRequest{
		"empty selection": {CrateID: c.ID, RecordIDs: []int64{}},
		"unknown order":   {CrateID: c.ID, Order: "title"},
		"missing crate":   {CrateID: "nope"},
	} {
		r.Template, r.Stock, r.Printer = "dj-single", "40x30", p.ID
		if _, err := e.Service.PrintCrate(ctx, r); err == nil {
			t.Errorf("%s: printed", name)
		}
	}
	empty, _ := e.Store.CreateCrate(ctx, "Empty")
	if _, err := e.Service.PrintCrate(ctx, service.CratePrintRequest{CrateID: empty.ID, Template: "dj-single", Stock: "40x30",
		Printer: p.ID}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty crate: %v", err)
	}
}

func TestSortCrate(t *testing.T) {
	y := func(v int64) *int64 { return &v }
	recs := []store.CrateRecord{
		{ID: 1, Position: 0, Artist: "The Prodigy", Catno: "XLT 39", Year: y(1992)},
		{ID: 2, Position: 1, Artist: "Acen", Catno: "PROD 9", Year: y(1992)},
		{ID: 3, Position: 2, Artist: "", Catno: "", Year: nil},
		{ID: 4, Position: 3, Artist: "acen", Catno: "PROD 10", Year: y(1993)},
		{ID: 5, Position: 4, Artist: "Bizzy B", Catno: "prod-009", Year: y(0)},
		{ID: 6, Position: 5, Artist: "The", Catno: "kf039", Year: y(1995)},
	}
	ids := func(list []store.CrateRecord) []int64 {
		var out []int64
		for _, r := range list {
			out = append(out, r.ID)
		}
		return out
	}
	for order, want := range map[string][]int64{
		service.OrderCrate: {1, 2, 3, 4, 5, 6},
		"":                 {1, 2, 3, 4, 5, 6},
		// "The Prodigy" files under P; a band called just "The" stays under T; blanks last.
		service.OrderArtist: {2, 4, 5, 1, 6, 3},
		// Numbers compare as numbers; "PROD 9" ties with "prod-009", and the artist breaks it.
		service.OrderCatno: {6, 2, 5, 4, 1, 3},
		// Newest first; a zero or missing year goes last.
		service.OrderNewest: {6, 4, 2, 1, 5, 3},
	} {
		if got := ids(service.SortCrate(recs, order)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %v, want %v", order, got, want)
		}
	}
	if recs[0].ID != 1 || recs[5].ID != 6 {
		t.Error("SortCrate changed its input")
	}
}

func TestReprintFromHistory(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", nil)
	job, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{ReleaseID: rid(118638)}}, Template: "dj-single",
		Stock: "50x30", Printer: p.ID, Copies: 2, Options: map[string]any{"show_qr": false}})
	wait(t, e.Service, job.ID)
	hist, _ := e.Store.History(ctx, 1)
	if _, err := e.Service.Reprint(ctx, hist[0].ID, "", 0); err == nil {
		t.Fatal("reprinted with no printer and no default")
	}
	_ = e.Store.SetSetting(ctx, "default_printer", p.ID)
	again, err := e.Service.Reprint(ctx, hist[0].ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Source != "reprint:"+itoa(hist[0].ID) || again.Total != 2 || again.Stock != "50x30" || again.Options["show_qr"] != false {
		t.Fatalf("reprint = %+v", again)
	}
	if wait(t, e.Service, again.ID).Status != store.JobDone {
		t.Fatal("reprint failed")
	}
	if h, _ := e.Store.History(ctx, 10); len(h) != 1 || h[0].Copies != 2 {
		t.Fatalf("reprint did not replace the entry: %+v", h)
	}
	if _, err := e.Service.Reprint(ctx, 9999, p.ID, 1); !store.IsNotFound(err) {
		t.Fatalf("missing entry: %v", err)
	}
}

func TestPrintValidation(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", nil)
	data := service.Label{Title: "x"}
	src := []service.Source{{Data: &data}}
	cases := []struct {
		req  service.PrintRequest
		want string
	}{
		{service.PrintRequest{Sources: src, Template: "dj-single", Stock: "40x30", Printer: "ghost"}, "No saved printer"},
		{service.PrintRequest{Sources: src, Template: "dj-single", Stock: "70x80", Printer: p.ID}, "can't print on"},
		{service.PrintRequest{Sources: src, Template: "nope", Stock: "40x30", Printer: p.ID}, "Unknown template"},
		{service.PrintRequest{Sources: src, Template: "dj-single", Stock: "40x30", Printer: p.ID, Copies: 100}, "Copies"},
		{service.PrintRequest{Template: "dj-single", Stock: "40x30", Printer: p.ID}, "Nothing to print"},
		{service.PrintRequest{Sources: []service.Source{{Ref: "Kniteforce"}}, Template: "dj-single", Stock: "40x30", Printer: p.ID}, "Paste a Discogs"},
	}
	for _, c := range cases {
		if _, err := e.Service.Print(ctx, c.req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.req, err)
		}
	}
	if jobs, _ := e.Store.Jobs(ctx, 10, false); len(jobs) != 0 {
		t.Fatalf("invalid requests queued jobs: %+v", jobs)
	}
}

func TestPrinterRemovedBeforeTheJobRan(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", printer.Settings{"hold_ms": 100})
	data := service.Label{Title: "x"}
	first, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: p.ID})
	second, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: p.ID})
	_ = e.Store.DeletePrinter(ctx, p.ID)
	wait(t, e.Service, first.ID)
	if j := wait(t, e.Service, second.ID); j.Status != store.JobFailed || j.Error != "That printer was removed." {
		t.Fatalf("job = %+v", j)
	}
}

func TestTokenSetTestAndClear(t *testing.T) {
	e := servicetest.New(t)
	_ = e.Tokens.Delete(ctx)
	if st := e.Service.TokenStatus(ctx); st.TokenSet || st.Source != "none" || st.Username != nil {
		t.Fatalf("status = %+v", st)
	}
	if _, err := e.Service.TestToken(ctx); err == nil {
		t.Fatal("tested without a token")
	}
	if _, err := e.Service.SetToken(ctx, "bad token"); err == nil {
		t.Fatal("bad token accepted")
	}
	tok := strings.Repeat("Z", 40)
	st, err := e.Service.SetToken(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !st.TokenSet || st.Source != "keychain" || st.Username == nil || *st.Username != "testuser" {
		t.Fatalf("status = %+v", st)
	}
	settings, _ := e.Store.Settings(ctx)
	for k, v := range settings {
		if strings.Contains(string(v), tok) || strings.Contains(k, tok) {
			t.Fatal("the token reached the settings table")
		}
	}
	t.Setenv("DISCOGS_TOKEN", "envtoken1234567890")
	if st := e.Service.TokenStatus(ctx); st.Source != "env" {
		t.Fatalf("env source = %+v", st)
	}
	t.Setenv("DISCOGS_TOKEN", "")
	st, _ = e.Service.ClearToken(ctx)
	if st.TokenSet || st.Source != "none" || st.Username != nil {
		t.Fatalf("cleared status = %+v", st)
	}
}

func TestLookupSearchAndCollection(t *testing.T) {
	e := servicetest.New(t)
	l, err := e.Service.LookupText(ctx, "https://www.discogs.com/master/4589", false)
	if err != nil {
		t.Fatal(err)
	}
	if l.Ref.Kind != "master" || l.Ref.ID != 4589 || l.Release.ID != 118638 || l.Data.Catno != "KF039" || l.Release.Printed {
		t.Fatalf("lookup = %+v", l)
	}
	if _, err := e.Service.LookupText(ctx, "Kniteforce", false); err == nil {
		t.Fatal("looked up plain text")
	}
	if _, err := e.Service.SetPrinted(ctx, 118638, store.OverridePrinted); err != nil {
		t.Fatal(err)
	}
	res, err := e.Service.Search(ctx, discogsParams("kf039"))
	if err != nil || len(res.Results) != 1 || !res.Results[0].Printed || string(res.Pagination) == "{}" {
		t.Fatalf("search = %+v, %v", res, err)
	}
	if _, err := e.Service.Search(ctx, discogsParams("  ")); err == nil {
		t.Fatal("empty search accepted")
	}
	coll, err := e.Service.Collection(ctx, collParams())
	if err != nil || len(coll.Items) != 1 || coll.Items[0].Notes != "plays great" {
		t.Fatalf("collection = %+v, %v", coll, err)
	}
	want, err := e.Service.Wantlist(ctx, 1, 50, false)
	if err != nil || len(want.Items) != 1 {
		t.Fatalf("wantlist = %+v, %v", want, err)
	}
	folders, err := e.Service.Folders(ctx)
	if err != nil || len(folders) != 2 {
		t.Fatalf("folders = %+v, %v", folders, err)
	}
	// prefer_anv applies to lookups.
	_ = e.Store.SetSetting(ctx, "discogs.prefer_anv", true)
	l, _ = e.Service.LookupRelease(ctx, 10550772, false)
	if l.Data.Artist != "Sanxion / Nicky Allen / TNO Project / Mannik" {
		t.Errorf("prefer_anv artist = %q", l.Data.Artist)
	}
}

func TestStocksAreInCatalogOrder(t *testing.T) {
	e := servicetest.New(t)
	var got []string
	for _, s := range e.Service.Stocks() {
		got = append(got, s.ID)
	}
	want := []string{"50x80-two-track", "50x30", "40x30", "70x80", "d30-15mm-tape", "15x50", "letter-uncut-50x30",
		"letter-uncut-50x80", "avery-5160", "avery-5163", "4x6", "center-7in", "obi-legal", "obi-mini-letter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stocks = %v", got)
	}
	tpl, _ := e.Service.Template("big-only")
	if ids := e.Service.TemplateStocks(tpl); !reflect.DeepEqual(ids, []string{"70x80"}) {
		t.Fatalf("template stocks = %v", ids)
	}
}

func TestCalibrationValidation(t *testing.T) {
	got, err := service.CleanCalibration(map[string]any{"darkness": "12", "extra_rotate_cw": "180", "ble_gap_ms": "15",
		"offset_dots": float64(-4), "paper_align": "center", "custom": float64(1), "speed": "", "density": nil})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"darkness": 12, "extra_rotate_cw": 180, "ble_gap_ms": 15.0, "offset_dots": -4,
		"paper_align": "center", "custom": float64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clean = %#v", got)
	}
	for _, bad := range []map[string]any{{"extra_rotate_cw": 45.0}, {"darkness": "dark"}, {"paper_align": "guides"},
		{"ble_gap_ms": 2000.0}, {"darkness": "1.5"}, {"speed": true}} {
		if _, err := service.CleanCalibration(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func waitFor(t *testing.T, e *servicetest.Env, id int64, cond func(store.Job) bool) store.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, _ := e.Store.Job(ctx, id)
		if cond(j) {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d never got there: %+v", id, j)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCloseInterruptsRunningAndQueuedJobs(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", printer.Settings{"hold_ms": 200})
	data := service.Label{Title: "x", ReleaseID: rid(9)}
	j, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single",
		Stock: "40x30", Printer: p.ID, Copies: 3})
	queued, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single",
		Stock: "40x30", Printer: p.ID})
	waitFor(t, e, j.ID, func(j store.Job) bool { return j.Message == "Printed 1 of 3" })
	e.Service.Close() // Ctrl-C or SIGTERM
	got, _ := e.Store.Job(ctx, j.ID)
	if got.Status != store.JobInterrupted || got.Printed != 1 || got.Error != store.InterruptedMessage {
		t.Fatalf("running job after shutdown = %+v", got)
	}
	if h, _ := e.Store.History(ctx, 10); len(h) != 1 || h[0].Copies != 1 {
		t.Fatalf("history = %+v", h)
	}
	if got, _ := e.Store.Job(ctx, queued.ID); got.Status != store.JobInterrupted {
		t.Fatalf("queued job after shutdown = %+v", got)
	}
	if n, _ := e.Service.Recover(ctx); n != 0 {
		t.Fatalf("recover found %d more", n)
	}
	if _, err := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single",
		Stock: "40x30", Printer: p.ID}); err == nil {
		t.Fatal("queued a job on a closed service")
	}
}

func TestUserCancelMidPrintKeepsWhatPrinted(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", printer.Settings{"hold_ms": 200})
	data := service.Label{Title: "x", ReleaseID: rid(9)}
	j, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single",
		Stock: "40x30", Printer: p.ID, Copies: 3})
	waitFor(t, e, j.ID, func(j store.Job) bool { return j.Message == "Printed 1 of 3" })
	if _, err := e.Service.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, e.Service, j.ID)
	if done.Status != store.JobCancelled || done.Printed != 1 || done.Error != "" {
		t.Fatalf("cancelled = %+v", done)
	}
	if h, _ := e.Store.History(ctx, 10); len(h) != 1 || h[0].Copies != 1 {
		t.Fatalf("history = %+v", h)
	}
	if st, _ := e.Store.PrintedState(ctx, 9); !st.Printed || st.Labels != 1 {
		t.Fatalf("printed state = %+v", st)
	}
}

func TestJobOwnersDecideRecoverAndCancel(t *testing.T) {
	e := servicetest.New(t)
	e.Service.OwnerAlive = func(owner string) bool { return owner == "live:1" }
	mk := func(owner, status string) store.Job {
		j, err := e.Store.CreateJob(ctx, store.NewJob{PrinterID: "p", Template: "dj-single", Stock: "40x30", Owner: owner})
		if err != nil {
			t.Fatal(err)
		}
		if status == store.JobPrinting {
			j, _, _ = e.Store.TransitionJob(ctx, j.ID, store.JobQueued, store.JobPrinting, "Sending")
		}
		return j
	}
	deadQueued, deadPrinting := mk("dead:1", store.JobQueued), mk("", store.JobPrinting)
	liveQueued, livePrinting := mk("live:1", store.JobQueued), mk("live:1", store.JobPrinting)
	cancelMe := mk("dead:2", store.JobPrinting)
	n, err := e.Service.Recover(ctx)
	if err != nil || n != 3 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	for id, want := range map[int64]string{deadQueued.ID: store.JobInterrupted, deadPrinting.ID: store.JobInterrupted,
		cancelMe.ID: store.JobInterrupted, liveQueued.ID: store.JobQueued, livePrinting.ID: store.JobPrinting} {
		if j, _ := e.Store.Job(ctx, id); j.Status != want {
			t.Errorf("job %d (%s) = %s, want %s", id, j.Owner, j.Status, want)
		}
	}
	// Cancel: another live process's printing job cannot be stopped here.
	_, err = e.Service.Cancel(ctx, livePrinting.ID)
	if status, _ := service.StatusOf(err); status != 409 {
		t.Fatalf("cancel a live process's printing job: %v", err)
	}
	// Its queued job can be cancelled (its worker will skip it).
	if j, err := e.Service.Cancel(ctx, liveQueued.ID); err != nil || j.Status != store.JobCancelled {
		t.Fatalf("cancel a live process's queued job = %+v, %v", j, err)
	}
	// A dead owner's job is interrupted by Cancel.
	stuck := mk("dead:3", store.JobPrinting)
	if j, err := e.Service.Cancel(ctx, stuck.ID); err != nil || j.Status != store.JobInterrupted {
		t.Fatalf("cancel a dead process's job = %+v, %v", j, err)
	}
	// The service's own jobs carry its owner.
	p := e.AddPrinter(t, "Bench", "", nil)
	data := service.Label{Title: "x"}
	own, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single",
		Stock: "40x30", Printer: p.ID})
	if own.Owner != e.Service.Owner || own.Owner == "" {
		t.Fatalf("owner = %q, service %q", own.Owner, e.Service.Owner)
	}
	wait(t, e.Service, own.ID)
}

func TestPrintersOnOneAddressShareAQueue(t *testing.T) {
	e := servicetest.New(t)
	dir := filepath.Join(e.Dir, "dev") + "/"
	a := e.AddPrinter(t, "A", dir, printer.Settings{"hold_ms": 60})
	b := e.AddPrinter(t, "B", dir, printer.Settings{"hold_ms": 60})
	data := service.Label{Title: "x"}
	ja, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: a.ID})
	jb, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: b.ID})
	da, db := wait(t, e.Service, ja.ID), wait(t, e.Service, jb.ID)
	if *db.StartedAt < *da.FinishedAt {
		t.Fatalf("two printers on one device ran at once: %v..%v and %v", *da.StartedAt, *da.FinishedAt, *db.StartedAt)
	}
	// A different address is a different device and runs in parallel.
	c := e.AddPrinter(t, "C", filepath.Join(e.Dir, "other")+"/", printer.Settings{"hold_ms": 300})
	d := e.AddPrinter(t, "D", filepath.Join(e.Dir, "third")+"/", printer.Settings{"hold_ms": 300})
	jc, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: c.ID})
	jd, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: d.ID})
	dc, dd := wait(t, e.Service, jc.ID), wait(t, e.Service, jd.ID)
	if *dd.StartedAt >= *dc.FinishedAt {
		t.Fatalf("two devices did not run in parallel")
	}
}

func TestAJobMovedWhileQueuedReleasesItsWaiters(t *testing.T) {
	e := servicetest.New(t)
	p := e.AddPrinter(t, "Bench", "", printer.Settings{"hold_ms": 100})
	data := service.Label{Title: "x"}
	first, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: p.ID})
	second, _ := e.Service.Print(ctx, service.PrintRequest{Sources: []service.Source{{Data: &data}}, Template: "dj-single", Stock: "40x30", Printer: p.ID})
	// Another process interrupts the queued job behind the service's back.
	if _, err := e.Store.InterruptStaleJobs(ctx, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if j := wait(t, e.Service, second.ID); j.Status != store.JobInterrupted {
		t.Fatalf("second = %+v", j)
	}
	_ = first
}

func TestATokenChangedByAnotherProcessIsPickedUp(t *testing.T) {
	e := servicetest.New(t)
	newToken := strings.Repeat("N", 40)
	e.Stub.Extra = map[string]func(*http.Request) (int, string){"/database/search": func(r *http.Request) (int, string) {
		if r.Header.Get("Authorization") != "Discogs token="+newToken {
			return 401, `{"message": "Invalid consumer token."}`
		}
		return 200, servicetest.SearchResult
	}}
	// The service caches the first token it reads.
	if st := e.Service.TokenStatus(ctx); !st.TokenSet {
		t.Fatal("no token")
	}
	// `crate-labels discogs token set` in another process writes the Keychain directly.
	if err := e.Tokens.Set(ctx, newToken); err != nil {
		t.Fatal(err)
	}
	res, err := e.Service.Search(ctx, discogsParams("kf039"))
	if err != nil || len(res.Results) != 1 {
		t.Fatalf("search after an outside token change = %+v, %v", res, err)
	}
	if n := e.Stub.Count("/database/search"); n != 2 {
		t.Fatalf("search requests = %d, want the 401 plus one retry", n)
	}
	// Cleared elsewhere: the Keychain is read again once the cached token is 5 minutes old.
	_ = e.Tokens.Delete(ctx)
	cache, ok := e.Service.TokenStore.(*discogs.CachedTokens)
	if !ok {
		t.Fatalf("token store = %T", e.Service.TokenStore)
	}
	if st := e.Service.TokenStatus(ctx); !st.TokenSet {
		t.Fatal("the cache expired early")
	}
	cache.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if st := e.Service.TokenStatus(ctx); st.TokenSet || st.Source != "none" {
		t.Fatalf("status after an outside clear = %+v", st)
	}
}

func TestSetTokenChecksWithDiscogsBeforeSaving(t *testing.T) {
	e := servicetest.New(t)
	old, _ := e.Tokens.Get(ctx)
	good, bad, offline := strings.Repeat("G", 40), strings.Repeat("B", 40), strings.Repeat("O", 40)
	e.Stub.Extra = map[string]func(*http.Request) (int, string){"/oauth/identity": func(r *http.Request) (int, string) {
		if r.Header.Get("Authorization") == "Discogs token="+bad {
			return 401, `{"message": "Invalid consumer token."}`
		}
		return 200, `{"id": 1, "username": "testuser"}`
	}}
	// Rejected: nothing stored, and the error says so.
	_, err := e.Service.SetToken(ctx, bad)
	if status, _ := service.StatusOf(err); status != 401 || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("rejected token: %v", err)
	}
	if got, _ := e.Tokens.Get(ctx); got != old {
		t.Fatal("a rejected token was stored")
	}
	// Accepted: stored, with the account.
	st, err := e.Service.SetToken(ctx, good)
	if err != nil || st.Warning != "" || st.Username == nil || *st.Username != "testuser" {
		t.Fatalf("good token = %+v, %v", st, err)
	}
	if got, _ := e.Tokens.Get(ctx); got != good {
		t.Fatal("the good token was not stored")
	}
	// Discogs unreachable: stored anyway, with a warning, and the old account forgotten.
	e.Stub.Down = true
	st, err = e.Service.SetToken(ctx, offline)
	if err != nil || !strings.HasPrefix(st.Warning, "Saved, but couldn't reach Discogs") || !st.TokenSet || st.Username != nil {
		t.Fatalf("offline = %+v, %v", st, err)
	}
	if got, _ := e.Tokens.Get(ctx); got != offline {
		t.Fatal("the unchecked token was not stored")
	}
	// The check used the candidate token, never the stored one.
	for _, r := range e.Stub.All() {
		if r.URL.Path == "/oauth/identity" && r.Header.Get("Authorization") == "Discogs token="+old {
			t.Fatal("the check sent the old token")
		}
	}
}

func TestDiscogsUserAgentCarriesVersion(t *testing.T) {
	e := servicetest.New(t)
	if got, want := e.Service.Discogs.UserAgent, "crate-labels/"+service.Version; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}
