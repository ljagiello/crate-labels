// Package printer holds the types every printer driver shares, the catalog of models
// and label stocks, and the dispatcher that connects drivers to links.
//
// Printer support works on three levels:
//
//  1. A new model in a family we already speak (e.g. another Phomemo): add a [[model]]
//     block to data/models/*.toml or to ~/.config/crate-labels/printers/*.toml. No code.
//  2. A new protocol family: add one package under internal/driver/ that implements
//     Driver and calls Register in init(), then blank-import it in cmd/crate-labels.
//     Links (BLE, TCP, IPP, CUPS) are shared, so a driver only encodes images.
//  3. Any printer macOS already has a driver for: the "cups" family prints through
//     CUPS, so nothing has to be written.
package printer

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"regexp"
	"time"
)

// MMPerInch converts between millimetres and inches.
const MMPerInch = 25.4

// LinkKind says how a printer is reached.
type LinkKind string

const (
	LinkBLE  LinkKind = "ble"  // GATT byte pipe
	LinkTCP  LinkKind = "tcp"  // raw socket, e.g. port 9100
	LinkIPP  LinkKind = "ipp"  // HTTP to an IPP endpoint (driver talks to Target.Address itself)
	LinkCUPS LinkKind = "cups" // a local CUPS queue (driver runs lp itself)
	LinkFile LinkKind = "file" // in-memory/fake byte link for dry runs and tests
)

// IsByteLink reports whether the dispatcher opens a Link for this kind.
func (k LinkKind) IsByteLink() bool { return k == LinkBLE || k == LinkTCP || k == LinkFile }

// MediaKind is the physical kind of a stock.
type MediaKind string

const (
	MediaDieCut     MediaKind = "die_cut"    // individual labels separated by gaps
	MediaContinuous MediaKind = "continuous" // tape or receipt; length follows content
	MediaSheet      MediaKind = "sheet"      // a page printed by a page printer
)

// SheetGrid imposes identical labels on a sheet.
type SheetGrid struct {
	PaperMM  [2]float64 // page width, height
	Rows     int
	Cols     int
	MarginMM [2]float64 // left, top of the first cell
	PitchMM  [2]float64 // distance between cell origins: x, y
	CellMM   [2]float64 // one cell; the design is centred in it, never scaled up
}

// Stock is a label stock in the orientation templates design on.
//
// DesignMM is the canvas a template draws on. FeedMM is the physical label as it leaves
// the printer: width across the head, then length along feed (0 for continuous media).
// RotateCW says how far a thermal driver turns the design before rasterising.
type Stock struct {
	ID       string
	Name     string
	Media    MediaKind
	DesignMM [2]float64
	FeedMM   [2]float64
	RotateCW int // 0, 90, 180 or 270
	Sheet    *SheetGrid
	Round    bool           // circular label (e.g. 7" centre label); cosmetic for previews
	Extra    map[string]any // family-specific knobs
}

// BLEProfile is everything the BLE link needs about a model.
type BLEProfile struct {
	NamePatterns []string          // case-insensitive regexes matched on the advertised name
	Service      string            // lower-case 128-bit UUID
	Write        map[string]string // role -> characteristic UUID, e.g. "data", "command", "image"
	Notify       string            // characteristic UUID, or ""
	ChunkBytes   int               // largest single GATT write (also capped by the link)
	ChunkGapMS   float64           // pause between chunks
}

// MatchesName reports whether an advertised name matches one of the name patterns.
// Patterns are case-insensitive; one that does not compile matches nothing (the
// catalog rejects those when it loads).
func (p *BLEProfile) MatchesName(name string) bool {
	if name == "" {
		return false
	}
	for _, pat := range p.NamePatterns {
		if re, err := regexp.Compile("(?i)" + pat); err == nil && re.MatchString(name) {
			return true
		}
	}
	return false
}

// Model is one printer model, loaded from TOML.
type Model struct {
	ID           string
	Label        string
	Family       string // driver key, e.g. "phomemo"
	Link         LinkKind
	DPI          float64     // use 203.2 for "8 dots per mm" thermal heads
	HeadDots     int         // printable dots across the head; 0 for page printers
	PaperWidthMM *[2]float64 // min, max roll width; nil for page printers
	Stocks       []string    // stocks known to work; others may still fit by rule
	BLE          *BLEProfile
	Options      Settings // family defaults, overridable per printer and per job
	Source       string   // file it came from, for error messages
}

// DotsPerMM is the model's resolution in dots per millimetre.
func (m *Model) DotsPerMM() float64 { return m.DPI / MMPerInch }

// Canvas is the exact pixel size a template must render for a (model, stock) pair.
type Canvas struct {
	Width, Height int
	DPI           float64
}

// Target is a concrete printer: a model plus an address on its link.
type Target struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	ModelID  string   `json:"model_id"`
	Address  string   `json:"address"` // BLE UUID, ipp://host:631/ipp/print, CUPS queue, host:port
	Settings Settings `json:"settings,omitempty"`
}

// ErrTimeout is returned by Link.Recv when no frame arrives in time.
var ErrTimeout = errors.New("timed out waiting for the printer")

// Link is a byte pipe to a device (BLE, TCP or fake). Drivers address characteristics
// by role names from BLEProfile.Write; non-BLE links ignore the role.
type Link interface {
	// MaxWrite is the largest payload a single Write may carry.
	MaxWrite() int
	// Write sends data as-is (one GATT write or one socket write). No chunking.
	Write(ctx context.Context, data []byte, role string, withResponse bool) error
	// Recv returns the next inbound frame (notification or socket read), or ErrTimeout.
	Recv(ctx context.Context, timeout time.Duration) ([]byte, error)
	Close() error
}

// Progress receives job progress; fraction is 0..1.
type Progress func(fraction float64, message string)

// PrintContext is what a driver gets for one job.
type PrintContext struct {
	Catalog  *Catalog // the catalog the job was dispatched from (e.g. to resolve sibling models)
	Model    *Model
	Stock    *Stock
	Target   *Target
	Link     Link     // nil for IPP and CUPS drivers, which use Target.Address
	Settings Settings // Model.Options, then Target.Settings, then per-job options
	Progress Progress
}

// Report sends progress if anyone is listening.
func (pc *PrintContext) Report(fraction float64, message string) {
	if pc.Progress != nil {
		pc.Progress(fraction, message)
	}
}

// Result describes a finished job.
type Result struct {
	OK      bool           `json:"ok"`
	Printed int            `json:"printed"` // labels sent and believed printed
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Error is a failure worth showing to the user as-is.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Errorf builds a user-facing *Error.
func Errorf(format string, args ...any) error { return &Error{Msg: fmt.Sprintf(format, args...)} }

// Driver turns rendered label images into a printer's wire format and sends them.
//
// Labels are *image.Gray, white = 255, exactly Canvas(model, stock) pixels, in design
// orientation. Rotation, head placement, tone mapping, dithering and packing are the
// driver's job.
type Driver interface {
	Family() string
	// Links lists every link kind the driver can talk over. Byte-stream protocols should
	// accept LinkBLE, LinkTCP and LinkFile alike.
	Links() []LinkKind
	// Canvas is the pixel size templates render at.
	Canvas(m *Model, s *Stock) Canvas
	// Preview is exactly what will print (dithered, in design orientation), honouring
	// calibration settings such as offsets and darkness.
	Preview(m *Model, s *Stock, label *image.Gray, settings Settings) (*image.Gray, error)
	// Send prints each label once, in order.
	Send(ctx context.Context, pc *PrintContext, labels []*image.Gray) (Result, error)
}

// DefaultCanvas is the design size at the model's resolution; most drivers use it.
// Sizes are rounded to the nearest pixel, halves to even.
func DefaultCanvas(m *Model, s *Stock) Canvas {
	d := m.DotsPerMM()
	return Canvas{Width: int(math.RoundToEven(s.DesignMM[0] * d)), Height: int(math.RoundToEven(s.DesignMM[1] * d)), DPI: m.DPI}
}
