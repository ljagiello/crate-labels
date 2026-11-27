package tempo

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"testing"
)

// A tiny drum machine for tests: patterns are 16 steps per bar of sixteenth notes.

type voice int

const (
	kick voice = iota
	snare
	hat
)

type hit struct {
	step  int // 0..15
	voice voice
	gain  float64
}

func synth(rng *rand.Rand, x []float64, at int, sr int, v voice, gain float64) {
	switch v {
	case kick:
		n := int(0.18 * float64(sr))
		phase := 0.0
		for i := 0; i < n && at+i < len(x); i++ {
			t := float64(i) / float64(sr)
			f := 50 + 100*math.Exp(-t*30)
			phase += 2 * math.Pi * f / float64(sr)
			x[at+i] += gain * math.Sin(phase) * math.Exp(-t*18)
		}
	case snare:
		n := int(0.15 * float64(sr))
		for i := 0; i < n && at+i < len(x); i++ {
			t := float64(i) / float64(sr)
			x[at+i] += gain * (0.6*(rng.Float64()*2-1) + 0.4*math.Sin(2*math.Pi*190*t)) * math.Exp(-t*25)
		}
	case hat:
		n := int(0.04 * float64(sr))
		prev := 0.0
		for i := 0; i < n && at+i < len(x); i++ {
			t := float64(i) / float64(sr)
			w := rng.Float64()*2 - 1
			x[at+i] += gain * 0.5 * (w - prev) * math.Exp(-t*90) // crude high-pass
			prev = w
		}
	}
}

// render plays pattern at bpm for seconds, with a little noise.
func render(bpm, seconds float64, sr int, pattern []hit, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	x := make([]float64, int(seconds*float64(sr)))
	step := 60 / bpm / 4 // seconds per sixteenth
	for bar := 0; ; bar++ {
		start := float64(bar) * 16 * step
		if start >= seconds {
			break
		}
		for _, h := range pattern {
			at := int((start + float64(h.step)*step) * float64(sr))
			if at < len(x) {
				synth(rng, x, at, sr, h.voice, h.gain)
			}
		}
	}
	out := make([]float32, len(x))
	for i, v := range x {
		out[i] = float32(0.3*v + 0.003*(rng.Float64()*2-1))
	}
	return out
}

var (
	fourOnTheFloor = []hit{
		{0, kick, 1}, {4, kick, 1}, {8, kick, 1}, {12, kick, 1},
		{4, snare, 0.6}, {12, snare, 0.6},
		{2, hat, 0.5}, {6, hat, 0.5}, {10, hat, 0.5}, {14, hat, 0.5},
	}
	// Two-step drum & bass at the 174 grid: kick on 1 and the "and" of 3, snare on 2 and 4,
	// eighth-note hats.
	twoStep = []hit{
		{0, kick, 1}, {10, kick, 0.9},
		{4, snare, 1}, {12, snare, 1},
		{0, hat, 0.3}, {2, hat, 0.3}, {4, hat, 0.3}, {6, hat, 0.3}, {8, hat, 0.3}, {10, hat, 0.3}, {12, hat, 0.3}, {14, hat, 0.3},
	}
	// Boom bap at 90: kick on 1 and the "and" of 2 and 3, snare on 2 and 4, eighth hats.
	boomBap = []hit{
		{0, kick, 1}, {6, kick, 0.8}, {10, kick, 0.8},
		{4, snare, 1}, {12, snare, 1},
		{0, hat, 0.3}, {2, hat, 0.3}, {4, hat, 0.3}, {6, hat, 0.3}, {8, hat, 0.3}, {10, hat, 0.3}, {12, hat, 0.3}, {14, hat, 0.3},
	}
)

func TestAnalyzeSynthetic(t *testing.T) {
	cases := []struct {
		name    string
		bpm     float64
		pattern []hit
		styles  []string
		sr      int
	}{
		{"house 128", 128, fourOnTheFloor, []string{"House"}, SampleRate},
		{"house 128 no styles", 128, fourOnTheFloor, nil, SampleRate},
		{"fractional 140.5", 140.5, fourOnTheFloor, []string{"Hard House"}, SampleRate},
		{"drum n bass 174", 174, twoStep, []string{"Drum n Bass"}, SampleRate},
		{"jungle 172.5 at 44.1 kHz", 172.5, twoStep, []string{"Jungle", "Breakbeat"}, 44100},
		{"hip hop 90", 90, boomBap, []string{"Hip Hop", "Boom Bap"}, SampleRate},
		{"happy hardcore 176", 176, fourOnTheFloor, []string{"Happy Hardcore"}, 16000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := render(c.bpm, 45, c.sr, c.pattern, 1)
			res, err := Analyze(context.Background(), x, c.sr, Options{Styles: c.styles})
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(res.BPM-c.bpm) > 0.05 {
				t.Fatalf("BPM = %.2f, want %.2f (alternatives %v)", res.BPM, c.bpm, res.Alternatives)
			}
			if !res.Steady {
				t.Errorf("Steady = false for a constant tempo")
			}
			if res.Confidence <= 0 || res.Confidence > 1 {
				t.Errorf("Confidence = %v", res.Confidence)
			}
			if math.Abs(res.Seconds-45) > 0.01 {
				t.Errorf("Seconds = %v", res.Seconds)
			}
			var half, double bool
			for _, a := range res.Alternatives {
				half = half || (a.Relation == "half" && math.Abs(a.BPM-c.bpm/2) < 0.02)
				double = double || (a.Relation == "double" && math.Abs(a.BPM-c.bpm*2) < 0.02)
			}
			if !half || (c.bpm*2 <= maxBPM && !double) {
				t.Errorf("alternatives %v lack the half and double", res.Alternatives)
			}
		})
	}
}

func TestAnalyzeTempoChange(t *testing.T) {
	a := render(120, 40, SampleRate, fourOnTheFloor, 2)
	b := render(132, 40, SampleRate, fourOnTheFloor, 3)
	res, err := Analyze(context.Background(), append(a, b...), SampleRate, Options{Styles: []string{"House", "Techno"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Steady {
		t.Errorf("Steady = true for 120 → 132 (BPM %.2f)", res.BPM)
	}
	if math.Abs(res.BPM-120) > 0.6 && math.Abs(res.BPM-132) > 0.6 {
		t.Errorf("BPM = %.2f, want one of the two sections", res.BPM)
	}
}

func TestAnalyzeErrors(t *testing.T) {
	if _, err := Analyze(context.Background(), make([]float32, 5*SampleRate), SampleRate, Options{}); !errors.Is(err, ErrTooShort) {
		t.Errorf("5 s: err = %v, want ErrTooShort", err)
	}
	if _, err := Analyze(context.Background(), make([]float32, 30*SampleRate), SampleRate, Options{}); !errors.Is(err, ErrNoPulse) {
		t.Errorf("silence: err = %v, want ErrNoPulse", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, render(128, 20, SampleRate, fourOnTheFloor, 4), SampleRate, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
	if _, err := Analyze(context.Background(), render(128, 20, 4000, fourOnTheFloor, 4), 4000, Options{}); !errors.Is(err, ErrTooShort) {
		t.Errorf("4 kHz: err = %v, want ErrTooShort", err)
	}
}

func TestStyleRange(t *testing.T) {
	cases := []struct {
		opt    Options
		lo, hi float64
		ok     bool
	}{
		{Options{Styles: []string{"Drum n Bass"}}, 160, 185, true},
		{Options{Styles: []string{"Drum & Bass"}}, 160, 185, true},
		{Options{Styles: []string{"Electronic", "Breakbeat", "Hardcore"}}, 125, 200, true}, // specific beats broad
		{Options{Styles: []string{"Electronic", "Breakbeat"}}, 95, 150, true},              // broad beats genre
		{Options{Styles: []string{"Electronic"}}, 85, 180, true},
		{Options{Styles: []string{"House", "Techno"}}, 115, 150, true}, // union
		{Options{Styles: []string{"Hip-Hop"}}, 80, 105, true},
		{Options{Styles: []string{"Polka"}}, 0, 0, false},
		{Options{Styles: []string{"Hip Hop"}, MinBPM: 150, MaxBPM: 190}, 150, 190, true},
	}
	for _, c := range cases {
		lo, hi, ok := styleRange(c.opt)
		if lo != c.lo || hi != c.hi || ok != c.ok {
			t.Errorf("styleRange(%v) = %v, %v, %v; want %v, %v, %v", c.opt, lo, hi, ok, c.lo, c.hi, c.ok)
		}
	}
	if lo, hi := StyleRange(Options{}); lo <= 0 || hi <= lo {
		t.Errorf("StyleRange(zero) = %v, %v", lo, hi)
	}
}

func TestAutocorrAndFFT(t *testing.T) {
	x := make([]float64, 1000)
	for i := range x {
		if i%50 == 0 {
			x[i] = 1
		}
	}
	a := autocorr(x, 200)
	for _, lag := range []int{50, 100, 150, 200} {
		if a[lag] < 0.9*a[0] {
			t.Errorf("acf[%d] = %v, acf[0] = %v", lag, a[lag], a[0])
		}
	}
	if a[25] > 1e-9 {
		t.Errorf("acf[25] = %v, want 0", a[25])
	}
}

// A bare click is the hardest case for the level choice: every beat looks alike, so the
// release's style has to decide.
func TestClickTrackFollowsStyle(t *testing.T) {
	for _, bpm := range []float64{160, 172, 174, 180} {
		x := make([]float32, 30*SampleRate)
		for k := 0; ; k++ {
			at := int(float64(k) * 60 / bpm * SampleRate)
			if at >= len(x) {
				break
			}
			for i := 0; i < 200 && at+i < len(x); i++ {
				x[at+i] += float32(math.Sin(2*math.Pi*1000*float64(i)/SampleRate) * math.Exp(-float64(i)/40))
			}
		}
		res, err := Analyze(context.Background(), x, SampleRate, Options{Styles: []string{"Drum n Bass"}})
		if err != nil || math.Abs(res.BPM-bpm) > 0.05 {
			t.Errorf("click at %.0f, Drum n Bass: %.2f, %v", bpm, res.BPM, err)
		}
	}
}
