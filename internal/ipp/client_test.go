package ipp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is an httptest IPP server that answers with reply and records requests.
type recorder struct {
	mu    sync.Mutex
	reqs  []*Message
	at    []time.Time
	hosts []string
	types []string
	paths []string
	reply func(*Message) (*Message, int)
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	m, err := Decode(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, m)
	r.at = append(r.at, time.Now())
	r.hosts = append(r.hosts, req.Host)
	r.types = append(r.types, req.Header.Get("Content-Type"))
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	resp, code := r.reply(m)
	if code != 0 {
		w.WriteHeader(code)
		return
	}
	resp.RequestID = m.RequestID
	out, err := Encode(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ContentType)
	_, _ = w.Write(out)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func opsGroup() Group {
	return Group{Tag: TagOperationGroup, Attributes: []Attribute{
		Charset("attributes-charset", "utf-8"), Language("attributes-natural-language", "en"),
	}}
}

func serve(t *testing.T, reply func(*Message) (*Message, int)) (*recorder, Endpoint) {
	t.Helper()
	rec := &recorder{reply: reply}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	ep, err := ParseEndpoint(strings.Replace(srv.URL, "http://", "ipp://", 1) + "/ipp/print")
	if err != nil {
		t.Fatal(err)
	}
	return rec, ep
}

// jobReply answers Get-Job-Attributes with one state, impressions and reasons.
func jobReply(state, impressions int, reasons ...string) *Message {
	attrs := []Attribute{Enum("job-state", state), Integer("job-impressions-completed", impressions)}
	if len(reasons) > 0 {
		attrs = append(attrs, Keyword("job-state-reasons", reasons...))
	}
	return NewMessage(0, 0, opsGroup(), Group{Tag: TagJobGroup, Attributes: attrs})
}

// step is one scripted answer to Get-Job-Attributes: an HTTP status when code != 0,
// else msg.
type step struct {
	code int
	msg  *Message
}

// scripted answers Get-Job-Attributes with steps in turn (the last repeats) and every
// other operation with success.
func scripted(t *testing.T, steps ...step) (*recorder, Endpoint) {
	t.Helper()
	var mu sync.Mutex
	return serve(t, func(m *Message) (*Message, int) {
		mu.Lock()
		defer mu.Unlock()
		if m.Code != OpGetJobAttributes {
			return NewMessage(0, 0, opsGroup()), 0
		}
		s := steps[0]
		if len(steps) > 1 {
			steps = steps[1:]
		}
		return s.msg, s.code
	})
}

func TestClientOperations(t *testing.T) {
	gpa := readFile(t, brotherGPA)
	var mu sync.Mutex
	polls := []int{JobProcessing, JobCompleted}
	rec, ep := serve(t, func(m *Message) (*Message, int) {
		switch m.Code {
		case OpGetPrinterAttributes:
			r, _ := Decode(gpa)
			return r, 0
		case OpValidateJob:
			return NewMessage(StatusOKIgnoredOrSubstituted, 0, opsGroup(),
				Group{Tag: TagUnsupportedGroup, Attributes: []Attribute{Keyword("print-scaling", "none")}}), 0
		case OpPrintJob:
			return NewMessage(0, 0, opsGroup(), Group{Tag: TagJobGroup, Attributes: []Attribute{Integer("job-id", 42), Enum("job-state", 3)}}), 0
		case OpGetJobAttributes:
			mu.Lock()
			defer mu.Unlock()
			st := polls[0]
			if len(polls) > 1 {
				polls = polls[1:]
			}
			return jobReply(st, 1, "none"), 0
		}
		return NewMessage(0x0501, 0, opsGroup()), 0
	})
	ctx := context.Background()
	c := NewClient(ep, true)
	p, err := c.GetPrinterAttributes(ctx)
	if err != nil || p.URFResolution(300) != 600 {
		t.Fatalf("GPA: %v %v", p, err)
	}
	job := JobRequest{Name: "crate-labels", DocumentFormat: MimeURF, JobAttributes: Ticket{Media: ChooseMedia(p, [2]float64{215.9, 279.4}, true)}.Attributes(p)}
	v, err := c.ValidateJob(ctx, job)
	if err != nil || v.Code != StatusOKIgnoredOrSubstituted || !slices.Equal(UnsupportedAttributes(v), []string{"print-scaling"}) {
		t.Fatalf("validate: %v %v", v, err)
	}
	id, err := c.PrintJob(ctx, job, []byte("UNIRAST\x00"))
	if err != nil || id != 42 {
		t.Fatalf("print: %d %v", id, err)
	}
	var seen []int
	st, err := c.Watch(ctx, id, Watch{Every: time.Millisecond, Patience: time.Minute, OnStatus: func(s JobStatus) { seen = append(seen, s.State) }})
	if err != nil || st.State != JobCompleted || !slices.Equal(seen, []int{JobProcessing, JobCompleted}) || st.ImpressionsCompleted != 1 || st.Gone {
		t.Fatalf("watch: %+v %v %v", st, seen, err)
	}
	var ids []int
	for _, r := range rec.reqs {
		ids = append(ids, r.RequestID)
	}
	if !slices.Equal(ids, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("request ids %v", ids)
	}
	for i := range rec.reqs {
		if rec.hosts[i] != ep.Authority() || rec.types[i] != ContentType || rec.paths[i] != "/ipp/print" {
			t.Fatalf("request %d: host %q type %q path %q", i, rec.hosts[i], rec.types[i], rec.paths[i])
		}
	}
	if len(rec.reqs[1].Data) != 0 || string(rec.reqs[2].Data) != "UNIRAST\x00" {
		t.Fatal("documents")
	}
}

func TestPrintJobIsGuarded(t *testing.T) {
	rec, ep := serve(t, func(*Message) (*Message, int) { return nil, 500 })
	_, err := NewClient(ep, false).PrintJob(context.Background(), JobRequest{Name: "x", DocumentFormat: MimeURF}, []byte("UNIRAST\x00"))
	if !errors.Is(err, ErrPrintingDisabled) || rec.count() != 0 {
		t.Fatalf("err %v, %d requests", err, rec.count())
	}
}

func TestPrintJobWithoutJobID(t *testing.T) {
	_, ep := serve(t, func(*Message) (*Message, int) { return NewMessage(0, 0, opsGroup()), 0 })
	var te *TransportError
	if _, err := NewClient(ep, true).PrintJob(context.Background(), JobRequest{Name: "x", DocumentFormat: MimeURF}, nil); !errors.As(err, &te) || !strings.Contains(te.Msg, "no job-id") {
		t.Fatalf("err %v", err)
	}
}

func TestHTTPAndIPPErrors(t *testing.T) {
	_, ep := serve(t, func(m *Message) (*Message, int) {
		if m.Code == OpGetPrinterAttributes {
			return nil, http.StatusServiceUnavailable
		}
		g := opsGroup()
		g.Attributes = append(g.Attributes, Attr("status-message", TagText, "busy now"))
		return NewMessage(0x0507, 0, g), 0
	})
	c := NewClient(ep, true)
	ctx := context.Background()
	var te *TransportError
	if _, err := c.GetPrinterAttributes(ctx); !errors.As(err, &te) || te.HTTPStatus != 503 || !strings.Contains(te.Msg, "HTTP status 503") || !Retryable(err) {
		t.Fatalf("GPA err %v", err)
	}
	var se *StatusError
	if _, err := c.ValidateJob(ctx, JobRequest{Name: "x", DocumentFormat: MimeURF}); !errors.As(err, &se) || se.Status != 0x0507 || se.Message != "busy now" ||
		!strings.Contains(err.Error(), "server-error-busy") {
		t.Fatalf("validate err %v", err)
	}
	if _, err := c.PrinterStatus(ctx); err == nil {
		t.Fatal("PrinterStatus hid the error")
	}
}

func TestPrinterStatusAsksForStateOnly(t *testing.T) {
	rec, ep := serve(t, func(*Message) (*Message, int) {
		return NewMessage(0, 0, opsGroup(), Group{Tag: TagPrinterGroup, Attributes: []Attribute{
			Enum("printer-state", PrinterStopped), Keyword("printer-state-reasons", "media-jam"), Attr("printer-state-message", TagText, "Jam in tray 1"),
		}}), 0
	})
	st, err := NewClient(ep, false).PrinterStatus(context.Background())
	if err != nil || st.State != PrinterStopped || st.Message != "Jam in tray 1" || st.Problem() != "paper or labels are jammed inside it" {
		t.Fatalf("%+v %v", st, err)
	}
	if got := rec.reqs[0].Lookup(TagOperationGroup, "requested-attributes").Strings(); !slices.Equal(got, statusAttributes) {
		t.Fatalf("asked for %v", got)
	}
}

func TestWatchWaitsBeforeTheFirstPoll(t *testing.T) {
	rec, ep := scripted(t, step{msg: jobReply(JobCompleted, 1)})
	start := time.Now()
	if _, err := NewClient(ep, true).Watch(context.Background(), 1, Watch{Every: 30 * time.Millisecond, Patience: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if d := rec.at[0].Sub(start); d < 30*time.Millisecond {
		t.Fatalf("first poll after %v", d)
	}
}

// RFC 8011 section 5.3.7.2: a printer may forget a finished job, so a missing job's
// outcome is unknown rather than a success.
func TestWatchReportsAVanishedJob(t *testing.T) {
	_, ep := scripted(t, step{msg: jobReply(JobProcessing, 0)}, step{msg: NewMessage(StatusNotFound, 0, opsGroup())})
	st, err := NewClient(ep, true).Watch(context.Background(), 7, Watch{Every: time.Millisecond, Patience: time.Minute})
	if err != nil || !st.Gone || st.State != JobProcessing || st.Terminal() {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestWatchGivesUpWithoutProgress(t *testing.T) {
	rec, ep := scripted(t, step{msg: jobReply(JobProcessing, 0)})
	st, err := NewClient(ep, true).Watch(context.Background(), 1, Watch{Every: time.Millisecond, Patience: 20 * time.Millisecond})
	if err != nil || st.State != JobProcessing || st.Gone || rec.count() < 2 {
		t.Fatalf("%+v %v after %d polls", st, err, rec.count())
	}
}

func TestWatchProgressRenewsPatience(t *testing.T) {
	var steps []step
	for i := range 6 {
		steps = append(steps, step{msg: jobReply(JobProcessing, i)})
	}
	steps = append(steps, step{msg: jobReply(JobCompleted, 6)})
	rec, ep := scripted(t, steps...)
	// Every poll shows a new impression, so the job outlasts a patience shorter than the
	// whole run.
	st, err := NewClient(ep, true).Watch(context.Background(), 1, Watch{Every: 10 * time.Millisecond, Patience: 40 * time.Millisecond})
	if err != nil || st.State != JobCompleted || rec.count() != 7 {
		t.Fatalf("%+v %v after %d polls", st, err, rec.count())
	}
}

func TestWatchRetriesTransientFailures(t *testing.T) {
	busy := NewMessage(0x0507, 0, opsGroup())
	rec, ep := scripted(t,
		step{msg: jobReply(JobProcessing, 0)},
		step{code: http.StatusServiceUnavailable},
		step{msg: busy},
		step{code: http.StatusBadGateway},
		step{msg: jobReply(JobCompleted, 1)},
	)
	var retries []error
	st, err := NewClient(ep, true).Watch(context.Background(), 7, Watch{
		Every: time.Millisecond, Patience: time.Minute, OnError: func(err error) { retries = append(retries, err) },
	})
	if err != nil || st.State != JobCompleted || len(retries) != 3 || rec.count() != 5 {
		t.Fatalf("status %+v err %v retries %v requests %d", st, err, retries, rec.count())
	}
}

func TestWatchStopsOnClientErrors(t *testing.T) {
	rec, ep := scripted(t, step{msg: jobReply(JobProcessing, 0)}, step{code: http.StatusBadRequest})
	st, err := NewClient(ep, true).Watch(context.Background(), 7, Watch{Every: time.Millisecond, Patience: time.Minute})
	var te *TransportError
	if !errors.As(err, &te) || te.HTTPStatus != 400 || Retryable(err) || st.State != JobProcessing || rec.count() != 2 {
		t.Fatalf("status %+v err %v requests %d", st, err, rec.count())
	}
}

// Failed polls are not progress: a printer that keeps failing uses up the patience.
func TestWatchFailuresUseUpPatience(t *testing.T) {
	_, ep := scripted(t, step{code: http.StatusServiceUnavailable})
	start := time.Now()
	_, err := NewClient(ep, true).Watch(context.Background(), 7, Watch{Every: time.Millisecond, Patience: 30 * time.Millisecond})
	if err == nil || !Retryable(err) {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

// A job held up by a stopped printer is watched past the patience until someone fixes
// the printer, including a processing job whose reasons say the printer stopped.
func TestWatchWaitsOutAStoppedPrinter(t *testing.T) {
	rec, ep := scripted(t,
		step{msg: jobReply(JobStopped, 0)},
		step{msg: jobReply(JobProcessing, 0, "printer-stopped")},
		step{msg: jobReply(JobStopped, 0)},
		step{msg: jobReply(JobStopped, 0)},
		step{msg: jobReply(JobCompleted, 1)},
	)
	var stopped int
	st, err := NewClient(ep, true).Watch(context.Background(), 7, Watch{
		Every: 5 * time.Millisecond, Patience: time.Nanosecond,
		OnStatus: func(s JobStatus) {
			if s.Stopped() {
				stopped++
			}
		},
	})
	if err != nil || st.State != JobCompleted || stopped != 4 || rec.count() != 5 {
		t.Fatalf("%+v %v stopped %d requests %d", st, err, stopped, rec.count())
	}
}

func TestWatchEndsWithTheContext(t *testing.T) {
	_, ep := scripted(t, step{msg: jobReply(JobStopped, 0)})
	ctx, cancel := context.WithCancel(context.Background())
	polls := 0
	st, err := NewClient(ep, true).Watch(ctx, 7, Watch{
		Every: time.Millisecond, Patience: time.Minute,
		OnStatus: func(JobStatus) {
			if polls++; polls == 3 {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) || st.State != JobStopped {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestCancelJob(t *testing.T) {
	rec, ep := serve(t, func(*Message) (*Message, int) { return NewMessage(0, 0, opsGroup()), 0 })
	if err := NewClient(ep, false).CancelJob(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	r := rec.reqs[0]
	if r.Code != OpCancelJob || r.Lookup(TagOperationGroup, "job-id").First() != 42 || r.Lookup(TagOperationGroup, "printer-uri").First() != ep.PrinterURI() {
		t.Fatalf("%+v", r)
	}
}

func TestOversizedReplyIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ContentType)
		head, _ := Encode(NewMessage(0, 1, opsGroup()))
		_, _ = w.Write(head)
		_, _ = w.Write(make([]byte, maxReply)) // document bytes past the end tag
	}))
	t.Cleanup(srv.Close)
	ep, _ := ParseEndpoint(strings.Replace(srv.URL, "http://", "ipp://", 1))
	var te *TransportError
	if _, err := NewClient(ep, false).GetPrinterAttributes(context.Background()); !errors.As(err, &te) || !strings.Contains(te.Msg, "MB this client reads") {
		t.Fatalf("err %v", err)
	}
}

func TestUnreachablePrinter(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	ep, _ := ParseEndpoint(strings.Replace(srv.URL, "http://", "ipp://", 1))
	srv.Close() // nothing listens there now
	var te *TransportError
	if _, err := NewClient(ep, false).GetPrinterAttributes(context.Background()); !errors.As(err, &te) || !Retryable(err) {
		t.Fatalf("err %v", err)
	}
}

func TestJobStatusParsing(t *testing.T) {
	st := ParseJobStatus(NewMessage(0, 1, opsGroup()))
	if st.State != 0 || st.HasImpressions || st.Terminal() {
		t.Fatalf("empty reply: %+v", st)
	}
	for state, terminal := range map[int]bool{JobPending: false, JobHeld: false, JobProcessing: false, JobStopped: false,
		JobCanceled: true, JobAborted: true, JobCompleted: true, 10: false} {
		if (JobStatus{State: state}).Terminal() != terminal {
			t.Errorf("state %d terminal %v", state, !terminal)
		}
	}
	st = ParseJobStatus(NewMessage(0, 1, opsGroup(), Group{Tag: TagJobGroup, Attributes: []Attribute{
		Enum("job-state", JobAborted), Attr("job-state-message", TagText, " ", "Out of labels"), Integer("job-impressions-completed", 2),
	}}))
	if st.State != JobAborted || st.Message != "Out of labels" || st.ImpressionsCompleted != 2 || !st.HasImpressions {
		t.Fatalf("%+v", st)
	}
}

func TestRetryable(t *testing.T) {
	for err, want := range map[error]bool{
		&TransportError{Msg: "no connection"}:               true,
		&TransportError{Msg: "x", HTTPStatus: 503}:          true,
		&TransportError{Msg: "x", HTTPStatus: 404}:          false,
		&StatusError{Status: 0x0507}:                        true,
		&StatusError{Status: 0x0500}:                        true,
		&StatusError{Status: 0x040b}:                        false,
		ErrPrintingDisabled:                                 false,
		fmt.Errorf("wrapped: %w", &StatusError{0x0502, ""}): true,
	} {
		if Retryable(err) != want {
			t.Errorf("%v: %v", err, !want)
		}
	}
}
