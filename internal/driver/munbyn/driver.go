// Package munbyn drives the Munbyn FM226 over BLE service ABF0.
//
// Commands and replies are protobuf-encoded payloads in 0x55 frames ([Wrap]). A label
// goes as heatshrink sections of whole 8-row strips, at most 4096 raster bytes each.
// Each section is cut into 200-byte DEVICE_PRINT packages, and the printer acknowledges
// every section.
package munbyn

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"time"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/ble"
)

// Family is the driver key models use.
const Family = "munbyn"

// Link roles (model.ble.write in the catalog).
const (
	roleCommand = "command" // ABF1, written with response
	roleImage   = "image"   // ABF4, chunked, without response
)

// Pacing is the timing the printer itself sees. These are the only values known to work
// with the FM226. Change them only after a test on the printer.
type Pacing struct {
	PackageGap  time.Duration // pause after every DEVICE_PRINT package
	PaperSettle time.Duration // pause after the printer accepts a new paper type
	ChunkBytes  int           // largest image write, unless the model says otherwise
	ChunkGapMS  float64       // between image writes, unless the model or ble_gap_ms says otherwise
}

// FM226Pacing is the pacing a print job uses.
var FM226Pacing = Pacing{PackageGap: 6 * time.Millisecond, PaperSettle: 3 * time.Second, ChunkBytes: 128}

// Waits bound how long the host waits for answers. The printer never sees them. They
// only decide when a silent printer counts as gone, so they are generous: answers
// normally take well under a second.
type Waits struct {
	Answer  time.Duration // the DEVICE_INFO and PAPER_TYPE_SET answers
	Section time.Duration // a section's acknowledgement
	Finish  time.Duration // the label-done report after PRINT_END
	Quiet   time.Duration // a gap this long ends the discarding of stale replies
}

// DefaultWaits are the waits a print job uses.
var DefaultWaits = Waits{Answer: 5 * time.Second, Section: 8 * time.Second, Finish: 15 * time.Second,
	Quiet: 20 * time.Millisecond}

// staleBudget caps the time spent discarding stale replies before a label.
const staleBudget = 500 * time.Millisecond

// Driver implements printer.Driver for the munbyn family.
type Driver struct {
	pacing Pacing
	waits  Waits
}

// Option configures a Driver.
type Option func(*Driver)

// WithPacing replaces FM226Pacing, e.g. to remove the pauses in tests.
func WithPacing(p Pacing) Option { return func(d *Driver) { d.pacing = p } }

// WithWaits replaces DefaultWaits, e.g. to shorten them in tests.
func WithWaits(w Waits) Option { return func(d *Driver) { d.waits = w } }

// New returns a driver with FM226Pacing and DefaultWaits unless an option says otherwise.
func New(opts ...Option) *Driver {
	d := &Driver{pacing: FM226Pacing, waits: DefaultWaits}
	for _, o := range opts {
		o(d)
	}
	return d
}

func init() { printer.Register(New()) }

// Family implements printer.Driver.
func (*Driver) Family() string { return Family }

// Links implements printer.Driver: the protocol is a byte stream over any link.
func (*Driver) Links() []printer.LinkKind {
	return []printer.LinkKind{printer.LinkBLE, printer.LinkTCP, printer.LinkFile}
}

// Canvas implements printer.Driver: the design size at the head's resolution.
func (*Driver) Canvas(m *printer.Model, s *printer.Stock) printer.Canvas {
	return printer.DefaultCanvas(m, s)
}

// Preview implements printer.Driver: the dithered content box in design orientation,
// exactly the dots Send burns.
func (*Driver) Preview(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) (*image.Gray, error) {
	job, err := EncodeJob(m, s, label, printer.Merge(m.Options, settings))
	if err != nil {
		return nil, err
	}
	return job.Preview(), nil
}

func errNothing() error { return printer.Errorf("There are no labels to print.") }

// Send implements printer.Driver. Every label is its own job: DEVICE_INFO, the paper
// type if it differs, the sections, then PRINT_END.
func (d *Driver) Send(ctx context.Context, pc *printer.PrintContext, labels []*image.Gray) (printer.Result, error) {
	if len(labels) == 0 {
		return printer.Result{}, errNothing()
	}
	jobs := make([]*Job, 0, len(labels))
	for _, label := range labels {
		job, err := EncodeJob(pc.Model, pc.Stock, label, pc.Settings)
		if err != nil {
			return printer.Result{}, err
		}
		jobs = append(jobs, job)
	}
	return d.SendJobs(ctx, pc, jobs)
}

// SendJobs runs already-encoded jobs over pc.Link.
func (d *Driver) SendJobs(ctx context.Context, pc *printer.PrintContext, jobs []*Job) (printer.Result, error) {
	if len(jobs) == 0 {
		return printer.Result{}, errNothing()
	}
	if pc.Link == nil {
		return printer.Result{}, printer.Errorf("The %s needs a Bluetooth or network link.", pc.Model.Label)
	}
	gap, err := thermal.ChunkGap(pc.Model, pc.Settings, d.pacing.ChunkGapMS)
	if err != nil {
		return printer.Result{}, err
	}
	s := &session{pacing: d.pacing, waits: d.waits, link: pc.Link, chunk: thermal.ChunkBytes(pc.Model, d.pacing.ChunkBytes),
		chunkGap: gap}
	confirmed := make([]bool, 0, len(jobs))
	var last Reply
	for i, job := range jobs {
		progress := func(part, parts int) {
			pc.Report((float64(i)+float64(part)/float64(parts))/float64(len(jobs)),
				fmt.Sprintf("Label %d of %d: part %d of %d sent", i+1, len(jobs), part, parts))
		}
		info, ok, err := s.label(ctx, job, progress)
		if err != nil {
			var perr *printer.Error
			if len(jobs) > 1 && errors.As(err, &perr) {
				err = printer.Errorf("Label %d of %d: %s", i+1, len(jobs), perr.Msg)
			}
			return printer.Result{Printed: i, Details: map[string]any{"confirmed": confirmed}}, err
		}
		confirmed = append(confirmed, ok)
		last = info
	}
	msg := fmt.Sprintf("Sent %s to %s.", thermal.Labels(len(jobs)), pc.Model.Label)
	unconfirmed := 0
	for _, ok := range confirmed {
		if !ok {
			unconfirmed++
		}
	}
	if unconfirmed > 0 {
		msg += fmt.Sprintf(" The printer did not confirm %d of them; check the output.", unconfirmed)
	}
	details := map[string]any{"confirmed": confirmed, "firmware": last.Firmware, "battery": last.Battery}
	return printer.Result{OK: true, Printed: len(jobs), Message: msg, Details: details}, nil
}

// session is one link's reply stream and pacing.
type session struct {
	pacing   Pacing
	waits    Waits
	link     printer.Link
	chunk    int
	chunkGap time.Duration
	frames   Reassembler
	queue    []Reply
}

// label prints one job. confirmed is false when PRINT_END got no label-done report: the
// label most likely printed, but the printer did not say so.
func (s *session) label(ctx context.Context, job *Job, progress func(part, parts int)) (info Reply, confirmed bool, err error) {
	if err := s.discardStale(ctx); err != nil {
		return Reply{}, false, err
	}
	if info, err = s.deviceInfo(ctx); err != nil {
		return Reply{}, false, err
	}
	if info.Paper != job.Paper {
		if err := s.setPaper(ctx, job.Paper); err != nil {
			return Reply{}, false, err
		}
	}
	for i, packages := range job.Sections {
		if err := s.section(ctx, packages, i+1, len(job.Sections)); err != nil {
			return Reply{}, false, err
		}
		progress(i+1, len(job.Sections))
	}
	confirmed, err = s.finish(ctx)
	return info, confirmed, err
}

// discardStale drops whatever the printer sent before this label (a late report of an
// earlier one, say). It reads until the link is quiet for Waits.Quiet, or for at most
// staleBudget, and starts the reassembler afresh.
func (s *session) discardStale(ctx context.Context) error {
	for until := time.Now().Add(staleBudget); time.Now().Before(until); {
		if _, err := s.link.Recv(ctx, s.waits.Quiet); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			break
		}
	}
	s.frames, s.queue = Reassembler{}, nil
	return nil
}

// errNoReply marks a wait that ran out.
var errNoReply = errors.New("munbyn: no reply")

// await returns the first reply of a kind in listen within timeout. Every wait listens
// only for what its step needs and drops the rest as noise:
//
//	DEVICE_INFO     info
//	PAPER_TYPE_SET  paper-set
//	a section       section-ack, rejected, label-done
//	PRINT_END       label-done
//
// So a problem report ends a label only once its image data is on the way. Before
// that, the printer may still be busy with something else, and the handshake waits
// for the answer it asked for.
func (s *session) await(ctx context.Context, timeout time.Duration, listen ...Kind) (Reply, error) {
	deadline := time.Now().Add(timeout)
	for {
		for len(s.queue) > 0 {
			r := s.queue[0]
			s.queue = s.queue[1:]
			for _, k := range listen {
				if r.Kind == k {
					return r, nil
				}
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return Reply{}, errNoReply
		}
		chunk, err := s.link.Recv(ctx, left)
		switch {
		case errors.Is(err, printer.ErrTimeout):
			return Reply{}, errNoReply
		case ctx.Err() != nil:
			return Reply{}, ctx.Err()
		case err != nil:
			return Reply{}, printer.Errorf("Lost the connection to the printer: %v", err)
		}
		for _, p := range s.frames.Push(chunk) {
			s.queue = append(s.queue, Decode(p))
		}
	}
}

// command writes a whole frame to ABF1 with response. A failed write ends the label
// with msg.
func (s *session) command(ctx context.Context, frame []byte, msg string) error {
	if err := s.link.Write(ctx, frame, roleCommand, true); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return printer.Errorf("%s", msg)
	}
	return nil
}

func (s *session) deviceInfo(ctx context.Context) (Reply, error) {
	silent := fmt.Sprintf("The printer did not answer within %s. Check that it is switched on, close by, "+
		"and not connected to a phone.", thermal.Seconds(s.waits.Answer))
	if err := s.command(ctx, DeviceInfoCommand, silent); err != nil {
		return Reply{}, err
	}
	r, err := s.await(ctx, s.waits.Answer, KindInfo)
	if errors.Is(err, errNoReply) {
		return Reply{}, printer.Errorf("%s", silent)
	}
	return r, err
}

func (s *session) setPaper(ctx context.Context, p PaperType) error {
	refused := fmt.Sprintf("The printer did not take the %s paper setting. Check which roll is loaded, then print again.", p)
	if err := s.command(ctx, PaperTypeSetCommand(p), refused); err != nil {
		return err
	}
	_, err := s.await(ctx, s.waits.Answer, KindPaperSet)
	if errors.Is(err, errNoReply) {
		return printer.Errorf("%s", refused)
	}
	if err != nil {
		return err
	}
	return ble.Sleep(ctx, s.pacing.PaperSettle)
}

// section writes one section's packages to ABF4 and waits for its acknowledgement.
// Packages are not acknowledged one by one.
func (s *session) section(ctx context.Context, packages [][]byte, part, parts int) error {
	for _, pkg := range packages {
		if err := ble.WriteChunked(ctx, s.link, pkg, roleImage, false, s.chunk, s.chunkGap); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return printer.Errorf("%s", msgDropped)
		}
		if err := ble.Sleep(ctx, s.pacing.PackageGap); err != nil {
			return err
		}
	}
	r, err := s.await(ctx, s.waits.Section, KindSectionAck, KindRejected, KindLabelDone)
	switch {
	case errors.Is(err, errNoReply):
		return printer.Errorf("No word from the printer after part %d of %d of the label. "+
			"Move the computer closer to it and print again.", part, parts)
	case err != nil:
		return err
	case r.Kind == KindRejected:
		return printer.Errorf("The printer refused the label data (code %d).", r.Code)
	case r.Kind == KindLabelDone && r.State != 0:
		return stopped(r)
	case r.Kind == KindLabelDone:
		return printer.Errorf("The printer ended the label after part %d of %d, before the rest arrived. "+
			"Print it again.", part, parts)
	}
	return nil
}

// stopped is the error for a label-done report with problem bits.
func stopped(r Reply) error {
	slog.Info("munbyn: label stopped", "state", r.State, "conditions", conditionNames(r.State))
	return printer.Errorf("%s", Problem(r.State))
}

// finish sends PRINT_END and waits for the label-done report. Silence is not an error:
// every section was acknowledged, so the label is counted as sent but unconfirmed.
func (s *session) finish(ctx context.Context) (confirmed bool, err error) {
	if err := s.command(ctx, PrintEndCommand, msgDropped); err != nil {
		return false, err
	}
	r, err := s.await(ctx, s.waits.Finish, KindLabelDone)
	switch {
	case errors.Is(err, errNoReply):
		return false, nil
	case err != nil:
		return false, err
	case r.State != 0:
		return false, stopped(r)
	}
	return true, nil
}

const msgDropped = "The connection to the printer dropped while the label was being sent. Print it again."
