package service_test

import (
	"context"
	"errors"
	"github.com/ljagiello/crate-labels/internal/audio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/audio/audiotest"
	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/service/servicetest"
	"github.com/ljagiello/crate-labels/internal/tempo"
)

var canned = tempo.Result{BPM: 174.02, Confidence: 0.83, Steady: true, Range: [2]float64{140, 190}, Seconds: 12,
	Alternatives: []tempo.Candidate{{BPM: 87.01, Score: 0.1, Relation: "half"}, {BPM: 116.01, Score: 0.05, Relation: "two-thirds"}}}

// countingAnalyzer replaces the estimator with one returning res (or err) and records
// each call's options and sample count.
type countingAnalyzer struct {
	mu    sync.Mutex
	calls []tempo.Options
	n     []int
	res   tempo.Result
	err   error
}

func (a *countingAnalyzer) install(s *service.Service) {
	s.AnalyzeTempo = func(_ context.Context, samples []float32, rate int, opt tempo.Options) (tempo.Result, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if rate != tempo.SampleRate {
			return tempo.Result{}, errors.New("wrong rate")
		}
		a.calls = append(a.calls, opt)
		a.n = append(a.n, len(samples))
		return a.res, a.err
	}
}

func (a *countingAnalyzer) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// clickWAV is a click track at the analysis rate (the resampler is tested in audio).
func clickWAV(seconds float64) []byte {
	return audiotest.WAV16(tempo.SampleRate, audiotest.Clicks(tempo.SampleRate, seconds, 174))
}

func TestDetectTempoCachesByAudioAndOptions(t *testing.T) {
	e := servicetest.New(t)
	a := &countingAnalyzer{res: canned}
	a.install(e.Service)
	wav := clickWAV(12)
	opt := tempo.Options{Styles: []string{" Hardcore ", "Breakbeat", "hardcore"}}
	res, err := e.Service.DetectTempo(ctx, wav, "clicks.wav", opt)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res, canned) {
		t.Fatalf("result = %+v", res)
	}
	if got := a.calls[0].Styles; !reflect.DeepEqual(got, []string{"Breakbeat", "Hardcore"}) {
		t.Errorf("styles passed on = %q", got)
	}
	if a.n[0] != 12*tempo.SampleRate {
		t.Errorf("analysed %d samples, want %d", a.n[0], 12*tempo.SampleRate)
	}
	// Same audio and the same styles in another order and case: served from the cache.
	again, err := e.Service.DetectTempo(ctx, wav, "", tempo.Options{Styles: []string{"breakbeat", "HARDCORE"}})
	if err != nil || !reflect.DeepEqual(again, canned) || a.count() != 1 {
		t.Fatalf("cache hit: %+v, %v, %d analyses", again, err, a.count())
	}
	// Different styles, a range, or different audio: analysed again.
	_, _ = e.Service.DetectTempo(ctx, wav, "", tempo.Options{Styles: []string{"House"}})
	_, _ = e.Service.DetectTempo(ctx, wav, "", tempo.Options{Styles: []string{"Hardcore", "Breakbeat"}, MinBPM: 140, MaxBPM: 190})
	_, _ = e.Service.DetectTempo(ctx, clickWAV(13), "", opt)
	if a.count() != 4 {
		t.Fatalf("%d analyses, want 4", a.count())
	}
	// A failed analysis is not cached.
	a.err = tempo.ErrNoPulse
	if _, err := e.Service.DetectTempo(ctx, clickWAV(14), "", opt); err == nil {
		t.Fatal("no error")
	}
	if _, err := e.Service.DetectTempo(ctx, clickWAV(14), "", opt); err == nil || a.count() != 6 {
		t.Fatalf("errors were cached: %v, %d analyses", err, a.count())
	}
}

func TestDetectTempoNeverReturnsNullAlternatives(t *testing.T) {
	e := servicetest.New(t)
	a := &countingAnalyzer{res: tempo.Result{BPM: 120}}
	a.install(e.Service)
	for range 2 { // computed, then from the cache
		res, err := e.Service.DetectTempo(ctx, clickWAV(10), "", tempo.Options{})
		if err != nil || res.Alternatives == nil {
			t.Fatalf("%+v %v", res, err)
		}
	}
}

func TestDetectTempoFileCachesByPathSizeAndTime(t *testing.T) {
	e := servicetest.New(t)
	a := &countingAnalyzer{res: canned}
	a.install(e.Service)
	path := filepath.Join(t.TempDir(), "clicks.wav")
	if err := os.WriteFile(path, clickWAV(10), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := e.Service.DetectTempoFile(ctx, path, tempo.Options{}, false); err != nil {
			t.Fatal(err)
		}
	}
	if a.count() != 1 {
		t.Fatalf("%d analyses, want 1", a.count())
	}
	if _, err := e.Service.DetectTempoFile(ctx, path, tempo.Options{}, true); err != nil || a.count() != 2 {
		t.Fatalf("refresh: %v, %d analyses", err, a.count())
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Service.DetectTempoFile(ctx, path, tempo.Options{}, false); err != nil || a.count() != 3 {
		t.Fatalf("after touching the file: %v, %d analyses", err, a.count())
	}
	if _, err := e.Service.DetectTempoFile(ctx, filepath.Join(t.TempDir(), "missing.wav"), tempo.Options{}, false); err == nil ||
		!strings.Contains(err.Error(), "There is no file") {
		t.Errorf("missing file: %v", err)
	}
}

func TestDetectTempoErrorsSayWhatToDo(t *testing.T) {
	e := servicetest.New(t)
	a := &countingAnalyzer{}
	a.install(e.Service)
	for _, c := range []struct {
		audio []byte
		err   error
		want  string
	}{
		{clickWAV(10), tempo.ErrTooShort, "This recording is too short to judge the tempo: give it at least 10 seconds."},
		{clickWAV(11), tempo.ErrNoPulse, "No steady beat found in this audio. Try a section with drums."},
		{[]byte("RIFF\x04\x00\x00\x00WAVE"), nil, "Can't read this audio file (a broken WAV file (no format or no audio data)). WAV, AIFF, MP3, AAC/M4A, ALAC and FLAC work."},
	} {
		a.err = c.err
		_, err := e.Service.DetectTempo(ctx, c.audio, "", tempo.Options{})
		status, user := service.StatusOf(err)
		if err == nil || err.Error() != c.want || status != 400 || !user {
			t.Errorf("got %d %v %q, want 400 %q", status, user, err, c.want)
		}
	}
}

func TestDetectTempoRunsTwoAtATime(t *testing.T) {
	e := servicetest.New(t)
	var running, most atomic.Int32
	release := make(chan struct{})
	e.Service.AnalyzeTempo = func(ctx context.Context, _ []float32, _ int, _ tempo.Options) (tempo.Result, error) {
		n := running.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		defer running.Add(-1)
		select {
		case <-release:
		case <-ctx.Done():
			return tempo.Result{}, ctx.Err()
		}
		return canned, nil
	}
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() {
			if _, err := e.Service.DetectTempo(ctx, clickWAV(10+float64(i)), "", tempo.Options{}); err != nil {
				t.Error(err)
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // give a third the chance to start, if it could
	if most.Load() != 2 {
		t.Fatalf("%d analyses ran at once, want 2", most.Load())
	}
	// One waiting for a slot gives up when its caller does.
	c, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := e.Service.DetectTempo(c, clickWAV(20), "", tempo.Options{}); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: %v", err)
	}
	close(release)
	wg.Wait()
	if most.Load() != 2 {
		t.Fatalf("%d analyses ran at once, want 2", most.Load())
	}
}

func TestTempoOptionsAndRange(t *testing.T) {
	for in, want := range map[string][2]float64{"140-190": {140, 190}, " 85 – 175 ": {85, 175}, "160..180": {160, 180}, "87.5-175": {87.5, 175}} {
		lo, hi, err := service.ParseBPMRange(in)
		if err != nil || lo != want[0] || hi != want[1] {
			t.Errorf("ParseBPMRange(%q) = %v %v %v", in, lo, hi, err)
		}
	}
	for _, in := range []string{"", "140", "190-140", "abc-def", "10-20", "140-500", "-140-190", "140-"} {
		if _, _, err := service.ParseBPMRange(in); err == nil {
			t.Errorf("ParseBPMRange(%q) accepted", in)
		}
	}
	if _, err := service.TempoOptions(nil, 140, 0); err == nil {
		t.Error("half a range accepted")
	}
	opt, err := service.TempoOptions(service.SplitStyles("Hardcore, ,Breakbeat,  Drum n  Bass "), 0, 0)
	if err != nil || !reflect.DeepEqual(opt.Styles, []string{"Breakbeat", "Drum n Bass", "Hardcore"}) || opt.MinBPM != 0 {
		t.Errorf("options = %+v %v", opt, err)
	}
}

func TestTempoLine(t *testing.T) {
	if got := service.TempoLine(canned); got != "174.02 BPM  (confidence 0.83, steady)  alternatives: 87.01 (half), 116.01 (two-thirds)" {
		t.Errorf("line = %q", got)
	}
	if got := service.TempoLine(tempo.Result{BPM: 128, Confidence: 0.3}); got != "128.00 BPM  (confidence 0.30, tempo varies)" {
		t.Errorf("line = %q", got)
	}
}

func TestDetectTempoVideo(t *testing.T) {
	e := servicetest.New(t)
	a := &countingAnalyzer{res: canned}
	a.install(e.Service)
	var fetched []string
	e.Service.DecodeVideo = func(ctx context.Context, id string, rate int, maxSeconds float64) ([]float32, error) {
		fetched = append(fetched, id)
		if id == "missingxxxx" {
			return nil, &audio.YouTubeError{Reason: "Video unavailable"}
		}
		if id == "noytdlpxxxx" {
			return nil, audio.ErrNoYtDlp
		}
		return make([]float32, 12*rate), nil
	}
	opt := tempo.Options{Styles: []string{"Hard House"}}
	for _, link := range []string{"https://www.youtube.com/watch?v=nMP1QjHNDeA", "https://youtu.be/nMP1QjHNDeA"} {
		res, err := e.Service.DetectTempoVideo(ctx, link, opt, false)
		if err != nil || !reflect.DeepEqual(res, canned) {
			t.Fatalf("%s: %+v, %v", link, res, err)
		}
	}
	if len(fetched) != 1 || fetched[0] != "nMP1QjHNDeA" || a.count() != 1 {
		t.Fatalf("fetched %v, %d analyses; want one of each (the second link is the same video, cached)", fetched, a.count())
	}
	if _, err := e.Service.DetectTempoVideo(ctx, "https://www.youtube.com/watch?v=nMP1QjHNDeA", opt, true); err != nil || len(fetched) != 2 {
		t.Fatalf("refresh: %v, fetched %v", err, fetched)
	}
	for link, want := range map[string]string{
		"https://vimeo.com/12345":                     "Only YouTube links",
		"https://www.youtube.com/watch?v=missingxxxx": "Video unavailable",
		"https://www.youtube.com/watch?v=noytdlpxxxx": "brew install yt-dlp",
	} {
		_, err := e.Service.DetectTempoVideo(ctx, link, opt, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", link, err, want)
		}
	}
}
