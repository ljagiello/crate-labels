// Command crate-labels looks records up on Discogs and prints labels for your crates on
// Bluetooth thermal printers, Wi-Fi (IPP) printers or any CUPS queue.
//
//	crate-labels serve [--port 8787] [--open]
//	crate-labels printers [--scan] [--ipp] [--cups] [--timeout 6]
//	crate-labels print --release <id|url> --template <id> --stock <id> --printer <id|name>
//	                   [--copies N] [--option key=value ...] [--dry-run out.png]
//	crate-labels templates
//	crate-labels discogs token set|test|clear [--stdin]
//	crate-labels discogs lookup <id|url>
//	crate-labels bpm [--styles "Hardcore,Breakbeat"] [--range 140-190] [--json] FILE...
//	crate-labels version
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ljagiello/crate-labels/internal/driver/cups"
	"github.com/ljagiello/crate-labels/internal/ipp"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/store"
	"github.com/ljagiello/crate-labels/internal/transport/ble"
	"github.com/ljagiello/crate-labels/internal/web"

	// Every driver and transport registers itself in init().
	_ "github.com/ljagiello/crate-labels/internal/driver/clabel"
	_ "github.com/ljagiello/crate-labels/internal/driver/cups"
	_ "github.com/ljagiello/crate-labels/internal/driver/ipp"
	_ "github.com/ljagiello/crate-labels/internal/driver/munbyn"
	_ "github.com/ljagiello/crate-labels/internal/driver/phomemo"
	_ "github.com/ljagiello/crate-labels/internal/transport/ble"
	_ "github.com/ljagiello/crate-labels/internal/transport/ble/corebluetooth" // the tinygo CoreBluetooth backend; main only
	_ "github.com/ljagiello/crate-labels/internal/transport/fake"
	_ "github.com/ljagiello/crate-labels/internal/transport/tcp"
)

const usage = `crate-labels: look records up on Discogs and print labels for your crates.

Usage:
  crate-labels serve [--port 8787] [--open]        run the web UI on 127.0.0.1
  crate-labels printers [--scan] [--ipp] [--cups]  list saved printers; find new ones
  crate-labels print --release <id|url> --template <id> --stock <id> --printer <id|name>
                     [--copies N] [--option key=value ...] [--dry-run out.png]
  crate-labels templates                           list label templates and their options
  crate-labels discogs token set|test|clear [--stdin]
  crate-labels discogs lookup <id|url>             show what a label would contain
  crate-labels bpm [--styles "Hardcore,Breakbeat"] [--range 140-190] [--json] FILE...
                                                   detect the tempo of audio files
  crate-labels version                             print the version and build details

Run "crate-labels <command> -h" for a command's flags.
`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// run is the whole CLI; it returns the exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(rest, stdout, stderr)
	case "printers":
		err = cmdPrinters(rest, stdout, stderr)
	case "print":
		err = cmdPrint(rest, stdout, stderr)
	case "templates":
		err = cmdTemplates(rest, stdout)
	case "discogs":
		err = cmdDiscogs(rest, stdin, stdout, stderr)
	case "bpm":
		err = cmdBPM(rest, stdout, stderr)
	case "version", "--version", "-version":
		err = cmdVersion(stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "crate-labels: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	}
	fmt.Fprintf(stderr, "crate-labels: %v\n", err)
	return 1
}

// errUsage means the flag package already explained the mistake.
var errUsage = errors.New("usage")

// errNotTerminal means stdin is not a terminal, so input cannot be hidden.
var errNotTerminal = errors.New("stdin is not a terminal")

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage
	}
	return nil
}

// app is the service and what it needs, opened per command.
type app struct {
	store *store.Store
	cat   *printer.Catalog
	svc   *service.Service
}

func openApp(log *slog.Logger) (*app, error) {
	st, err := store.Open("")
	if err != nil {
		return nil, err
	}
	cat, err := printer.LoadCatalog("")
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("the printer catalog: %w", err)
	}
	svc := service.New(service.Config{Store: st, Catalog: cat, Log: log})
	return &app{store: st, cat: cat, svc: svc}, nil
}

func (a *app) close() {
	a.svc.Close()
	a.store.Close()
}

// --- serve -------------------------------------------------------------------------------

func cmdServe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", web.DefaultPort, "port on 127.0.0.1")
	open := fs.Bool("open", false, "open the UI in your browser")
	if err := parse(fs, args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a, err := openApp(log)
	if err != nil {
		return err
	}
	defer a.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := web.New(a.svc, web.Options{Discovery: discovery(a), DataDir: store.DataDir(), Log: log})
	return web.ListenAndServe(ctx, srv, *port, func(addr string) error {
		// Only the server that owns the port may mark a previous run's jobs interrupted;
		// a second `serve` fails to bind first and leaves the live server's jobs alone.
		if _, err := a.svc.Recover(ctx); err != nil {
			return err
		}
		url := "http://" + addr + "/"
		fmt.Fprintf(stdout, "crate-labels on %s  (Ctrl-C to stop)\n", url)
		if *open {
			_ = exec.Command("/usr/bin/open", url).Start()
		}
		return nil
	})
}

// discovery adapts the BLE, IPP and CUPS packages to the web API's shapes.
func discovery(a *app) web.Discovery {
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	return web.Discovery{
		BLEScan: func(ctx context.Context, timeout time.Duration, showAll bool) ([]web.BLEDevice, error) {
			scan := ble.Scan
			if showAll {
				scan = ble.ScanAll
			}
			found, err := scan(ctx, timeout, a.cat)
			if err != nil {
				return nil, err
			}
			out := make([]web.BLEDevice, 0, len(found))
			for _, d := range found {
				out = append(out, web.BLEDevice{Address: d.Address, Name: d.Name, RSSI: d.RSSI, ServiceUUIDs: d.ServiceUUIDs,
					ModelIDs: d.ModelIDs, MaybeFamily: opt(d.MaybeFamily)})
			}
			return out, nil
		},
		BLEProbe: func(ctx context.Context, address string, _ time.Duration) (web.BLEProbe, error) {
			hint := ble.Hint{}
			if saved, err := a.store.Printers(ctx); err == nil {
				for _, p := range saved {
					if strings.EqualFold(p.Address, address) {
						hint.KnownModel = p.ModelID
					}
				}
			}
			p, err := ble.ProbeHint(ctx, address, a.cat, hint)
			if err != nil {
				return web.BLEProbe{}, err
			}
			out := web.BLEProbe{Services: p.Services, Family: opt(p.Family), ModelID: opt(p.ModelID)}
			if len(p.Handshake) > 0 {
				out.Handshake = opt(hex.EncodeToString(p.Handshake))
			}
			return out, nil
		},
		IPPDiscover: func(ctx context.Context, timeout time.Duration) ([]web.IPPPrinter, error) {
			found, err := ipp.Discover(ctx, timeout)
			if err != nil {
				return nil, err
			}
			out := make([]web.IPPPrinter, 0, len(found))
			for _, p := range found {
				out = append(out, web.IPPPrinter{Name: p.Name, Host: p.Host, Port: p.Port, Resource: p.Resource, UUID: p.UUID,
					Model: p.Model, Formats: p.Formats, URF: p.URF, Kind: p.Kind, Server: p.Server, TXT: p.TXT,
					URI: p.URI(), HostnameURI: opt(p.HostnameURI()), Printable: p.Printable()})
			}
			return out, nil
		},
		CUPSQueues: func(ctx context.Context) ([]web.CUPSQueue, error) {
			queues, err := cups.ListQueues(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]web.CUPSQueue, 0, len(queues))
			for _, q := range queues {
				out = append(out, web.CUPSQueue{Name: q.Name, Description: q.Description, State: q.State, Enabled: q.Enabled,
					Message: q.Message, IsDefault: q.IsDefault})
			}
			return out, nil
		},
	}
}

// --- printers ------------------------------------------------------------------------------

func cmdPrinters(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("printers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scan := fs.Bool("scan", false, "scan for Bluetooth label printers")
	all := fs.Bool("all", false, "with --scan, list every Bluetooth device")
	findIPP := fs.Bool("ipp", false, "discover Wi-Fi (IPP) printers")
	findCUPS := fs.Bool("cups", false, "list macOS (CUPS) queues")
	timeout := fs.Duration("timeout", 6*time.Second, "how long to scan or discover")
	if err := parse(fs, args); err != nil {
		return err
	}
	a, err := openApp(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		return err
	}
	defer a.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	saved, err := a.store.Printers(ctx)
	if err != nil {
		return err
	}
	def := a.store.SettingString(ctx, "default_printer")
	if len(saved) == 0 {
		fmt.Fprintln(stdout, "No saved printers. Add one in the web UI (crate-labels serve --open).")
	} else {
		tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tNAME\tMODEL\tADDRESS")
		for _, p := range saved {
			mark := "  "
			if p.ID == def {
				mark = "* "
			}
			fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n", mark, p.ID, p.Name, p.ModelID, p.Address)
		}
		tw.Flush()
	}
	d := discovery(a)
	if *scan {
		fmt.Fprintf(stderr, "\nScanning Bluetooth for %s…\n", *timeout)
		devices, err := d.BLEScan(ctx, *timeout, *all)
		if err != nil {
			return fmt.Errorf("Bluetooth scan: %w", err)
		}
		if len(devices) == 0 {
			fmt.Fprintln(stdout, "No Bluetooth label printers found. Is it on and not connected to a phone?")
		} else {
			tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ADDRESS\tNAME\tRSSI\tMODELS")
			for _, dev := range devices {
				models := strings.Join(dev.ModelIDs, ", ")
				if models == "" && dev.MaybeFamily != nil {
					models = "looks like " + *dev.MaybeFamily
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", dev.Address, cmp(dev.Name, "?"), dev.RSSI, cmp(models, "-"))
			}
			tw.Flush()
		}
	}
	if *findIPP {
		fmt.Fprintf(stderr, "\nLooking for Wi-Fi printers for %s…\n", *timeout)
		found, err := d.IPPDiscover(ctx, *timeout)
		if err != nil {
			return fmt.Errorf("Wi-Fi discovery: %w", err)
		}
		for _, p := range found {
			fmt.Fprintf(stdout, "  %s  %s\n", p.Name, p.URI)
		}
		if len(found) == 0 {
			fmt.Fprintln(stdout, "No IPP printers found on this network.")
		}
	}
	if *findCUPS {
		queues, err := d.CUPSQueues(ctx)
		if err != nil {
			return fmt.Errorf("CUPS queues: %w", err)
		}
		fmt.Fprintln(stdout, "\nCUPS queues:")
		for _, q := range queues {
			fmt.Fprintf(stdout, "  %s  %s\n", q.Name, q.Description)
		}
	}
	return nil
}

func cmp(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// --- print -------------------------------------------------------------------------------------

// options collects repeatable --option key=value flags.
type options map[string]any

func (o options) String() string { return fmt.Sprint(map[string]any(o)) }

func (o options) Set(v string) error {
	key, value, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("--option needs key=value, got %q", v)
	}
	o[strings.TrimSpace(key)] = strings.TrimSpace(value)
	return nil
}

func cmdPrint(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("print", flag.ContinueOnError)
	fs.SetOutput(stderr)
	release := fs.String("release", "", "Discogs release id or URL (a master URL prints its main release)")
	templateID := fs.String("template", "", "template id (see: crate-labels templates)")
	stockID := fs.String("stock", "", "label stock id, e.g. 40x30")
	printerName := fs.String("printer", "", "saved printer id or name (with --dry-run, also a model id)")
	copies := fs.Int("copies", 1, "copies (1-99)")
	dryRun := fs.String("dry-run", "", "write the exact 1-bit preview to this PNG instead of printing")
	opts := options{}
	fs.Var(opts, "option", "template option key=value (repeatable)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *release == "" || *templateID == "" || *stockID == "" {
		fmt.Fprintln(stderr, "crate-labels print: --release, --template and --stock are required")
		fs.Usage()
		return errUsage
	}
	a, err := openApp(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		return err
	}
	defer a.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	src := service.Source{Ref: *release}
	if *dryRun != "" {
		req := service.PreviewRequest{Source: src, Template: *templateID, Stock: *stockID, Options: opts}
		if _, isModel := a.cat.Models[*printerName]; isModel {
			req.ModelID = *printerName
		} else {
			req.PrinterID = *printerName
		}
		p, err := a.svc.Preview(ctx, req)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(*dryRun), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*dryRun, p.PNG, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Wrote %s (%d×%d px at %g dpi, %s). Nothing was printed.\n",
			*dryRun, p.Canvas.Width, p.Canvas.Height, p.Canvas.DPI, p.ModelID)
		return nil
	}
	if *printerName == "" {
		return errors.New("--printer is required unless you use --dry-run")
	}
	events, unsubscribe := a.svc.Subscribe()
	defer unsubscribe()
	job, err := a.svc.Print(ctx, service.PrintRequest{Sources: []service.Source{src}, Template: *templateID, Stock: *stockID,
		Printer: *printerName, Copies: *copies, Options: opts, Origin: "cli"})
	if err != nil {
		return err
	}
	go func() {
		for j := range events {
			if j.ID == job.ID {
				fmt.Fprintf(stderr, "\r[%s] %3.0f%%  %-60.60s", j.Status, j.Progress*100, j.Message)
			}
		}
	}()
	done, err := a.svc.Wait(ctx, job.ID)
	fmt.Fprintln(stderr)
	if err != nil {
		if ctx.Err() != nil {
			_, _ = a.svc.Cancel(context.Background(), job.ID)
			done, _ = a.svc.Wait(context.Background(), job.ID)
			return fmt.Errorf("job %d %s after %d of %d labels", done.ID, done.Status, done.Printed, done.Total)
		}
		return err
	}
	if done.Status != store.JobDone {
		msg := done.Error
		if msg == "" {
			msg = done.Message
		}
		return fmt.Errorf("job %d %s: %s", done.ID, done.Status, msg)
	}
	noun := "labels"
	if done.Printed == 1 {
		noun = "label"
	}
	fmt.Fprintf(stdout, "Printed %d %s on %s.\n", done.Printed, noun, done.PrinterName)
	return nil
}

// --- templates ----------------------------------------------------------------------------------

func cmdTemplates(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("templates", flag.ContinueOnError)
	if err := parse(fs, args); err != nil {
		return err
	}
	a, err := openApp(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer a.close()
	for _, t := range a.svc.Layouts.Templates() {
		fmt.Fprintf(stdout, "%-14s %s: %s\n", t.ID(), t.Name(), t.Description())
		fmt.Fprintf(stdout, "%-14s stocks: %s\n", "", strings.Join(a.svc.TemplateStocks(t), ", "))
		for _, o := range t.Options() {
			extra := ""
			if len(o.Choices) > 0 {
				extra = " [" + strings.Join(o.Choices, ", ") + "]"
			}
			fmt.Fprintf(stdout, "%-14s --option %s=… (%s, default %v)%s\n", "", o.ID, o.Type, o.Default, extra)
		}
	}
	return nil
}

// --- discogs ------------------------------------------------------------------------------------

func cmdDiscogs(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, "usage: crate-labels discogs token set|test|clear [--stdin]\n       crate-labels discogs lookup <id|url>\n")
		return errUsage
	}
	a, err := openApp(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		return err
	}
	defer a.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "token":
		fs := flag.NewFlagSet("discogs token", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fromStdin := fs.Bool("stdin", false, "read the token from standard input")
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: crate-labels discogs token set|test|clear [--stdin]")
			return errUsage
		}
		action := args[1]
		if err := parse(fs, args[2:]); err != nil {
			return err
		}
		switch action {
		case "set":
			token, err := readToken(stdin, stderr, *fromStdin)
			if err != nil {
				return err
			}
			st, err := a.svc.SetToken(ctx, token)
			if err != nil {
				return err
			}
			if st.Warning != "" {
				fmt.Fprintln(stdout, st.Warning)
				return nil
			}
			fmt.Fprintf(stdout, "Saved to the macOS Keychain. Signed in as %s.\n", deref(st.Username))
		case "test":
			st, err := a.svc.TestToken(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Token OK (%s): %s\n", st.Source, deref(st.Username))
		case "clear":
			if _, err := a.svc.ClearToken(ctx); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "Removed the Discogs token from the Keychain.")
		default:
			fmt.Fprintf(stderr, "crate-labels: unknown token action %q (set, test or clear)\n", action)
			return errUsage
		}
		return nil
	case "lookup":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: crate-labels discogs lookup <id|url>")
			return errUsage
		}
		l, err := a.svc.LookupText(ctx, args[1], false)
		if err != nil {
			return err
		}
		r, d := l.Release, l.Data
		fmt.Fprintf(stdout, "%s – %s  [%s %s, %s]\n", r.Artist, r.Title, r.Label, r.Catno, r.Year)
		genre := d.Styles
		if len(genre) == 0 {
			genre = d.Genres
		}
		fmt.Fprintf(stdout, "  %s   genre: %s\n", r.Format, strings.Join(genre, ", "))
		for _, t := range d.Tracks {
			fmt.Fprintf(stdout, "  %-4s %s  %s\n", t.Position, t.Title, t.Duration)
		}
		fmt.Fprintf(stdout, "  %s\n", r.URL)
		return nil
	}
	fmt.Fprintf(stderr, "crate-labels: unknown discogs command %q (token or lookup)\n", args[0])
	return errUsage
}

func deref(s *string) string {
	if s == nil {
		return "(unknown account)"
	}
	return *s
}

// readToken reads the token without echoing it on a terminal, and never from argv.
func readToken(stdin io.Reader, stderr io.Writer, fromStdin bool) (string, error) {
	if !fromStdin {
		if f, ok := stdin.(*os.File); ok {
			fmt.Fprint(stderr, "Create a personal access token at https://www.discogs.com/settings/developers\n"+
				"and paste it here (input is hidden): ")
			token, err := readHidden(f)
			fmt.Fprintln(stderr)
			if err == nil {
				return strings.TrimSpace(token), nil
			}
			if !errors.Is(err, errNotTerminal) {
				return "", err
			}
		}
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "" {
		return "", errors.New("no token given")
	}
	return strings.TrimSpace(line), nil
}
