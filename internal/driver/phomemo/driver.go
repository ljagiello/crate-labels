// Package phomemo drives Phomemo M110, M220 and D30 label printers over a byte link.
//
// Each label is sent as one or more jobs: header 1b 4e 0d <speed> 1b 4e 04 <darkness>
// 1f 11 <media>, then 1d 76 30 00 <bytes per row:u16le> <rows:u16le> <bits> blocks,
// then the footer 1f f0 05 00 [1f f0 03 00]. Labels taller than 1200 rows become
// several jobs.
//
// Label orientation and placement have not been checked on every model, so the
// driver takes calibration settings:
//   - paper_align: "right" puts the label flush to high x (a roll against the rod),
//     "center" centres it (a roll between the guides);
//   - offset_dots: white dots on the low-x side, which overrides paper_align;
//   - extra_rotate_cw: 0, 90, 180 or 270 on top of the stock's own turn;
//   - darkness and speed: header bytes;
//   - ble_gap_ms: the pause between BLE writes.
package phomemo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/ble"
)

// Family is the driver key in model TOML.
const Family = "phomemo"

// How long a BLE bring-up listens before going on without an answer. Neither is a
// protocol requirement: a printer that answers sooner ends the wait at once. They are
// generous because a missed firmware reply means an old M110 gets its raster in one
// block it cannot print.
const (
	helloWindow    = 2 * time.Second // for the notification sent on subscribing
	firmwareWindow = 2 * time.Second // for the reply to FirmwareQuery
)

// unboundedChunk makes non-BLE links take one write per job (still capped by MaxWrite).
const unboundedChunk = 1 << 20

func init() {
	printer.Register(New())
	ble.RegisterModelResolver(Family, IdentifyModel)
}

// Driver is the Phomemo printer.Driver. It remembers, per printer address, when the
// printer can take its next job, so the pause holds across prints.
type Driver struct {
	now            func() time.Time
	sleep          func(ctx context.Context, d time.Duration) error
	helloWindow    time.Duration
	firmwareWindow time.Duration

	mu       sync.Mutex
	printers map[string]*printerState
}

type printerState struct {
	mu      sync.Mutex // one print at a time per printer
	readyAt time.Time  // the next job waits until then
}

// New returns a driver with the real clock and the default listening windows.
func New() *Driver {
	return &Driver{
		now:            time.Now,
		sleep:          ble.Sleep,
		helloWindow:    helloWindow,
		firmwareWindow: firmwareWindow,
		printers:       map[string]*printerState{},
	}
}

var _ printer.Driver = (*Driver)(nil)

// Family implements printer.Driver.
func (d *Driver) Family() string { return Family }

// Links implements printer.Driver: the job bytes go over any byte link.
func (d *Driver) Links() []printer.LinkKind {
	return []printer.LinkKind{printer.LinkBLE, printer.LinkTCP, printer.LinkFile}
}

// Canvas is the design size at 8 dots per mm.
func (d *Driver) Canvas(_ *printer.Model, s *printer.Stock) printer.Canvas {
	return printer.Canvas{Width: dots(s.DesignMM[0]), Height: dots(s.DesignMM[1]), DPI: DotsPerMM * printer.MMPerInch}
}

// Preview is exactly the dots Send burns, cropped to the label and turned back to design
// orientation. It honours the placement and rotation calibration. Darkness and speed
// change only the heat, so the dots are the same. With extra_rotate_cw at 90 or 270 the
// preview is the canvas turned on its side.
func (d *Driver) Preview(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) (*image.Gray, error) {
	return preview(m, s, label, settings)
}

func (d *Driver) state(address string) *printerState {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.printers[address]
	if !ok {
		st = &printerState{}
		d.printers[address] = st
	}
	return st
}

// Send prints each label once, in order. Over BLE it first listens for the printer's
// hello and asks for its firmware version; legacy firmware gets short raster blocks.
// The jobs are chunked per the model's BLE profile, and each job waits out the
// previous job's pause, even across Send calls.
func (d *Driver) Send(ctx context.Context, pc *printer.PrintContext, labels []*image.Gray) (printer.Result, error) {
	if len(labels) == 0 {
		return printer.Result{}, printer.Errorf("Nothing to print.")
	}
	if pc.Link == nil {
		return printer.Result{}, printer.Errorf("There is no connection to %s.", pc.Model.Label)
	}
	m, link := pc.Model, pc.Link
	opts := printer.Merge(m.Options, pc.Settings)
	address := m.ID
	if pc.Target != nil {
		address = pc.Target.Address
	}
	st := d.state(address)
	st.mu.Lock()
	defer st.mu.Unlock()
	details := map[string]any{"model": m.ID}

	var firmware Version
	if m.Link == printer.LinkBLE {
		pc.Report(0, "Talking to the printer")
		hello := listen(ctx, link, d.helloWindow, func(f []byte) bool { return len(f) > 0 })
		firmware = d.askFirmware(ctx, link)
		details["hello"] = nil
		if hello != nil {
			details["hello"] = hex.EncodeToString(hello)
		}
		details["firmware"] = nil
		if firmware != nil {
			details["firmware"] = firmware.String()
		}
		if others := otherSuspects(hello, advertisedName(pc, link, opts), m, familyModels(pc.Catalog, m)); len(others) > 0 {
			// The configured model decides the geometry; the evidence is only reported.
			slog.Warn("phomemo printer looks like another model", "configured", m.ID, "suspects", others)
			details["suspected_models"] = others
		}
		if err := ctx.Err(); err != nil {
			return printer.Result{}, err
		}
	}

	blockRows := legacyBlockRows(opts, firmware)
	details["block_rows"] = blockRows
	encoded := make([][]Job, len(labels))
	jobCount, total := 0, 0
	for i, l := range labels {
		jobs, err := Encode(m, pc.Stock, l, pc.Settings, blockRows)
		if err != nil {
			return printer.Result{}, err
		}
		encoded[i] = jobs
		jobCount += len(jobs)
		for _, j := range jobs {
			total += len(j.Data)
		}
	}
	if total == 0 {
		return printer.Result{}, printer.Errorf("Nothing to print.")
	}

	chunk := thermal.ChunkBytes(m, unboundedChunk)
	gap, err := thermal.ChunkGap(m, opts, 0)
	if err != nil {
		return printer.Result{}, err
	}

	done, printed := 0, 0
	for i, jobs := range encoded {
		message := fmt.Sprintf("Sending label %d of %d", i+1, len(labels))
		for _, job := range jobs {
			if wait := st.readyAt.Sub(d.now()); wait > 0 {
				pc.Report(float64(done)/float64(total), fmt.Sprintf("Waiting %.1f s for the printer", wait.Seconds()))
				if err := d.sleep(ctx, wait); err != nil {
					return printer.Result{Printed: printed}, partial(ctx, err, printed, len(labels))
				}
			}
			base := done
			c := ble.Chunker{Role: "data", Chunk: chunk, Gap: gap, Sleep: d.sleep,
				OnChunk: func(sent, _ int) { pc.Report(float64(base+sent)/float64(total), message) }}
			if _, err := c.Write(ctx, link, job.Data); err != nil {
				return printer.Result{Printed: printed}, partial(ctx, err, printed, len(labels))
			}
			done += len(job.Data)
			st.readyAt = d.now().Add(job.Pause())
		}
		printed++
	}
	details["jobs"] = jobCount
	details["bytes"] = total
	pc.Report(1, "Sent "+thermal.Labels(printed))
	return printer.Result{OK: true, Printed: printed, Message: "Sent " + thermal.Labels(printed) + ".", Details: details}, nil
}

// advertisedName is the printer's name as the link, the settings or the saved target
// know it, in that order.
func advertisedName(pc *printer.PrintContext, link printer.Link, opts printer.Settings) string {
	if n, ok := link.(interface{ Name() string }); ok && n.Name() != "" {
		return n.Name()
	}
	if name := opts.String(SettingBLEName, ""); name != "" {
		return name
	}
	if pc.Target != nil {
		return pc.Target.Name
	}
	return ""
}

// partial says how many labels went out before a failure. A cancelled or expired
// context is returned as is, so errors.Is(err, context.Canceled) holds; the Result
// carries the count.
func partial(ctx context.Context, err error, printed, total int) error {
	if printed == 0 || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return printer.Errorf("%v (%d of %s were sent.)", err, printed, thermal.Labels(total))
}

// listen reads frames for up to window and returns the first that want accepts, or
// nil. Frames it passes over are logged.
func listen(ctx context.Context, link printer.Link, window time.Duration, want func([]byte) bool) []byte {
	deadline := time.Now().Add(window)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		frame, err := link.Recv(ctx, remaining)
		if err != nil {
			return nil
		}
		if want(frame) {
			return frame
		}
		slog.Debug("phomemo notification", "frame", hex.EncodeToString(frame))
	}
}

// askFirmware sends FirmwareQuery and listens for the reply. A printer that does not
// answer is treated as current firmware.
func (d *Driver) askFirmware(ctx context.Context, link printer.Link) Version {
	if err := link.Write(ctx, FirmwareQuery, "data", false); err != nil {
		slog.Warn("phomemo: could not ask for the firmware version", "err", err)
		return nil
	}
	frame := listen(ctx, link, d.firmwareWindow, func(f []byte) bool {
		v, ok := firmwareIn(f)
		return ok && len(v) > 0
	})
	v, _ := firmwareIn(frame)
	return v
}

// familyModels is every model of the configured one's family in the job's catalog, in
// file order, with the configured one in its place. Without a catalog it is just the
// configured model.
func familyModels(cat *printer.Catalog, configured *printer.Model) []*printer.Model {
	if cat == nil {
		return []*printer.Model{configured}
	}
	var out []*printer.Model
	found := false
	for _, m := range cat.ModelList() {
		if m.Family != configured.Family {
			continue
		}
		if m.ID == configured.ID {
			m, found = configured, true
		}
		out = append(out, m)
	}
	if !found {
		out = append([]*printer.Model{configured}, out...)
	}
	return out
}
