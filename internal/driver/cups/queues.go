package cups

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/sheets"
)

// DefaultPPDDir is where macOS keeps each queue's PPD.
const DefaultPPDDir = "/private/etc/cups/ppd"

const ptPerInch = 72.0

// namedSizesMM are common size names that carry no dimensions (mm, portrait).
var namedSizesMM = map[string][2]float64{
	"letter":             {215.9, 279.4},
	"legal":              {215.9, 355.6},
	"tabloid":            {279.4, 431.8},
	"ledger":             {431.8, 279.4},
	"executive":          {184.15, 266.7},
	"statement":          {139.7, 215.9},
	"a3":                 {297, 420},
	"a4":                 {210, 297},
	"a5":                 {148, 210},
	"a6":                 {105, 148},
	"b5":                 {182, 257},
	"isob5":              {176, 250},
	"env10":              {104.775, 241.3},
	"envdl":              {110, 220},
	"envc5":              {162, 229},
	"envmonarch":         {98.425, 190.5},
	"postcard":           {100, 148},
	"oficio":             {215.9, 340.36},
	"fanfoldgermanlegal": {215.9, 330.2},
}

// Queue is one CUPS destination. Its JSON is the docs/API.md queue shape plus "media".
type Queue struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	State       string `json:"state"` // idle, printing, disabled or unknown
	Enabled     bool   `json:"enabled"`
	Message     string `json:"message"`
	IsDefault   bool   `json:"is_default"`
	// Media is the queue's paper sizes; empty when lpoptions could not be read.
	Media QueueMedia `json:"media"`
}

// MediaSize is a named paper size in mm.
type MediaSize struct {
	Name     string  `json:"name"`
	WidthMM  float64 `json:"width_mm"`
	HeightMM float64 `json:"height_mm"`
}

// CustomRange is the custom page size limits in mm.
type CustomRange struct {
	Width, Height [2]float64 // min, max
}

// Allows reports whether w×h mm is inside the limits.
func (c CustomRange) Allows(w, h float64) bool {
	const tol = 0.05
	return c.Width[0]-tol <= w && w <= c.Width[1]+tol && c.Height[0]-tol <= h && h <= c.Height[1]+tol
}

// QueueMedia is what paper a queue offers.
type QueueMedia struct {
	Sizes         []MediaSize  `json:"sizes"`
	Default       string       `json:"default,omitempty"`
	AcceptsCustom bool         `json:"accepts_custom"`
	Custom        *CustomRange `json:"-"`
}

// Size returns the named size, or nil.
func (m QueueMedia) Size(name string) *MediaSize {
	for i := range m.Sizes {
		if m.Sizes[i].Name == name {
			return &m.Sizes[i]
		}
	}
	return nil
}

// --- lpstat ------------------------------------------------------------------------------

// ParseLpstatE lists the destinations printed by lpstat -e.
func ParseLpstatE(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

var lpstatDRe = regexp.MustCompile(`system default destination:\s*(\S+)`)

// ParseLpstatD returns the default destination from lpstat -d, or "".
func ParseLpstatD(text string) string {
	if m := lpstatDRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// QueueStatus is one printer's state from lpstat -p.
type QueueStatus struct {
	State       string
	Enabled     bool
	Description string
	Message     []string
}

var lpstatPRe = regexp.MustCompile(`^printer (\S+) (.*)$`)

// ParseLpstatP reads lpstat -l -p in the C locale. The indented lines straight after a
// "printer" line, before any "Key: value" line, are its state message.
func ParseLpstatP(text string) map[string]*QueueStatus {
	out := map[string]*QueueStatus{}
	var cur *QueueStatus
	inMessage := false
	for _, line := range strings.Split(text, "\n") {
		if m := lpstatPRe.FindStringSubmatch(line); m != nil {
			rest := m[2]
			cur = &QueueStatus{State: "unknown", Enabled: !strings.Contains(rest, "disabled")}
			switch {
			case strings.Contains(rest, "now printing"):
				cur.State = "printing"
			case strings.Contains(rest, "is idle"):
				cur.State = "idle"
			case strings.Contains(rest, "disabled"):
				cur.State = "disabled"
			}
			out[m[1]] = cur
			inMessage = true
			continue
		}
		if cur == nil || !(strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ")) {
			continue
		}
		item := strings.TrimSpace(line)
		if d, ok := strings.CutPrefix(item, "Description:"); ok {
			cur.Description = strings.TrimSpace(d)
		}
		if strings.Contains(item, ":") {
			inMessage = false
		} else if inMessage && item != "" {
			cur.Message = append(cur.Message, item)
		}
	}
	return out
}

// --- lpoptions and PPD -----------------------------------------------------------------

// Option is one line of lpoptions -p Q -l.
type Option struct {
	Key, Label string
	Choices    []string
	Default    string
}

var lpoptionsRe = regexp.MustCompile(`^([^/:\s]+)(?:/([^:]*))?:\s*(.*)$`)

// ParseLpoptions reads "Key/Label: choice *default choice" lines.
func ParseLpoptions(text string) map[string]Option {
	out := map[string]Option{}
	for _, line := range strings.Split(text, "\n") {
		m := lpoptionsRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		o := Option{Key: m[1], Label: m[2]}
		if o.Label == "" {
			o.Label = m[1]
		}
		for _, tok := range strings.Fields(m[3]) {
			if d, ok := strings.CutPrefix(tok, "*"); ok {
				tok, o.Default = d, d
			}
			o.Choices = append(o.Choices, tok)
		}
		out[o.Key] = o
	}
	return out
}

// PPDSizes is what a PPD says about paper.
type PPDSizes struct {
	PaperMM map[string][2]float64
	Custom  *CustomRange
	Default string
}

var (
	ppdDimRe     = regexp.MustCompile(`^\*PaperDimension\s+([^/:\s]+)[^:]*:\s*"([\d.]+)\s+([\d.]+)"`)
	ppdCustomRe  = regexp.MustCompile(`^\*ParamCustomPageSize\s+(Width|Height):\s*\d+\s+(\w+)\s+([\d.]+)\s+([\d.]+)`)
	ppdDefaultRe = regexp.MustCompile(`^\*DefaultPageSize:\s*(\S+)`)
	unitMM       = map[string]float64{"points": printer.MMPerInch / ptPerInch, "in": printer.MMPerInch, "cm": 10, "mm": 1}
)

func num(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// ParsePPD reads paper dimensions, custom size limits and the default size.
func ParsePPD(text string) PPDSizes {
	out := PPDSizes{PaperMM: map[string][2]float64{}}
	custom := map[string][2]float64{}
	pt := printer.MMPerInch / ptPerInch
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := ppdDimRe.FindStringSubmatch(line); m != nil {
			out.PaperMM[m[1]] = [2]float64{num(m[2]) * pt, num(m[3]) * pt}
		} else if m := ppdCustomRe.FindStringSubmatch(line); m != nil {
			if u, ok := unitMM[m[2]]; ok {
				custom[m[1]] = [2]float64{num(m[3]) * u, num(m[4]) * u}
			}
		} else if m := ppdDefaultRe.FindStringSubmatch(line); m != nil {
			out.Default = m[1]
		}
	}
	w, okW := custom["Width"]
	h, okH := custom["Height"]
	if okW && okH {
		out.Custom = &CustomRange{Width: w, Height: h}
	}
	return out
}

var (
	sizeUnitRe   = regexp.MustCompile(`(\d+(?:\.\d+)?)x(\d+(?:\.\d+)?)(mm|in)\b`)
	sizeInchesRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)x(\d+(?:\.\d+)?)$`)
	sizePointsRe = regexp.MustCompile(`^w(\d+(?:\.\d+)?)h(\d+(?:\.\d+)?)(?:\..*)?$`)
)

// SizeFromName reads dimensions from a size name: 62x29mm, 4x6in, 3x5 (CUPS: inches),
// w288h432 (points), a PWG 5101.1 self-describing name such as na_letter_8.5x11in
// (whose last part is WxH plus a unit), or a common name like Letter or A4.
func SizeFromName(name string) (w, h float64, ok bool) {
	if m := sizeUnitRe.FindStringSubmatch(name); m != nil {
		unit := 1.0
		if m[3] == "in" {
			unit = printer.MMPerInch
		}
		return num(m[1]) * unit, num(m[2]) * unit, true
	}
	if m := sizeInchesRe.FindStringSubmatch(name); m != nil {
		return num(m[1]) * printer.MMPerInch, num(m[2]) * printer.MMPerInch, true
	}
	if m := sizePointsRe.FindStringSubmatch(name); m != nil {
		pt := printer.MMPerInch / ptPerInch
		return num(m[1]) * pt, num(m[2]) * pt, true
	}
	base, _, _ := strings.Cut(name, ".")
	if s, found := namedSizesMM[strings.ToLower(base)]; found {
		return s[0], s[1], true
	}
	return 0, 0, false
}

// QueueMediaFrom combines lpoptions (PageSize or media) with the PPD (may be nil).
func QueueMediaFrom(options map[string]Option, ppd *PPDSizes) QueueMedia {
	var opt *Option
	for _, k := range []string{"PageSize", "media"} {
		if o, ok := options[k]; ok {
			opt = &o
			break
		}
	}
	var qm QueueMedia
	if opt != nil {
		for _, name := range opt.Choices {
			if strings.HasPrefix(name, "Custom.") {
				qm.AcceptsCustom = true
				continue
			}
			if ppd != nil {
				if d, ok := ppd.PaperMM[name]; ok {
					qm.Sizes = append(qm.Sizes, MediaSize{name, d[0], d[1]})
					continue
				}
			}
			if w, h, ok := SizeFromName(name); ok {
				qm.Sizes = append(qm.Sizes, MediaSize{name, w, h})
			}
		}
		qm.Default = opt.Default
	}
	if ppd != nil {
		if qm.Default == "" {
			qm.Default = ppd.Default
		}
		qm.Custom = ppd.Custom
	}
	return qm
}

func readPPD(dir, queue string) *PPDSizes {
	if dir == "" {
		dir = DefaultPPDDir
	}
	name, _, _ := strings.Cut(queue, "/")
	data, err := os.ReadFile(filepath.Join(dir, name+".ppd"))
	if err != nil {
		return nil
	}
	p := ParsePPD(string(data))
	return &p
}

// --- choosing a size -------------------------------------------------------------------

var edgeToEdgeRe = regexp.MustCompile(`(?i)\.(fullbleed|borderless)`)

func near(a, b float64) bool { return math.Abs(a-b) <= sheets.SizeToleranceMM }

// CustomName is the CUPS custom size for w×h mm, e.g. Custom.48x30.25mm.
func CustomName(w, h float64) string {
	f := func(v float64) string {
		s := strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0")
		return strings.TrimSuffix(s, ".")
	}
	return "Custom." + f(w) + "x" + f(h) + "mm"
}

// BestMedia picks a named size within sheets.SizeToleranceMM of pageMM (same
// orientation first, then turned; bordered sizes before edge-to-edge ones), else a
// Custom.WxHmm size the queue accepts, else "".
func BestMedia(m QueueMedia, pageMM [2]float64) string {
	w, h := pageMM[0], pageMM[1]
	var ordered []MediaSize
	for _, s := range m.Sizes {
		if !edgeToEdgeRe.MatchString(s.Name) {
			ordered = append(ordered, s)
		}
	}
	for _, s := range m.Sizes {
		if edgeToEdgeRe.MatchString(s.Name) {
			ordered = append(ordered, s)
		}
	}
	for _, s := range ordered {
		if near(s.WidthMM, w) && near(s.HeightMM, h) {
			return s.Name
		}
	}
	for _, s := range ordered {
		if near(s.WidthMM, h) && near(s.HeightMM, w) {
			return s.Name
		}
	}
	if m.AcceptsCustom && (m.Custom == nil || m.Custom.Allows(w, h)) {
		return CustomName(w, h)
	}
	return ""
}

// --- listing -----------------------------------------------------------------------------

// ListQueues lists every CUPS destination with its state and paper sizes, using
// lpstat and lpoptions. No printer is contacted beyond what those commands do.
func ListQueues(ctx context.Context) ([]Queue, error) { return (&Driver{}).ListQueues(ctx) }

// ListQueues is the package-level ListQueues with this driver's runner and PPD dir.
func (d *Driver) ListQueues(ctx context.Context) ([]Queue, error) {
	run := d.runner()
	res, err := run(ctx, []string{"lpstat", "-e"})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 { // "lpstat: No destinations added." exits 1
		return nil, nil
	}
	names := ParseLpstatE(res.Stdout)
	if len(names) == 0 {
		return nil, nil
	}
	var status map[string]*QueueStatus
	if r, err := run(ctx, []string{"lpstat", "-l", "-p"}); err == nil {
		status = ParseLpstatP(r.Stdout)
	}
	var def string
	if r, err := run(ctx, []string{"lpstat", "-d"}); err == nil {
		def = ParseLpstatD(r.Stdout)
	}
	queues := make([]Queue, 0, len(names))
	for _, n := range names {
		base, _, _ := strings.Cut(n, "/")
		q := Queue{Name: n, State: "unknown", Enabled: true, IsDefault: n == def}
		if s := status[base]; s != nil {
			q.Description, q.State, q.Enabled, q.Message = s.Description, s.State, s.Enabled, strings.Join(s.Message, " ")
		}
		if m, err := d.QueueMedia(ctx, n); err == nil {
			q.Media = m
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		queues = append(queues, q)
	}
	return queues, nil
}

// QueueMedia reads the sizes a queue offers (lpoptions -p Q -l), with dimensions from
// its PPD when readable, else from the size names.
func (d *Driver) QueueMedia(ctx context.Context, queue string) (QueueMedia, error) {
	res, err := d.runner()(ctx, []string{"lpoptions", "-p", queue, "-l"})
	if err != nil {
		return QueueMedia{}, err
	}
	if res.Code != 0 {
		return QueueMedia{}, printer.Errorf("CUPS has no printer queue %q (%s). Check the name in System Settings → Printers & Scanners.",
			queue, strings.TrimSpace(res.Stderr))
	}
	return QueueMediaFrom(ParseLpoptions(res.Stdout), readPPD(d.PPDDir, queue)), nil
}
