package munbyn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/fake"
)

var (
	fastPacing = Pacing{PackageGap: 0, PaperSettle: time.Millisecond, ChunkBytes: 128}
	fastWaits  = Waits{Answer: 300 * time.Millisecond, Section: 300 * time.Millisecond, Finish: 300 * time.Millisecond,
		Quiet: time.Millisecond}
)

func fastDriver() *Driver { return New(WithPacing(fastPacing), WithWaits(fastWaits)) }

func mustFrame(t *testing.T, payload []byte) []byte {
	t.Helper()
	f, err := Wrap(payload)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func reportFrame(t *testing.T, kind uint32, status string) []byte {
	var nested, outer Builder
	nested.Uint(1, kind).Blob(2, []byte(status))
	return mustFrame(t, outer.Uint(1, uint32(EventDeviceReport)).Blob(3, nested.Payload()).Payload())
}

func replyFrame(t *testing.T, event Event, code uint32, nested []byte) []byte {
	var b Builder
	return mustFrame(t, b.Uint(1, uint32(event)).Uint(2, code).Blob(3, nested).Payload())
}

// fakeFM226 answers like the printer: device info, paper type, one ack per section once
// its last package has been reassembled from the chunked image writes, and label-done
// after PRINT_END.
type fakeFM226 struct {
	t           *testing.T
	paperType   uint32
	refusePaper bool
	paperReply  [][]byte             // non-nil: the answer to PAPER_TYPE_SET
	silent      bool                 // never answers DEVICE_INFO
	onSection   func(n int) [][]byte // nil: acknowledge the section
	endReplies  [][]byte             // nil: label-done with state 0; empty non-nil: no answer
	before      [][]byte             // extra frames queued before the DEVICE_INFO answer

	mu        sync.Mutex
	frames    Reassembler
	sections  int
	infoCount int
	paperSets []PaperType
}

func (f *fakeFM226) respond(w fake.Write) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch w.Role {
	case roleCommand:
		if !w.WithResponse {
			f.t.Errorf("command written without response")
		}
		var r Reassembler
		payloads := r.Push(w.Data)
		if len(payloads) != 1 {
			f.t.Errorf("command write is not one whole frame: %x", w.Data)
			return nil
		}
		fields := DecodeFields(payloads[0])
		switch Event(fields[1].Num) {
		case EventDeviceInfo:
			f.infoCount++
			if f.silent {
				return nil
			}
			var nested Builder
			nested.Blob(3, []byte("1.0.0")).Uint(5, 80).Uint(10, f.paperType)
			return append(append([][]byte(nil), f.before...), replyFrame(f.t, EventDeviceInfo, codeOK, nested.Payload()))
		case EventPaperTypeSet:
			p := PaperType(int32(fields[4].Num) >> 1)
			f.paperSets = append(f.paperSets, p)
			if f.paperReply != nil {
				return f.paperReply
			}
			if f.refusePaper {
				return [][]byte{replyFrame(f.t, EventPaperTypeSet, 500, nil)}
			}
			f.paperType = uint32(p)
			return [][]byte{replyFrame(f.t, EventPaperTypeSet, codeOK, nil)}
		case EventPrintEnd:
			if f.endReplies != nil {
				return f.endReplies
			}
			return [][]byte{reportFrame(f.t, 10, "0")}
		}
	case roleImage:
		if w.WithResponse {
			f.t.Errorf("image chunk written with response")
		}
		var out [][]byte
		for _, payload := range f.frames.Push(w.Data) {
			outer := DecodeFields(payload)
			body := DecodeFields(outer[5].Bytes)
			if Event(outer[1].Num) != EventDevicePrint || body[5].Num != body[4].Num {
				continue
			}
			f.sections++
			if f.onSection != nil {
				out = append(out, f.onSection(f.sections)...)
			} else {
				out = append(out, reportFrame(f.t, 11, "2"))
			}
		}
		return out
	default:
		f.t.Errorf("unexpected role %q", w.Role)
	}
	return nil
}

func testModel() *printer.Model {
	return &printer.Model{ID: "munbyn-fm226", Label: "Munbyn FM226", Family: Family, Link: printer.LinkBLE,
		DPI: 203.2, HeadDots: 384, Options: printer.Settings{},
		BLE: &printer.BLEProfile{ChunkBytes: 128, ChunkGapMS: 0}}
}

func rasterJob(t *testing.T, name string) (*Job, []byte) {
	t.Helper()
	var m jobManifest
	if err := json.Unmarshal(readGolden(t, "jobs", name, "manifest.json"), &m); err != nil {
		t.Fatal(err)
	}
	job, err := JobFromRaster(PaperGap, readGolden(t, "jobs", name, "raster.bin"), m.WidthDots)
	if err != nil {
		t.Fatal(err)
	}
	return job, readGolden(t, "jobs", name, "all-packages.bin")
}

func run(t *testing.T, printerFake *fakeFM226, link *fake.Link, jobs []*Job, settings printer.Settings) (printer.Result, error, *printer.PrintContext) {
	t.Helper()
	printerFake.t = t
	link.Responder = printerFake.respond
	pc := &printer.PrintContext{Model: testModel(), Stock: &printer.Stock{ID: "50x30", Name: "50x30"},
		Link: link, Settings: settings}
	res, err := fastDriver().SendJobs(context.Background(), pc, jobs)
	return res, err, pc
}

func TestHappyPathConversation(t *testing.T) {
	job, all := rasterJob(t, "50x30")
	link := fake.New(128)
	f := &fakeFM226{paperType: 1}
	res, err, _ := run(t, f, link, []*Job{job}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Printed != 1 || !strings.Contains(res.Message, "Sent 1 label to Munbyn FM226.") {
		t.Fatalf("result %+v", res)
	}
	if res.Details["firmware"] != "1.0.0" || res.Details["battery"] != int32(80) {
		t.Errorf("details %+v", res.Details)
	}
	if got := link.Sent(roleImage); !bytes.Equal(got, all) {
		t.Fatalf("image bytes differ from all-packages.bin (%d vs %d bytes)", len(got), len(all))
	}
	writes := link.Writes()
	if !bytes.Equal(writes[0].Data, DeviceInfoCommand) || !bytes.Equal(writes[len(writes)-1].Data, PrintEndCommand) {
		t.Errorf("conversation starts %x and ends %x", writes[0].Data, writes[len(writes)-1].Data)
	}
	for _, w := range writes {
		if w.Role == roleImage && len(w.Data) > 128 {
			t.Fatalf("image write of %d bytes exceeds the 128-byte chunk", len(w.Data))
		}
	}
	if f.sections != 3 || len(f.paperSets) != 0 {
		t.Errorf("printer saw %d sections and paper sets %v", f.sections, f.paperSets)
	}
}

// TestTinyJobOverTwentyByteLink sends the smallest job (one 33-byte package) over a
// 20-byte link: two writes.
func TestTinyJobOverTwentyByteLink(t *testing.T) {
	job, all := rasterJob(t, "worked-8x2")
	link := fake.New(20)
	if _, err, _ := run(t, &fakeFM226{paperType: 1}, link, []*Job{job}, nil); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, w := range link.Writes() {
		if w.Role == roleImage {
			sizes = append(sizes, len(w.Data))
		}
	}
	if len(sizes) != 2 || sizes[0] != 20 || sizes[1] != 13 || !bytes.Equal(link.Sent(roleImage), all) {
		t.Fatalf("image writes %v, want [20 13]", sizes)
	}
}

func TestPaperTypeIsSetWhenItDiffers(t *testing.T) {
	job, _ := rasterJob(t, "40x30")
	f := &fakeFM226{paperType: uint32(PaperContinuous)}
	link := fake.New(128)
	if _, err, _ := run(t, f, link, []*Job{job}, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.paperSets) != 1 || f.paperSets[0] != PaperGap {
		t.Fatalf("paper sets %v, want [gap]", f.paperSets)
	}
	if w := link.Writes()[1]; !bytes.Equal(w.Data, PaperTypeSetCommand(PaperGap)) || w.Role != roleCommand {
		t.Errorf("second write %+v, want PAPER_TYPE_SET gap", w)
	}
}

func TestSettleWaitAfterPaperChange(t *testing.T) {
	job, _ := rasterJob(t, "worked-8x2")
	f := &fakeFM226{paperType: uint32(PaperContinuous), t: t}
	link := fake.New(128)
	link.Responder = f.respond
	pacing := fastPacing
	pacing.PaperSettle = 80 * time.Millisecond
	pc := &printer.PrintContext{Model: testModel(), Link: link}
	start := time.Now()
	if _, err := New(WithPacing(pacing), WithWaits(fastWaits)).SendJobs(context.Background(), pc, []*Job{job}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < pacing.PaperSettle {
		t.Fatalf("job took %v, shorter than the %v settle wait", elapsed, pacing.PaperSettle)
	}
}

func TestFailuresAreUserErrors(t *testing.T) {
	tests := []struct {
		name  string
		f     *fakeFM226
		words string
	}{
		{"silent", &fakeFM226{paperType: 1, silent: true}, "did not answer within 0.3 s"},
		{"paper refused", &fakeFM226{paperType: 2, refusePaper: true}, "did not take the gap paper setting"},
		// The handshake waits for its own answer: a problem report there is noise.
		{"problem report while setting paper", &fakeFM226{paperType: 2, paperReply: [][]byte{reportFrame(t, 10, "8")}},
			"did not take the gap paper setting"},
		{"refused", &fakeFM226{paperType: 1, onSection: func(int) [][]byte {
			return [][]byte{replyFrame(t, EventDevicePrint, 500, nil)}
		}}, "refused the label data (code 500)"},
		{"no labels", &fakeFM226{paperType: 1, onSection: func(int) [][]byte {
			return [][]byte{reportFrame(t, 10, "2")}
		}}, "no labels left"},
		{"page done 0 mid-section", &fakeFM226{paperType: 1, onSection: func(int) [][]byte {
			return [][]byte{reportFrame(t, 10, "0")}
		}}, "ended the label after part 1 of 3"},
		{"no section ack", &fakeFM226{paperType: 1, onSection: func(n int) [][]byte {
			if n == 2 {
				return nil
			}
			return [][]byte{reportFrame(t, 11, "2")}
		}}, "No word from the printer after part 2 of 3"},
		{"lid open at the end", &fakeFM226{paperType: 1, endReplies: [][]byte{reportFrame(t, 10, "8")}}, "lid is open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, _ := rasterJob(t, "50x30")
			res, err, _ := run(t, tt.f, fake.New(128), []*Job{job}, nil)
			var perr *printer.Error
			if !errors.As(err, &perr) || !strings.Contains(perr.Msg, tt.words) {
				t.Fatalf("err = %v, want a printer.Error with %q", err, tt.words)
			}
			if res.OK || res.Printed != 0 {
				t.Errorf("result %+v after a failure", res)
			}
		})
	}
}

func TestNoEndReplyStillCountsAsSent(t *testing.T) {
	job, _ := rasterJob(t, "worked-8x2")
	for name, end := range map[string][][]byte{
		"silence":                  {},
		"only a refusal":           {replyFrame(t, EventDevicePrint, 503, nil)}, // PRINT_END waits for label-done only
		"a report of another kind": {reportFrame(t, 11, "2")},
	} {
		res, err, _ := run(t, &fakeFM226{paperType: 1, endReplies: end}, fake.New(128), []*Job{job}, nil)
		if err != nil || !res.OK || !strings.Contains(res.Message, "did not confirm 1 of them") {
			t.Fatalf("%s: res %+v, err %v", name, res, err)
		}
		if c := res.Details["confirmed"].([]bool); len(c) != 1 || c[0] {
			t.Errorf("%s: confirmed = %v", name, c)
		}
	}
}

func TestUnrelatedRepliesAreDiscarded(t *testing.T) {
	job, _ := rasterJob(t, "worked-8x2")
	f := &fakeFM226{paperType: 1, before: [][]byte{
		reportFrame(t, 11, "2"), // a stray section ack before the job: must not count later
		reportFrame(t, 12, "0"),
		{0x00, 0x13}, // noise
	}}
	f.onSection = func(int) [][]byte { return [][]byte{reportFrame(t, 11, "3"), reportFrame(t, 11, "2")} }
	if _, err, _ := run(t, f, fake.New(128), []*Job{job}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStaleRepliesAreClearedBeforeAJob(t *testing.T) {
	job, _ := rasterJob(t, "worked-8x2")
	// A queued label-done report with state 2 from an earlier job must not fail this one.
	link := fake.New(128, reportFrame(t, 10, "2"), reportFrame(t, 11, "2"))
	if _, err, _ := run(t, &fakeFM226{paperType: 1}, link, []*Job{job}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTwoLabelsRunTwoJobs(t *testing.T) {
	a, _ := rasterJob(t, "worked-8x2")
	b, _ := rasterJob(t, "50x30-blank")
	f := &fakeFM226{paperType: 1}
	res, err, _ := run(t, f, fake.New(128), []*Job{a, b}, nil)
	if err != nil || res.Printed != 2 || !strings.Contains(res.Message, "Sent 2 labels") {
		t.Fatalf("res %+v, err %v", res, err)
	}
	if f.infoCount != 2 || f.sections != 4 {
		t.Errorf("printer saw %d DEVICE_INFO and %d sections", f.infoCount, f.sections)
	}
}

func TestSecondLabelFailureNamesTheLabel(t *testing.T) {
	a, _ := rasterJob(t, "worked-8x2")
	f := &fakeFM226{paperType: 1}
	f.onSection = func(n int) [][]byte {
		if n == 2 {
			return [][]byte{reportFrame(t, 10, "8")}
		}
		return [][]byte{reportFrame(t, 11, "2")}
	}
	res, err, _ := run(t, f, fake.New(128), []*Job{a, a}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "Label 2 of 2: The printer's lid is open") {
		t.Fatalf("err = %v", err)
	}
	if res.Printed != 1 {
		t.Errorf("Printed = %d, want 1", res.Printed)
	}
}

// failingLink fails image writes after a number of them.
type failingLink struct {
	*fake.Link
	okImages int
}

func (l *failingLink) Write(ctx context.Context, data []byte, role string, withResponse bool) error {
	if role == roleImage {
		if l.okImages == 0 {
			return errors.New("GATT write failed")
		}
		l.okImages--
	}
	return l.Link.Write(ctx, data, role, withResponse)
}

func TestWriteFailureIsAUserError(t *testing.T) {
	job, _ := rasterJob(t, "50x30")
	f := &fakeFM226{paperType: 1, t: t}
	link := fake.New(128)
	link.Responder = f.respond
	pc := &printer.PrintContext{Model: testModel(), Link: &failingLink{Link: link, okImages: 5}}
	_, err := fastDriver().SendJobs(context.Background(), pc, []*Job{job})
	var perr *printer.Error
	if !errors.As(err, &perr) || perr.Msg != msgDropped {
		t.Fatalf("err = %v", err)
	}
}

func TestProgressReachesOne(t *testing.T) {
	job, _ := rasterJob(t, "50x30")
	f := &fakeFM226{paperType: 1, t: t}
	link := fake.New(128)
	link.Responder = f.respond
	var fractions []float64
	pc := &printer.PrintContext{Model: testModel(), Link: link,
		Progress: func(fr float64, _ string) { fractions = append(fractions, fr) }}
	if _, err := fastDriver().SendJobs(context.Background(), pc, []*Job{job, job}); err != nil {
		t.Fatal(err)
	}
	if len(fractions) != 6 || fractions[len(fractions)-1] != 1 {
		t.Fatalf("progress %v", fractions)
	}
	for i := 1; i < len(fractions); i++ {
		if fractions[i] <= fractions[i-1] {
			t.Fatalf("progress not increasing: %v", fractions)
		}
	}
}

func TestBLEGapOverrideSpacesChunks(t *testing.T) {
	job, _ := rasterJob(t, "worked-8x2")
	link := fake.New(20)
	start := time.Now()
	if _, err, _ := run(t, &fakeFM226{paperType: 1}, link, []*Job{job}, printer.Settings{"ble_gap_ms": 60.0}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 60*time.Millisecond {
		t.Fatal("ble_gap_ms did not space the two image chunks")
	}
	if _, err, _ := run(t, &fakeFM226{paperType: 1}, fake.New(20), []*Job{job}, printer.Settings{"ble_gap_ms": 5000.0}); err == nil {
		t.Fatal("an out-of-range ble_gap_ms was accepted")
	}
}

func TestCancelledContextStopsTheJob(t *testing.T) {
	job, _ := rasterJob(t, "50x30")
	f := &fakeFM226{paperType: 1, silent: true, t: t}
	link := fake.New(128)
	link.Responder = f.respond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	waits := fastWaits
	waits.Answer = 5 * time.Second
	_, err := New(WithPacing(fastPacing), WithWaits(waits)).SendJobs(ctx, &printer.PrintContext{Model: testModel(), Link: link}, []*Job{job})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's error", err)
	}
}

// TestPacingIsTheKnownGoodTable pins the timing the printer sees. A change needs a test
// on the printer first.
func TestPacingIsTheKnownGoodTable(t *testing.T) {
	if FM226Pacing != (Pacing{PackageGap: 6 * time.Millisecond, PaperSettle: 3 * time.Second, ChunkBytes: 128, ChunkGapMS: 0}) {
		t.Fatalf("FM226Pacing = %+v", FM226Pacing)
	}
	d := New()
	if d.pacing != FM226Pacing || d.waits != DefaultWaits {
		t.Fatalf("New() uses %+v and %+v", d.pacing, d.waits)
	}
}

// TestCommandSequence pins what the printer is sent for one label on the right paper:
// DEVICE_INFO, the packages, then PRINT_END, and nothing else on the command role.
func TestCommandSequence(t *testing.T) {
	job, _ := rasterJob(t, "50x30")
	link := fake.New(128)
	if _, err, _ := run(t, &fakeFM226{paperType: 1}, link, []*Job{job}, nil); err != nil {
		t.Fatal(err)
	}
	var commands [][]byte
	for _, w := range link.Writes() {
		if w.Role == roleCommand {
			commands = append(commands, w.Data)
		}
	}
	if len(commands) != 2 || !bytes.Equal(commands[0], DeviceInfoCommand) || !bytes.Equal(commands[1], PrintEndCommand) {
		t.Fatalf("commands %x", commands)
	}
}
