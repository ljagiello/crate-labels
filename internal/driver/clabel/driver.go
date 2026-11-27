// Package clabel drives the Clabel CT-230B and CT-320B over the ISSC transparent UART
// BLE service.
//
// One byte stream carries two things. Control frames ([ControlFrame]) carry the
// identity query and two settings, and are written with response. The print data is
// TSPL text with raw-DEFLATE BITMAP bands. It is written without response and never
// acknowledged, so it is paced by the time a page takes to print.
package clabel

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"time"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/ble"
)

// Family is the driver key models use.
const Family = "clabel"

// roleData is the ISSC write characteristic, used for control frames and the stream.
const roleData = "data"

// Pacing is the timing the printer itself sees. These are the only values known to work
// with these printers. Change them only after a test on the printer.
type Pacing struct {
	Settle     time.Duration // after writing a setting, before reading it back
	PageBase   time.Duration // print time of a page: PageBase + rows*PagePerRow
	PagePerRow time.Duration
	ChunkGapMS float64 // between stream writes, unless the model or ble_gap_ms says otherwise
	ChunkBytes int     // largest stream write, unless the model says otherwise
}

// CTPacing is the pacing a print job uses.
var CTPacing = Pacing{Settle: 700 * time.Millisecond, PageBase: 1500 * time.Millisecond,
	PagePerRow: 3 * time.Millisecond, ChunkGapMS: 5, ChunkBytes: 200}

// PrintTime is how long a page of rows takes to print. Nothing acknowledges a page, so
// the next page, the next job, or closing the link waits this long.
func (p Pacing) PrintTime(rows int) time.Duration {
	return p.PageBase + time.Duration(rows)*p.PagePerRow
}

// Waits bound how long the host waits for answers. The printer never sees them.
type Waits struct {
	Answer time.Duration // the answer to a setting read
	Quiet  time.Duration // a gap this long ends the discarding of stale replies
}

// DefaultWaits are the waits a print job uses. An answer normally takes well under a
// second. A printer that does not answer settings costs one Answer per job.
var DefaultWaits = Waits{Answer: 2 * time.Second, Quiet: 20 * time.Millisecond}

// staleBudget caps the time spent discarding stale replies.
const staleBudget = 500 * time.Millisecond

// Driver implements printer.Driver for the clabel family.
type Driver struct {
	pacing Pacing
	waits  Waits
}

// Option configures a Driver.
type Option func(*Driver)

// WithPacing replaces CTPacing, e.g. to shorten the print time in tests.
func WithPacing(p Pacing) Option { return func(d *Driver) { d.pacing = p } }

// WithWaits replaces DefaultWaits, e.g. to shorten them in tests.
func WithWaits(w Waits) Option { return func(d *Driver) { d.waits = w } }

// New returns a driver with CTPacing and DefaultWaits unless an option says otherwise.
func New(opts ...Option) *Driver {
	d := &Driver{pacing: CTPacing, waits: DefaultWaits}
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
// exactly the dots Send burns. The box is often narrower than the design (545 of 567
// dots on a CT-230B 50x30), so the label is scaled to fit.
func (*Driver) Preview(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) (*image.Gray, error) {
	job, err := EncodeJob(m, s, label, printer.Merge(m.Options, settings))
	if err != nil {
		return nil, err
	}
	return job.Preview(), nil
}

// Send implements printer.Driver.
func (d *Driver) Send(ctx context.Context, pc *printer.PrintContext, labels []*image.Gray) (printer.Result, error) {
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

// SendJobs runs already-encoded jobs over pc.Link: the identity query, the settings
// check, then every page, each followed by its print time.
func (d *Driver) SendJobs(ctx context.Context, pc *printer.PrintContext, jobs []*Job) (printer.Result, error) {
	var pages []Page
	for _, j := range jobs {
		pages = append(pages, j.Pages...)
	}
	if len(pages) == 0 {
		return printer.Result{}, printer.Errorf("There are no labels to print.")
	}
	if pc.Link == nil {
		return printer.Result{}, printer.Errorf("The %s needs a Bluetooth or network link.", pc.Model.Label)
	}
	gap, err := thermal.ChunkGap(pc.Model, pc.Settings, d.pacing.ChunkGapMS)
	if err != nil {
		return printer.Result{}, err
	}
	chunk := thermal.ChunkBytes(pc.Model, d.pacing.ChunkBytes)
	s := &session{waits: d.waits, settle: d.pacing.Settle, link: pc.Link}
	if err := s.discardStale(ctx); err != nil {
		return printer.Result{}, err
	}
	s.control(ctx, IdentityQuery) // once per connection; the answer is only recorded
	answers, err := s.checkSettings(ctx, wantedSettings(pages[0].Paper), pc.Settings.Bool("write_settings", false))
	if err != nil {
		return printer.Result{}, err
	}
	sent := 0
	for done, j := range jobs {
		for _, page := range j.Pages {
			if err := ble.WriteChunked(ctx, pc.Link, page.Data, roleData, false, chunk, gap); err != nil {
				if ctx.Err() != nil {
					return printer.Result{Printed: done}, ctx.Err()
				}
				msg := "Sending to the printer failed partway through. Bring the computer closer to it and print again."
				if sent > 0 {
					msg += fmt.Sprintf(" %d of %d pages had already gone through.", sent, len(pages))
				}
				return printer.Result{Printed: done}, printer.Errorf("%s", msg)
			}
			sent++
			pc.Report(float64(sent)/float64(len(pages)), fmt.Sprintf("Page %d of %d sent", sent, len(pages)))
			// Wait out the print time after the last page too: the link closes when
			// Send returns.
			if err := ble.Sleep(ctx, d.pacing.PrintTime(page.Rows)); err != nil {
				return printer.Result{Printed: done}, err
			}
		}
	}
	msg := fmt.Sprintf("Sent %s to %s.", thermal.Labels(len(jobs)), pc.Model.Label)
	if !answers {
		msg += " The printer did not report its settings."
		if hint := pc.Settings.String("setup_hint", ""); hint != "" {
			msg += " If nothing printed: " + hint
		}
	}
	details := map[string]any{"answers_settings": answers}
	if s.identity != nil {
		if s.identity.Firmware != "" {
			details["firmware"] = s.identity.Firmware
		}
		if m := reportedModel(pc, s.identity.Fields); m != nil && pc.Model != nil && m.ID != pc.Model.ID {
			slog.Warn("clabel: the printer names another model", "configured", pc.Model.ID, "reported", m.ID)
			details["reported_model"] = m.ID
		}
	}
	if s.status != nil {
		details["status"] = s.status.Conditions()
	}
	return printer.Result{OK: true, Printed: len(jobs), Message: msg, Details: details}, nil
}

// reportedModel finds the Clabel model the identity's text names, through the catalog's
// name_patterns. Without a catalog, only the configured model's patterns are tried. It
// returns nil when no field matches.
func reportedModel(pc *printer.PrintContext, fields []string) *printer.Model {
	for _, field := range fields {
		if pc.Catalog != nil {
			for _, m := range pc.Catalog.MatchBLEName(field) {
				if m.Family == Family {
					return m
				}
			}
			continue
		}
		if pc.Model != nil && pc.Model.BLE != nil && pc.Model.BLE.MatchesName(field) {
			return pc.Model
		}
	}
	return nil
}

// desired is a setting value a job needs, and what to tell the user when the printer
// will not take it.
type desired struct {
	setting Setting
	value   byte
	refusal string
}

// wantedSettings lists what a job needs, in the order the settings are read and then
// fixed: TSPL first, then the paper type of the first page.
func wantedSettings(paper Paper) []desired {
	return []desired{
		{SettingLanguage, LanguageTSPL,
			"The printer stayed out of TSPL mode after being asked to switch. Pick TSPL in its menu, then print again."},
		{SettingPaperType, byte(paper),
			fmt.Sprintf("The printer kept its paper type after being asked for %s. Pick %s paper in its menu, then print again.", paper, paper)},
	}
}

// reading is what one setting read got back.
type reading struct {
	answered bool // the printer replied at all
	has      bool // the reply carried a value
	value    byte
}

// checkSettings reads every wanted setting and then, when fix is set (write_settings,
// the CT-320B), writes each one that holds a different value and reads it back.
//
// When the first read gets no reply, the printer does not answer settings at all, and
// nothing else is asked. answers reports which case this was. A reply without a value
// is accepted on the first read. A read-back must show exactly the value written.
func (s *session) checkSettings(ctx context.Context, want []desired, fix bool) (answers bool, err error) {
	have := make([]reading, len(want))
	for i, w := range want {
		if have[i], err = s.read(ctx, w.setting); err != nil {
			return false, err
		}
		if i == 0 && !have[i].answered {
			slog.Info("clabel: no reply to a settings read; printing without checking settings")
			return false, nil
		}
	}
	if !fix {
		return true, nil
	}
	for i, w := range want {
		if !have[i].has || have[i].value == w.value {
			continue
		}
		got, err := s.write(ctx, w.setting, w.value)
		if err != nil {
			return false, err
		}
		if got != (reading{answered: true, has: true, value: w.value}) {
			return false, printer.Errorf("%s", w.refusal)
		}
	}
	return true, nil
}

// session is one connection's replies while a job runs.
type session struct {
	waits    Waits
	settle   time.Duration
	link     printer.Link
	frames   Reassembler
	queue    []Reply
	identity *Reply
	status   *StatusBits
}

// note records identity and status replies whenever they arrive, and queues all replies.
func (s *session) note(chunk []byte) {
	for _, f := range s.frames.Push(chunk) {
		r := Decode(f)
		switch r.Kind {
		case ReplyIdentity:
			slog.Info("clabel: identity", "fields", r.Fields, "firmware", r.Firmware)
			s.identity = &r
		case ReplyStatus:
			slog.Info("clabel: status", "byte", byte(r.Status), "conditions", r.Status.Conditions())
			st := r.Status
			s.status = &st
		}
		s.queue = append(s.queue, r)
	}
}

// discardStale drops queued replies nobody waits for. It reads until the link is quiet
// for Waits.Quiet, or for at most staleBudget. Identity and status replies are still
// recorded.
func (s *session) discardStale(ctx context.Context) error {
	for until := time.Now().Add(staleBudget); time.Now().Before(until); {
		chunk, err := s.link.Recv(ctx, s.waits.Quiet)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			break
		}
		s.note(chunk)
	}
	s.queue = nil
	return nil
}

// control writes a control frame with response. A failed write counts as no answer.
func (s *session) control(ctx context.Context, frame []byte) bool {
	if err := s.link.Write(ctx, frame, roleData, true); err != nil {
		slog.Debug("clabel: control write failed", "err", err)
		return false
	}
	return true
}

// read asks for one setting and waits up to Waits.Answer for its reply. Replies that
// came in before the request (an echo of a write, say) are dropped first.
func (s *session) read(ctx context.Context, setting Setting) (reading, error) {
	if err := s.discardStale(ctx); err != nil {
		return reading{}, err
	}
	if !s.control(ctx, ReadSetting(setting)) {
		return reading{}, ctx.Err()
	}
	deadline := time.Now().Add(s.waits.Answer)
	for {
		for len(s.queue) > 0 {
			r := s.queue[0]
			s.queue = s.queue[1:]
			if r.Kind == ReplySetting && r.Setting == setting {
				return reading{answered: true, has: r.HasValue, value: r.Value}, nil
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return reading{}, nil
		}
		chunk, err := s.link.Recv(ctx, left)
		switch {
		case ctx.Err() != nil:
			return reading{}, ctx.Err()
		case err != nil:
			// A timeout, or a dead link that answers nothing; the stream write will
			// report the latter.
			return reading{}, nil
		}
		s.note(chunk)
	}
}

// write changes a setting, lets the printer settle, and reads the setting back.
func (s *session) write(ctx context.Context, setting Setting, value byte) (reading, error) {
	if !s.control(ctx, WriteSetting(setting, value)) {
		return reading{}, ctx.Err()
	}
	if err := ble.Sleep(ctx, s.settle); err != nil {
		return reading{}, err
	}
	return s.read(ctx, setting)
}
