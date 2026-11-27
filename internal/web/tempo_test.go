package web

import (
	"context"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ljagiello/crate-labels/internal/audio/audiotest"
	"github.com/ljagiello/crate-labels/internal/tempo"
)

// fakeTempo replaces the estimator with a canned answer and records its calls.
type fakeTempo struct {
	mu      sync.Mutex
	opts    []tempo.Options
	samples []int
	err     error
}

var cannedTempo = tempo.Result{BPM: 174.02, Confidence: 0.83, Steady: true, Range: [2]float64{140, 190}, Seconds: 12,
	Alternatives: []tempo.Candidate{{BPM: 87.01, Score: 0.1, Relation: "half"}}}

func (f *fakeTempo) install(e *env) {
	e.Service.AnalyzeTempo = func(_ context.Context, samples []float32, _ int, opt tempo.Options) (tempo.Result, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.opts = append(f.opts, opt)
		f.samples = append(f.samples, len(samples))
		return cannedTempo, f.err
	}
}

func (f *fakeTempo) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opts)
}

func clicks(seconds float64) string {
	return string(audiotest.WAV16(tempo.SampleRate, audiotest.Clicks(tempo.SampleRate, seconds, 174)))
}

// upload posts audio as the UI does: the raw file, as application/octet-stream.
func (e *env) upload(t *testing.T, path, body string, headers ...string) reply {
	t.Helper()
	return e.do(t, "POST", path, body, append([]string{"Content-Type", "application/octet-stream"}, headers...)...)
}

func TestTempoUpload(t *testing.T) {
	e := newEnv(t, Discovery{})
	f := &fakeTempo{}
	f.install(e)
	wav := clicks(12)
	r := e.upload(t, "/api/tempo?styles=Hardcore,Breakbeat&min=140&max=190", wav, "X-Filename", "%C5%9Alad%20A1.wav")
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("upload = %d %s", r.status, r.body)
	}
	res := r.json(t)["result"].(map[string]any)
	if res["bpm"] != 174.02 || res["steady"] != true || len(res["alternatives"].([]any)) != 1 {
		t.Fatalf("result = %v", res)
	}
	want := tempo.Options{Styles: []string{"Breakbeat", "Hardcore"}, MinBPM: 140, MaxBPM: 190}
	if !reflect.DeepEqual(f.opts[0], want) || f.samples[0] != 12*tempo.SampleRate {
		t.Fatalf("analysed %d samples with %+v", f.samples[0], f.opts[0])
	}
	// The same file again comes from the cache.
	if r := e.upload(t, "/api/tempo?styles=Breakbeat,Hardcore&min=140&max=190", wav); r.status != 200 || f.calls() != 1 {
		t.Fatalf("second upload = %d, %d analyses", r.status, f.calls())
	}
	// Same-origin requests with the browser's headers are fine.
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(e.http.URL, "http://"))
	if r := e.upload(t, "/api/tempo", clicks(13), "Origin", "http://127.0.0.1:"+port, "Sec-Fetch-Site", "same-origin"); r.status != 200 {
		t.Fatalf("same-origin upload = %d %s", r.status, r.body)
	}
}

func TestTempoUploadKeepsTheGuards(t *testing.T) {
	e := newEnv(t, Discovery{})
	f := &fakeTempo{}
	f.install(e)
	wav := clicks(10)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(e.http.URL, "http://"))
	other := "9999"
	if port == other {
		other = "9998"
	}
	for _, h := range [][]string{
		{"Origin", "null"}, {"Origin", "https://evil.example.com"}, {"Origin", "http://127.0.0.1:" + other},
		{"Origin", "http://localhost:" + other}, {"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"},
	} {
		if r := e.upload(t, "/api/tempo", wav, h...); r.status != 403 || r.errorText(t) != "Cross-site requests are not allowed." {
			t.Errorf("%s: %s = %d %s", h[0], h[1], r.status, r.body)
		}
	}
	if r := e.upload(t, "/api/tempo", wav, "Host", "evil.example.com"); r.status != 400 {
		t.Errorf("foreign Host = %d", r.status)
	}
	// What a cross-site form or fetch can send without a preflight is refused.
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		if r := e.upload(t, "/api/tempo", wav, "Content-Type", ct); r.status != 415 || r.errorText(t) != audioUploadHint {
			t.Errorf("Content-Type %q = %d", ct, r.status)
		}
	}
	if r := e.upload(t, "/api/tempo", wav, "Content-Type", "application/json"); r.status != 415 || r.errorText(t) != audioUploadHint {
		t.Errorf("JSON to the upload = %d", r.status)
	}
	// The octet-stream exception is this one route only.
	for _, c := range []struct{ method, path string }{{"POST", "/api/crates"}, {"PUT", "/api/settings"},
		{"POST", "/api/tempo/"}, {"PUT", "/api/tempo"}, {"POST", "/api/preview"}} {
		if r := e.do(t, c.method, c.path, `{}`, "Content-Type", "application/octet-stream"); r.status != 415 {
			t.Errorf("octet-stream %s %s = %d", c.method, c.path, r.status)
		}
	}
	if f.calls() != 0 {
		t.Fatalf("a refused request was analysed (%d)", f.calls())
	}
}

func TestTempoUploadErrors(t *testing.T) {
	e := newEnv(t, Discovery{})
	f := &fakeTempo{}
	f.install(e)
	for _, c := range []struct {
		name, path, body string
		err              error
		status           int
		want             string
	}{
		{"empty", "/api/tempo", "", nil, 400, "Send an audio file as the request body."},
		{"not audio", "/api/tempo", "hello, I am a text file", nil, 400, "Can't read this audio file ("},
		{"no beat", "/api/tempo", clicks(10), tempo.ErrNoPulse, 400, "No steady beat found in this audio. Try a section with drums."},
		{"too short", "/api/tempo", clicks(11), tempo.ErrTooShort, 400, "This recording is too short to judge the tempo"},
		{"half a range", "/api/tempo?min=140", clicks(10), nil, 400, "Give both ends of the BPM range"},
		{"bad number", "/api/tempo?min=fast&max=190", clicks(10), nil, 422, "min: must be a number"},
		{"upside-down range", "/api/tempo?min=190&max=140", clicks(10), nil, 400, "The BPM range runs from low to high"},
	} {
		f.err = c.err
		r := e.upload(t, c.path, c.body)
		if r.status != c.status || !strings.HasPrefix(r.errorText(t), c.want) {
			t.Errorf("%s: %d %s, want %d %q", c.name, r.status, r.body, c.status, c.want)
		}
	}

	e.srv.maxAudio = 4 << 10
	big := clicks(1) // 44 KB
	if r := e.upload(t, "/api/tempo", big); r.status != 413 || !strings.Contains(r.errorText(t), "over 4 KB") {
		t.Errorf("oversized = %d %s", r.status, r.body)
	}
	// Without a Content-Length (chunked), the reader stops it.
	req, _ := http.NewRequest("POST", e.http.URL+"/api/tempo", io.MultiReader(strings.NewReader(big)))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 413 || req.ContentLength != 0 {
		t.Errorf("oversized, chunked = %d (length %d)", resp.StatusCode, req.ContentLength)
	}
}

func TestFilenameHint(t *testing.T) {
	for in, want := range map[string]string{
		"track.mp3": "track.mp3", "%C5%9Alad.flac": "Ślad.flac", "../../etc/passwd": "passwd", `C:\Music\a.m4a`: "a.m4a",
		"a\x00b\nc.wav": "abc.wav", "%2e%2e": "", "": "", "100%.wav": "100%.wav", strings.Repeat("x", 300) + ".aiff": strings.Repeat("x", 250) + ".aiff",
	} {
		if got := filenameHint(in); got != want {
			t.Errorf("filenameHint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTempoVideo(t *testing.T) {
	e := newEnv(t, Discovery{})
	f := &fakeTempo{}
	f.install(e)
	var ids []string
	e.Service.DecodeVideo = func(ctx context.Context, id string, rate int, maxSeconds float64) ([]float32, error) {
		ids = append(ids, id)
		return make([]float32, 12*rate), nil
	}
	r := e.do(t, "POST", "/api/tempo/video", map[string]any{"url": "https://www.youtube.com/watch?v=nMP1QjHNDeA", "styles": []string{"Hard House"}},
		"Content-Type", "application/json")
	if r.status != 200 || r.json(t)["result"].(map[string]any)["bpm"] != 174.02 || len(ids) != 1 {
		t.Fatalf("video = %d %s (fetched %v)", r.status, r.body, ids)
	}
	if r := e.do(t, "POST", "/api/tempo/video", map[string]any{"url": "file:///etc/passwd"}, "Content-Type", "application/json"); r.status < 400 || r.status >= 500 {
		t.Fatalf("non-YouTube url = %d %s", r.status, r.body)
	}
	// It is an ordinary JSON write: other content types and cross-site requests are refused.
	if r := e.do(t, "POST", "/api/tempo/video", map[string]any{"url": "https://youtu.be/nMP1QjHNDeA"}, "Content-Type", "application/octet-stream"); r.status != 415 {
		t.Fatalf("octet-stream = %d", r.status)
	}
	if r := e.do(t, "POST", "/api/tempo/video", map[string]any{"url": "https://youtu.be/nMP1QjHNDeA"}, "Content-Type", "application/json", "Origin", "https://evil.example"); r.status != 403 {
		t.Fatalf("cross-site = %d", r.status)
	}
	if len(ids) != 1 {
		t.Fatalf("refused requests fetched videos: %v", ids)
	}
}
