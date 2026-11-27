// Package cups is the "cups" printer family: any printer macOS can print to, through
// CUPS. The target address is the queue name.
//
// Labels are imposed (sheets.Impose), written as a PDF at the exact paper size, and
// handed to lp with scaling off. The media is the queue's closest named size (within
// sheets.SizeToleranceMM, either orientation) or Custom.WxHmm. Queues come from lpstat, sizes from
// lpoptions -p Q -l plus the queue's PPD. Commands run through a Runner with argument
// lists, never a shell, so tests swap in a fake. Brother QL, Dymo and Zebra printers
// with macOS drivers work this way with no code of their own.
package cups

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/ljagiello/crate-labels/internal/driver/internal/page"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/sheets"
)

func init() { printer.Register(&Driver{}) }

// RunResult is a finished command.
type RunResult struct {
	Code           int
	Stdout, Stderr string
}

// Runner runs argv (argv[0] is the program) and returns its exit code and output. A
// non-zero exit is a RunResult, not an error; errors mean the command could not run.
type Runner func(ctx context.Context, argv []string) (RunResult, error)

// ExecRunner runs argv with exec.CommandContext, without a shell, in the C locale so the
// output parses the same everywhere.
func ExecRunner(ctx context.Context, argv []string) (RunResult, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && ctx.Err() == nil:
		return RunResult{Code: exit.ExitCode(), Stdout: out.String(), Stderr: errOut.String()}, nil
	case errors.Is(err, exec.ErrNotFound):
		return RunResult{}, printer.Errorf("%s not found; is CUPS installed?", argv[0])
	case ctx.Err() != nil:
		return RunResult{}, ctx.Err()
	default:
		return RunResult{}, fmt.Errorf("run %s: %w", argv[0], err)
	}
	return RunResult{Stdout: out.String(), Stderr: errOut.String()}, nil
}

// Driver prints through CUPS. The zero value runs the real commands.
type Driver struct {
	Runner  Runner // nil means ExecRunner
	PPDDir  string // "" means DefaultPPDDir
	TempDir string // where the PDF is written for lp; "" means os.TempDir()
}

func (d *Driver) runner() Runner {
	if d.Runner != nil {
		return d.Runner
	}
	return ExecRunner
}

// Family implements printer.Driver.
func (*Driver) Family() string { return "cups" }

// Links implements printer.Driver: the driver runs lp itself.
func (*Driver) Links() []printer.LinkKind { return []printer.LinkKind{printer.LinkCUPS} }

// Canvas implements printer.Driver: the design size at the model's dpi.
func (*Driver) Canvas(m *printer.Model, s *printer.Stock) printer.Canvas {
	return printer.DefaultCanvas(m, s)
}

// Preview implements printer.Driver: the imposed first page for sheet stocks, else the
// label itself (it prints at actual size).
func (*Driver) Preview(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) (*image.Gray, error) {
	if s.Sheet == nil {
		return sheets.Normalize(label), nil
	}
	return sheets.Impose([]*image.Gray{label}, s, m.DPI, page.CropMarks(s, settings))[0], nil
}

var (
	queueRe     = regexp.MustCompile(`^[^\s/#\-][^\s/#]*(/\S+)?$`)
	optionKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]*$`)
	requestIDRe = regexp.MustCompile(`request id is (\S+)`)
)

// LPCommand is the lp argument list. Scaling is off so a PDF page prints at its true
// size. Extra options are added in key order.
func LPCommand(queue, pdfPath, media, title string, options map[string]string) ([]string, error) {
	if !queueRe.MatchString(queue) {
		return nil, printer.Errorf("%q is not a CUPS queue name", queue)
	}
	argv := []string{"lp", "-d", queue, "-t", title, "-o", "media=" + media,
		"-o", "fit-to-page=false", "-o", "scaling=100", "-o", "print-scaling=none"}
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := options[k]
		if !optionKeyRe.MatchString(k) || strings.ContainsAny(v, "\r\n") {
			return nil, printer.Errorf("bad CUPS option %s=%q", k, v)
		}
		argv = append(argv, "-o", k+"="+v)
	}
	return append(argv, pdfPath), nil
}

// ParseRequestID reads lp's "request id is Q-17 (1 file(s))".
func ParseRequestID(stdout string) string {
	if m := requestIDRe.FindStringSubmatch(stdout); m != nil {
		return m[1]
	}
	return ""
}

// place centres page at actual size on a sheet of paperMM, turned clockwise only when
// that is what makes it fit.
func place(page *image.Gray, paperMM [2]float64, dpi float64) *image.Gray {
	w, h := sheets.MMToPx(paperMM[0], dpi), sheets.MMToPx(paperMM[1], dpi)
	pw, ph := page.Bounds().Dx(), page.Bounds().Dy()
	if (pw > w || ph > h) && ph <= w && pw <= h {
		page = sheets.Rotate(page, 90)
	}
	out := sheets.White(w, h)
	sheets.PasteCentred(out, page)
	return out
}

// cupsOptions reads the cups_options setting (a table of extra lp -o options).
func cupsOptions(s printer.Settings) map[string]string {
	out := map[string]string{}
	for k, v := range s.Map("cups_options") {
		out[k] = fmt.Sprint(v)
	}
	if m, ok := s["cups_options"].(map[string]string); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// Send implements printer.Driver.
func (d *Driver) Send(ctx context.Context, pc *printer.PrintContext, labels []*image.Gray) (printer.Result, error) {
	if len(labels) == 0 {
		return printer.Result{OK: true, Message: "nothing to print"}, nil
	}
	queue := strings.TrimSpace(pc.Target.Address)
	if queue == "" {
		return printer.Result{}, printer.Errorf("The CUPS printer %q has no queue name. Pick its queue from the printer list.", pc.Target.Name)
	}
	s, dpi := pc.Stock, pc.Model.DPI
	pc.Report(0, "Reading paper sizes for "+queue)
	media, err := d.QueueMedia(ctx, queue)
	if err != nil {
		return printer.Result{}, err
	}

	pages := sheets.Impose(labels, s, dpi, page.CropMarks(s, pc.Settings))
	paper := sheets.PageMM(s)
	name := BestMedia(media, paper)
	note := ""
	if name == "" {
		fallback := media.Size(media.Default)
		if s.Sheet != nil || fallback == nil {
			var sizes []string
			for _, m := range media.Sizes {
				sizes = append(sizes, m.Name)
			}
			list := strings.Join(sizes, ", ")
			if list == "" {
				list = "none"
			}
			return printer.Result{}, printer.Errorf("%s has no %g×%g mm paper size for %s and does not take that custom size. Its sizes: %s.",
				queue, paper[0], paper[1], s.Name, list)
		}
		name = fallback.Name
		paper = [2]float64{fallback.WidthMM, fallback.HeightMM}
		for i, p := range pages {
			pages[i] = place(p, paper, dpi)
		}
		note = fmt.Sprintf(" Each label was printed at actual size on a %s page.", name)
	}

	pc.Report(0.3, "Writing PDF")
	pdf := sheets.PDF(pages, paper, dpi)
	options := cupsOptions(pc.Settings)
	title := pc.Settings.String("job_name", "")
	if title == "" {
		title = "crate-labels"
	}
	details := map[string]any{"media": name, "pages": len(pages), "pdf_bytes": len(pdf)}
	if page.Flag(pc.Settings, "dry_run", false) {
		argv, err := LPCommand(queue, "<pdf>", name, title, options)
		if err != nil {
			return printer.Result{}, err
		}
		details["argv"] = argv
		return printer.Result{OK: true, Message: fmt.Sprintf("Dry run: would print %d page(s) on %s.", len(pages), name), Details: details}, nil
	}

	f, err := os.CreateTemp(d.TempDir, "crate-labels-*.pdf")
	if err != nil {
		return printer.Result{}, fmt.Errorf("write the PDF for lp: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(pdf); err != nil {
		f.Close()
		return printer.Result{}, fmt.Errorf("write the PDF for lp: %w", err)
	}
	if err := f.Close(); err != nil {
		return printer.Result{}, fmt.Errorf("write the PDF for lp: %w", err)
	}
	argv, err := LPCommand(queue, path, name, title, options)
	if err != nil {
		return printer.Result{}, err
	}
	details["argv"] = argv
	pc.Report(0.6, "Sending to "+queue)
	res, err := d.runner()(ctx, argv)
	if err != nil {
		return printer.Result{}, err
	}
	if res.Code != 0 {
		reason := strings.TrimSpace(res.Stderr)
		if reason == "" {
			reason = strings.TrimSpace(res.Stdout)
		}
		if reason == "" {
			reason = fmt.Sprintf("exit %d", res.Code)
		}
		return printer.Result{}, printer.Errorf("CUPS refused the job for %s: %s", queue, reason)
	}
	details["job"] = ParseRequestID(res.Stdout)
	pc.Report(1, "Sent")
	return printer.Result{OK: true, Printed: len(labels), Message: fmt.Sprintf("Sent %d label(s) to %s.%s", len(labels), queue, note), Details: details}, nil
}
