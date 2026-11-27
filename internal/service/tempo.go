package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/audio"
	"github.com/ljagiello/crate-labels/internal/discogs"
	"github.com/ljagiello/crate-labels/internal/tempo"
)

const (
	// analysisVersion is part of every tempo cache key. Bump it when the estimator in
	// internal/tempo changes its answers, so old results are not served.
	analysisVersion = "3"
	// maxTempoAnalyses bounds concurrent decodes and analyses: both are CPU-heavy.
	maxTempoAnalyses = 2
	// TempoMaxSeconds is how much of a recording is analysed, so a DJ mix cannot use up
	// the memory.
	TempoMaxSeconds = 900
	// Tempo ranges users may ask for.
	minRangeBPM, maxRangeBPM = 30, 400
)

// TempoOptions checks a requested style list and BPM range (both 0 = none) and
// normalises them: styles trimmed, de-duplicated and sorted.
func TempoOptions(styles []string, minBPM, maxBPM float64) (tempo.Options, error) {
	if (minBPM == 0) != (maxBPM == 0) {
		return tempo.Options{}, Errorf("Give both ends of the BPM range, e.g. 140-190.")
	}
	if minBPM != 0 && (minBPM < minRangeBPM || maxBPM > maxRangeBPM || maxBPM <= minBPM) {
		return tempo.Options{}, Errorf("The BPM range runs from low to high between %d and %d, e.g. 140-190.", minRangeBPM, maxRangeBPM)
	}
	return normalizeTempoOptions(tempo.Options{Styles: styles, MinBPM: minBPM, MaxBPM: maxBPM}), nil
}

// ParseBPMRange reads a range written "140-190" (also "140–190" or "140..190").
func ParseBPMRange(s string) (lo, hi float64, err error) {
	bad := Errorf("Write the BPM range as low-high, e.g. 140-190.")
	s = strings.TrimSpace(s)
	var a, b string
	var ok bool
	for _, sep := range []string{"..", "–", "-"} {
		if a, b, ok = strings.Cut(s, sep); ok {
			break
		}
	}
	if !ok {
		return 0, 0, bad
	}
	lo, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	hi, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, bad
	}
	if _, err := TempoOptions(nil, lo, hi); err != nil {
		return 0, 0, err
	}
	return lo, hi, nil
}

// SplitStyles splits a comma-separated style list ("Hardcore, Breakbeat").
func SplitStyles(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeTempoOptions(opt tempo.Options) tempo.Options {
	seen := map[string]bool{}
	styles := []string{}
	for _, st := range opt.Styles {
		st = strings.Join(strings.Fields(st), " ")
		if k := strings.ToLower(st); st != "" && !seen[k] && len(styles) < 32 {
			seen[k] = true
			styles = append(styles, st)
		}
	}
	slices.SortFunc(styles, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	out := tempo.Options{Styles: styles}
	if opt.MinBPM > 0 && opt.MaxBPM > opt.MinBPM {
		out.MinBPM, out.MaxBPM = opt.MinBPM, opt.MaxBPM
	}
	return out
}

// tempoKey is the cache key: a hash, so no file path is stored.
func tempoKey(content string, opt tempo.Options) string {
	styles := make([]string, len(opt.Styles))
	for i, st := range opt.Styles {
		styles[i] = strings.ToLower(st)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"tempo", analysisVersion, content, strings.Join(styles, ","),
		strconv.FormatFloat(opt.MinBPM, 'g', -1, 64), strconv.FormatFloat(opt.MaxBPM, 'g', -1, 64)}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// DetectTempo estimates the tempo of an audio file held in memory (an upload). nameHint
// is the file's name, used only as a format hint. Results are cached by the audio's
// hash and the options.
func (s *Service) DetectTempo(ctx context.Context, data []byte, nameHint string, opt tempo.Options) (tempo.Result, error) {
	sum := sha256.Sum256(data)
	return s.detectTempo(ctx, "sha256:"+hex.EncodeToString(sum[:]), opt, false, func(ctx context.Context) ([]float32, error) {
		return audio.DecodeBytes(ctx, data, nameHint, tempo.SampleRate, TempoMaxSeconds)
	})
}

// DetectTempoVideo estimates the tempo of a YouTube video (a release's track video on
// Discogs). Results are cached by video id and the options, so a video is fetched once.
func (s *Service) DetectTempoVideo(ctx context.Context, link string, opt tempo.Options, refresh bool) (tempo.Result, error) {
	id := discogs.YouTubeID(link)
	if id == "" {
		return tempo.Result{}, Errorf("Only YouTube links can be analysed, e.g. https://www.youtube.com/watch?v=…")
	}
	return s.detectTempo(ctx, "youtube:"+id, opt, refresh, func(ctx context.Context) ([]float32, error) {
		return s.DecodeVideo(ctx, id, tempo.SampleRate, TempoMaxSeconds)
	})
}

// DetectTempoFile estimates the tempo of the audio file at path. Results are cached by
// the file's path, size and modification time and the options; refresh analyses the
// file again and replaces the cached result.
func (s *Service) DetectTempoFile(ctx context.Context, path string, opt tempo.Options, refresh bool) (tempo.Result, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return tempo.Result{}, err
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return tempo.Result{}, Errorf("There is no file at %s.", path)
	case err != nil:
		return tempo.Result{}, Errorf("Can't open %s: %v", path, errors.Unwrap(err))
	case info.IsDir():
		return tempo.Result{}, Errorf("%s is a folder; give an audio file.", path)
	}
	content := fmt.Sprintf("file:%s|%d|%d", abs, info.Size(), info.ModTime().UnixNano())
	return s.detectTempo(ctx, content, opt, refresh, func(ctx context.Context) ([]float32, error) {
		return audio.Decode(ctx, abs, tempo.SampleRate, TempoMaxSeconds)
	})
}

func (s *Service) detectTempo(ctx context.Context, content string, opt tempo.Options, refresh bool,
	decode func(context.Context) ([]float32, error)) (tempo.Result, error) {
	opt = normalizeTempoOptions(opt)
	key := tempoKey(content, opt)
	if !refresh {
		if res, ok := s.cachedTempo(ctx, key); ok {
			return res, nil
		}
	}
	// Stop when the caller goes away or the service closes.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case s.tempoSlots <- struct{}{}:
	case <-ctx.Done():
		return tempo.Result{}, ctx.Err()
	}
	defer func() { <-s.tempoSlots }()
	samples, err := decode(ctx)
	if err != nil {
		return tempo.Result{}, tempoError(ctx, err)
	}
	res, err := s.AnalyzeTempo(ctx, samples, tempo.SampleRate, opt)
	if err != nil {
		return tempo.Result{}, tempoError(ctx, err)
	}
	res = tidyTempo(res)
	if raw, err := json.Marshal(res); err == nil {
		if err := s.Store.TempoPut(ctx, key, raw); err != nil {
			s.Log.Warn("could not cache a tempo analysis", "err", err)
		}
	}
	return res, nil
}

func (s *Service) cachedTempo(ctx context.Context, key string) (tempo.Result, bool) {
	raw, ok, err := s.Store.TempoGet(ctx, key)
	if err != nil {
		s.Log.Warn("could not read the tempo cache", "err", err)
	}
	var res tempo.Result
	if !ok || json.Unmarshal(raw, &res) != nil {
		return tempo.Result{}, false
	}
	return tidyTempo(res), true
}

// tidyTempo keeps lists non-null for the API.
func tidyTempo(r tempo.Result) tempo.Result {
	if r.Alternatives == nil {
		r.Alternatives = []tempo.Candidate{}
	}
	return r
}

// tempoError says what went wrong in words the user can act on.
func tempoError(ctx context.Context, err error) error {
	var fe *audio.FormatError
	var ye *audio.YouTubeError
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, tempo.ErrTooShort):
		return Errorf("This recording is too short to judge the tempo: give it at least 10 seconds.")
	case errors.Is(err, tempo.ErrNoPulse):
		return Errorf("No steady beat found in this audio. Try a section with drums.")
	case errors.Is(err, audio.ErrTooLarge):
		return &Error{Status: http.StatusRequestEntityTooLarge,
			Msg: fmt.Sprintf("This audio file is over %d MB. Use one track rather than a whole mix.", audio.MaxBytes>>20)}
	case errors.Is(err, audio.ErrNoYtDlp):
		return Errorf("To use the release's YouTube videos, install yt-dlp (brew install yt-dlp), then try again.")
	case errors.As(err, &ye):
		return Errorf("Couldn't get this video's audio from YouTube: %s.", strings.TrimSuffix(ye.Reason, "."))
	case errors.As(err, &fe):
		return Errorf("Can't read this audio file (%s). WAV, AIFF, MP3, AAC/M4A, ALAC and FLAC work.", fe.Reason)
	}
	return err
}

// TempoLine is a result on one line, as the CLI prints it:
// "174.02 BPM  (confidence 0.83, steady)  alternatives: 87.01 (half), 116.01 (two-thirds)".
func TempoLine(r tempo.Result) string {
	steady := "steady"
	if !r.Steady {
		steady = "tempo varies"
	}
	line := fmt.Sprintf("%.2f BPM  (confidence %.2f, %s)", r.BPM, r.Confidence, steady)
	var alts []string
	for _, c := range r.Alternatives {
		a := fmt.Sprintf("%.2f", c.BPM)
		if c.Relation != "" {
			a += " (" + c.Relation + ")"
		}
		alts = append(alts, a)
	}
	if len(alts) > 0 {
		line += "  alternatives: " + strings.Join(alts, ", ")
	}
	return line
}
