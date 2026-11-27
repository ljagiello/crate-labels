package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/tempo"
)

// --- bpm ---------------------------------------------------------------------------------

// cmdBPM prints the tempo of each audio file. A file that fails is reported and the rest
// still run; the exit code is non-zero if any failed. The flag helpers are in
// internal/service (ParseBPMRange, SplitStyles), where they are tested.
func cmdBPM(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bpm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	styles := fs.String("styles", "", `the record's Discogs genres and styles, comma separated (e.g. "Hardcore,Breakbeat"); they steer half or double tempo`)
	bpmRange := fs.String("range", "", "the tempo range to prefer, low-high (e.g. 140-190); overrides --styles")
	asJSON := fs.Bool("json", false, `print one JSON object per file: {"file", "result"} or {"file", "error"}`)
	noCache := fs.Bool("no-cache", false, "analyse again instead of using a cached result")
	release := fs.String("release", "", "a Discogs release link or id: detect each track's BPM from the YouTube videos linked on its page (needs yt-dlp)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, `usage: crate-labels bpm [--styles "Hardcore,Breakbeat"] [--range 140-190] [--json] [--no-cache] FILE...
       crate-labels bpm --release DISCOGS-LINK-OR-ID [--json] [--no-cache]`)
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	files := fs.Args()
	if *release != "" && len(files) > 0 {
		fmt.Fprintln(stderr, "crate-labels bpm: give either --release or files, not both")
		return errUsage
	}
	if len(files) == 0 && *release == "" {
		fmt.Fprintln(stderr, "crate-labels bpm: give at least one audio file (WAV, AIFF, MP3, AAC/M4A, ALAC or FLAC)")
		fs.Usage()
		return errUsage
	}
	var lo, hi float64
	if *bpmRange != "" {
		var err error
		if lo, hi, err = service.ParseBPMRange(*bpmRange); err != nil {
			fmt.Fprintf(stderr, "crate-labels bpm: --range: %v\n", err)
			return errUsage
		}
	}
	opt, err := service.TempoOptions(service.SplitStyles(*styles), lo, hi)
	if err != nil {
		return err
	}
	a, err := openApp(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		return err
	}
	defer a.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	enc := json.NewEncoder(stdout)
	type item struct {
		name string
		run  func() (tempo.Result, error)
	}
	var items []item
	for _, file := range files {
		items = append(items, item{file, func() (tempo.Result, error) { return a.svc.DetectTempoFile(ctx, file, opt, *noCache) }})
	}
	if *release != "" {
		lk, err := a.svc.LookupText(ctx, *release, false)
		if err != nil {
			return err
		}
		if *styles == "" && *bpmRange == "" {
			opt, _ = service.TempoOptions(append(append([]string{}, lk.Release.Styles...), lk.Release.Genres...), 0, 0)
		}
		if len(lk.Release.Videos) == 0 {
			return fmt.Errorf("%s – %s has no YouTube videos on Discogs; analyse its audio files, or use Listen… in the web UI (crate-labels serve)", lk.Release.Artist, lk.Release.Title)
		}
		for _, v := range lk.Release.Videos {
			name := v.Title
			if v.Track != "" {
				name = v.Track + "  " + v.Title
			}
			items = append(items, item{name, func() (tempo.Result, error) { return a.svc.DetectTempoVideo(ctx, v.URL, opt, *noCache) }})
		}
	}
	failed := 0
	for _, it := range items {
		file := it.name
		res, err := it.run()
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted")
		}
		switch {
		case err != nil && *asJSON:
			failed++
			_ = enc.Encode(struct {
				File  string `json:"file"`
				Error string `json:"error"`
			}{file, err.Error()})
		case err != nil:
			failed++
			fmt.Fprintf(stderr, "crate-labels: %s: %v\n", file, err)
		case *asJSON:
			_ = enc.Encode(struct {
				File   string       `json:"file"`
				Result tempo.Result `json:"result"`
			}{file, res})
		case len(items) > 1:
			fmt.Fprintf(stdout, "%s: %s\n", file, service.TempoLine(res))
		default:
			fmt.Fprintln(stdout, service.TempoLine(res))
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, len(items))
	}
	return nil
}
