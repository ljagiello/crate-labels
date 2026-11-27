package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/store"
	"github.com/ljagiello/crate-labels/internal/transport/fake"
)

// Limits of one print request.
const (
	MaxCopies = 99
	MaxItems  = 500
)

// fileLinkMaxWrite matches the "file" link opener in internal/transport/fake.
const fileLinkMaxWrite = 244

// PrintRequest queues labels on a saved printer.
type PrintRequest struct {
	Sources      []Source
	Template     string
	Stock        string
	Printer      string // saved printer id or name
	Copies       int    // per source, 1..99
	Options      map[string]any
	PrintOptions map[string]any
	Origin       string // job source: "ui", "cli", "test", "crate:<name>", ...
}

// printQueue is one device's pending job ids. Queues are keyed by link and address, so
// two saved printers on one device (e.g. the same BLE printer saved twice) share one
// sequential queue.
type printQueue struct {
	key     string
	pending []int64
	wake    chan struct{}
}

// deviceKey names the device a printer reaches: "<link>:<address>", or the printer id
// when the address is empty or the model is unknown.
func deviceKey(t printer.Target, m *printer.Model) string {
	addr := strings.ToLower(strings.TrimSpace(t.Address))
	if m == nil || addr == "" {
		return "printer:" + t.ID
	}
	return string(m.Link) + ":" + addr
}

// queueKey is the device key of a saved printer.
func (s *Service) queueKey(ctx context.Context, printerID string) string {
	t, err := s.Store.Printer(ctx, printerID)
	if err != nil {
		return "printer:" + printerID
	}
	m, _ := s.Catalog.Model(t.ModelID)
	return deviceKey(t, m)
}

// DeviceBusy returns a job printing on the device at link and address, in this or any
// other crate-labels process. A BLE probe must not connect while a job prints there.
func (s *Service) DeviceBusy(ctx context.Context, link printer.LinkKind, address string) (store.Job, bool, error) {
	want := deviceKey(printer.Target{Address: address}, &printer.Model{Link: link})
	jobs, err := s.Store.Jobs(ctx, 200, true)
	if err != nil {
		return store.Job{}, false, err
	}
	for _, j := range jobs {
		if j.Status == store.JobPrinting && s.queueKey(ctx, j.PrinterID) == want {
			return j, true, nil
		}
	}
	return store.Job{}, false, nil
}

// Subscribe returns a channel of job changes and a function that ends the
// subscription. Slow subscribers miss events rather than stall printing; the channel
// is closed when the service closes.
func (s *Service) Subscribe() (<-chan store.Job, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan store.Job, 256)
	if s.closed {
		close(ch)
		return ch, func() {}
	}
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

func (s *Service) publish(j store.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- j:
		default: // a stuck subscriber must not stop printing
		}
	}
}

func (s *Service) finish(j store.Job) {
	s.publish(j)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.done[j.ID]; ok {
		close(ch)
		delete(s.done, j.ID)
	}
	delete(s.userCancel, j.ID)
}

// Print resolves every source to label data (a snapshot) and queues one job.
func (s *Service) Print(ctx context.Context, r PrintRequest) (store.Job, error) {
	if len(r.Sources) == 0 {
		return store.Job{}, Errorf("Nothing to print.")
	}
	if len(r.Sources) > MaxItems {
		return store.Job{}, Errorf("At most %d records fit in one job.", MaxItems)
	}
	if r.Copies == 0 {
		r.Copies = 1
	}
	if r.Copies < 1 || r.Copies > MaxCopies {
		return store.Job{}, Errorf("Copies must be between 1 and %d.", MaxCopies)
	}
	target, err := s.Store.FindPrinter(ctx, r.Printer)
	if err != nil {
		if store.IsNotFound(err) {
			return store.Job{}, Errorf("No saved printer called %q.", r.Printer)
		}
		return store.Job{}, err
	}
	m, err := s.Catalog.Model(target.ModelID)
	if err != nil {
		return store.Job{}, Errorf("Printer %s uses model %q, which is not in the catalog.", target.Name, target.ModelID)
	}
	t, err := s.Template(r.Template)
	if err != nil {
		return store.Job{}, err
	}
	st, err := s.Catalog.Stock(r.Stock)
	if err != nil {
		return store.Job{}, err
	}
	if !s.Catalog.Fits(m, st) {
		return store.Job{}, Errorf("%s (%s) can't print on %s.", target.Name, m.Label, st.Name)
	}
	if !t.Supports(st) {
		return store.Job{}, Errorf("The %s template does not fit %s.", t.Name(), st.Name)
	}
	records := make([]json.RawMessage, 0, len(r.Sources))
	for _, src := range r.Sources {
		l, err := s.Resolve(ctx, src)
		if err != nil {
			return store.Job{}, err
		}
		records = append(records, l.JSON())
	}
	return s.enqueue(ctx, store.NewJob{PrinterID: target.ID, PrinterName: target.Name, Template: r.Template,
		Stock: r.Stock, Records: records, Copies: r.Copies, Options: r.Options, PrintOptions: r.PrintOptions, Source: r.Origin})
}

func (s *Service) enqueue(ctx context.Context, n store.NewJob) (store.Job, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return store.Job{}, Errorf("crate-labels is shutting down.")
	}
	n.Owner = s.Owner
	key := s.queueKey(ctx, n.PrinterID)
	j, err := s.Store.CreateJob(ctx, n)
	if err != nil {
		return store.Job{}, err
	}
	s.mu.Lock()
	s.done[j.ID] = make(chan struct{})
	q, ok := s.queues[key]
	if !ok {
		q = &printQueue{key: key, wake: make(chan struct{}, 1)}
		s.queues[key] = q
		s.wg.Add(1)
		go s.worker(q)
	}
	q.pending = append(q.pending, j.ID)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	s.mu.Unlock()
	s.publish(j)
	return j, nil
}

// worker runs one printer's jobs in order until the service closes.
func (s *Service) worker(q *printQueue) {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		var id int64
		have := len(q.pending) > 0
		if have {
			id, q.pending = q.pending[0], q.pending[1:]
		}
		s.mu.Unlock()
		if !have {
			select {
			case <-q.wake:
				continue
			case <-s.ctx.Done():
				return
			}
		}
		if s.ctx.Err() != nil {
			return
		}
		s.runJob(id)
	}
}

// Cancel cancels a job: a queued job at once, a printing one of this process shortly
// after. A job whose owning process has died is marked interrupted; a job printing in
// another live process cannot be stopped from here (409).
func (s *Service) Cancel(ctx context.Context, id int64) (store.Job, error) {
	j, err := s.Store.Job(ctx, id)
	if err != nil {
		return j, err
	}
	if !store.Active(j.Status) {
		return j, nil
	}
	if j.Owner != s.Owner && !s.OwnerAlive(j.Owner) {
		dead := j.Owner
		if _, err := s.Store.InterruptStaleJobs(ctx, func(owner string) bool { return owner == dead }); err != nil {
			return j, err
		}
		j, err = s.Store.Job(ctx, id)
		if err == nil {
			s.publish(j)
		}
		return j, err
	}
	if j.Status == store.JobQueued {
		cj, ok, err := s.Store.TransitionJob(ctx, id, store.JobQueued, store.JobCancelled, "Cancelled")
		if err != nil {
			return j, err
		}
		if ok {
			s.mu.Lock()
			for _, q := range s.queues {
				q.pending = slices.DeleteFunc(q.pending, func(x int64) bool { return x == id })
			}
			s.mu.Unlock()
			s.finish(cj)
			return cj, nil
		}
		j = cj // a worker started it in the meantime
	}
	if j.Status == store.JobPrinting {
		if j.Owner != s.Owner {
			pid, _, _ := strings.Cut(j.Owner, ":")
			return j, &Error{Status: 409, Msg: fmt.Sprintf(
				"Job %d is printing in another crate-labels process (pid %s). Stop it there.", id, pid)}
		}
		s.mu.Lock()
		if cancel, ok := s.running[id]; ok {
			s.userCancel[id] = true
			cancel()
		}
		s.mu.Unlock()
	}
	return s.Store.Job(ctx, id)
}

// Retry queues a new job with a finished job's records and settings.
func (s *Service) Retry(ctx context.Context, id int64) (store.Job, error) {
	old, err := s.Store.Job(ctx, id)
	if err != nil {
		return old, err
	}
	if store.Active(old.Status) {
		return store.Job{}, Errorf("That job has not finished yet.")
	}
	return s.enqueue(ctx, store.NewJob{PrinterID: old.PrinterID, PrinterName: old.PrinterName, Template: old.Template,
		Stock: old.Stock, Records: old.Records, Copies: old.Copies, Options: old.Options, PrintOptions: old.PrintOptions,
		Source: fmt.Sprintf("retry:%d", old.ID)})
}

// Wait blocks until a job leaves the queue, then returns it.
func (s *Service) Wait(ctx context.Context, id int64) (store.Job, error) {
	j, err := s.Store.Job(ctx, id)
	if err != nil || !store.Active(j.Status) {
		return j, err
	}
	s.mu.Lock()
	ch, ok := s.done[id]
	s.mu.Unlock()
	if !ok {
		return j, Errorf("Job %d is owned by another crate-labels process.", id)
	}
	select {
	case <-ch:
	case <-ctx.Done():
		return j, ctx.Err()
	case <-s.ctx.Done():
	}
	return s.Store.Job(context.WithoutCancel(ctx), id)
}

// Reprint prints a history entry again: the same data, template, stock and options.
// printerID defaults to default_printer and copies to the entry's copies.
func (s *Service) Reprint(ctx context.Context, historyID int64, printerID string, copies int) (store.Job, error) {
	h, err := s.Store.HistoryEntry(ctx, historyID)
	if err != nil {
		return store.Job{}, err
	}
	if printerID == "" {
		printerID = s.Store.SettingString(ctx, "default_printer")
	}
	if printerID == "" {
		return store.Job{}, Errorf("Choose a printer to reprint on.")
	}
	if copies == 0 {
		copies = h.Copies
	}
	l, err := ParseLabel(h.Data)
	if err != nil {
		return store.Job{}, Errorf("That history entry is damaged.")
	}
	return s.Print(ctx, PrintRequest{Sources: []Source{{Data: &l}}, Template: h.Template, Stock: h.Stock,
		Printer: printerID, Copies: copies, Options: h.Options, Origin: fmt.Sprintf("reprint:%d", h.ID)})
}

// TestPrint prints a template (test-pattern by default) with placeholder data.
func (s *Service) TestPrint(ctx context.Context, printerID, templateID, stockID string, options map[string]any) (store.Job, error) {
	if _, err := s.Store.Printer(ctx, printerID); err != nil {
		return store.Job{}, err
	}
	if templateID == "" {
		templateID = "test-pattern"
	}
	l := Label{Artist: "crate-labels", Title: "Test pattern"}
	return s.Print(ctx, PrintRequest{Sources: []Source{{Data: &l}}, Template: templateID, Stock: stockID,
		Printer: printerID, Copies: 1, Options: options, Origin: "test"})
}

func ptr[T any](v T) *T { return &v }

// runJob runs one job with a context the user's Cancel and Close can end. The cancel
// function is registered before the job turns "printing", so Cancel always finds it.
func (s *Service) runJob(id int64) {
	jctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.running[id] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
		cancel()
	}()
	bg := context.WithoutCancel(s.ctx)
	j, ok, err := s.Store.TransitionJob(bg, id, store.JobQueued, store.JobPrinting, "Rendering labels…")
	switch {
	case err != nil:
		// The job cannot start; fail it rather than leave it queued for ever.
		msg := "Could not start the job: " + err.Error()
		if jj, uerr := s.Store.UpdateJob(bg, id, store.JobUpdate{Status: ptr(store.JobFailed), Error: &msg,
			Message: ptr("Failed")}); uerr == nil {
			s.finish(jj)
		} else {
			s.Log.Error("could not start or fail a job", "job", id, "err", err, "update_err", uerr)
			s.finish(store.Job{ID: id})
		}
		return
	case !ok:
		s.finish(j) // cancelled or interrupted while it waited
		return
	}
	s.publish(j)
	s.run(jctx, j)
}

// progress throttles driver progress to steps of 2 % or a new message.
type progress struct {
	s        *Service
	id       int64
	fraction float64
	message  string
}

func (p *progress) report(fraction float64, message string) {
	fraction = min(1, max(0, fraction))
	if message == p.message && fraction < 1 && fraction-p.fraction < 0.02 {
		return
	}
	p.fraction, p.message = fraction, message
	ctx := context.WithoutCancel(p.s.ctx)
	j, err := p.s.Store.UpdateJob(ctx, p.id, store.JobUpdate{Progress: &fraction, Message: &message})
	if err == nil {
		p.s.publish(j)
	}
}

// run prints one job. Every store write uses a context that outlives cancellation, so
// a cancelled job still records what got out.
func (s *Service) run(ctx context.Context, j store.Job) {
	bg := context.WithoutCancel(ctx)
	fail := func(msg string, printed int) {
		u := store.JobUpdate{Status: ptr(store.JobFailed), Error: &msg, Message: ptr("Failed")}
		if printed >= 0 {
			u.Printed = &printed
		}
		if jj, err := s.Store.UpdateJob(bg, j.ID, u); err == nil {
			s.finish(jj)
		}
	}
	target, err := s.Store.Printer(bg, j.PrinterID)
	if err != nil {
		fail("That printer was removed.", -1)
		return
	}
	p := &progress{s: s, id: j.ID, fraction: -1, message: j.Message}

	labels, images, result, sendErr := s.send(ctx, j, &target, p)
	if sendErr != nil && ctx.Err() != nil {
		// However the driver words it, an error after a cancel is the cancel.
		sendErr = context.Canceled
	}
	printed := min(max(result.Printed, 0), len(images))
	if sendErr != nil && labels == nil {
		// Nothing was sent: the job failed while rendering, or was cancelled.
		s.conclude(bg, j, printed, result, sendErr)
		return
	}
	s.record(bg, j, &target, labels, printed)
	_ = s.Store.TouchPrinter(bg, target.ID)
	s.conclude(bg, j, printed, result, sendErr)
}

// conclude writes a job's final state.
func (s *Service) conclude(bg context.Context, j store.Job, printed int, result printer.Result, sendErr error) {
	total := j.Total
	switch {
	case sendErr != nil && errors.Is(sendErr, context.Canceled):
		s.mu.Lock()
		user := s.userCancel[j.ID]
		s.mu.Unlock()
		if !user {
			// The service is closing (Ctrl-C, SIGTERM): the job is interrupted, not
			// cancelled, and keeps what got out.
			if jj, err := s.Store.UpdateJob(bg, j.ID, store.JobUpdate{Status: ptr(store.JobInterrupted), Printed: &printed,
				Error: ptr(store.InterruptedMessage), Message: ptr(fmt.Sprintf("Stopped after %d of %d", printed, total))}); err == nil {
				s.finish(jj)
			}
			return
		}
		if jj, err := s.Store.UpdateJob(bg, j.ID, store.JobUpdate{Status: ptr(store.JobCancelled), Printed: &printed,
			Message: ptr("Cancelled")}); err == nil {
			s.finish(jj)
		}
	case sendErr != nil:
		msg := sendErr.Error()
		if _, user := StatusOf(sendErr); !user {
			s.Log.Error("print job crashed", "job", j.ID, "err", sendErr)
			msg = "Unexpected error: " + msg
		}
		if jj, err := s.Store.UpdateJob(bg, j.ID, store.JobUpdate{Status: ptr(store.JobFailed), Printed: &printed,
			Error: &msg, Message: ptr(fmt.Sprintf("Stopped after %d of %d", printed, total))}); err == nil {
			s.finish(jj)
		}
	case result.OK:
		msg := result.Message
		if msg == "" {
			msg = "Printed " + plural(printed, "label")
		}
		if jj, err := s.Store.UpdateJob(bg, j.ID, store.JobUpdate{Status: ptr(store.JobDone), Printed: &printed,
			Progress: ptr(1.0), Message: &msg}); err == nil {
			s.finish(jj)
		}
	default:
		msg := result.Message
		if msg == "" {
			msg = "The printer reported a failure."
		}
		if jj, err := s.Store.UpdateJob(bg, j.ID, store.JobUpdate{Status: ptr(store.JobFailed), Printed: &printed,
			Error: &msg, Message: ptr(fmt.Sprintf("Stopped after %d of %d", printed, total))}); err == nil {
			s.finish(jj)
		}
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// send renders every record and sends all labels in one driver call. labels is nil when
// nothing reached the driver.
func (s *Service) send(ctx context.Context, j store.Job, target *printer.Target, p *progress) (labels []Label, images []*image.Gray, result printer.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	m, err := s.Catalog.Model(target.ModelID)
	if err != nil {
		return nil, nil, result, err
	}
	t, st, err := s.checkFit(m, j.Template, j.Stock)
	if err != nil {
		return nil, nil, result, err
	}
	var datas []Label
	for i, raw := range j.Records {
		l, err := ParseLabel(raw)
		if err != nil {
			return nil, nil, result, Errorf("Record %d of this job is damaged.", i+1)
		}
		img, _, err := s.render(ctx, l, t, m, st, j.Options)
		if err != nil {
			return nil, nil, result, err
		}
		for range j.Copies {
			images = append(images, img)
		}
		datas = append(datas, l)
		p.report(0, fmt.Sprintf("Rendered %d of %d", i+1, len(j.Records)))
		if ctx.Err() != nil {
			return nil, images, result, ctx.Err()
		}
	}
	var link *fake.Link
	opts := printer.PrintOptions{Settings: printer.Settings(j.PrintOptions), Progress: p.report}
	if m.Link == printer.LinkFile {
		link = fake.New(fileLinkMaxWrite)
		opts.Link = link
	}
	p.report(0, fmt.Sprintf("Sending to %s…", target.Name))
	result, err = s.Catalog.Print(ctx, target, st.ID, images, opts)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if link != nil && target.Address != "" {
		if werr := writeFileOutput(target.Address, j.ID, link.Sent("")); werr != nil && err == nil {
			err = Errorf("Could not write the printer output to %s: %v", target.Address, werr)
		}
	}
	return datas, images, result, err
}

// writeFileOutput saves a FILE printer's raw bytes. An existing directory, or an address
// ending in "/", gets job-<id>.bin; anything else is a file each job overwrites.
func writeFileOutput(address string, jobID int64, data []byte) error {
	path := address
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if info, err := os.Stat(path); strings.HasSuffix(address, "/") || (err == nil && info.IsDir()) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		path = filepath.Join(path, fmt.Sprintf("job-%d.bin", jobID))
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// record adds what printed to the history and to the label-print log. A record's copies
// print next to each other, so record i got min(copies, printed - i*copies) labels.
func (s *Service) record(ctx context.Context, j store.Job, target *printer.Target, labels []Label, printed int) {
	var prints []store.LabelPrint
	for i, l := range labels {
		n := min(j.Copies, max(0, printed-i*j.Copies))
		if n == 0 {
			break
		}
		if _, err := s.Store.AddHistory(ctx, store.NewHistoryEntry{Data: l.JSON(), Template: j.Template, Stock: j.Stock,
			Printer: target.Name, Copies: n, Options: j.Options}); err != nil {
			s.Log.Error("could not add to the print history", "job", j.ID, "err", err)
		}
		prints = append(prints, store.LabelPrint{ReleaseID: l.RID(), JobID: j.ID, Template: j.Template, Copies: n})
	}
	if err := s.Store.LogPrints(ctx, prints); err != nil {
		s.Log.Error("could not log which records were printed", "job", j.ID, "err", err)
	}
}
