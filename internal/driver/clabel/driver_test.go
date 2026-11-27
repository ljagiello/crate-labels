package clabel

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/fake"
)

var (
	fastPacing = Pacing{Settle: time.Millisecond, PageBase: time.Millisecond, ChunkGapMS: 0, ChunkBytes: 200}
	fastWaits  = Waits{Answer: 150 * time.Millisecond, Quiet: time.Millisecond}
)

func fastDriver() *Driver { return New(WithPacing(fastPacing), WithWaits(fastWaits)) }

func ptr(b byte) *byte { return &b }

// fakeClabel answers control frames like the printer. Settings map to their value, or
// nil for "no value"; a missing setting is never answered.
type fakeClabel struct {
	t        *testing.T
	silent   bool
	values   map[Setting]*byte
	sticky   bool // writes change the value
	echo     bool // answer a write with the old value right away
	identity []byte
	extra    [][]byte // frames sent with the identity answer

	mu       sync.Mutex
	stream   bytes.Buffer
	writes   []RawFrame // setting writes
	controls int
}

func (f *fakeClabel) frame(fn, sub byte, content ...byte) []byte {
	b, err := ControlFrame(fn, sub, content)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *fakeClabel) answer(s Setting) [][]byte {
	v, ok := f.values[s]
	switch {
	case !ok:
		return nil
	case v == nil:
		return [][]byte{f.frame(5, byte(s)|0x80)}
	}
	return [][]byte{f.frame(5, byte(s)|0x80, *v)}
}

func (f *fakeClabel) respond(w fake.Write) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w.Role != roleData {
		f.t.Errorf("unexpected role %q", w.Role)
	}
	if !w.WithResponse {
		f.stream.Write(w.Data)
		return nil
	}
	f.controls++
	var r Reassembler
	frames := r.Push(w.Data)
	if len(frames) != 1 || !bytes.Equal(w.Data, f.frame(frames[0].Function, frames[0].Sub, frames[0].Content...)) {
		f.t.Errorf("control write is not one whole frame: %x", w.Data)
		return nil
	}
	fr := frames[0]
	if f.silent {
		return nil
	}
	switch {
	case fr.Function == 5 && fr.Sub == 4:
		if f.identity == nil {
			return f.extra
		}
		return append([][]byte{f.frame(5, 0x84, f.identity...)}, f.extra...)
	case fr.Function == 5 && len(fr.Content) == 0:
		return f.answer(Setting(fr.Sub))
	case fr.Function == 5 && len(fr.Content) == 1:
		f.writes = append(f.writes, fr)
		var out [][]byte
		if f.echo {
			out = f.answer(Setting(fr.Sub))
		}
		if f.sticky {
			f.values[Setting(fr.Sub)] = ptr(fr.Content[0])
		}
		return out
	}
	f.t.Errorf("unexpected control frame %+v", fr)
	return nil
}

// fixtureJob builds a Job from a golden fixture's raster (the image step is covered by
// the encode tests).
func fixtureJob(t *testing.T, model, stock string) (goldenJob, *Job) {
	t.Helper()
	for _, j := range loadJobs(t) {
		if j.dir[1] != model || j.dir[2] != stock {
			continue
		}
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
			t.Fatal(err)
		}
		j.model.Label = map[string]string{"ct230b": "Clabel CT-230B", "ct320b": "Clabel CT-320B"}[model]
		j.model.BLE.NamePatterns = map[string][]string{"ct230b": {"ct-?230b"}, "ct320b": {"ct-?320b"}}[model]
		return j, &Job{Raster: raster, Pages: pages}
	}
	t.Fatalf("no fixture %s/%s", model, stock)
	return goldenJob{}, nil
}

func sendWith(t *testing.T, f *fakeClabel, link printer.Link, model, stock string, extra printer.Settings, jobs int) (printer.Result, error, []*Job) {
	t.Helper()
	f.t = t
	if fl, ok := link.(*fake.Link); ok {
		fl.Responder = f.respond
	}
	j, job := fixtureJob(t, model, stock)
	list := make([]*Job, jobs)
	for i := range list {
		list[i] = job
	}
	pc := &printer.PrintContext{Model: j.model, Stock: j.stock, Link: link, Settings: printer.Merge(j.settings, extra)}
	res, err := fastDriver().SendJobs(context.Background(), pc, list)
	return res, err, list
}

func streamOf(jobs []*Job) []byte {
	var b []byte
	for _, j := range jobs {
		for _, p := range j.Pages {
			b = append(b, p.Data...)
		}
	}
	return b
}

func TestCT320BHappyPath(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(0)},
		identity: []byte("CT-320B\x00V1.0.2")}
	link := fake.New(244)
	res, err, jobs := sendWith(t, f, link, "ct320b", "50x30", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Printed != 1 || res.Message != "Sent 1 label to Clabel CT-320B." {
		t.Fatalf("result %+v", res)
	}
	if res.Details["firmware"] != "V1.0.2" || res.Details["answers_settings"] != true {
		t.Errorf("details %+v", res.Details)
	}
	if !bytes.Equal(f.stream.Bytes(), streamOf(jobs)) {
		t.Fatal("streamed bytes differ from the encoded page")
	}
	writes := link.Writes()
	if !bytes.Equal(writes[0].Data, IdentityQuery) || !bytes.Equal(writes[1].Data, ReadSetting(SettingLanguage)) ||
		!bytes.Equal(writes[2].Data, ReadSetting(SettingPaperType)) {
		t.Errorf("control sequence %x %x %x", writes[0].Data, writes[1].Data, writes[2].Data)
	}
	for _, w := range writes[3:] {
		if w.WithResponse || len(w.Data) > 238 {
			t.Fatalf("stream write of %d bytes (with response %v)", len(w.Data), w.WithResponse)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("settings written: %+v", f.writes)
	}
}

func TestCT230BUses200ByteWritesAndNeverWritesSettings(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(1), SettingPaperType: ptr(1)}}
	link := fake.New(244)
	if _, err, _ := sendWith(t, f, link, "ct230b", "50x30", nil, 1); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 || f.controls != 3 {
		t.Fatalf("CT-230B wrote settings %+v (%d control frames)", f.writes, f.controls)
	}
	sizes := map[int]int{}
	for _, w := range link.Writes() {
		if !w.WithResponse {
			sizes[len(w.Data)]++
		}
	}
	for size := range sizes {
		if size > 200 {
			t.Fatalf("stream write of %d bytes on a CT-230B", size)
		}
	}
	if sizes[200] == 0 {
		t.Errorf("no full 200-byte stream writes: %v", sizes)
	}
}

func TestMutePrinterStillPrintsWithAHint(t *testing.T) {
	f := &fakeClabel{silent: true}
	hint := catalogHint(t, "clabel-ct230b")
	res, err, jobs := sendWith(t, f, fake.New(244), "ct230b", "40x30", printer.Settings{"setup_hint": hint}, 1)
	if err != nil || !res.OK {
		t.Fatalf("res %+v, err %v", res, err)
	}
	if hint == "" || !strings.HasSuffix(res.Message, "did not report its settings. If nothing printed: "+hint) {
		t.Errorf("message %q, hint %q", res.Message, hint)
	}
	if !bytes.Equal(f.stream.Bytes(), streamOf(jobs)) {
		t.Error("the page was not streamed")
	}
	if f.controls != 2 {
		t.Errorf("%d control frames, want identity + one language read", f.controls)
	}
}

func TestCT320BSwitchesToTSPLAndGap(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(1), SettingPaperType: ptr(1)}, sticky: true}
	link := fake.New(244)
	if _, err, _ := sendWith(t, f, link, "ct320b", "50x30", nil, 1); err != nil {
		t.Fatal(err)
	}
	want := []RawFrame{{5, byte(SettingLanguage), []byte{0}}, {5, byte(SettingPaperType), []byte{0}}}
	if len(f.writes) != 2 || f.writes[0].Sub != want[0].Sub || f.writes[1].Sub != want[1].Sub ||
		f.writes[0].Content[0] != 0 || f.writes[1].Content[0] != 0 {
		t.Fatalf("setting writes %+v, want language=0 then paper=0", f.writes)
	}
	// The printer sees every read first, then each fix and its read-back.
	sequence := [][]byte{IdentityQuery, ReadSetting(SettingLanguage), ReadSetting(SettingPaperType),
		WriteSetting(SettingLanguage, 0), ReadSetting(SettingLanguage),
		WriteSetting(SettingPaperType, 0), ReadSetting(SettingPaperType)}
	var controls [][]byte
	for _, w := range link.Writes() {
		if w.WithResponse {
			controls = append(controls, w.Data)
		}
	}
	if !reflect.DeepEqual(controls, sequence) {
		t.Errorf("control frames %x, want %x", controls, sequence)
	}
}

func TestReadBackIgnoresEchoesReceivedWhileSettling(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(1)}, sticky: true, echo: true}
	if _, err, _ := sendWith(t, f, fake.New(244), "ct320b", "50x30", nil, 1); err != nil {
		t.Fatal(err)
	}
}

func TestCT320BSettingsThatDoNotStick(t *testing.T) {
	for _, tc := range []struct {
		values map[Setting]*byte
		words  string
	}{
		{map[Setting]*byte{SettingLanguage: ptr(2), SettingPaperType: ptr(0)}, "stayed out of TSPL mode"},
		{map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(1)}, "kept its paper type after being asked for gap"},
	} {
		_, err, _ := sendWith(t, &fakeClabel{values: tc.values}, fake.New(244), "ct320b", "50x30", nil, 1)
		var perr *printer.Error
		if !errors.As(err, &perr) || !strings.Contains(perr.Msg, tc.words) {
			t.Errorf("err = %v, want %q", err, tc.words)
		}
	}
}

func TestUnavailableSettingsAreAcceptedButNotAsReadBack(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: nil, SettingPaperType: nil}}
	if res, err, _ := sendWith(t, f, fake.New(244), "ct320b", "50x30", nil, 1); err != nil || res.Details["answers_settings"] != true {
		t.Fatalf("res %+v, err %v", res, err)
	}
	// Paper 1 on a gap job: the write is read back as "unavailable", which fails.
	f = &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(1)}}
	f.t = t
	link := fake.New(244)
	link.Responder = func(w fake.Write) [][]byte {
		out := f.respond(w)
		if w.WithResponse && len(f.writes) > 0 {
			f.values[SettingPaperType] = nil
		}
		return out
	}
	j, job := fixtureJob(t, "ct320b", "50x30")
	_, err := fastDriver().SendJobs(context.Background(),
		&printer.PrintContext{Model: j.model, Stock: j.stock, Link: link, Settings: j.settings}, []*Job{job})
	if err == nil || !strings.Contains(err.Error(), "asked for gap") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusBitsAreRecorded(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(0)}}
	f.t = t
	f.extra = [][]byte{f.frame(1, 0x81, 0x05)}
	res, err, _ := sendWith(t, f, fake.New(244), "ct320b", "50x30", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := res.Details["status"].([]string); len(got) != 2 || got[0] != "lid-open" || got[1] != "no-paper" {
		t.Errorf("status %v", res.Details["status"])
	}
}

// failingLink fails stream writes after okStream of them.
type failingLink struct {
	*fake.Link
	okStream int
}

func (l *failingLink) Write(ctx context.Context, data []byte, role string, withResponse bool) error {
	if !withResponse {
		if l.okStream == 0 {
			return errors.New("not connected")
		}
		l.okStream--
	}
	return l.Link.Write(ctx, data, role, withResponse)
}

func TestStreamFailureIsAUserError(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(0)}}
	f.t = t
	inner := fake.New(244)
	inner.Responder = f.respond
	// Two pages of a two-label job: the first page goes through, the second fails.
	j, job := fixtureJob(t, "ct320b", "50x30")
	writes := (len(job.Pages[0].Data) + 237) / 238
	link := &failingLink{Link: inner, okStream: writes}
	res, err := fastDriver().SendJobs(context.Background(),
		&printer.PrintContext{Model: j.model, Stock: j.stock, Link: link, Settings: j.settings}, []*Job{job, job})
	var perr *printer.Error
	if !errors.As(err, &perr) || perr.Msg != "Sending to the printer failed partway through. Bring the computer closer to it and print again. 1 of 2 pages had already gone through." {
		t.Fatalf("err = %v", err)
	}
	if res.Printed != 1 {
		t.Errorf("Printed = %d, want 1", res.Printed)
	}
}

func TestPagesArePacedByPrintTime(t *testing.T) {
	f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(0)}, t: t}
	link := fake.New(244)
	link.Responder = f.respond
	j, job := fixtureJob(t, "ct320b", "50x30")
	pacing := fastPacing
	pacing.PageBase, pacing.PagePerRow = 20*time.Millisecond, 100*time.Microsecond // 44 ms per 240-row page
	var at []time.Duration
	start := time.Now()
	pc := &printer.PrintContext{Model: j.model, Stock: j.stock, Link: link, Settings: j.settings,
		Progress: func(float64, string) { at = append(at, time.Since(start)) }}
	if _, err := New(WithPacing(pacing), WithWaits(fastWaits)).SendJobs(context.Background(), pc, []*Job{job, job}); err != nil {
		t.Fatal(err)
	}
	if len(at) != 2 || at[1]-at[0] < pacing.PrintTime(240) {
		t.Fatalf("pages reported at %v; want them at least %v apart", at, pacing.PrintTime(240))
	}
	if total := time.Since(start); total < 2*pacing.PrintTime(240) {
		t.Errorf("job took %v; the print time after the last page was not waited", total)
	}
}

func TestBLEGapOverrideReachesTheChunker(t *testing.T) {
	f := &fakeClabel{silent: true}
	start := time.Now()
	// a short page, but still several 20-byte writes
	if _, err, _ := sendWith(t, f, fake.New(20), "ct320b", "50x30", printer.Settings{"ble_gap_ms": 2.0}, 1); err != nil {
		t.Fatal(err)
	}
	writes := (f.stream.Len() + 19) / 20
	if least := time.Duration(writes-1) * 2 * time.Millisecond; time.Since(start) < least {
		t.Fatalf("%d writes took %v, want at least %v", writes, time.Since(start), least)
	}
}

func TestNothingToPrint(t *testing.T) {
	_, err := fastDriver().SendJobs(context.Background(), &printer.PrintContext{Link: fake.New(20)}, nil)
	if err == nil || err.Error() != "There are no labels to print." {
		t.Fatalf("err = %v", err)
	}
}

func catalogHint(t *testing.T, id string) string {
	t.Helper()
	m, err := catalog(t).Model(id)
	if err != nil {
		t.Fatal(err)
	}
	return m.Options.String("setup_hint", "")
}

// TestIdentityNamesTheModel checks the model the printer reports against the catalog's
// name_patterns, and against the configured model's patterns without a catalog.
func TestIdentityNamesTheModel(t *testing.T) {
	c := catalog(t)
	for _, tc := range []struct {
		identity string
		catalog  bool
		reported any // the "reported_model" detail, or nil when there is none
	}{
		{"CT-230B\x00V1.0.2", true, "clabel-ct230b"},
		{"vendor\x00ct230b_7", true, "clabel-ct230b"},
		{"CT320B\x00V1.0.2", true, nil}, // the configured CT-320B
		{"D30 CT-230B", true, "clabel-ct230b"},
		{"FM226\x00V3", true, nil}, // a match, but not a Clabel model
		{"CT-230B\x00V1.0.2", false, nil},
		{"Printer", true, nil},
	} {
		f := &fakeClabel{values: map[Setting]*byte{SettingLanguage: ptr(0), SettingPaperType: ptr(0)},
			identity: []byte(tc.identity)}
		f.t = t
		link := fake.New(244)
		link.Responder = f.respond
		j, job := fixtureJob(t, "ct320b", "50x30")
		pc := &printer.PrintContext{Model: j.model, Stock: j.stock, Link: link, Settings: j.settings}
		if tc.catalog {
			pc.Catalog = c
		}
		res, err := fastDriver().SendJobs(context.Background(), pc, []*Job{job})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Details["reported_model"]; got != tc.reported {
			t.Errorf("%q (catalog %v): reported_model %v, want %v", tc.identity, tc.catalog, got, tc.reported)
		}
	}
	m, _ := c.Model("clabel-ct320b")
	if got := reportedModel(&printer.PrintContext{Model: m}, []string{"x", "ct-320b"}); got != m {
		t.Errorf("without a catalog, the configured model's pattern did not match: %v", got)
	}
}

// TestMuteControlSequence: a printer that leaves the first read unanswered gets only the
// identity query and that read.
func TestMuteControlSequence(t *testing.T) {
	link := fake.New(244)
	if _, err, _ := sendWith(t, &fakeClabel{silent: true}, link, "ct320b", "50x30", nil, 1); err != nil {
		t.Fatal(err)
	}
	var controls [][]byte
	for _, w := range link.Writes() {
		if w.WithResponse {
			controls = append(controls, w.Data)
		}
	}
	if !reflect.DeepEqual(controls, [][]byte{IdentityQuery, ReadSetting(SettingLanguage)}) {
		t.Errorf("control frames %x", controls)
	}
}
