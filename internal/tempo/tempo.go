// Package tempo estimates the tempo (BPM) of a piece of music from its audio.
//
// It is tuned for what DJs put on labels: mostly machine-made dance music with one steady
// tempo, where the hard part is not finding the pulse but choosing the right metrical level
// (174 rather than 87) and pinning the value to a fraction of a BPM. The whole recording is
// analysed, not a short excerpt, and the release's Discogs genres and styles steer the
// choice between half and double tempo.
//
// The package is pure Go and has no I/O: callers decode audio (internal/audio) and pass
// mono samples.
package tempo

import (
	"context"
	"errors"
)

// SampleRate is the rate Analyze works best at. Other rates are accepted.
const SampleRate = 22050

// Errors Analyze returns for audio it cannot judge.
var (
	ErrTooShort = errors.New("tempo: need at least 8 seconds of audio")
	ErrNoPulse  = errors.New("tempo: no steady beat found")
)

// Options steer the choice of metrical level. The zero value uses a generic dance-music
// prior.
type Options struct {
	// Styles are the release's Discogs genres and styles ("Electronic", "Hardcore",
	// "Drum n Bass"). They set the tempo range the estimate prefers (see StyleRange).
	Styles []string
	// MinBPM and MaxBPM, when both are > 0, set that range directly and override Styles.
	MinBPM, MaxBPM float64
}

// Candidate is one reading of the tempo.
type Candidate struct {
	BPM float64 `json:"bpm"`
	// Score is the reading's share of the evidence, 0..1; candidates' scores sum to ≤ 1.
	Score float64 `json:"score"`
	// Relation names how it relates to the best estimate: "half", "double", "two-thirds",
	// "three-halves", or "" for an unrelated reading.
	Relation string `json:"relation"`
}

// Result is a tempo estimate.
type Result struct {
	// BPM is the best estimate, rounded to 0.01.
	BPM float64 `json:"bpm"`
	// Confidence is 0..1: how clearly the audio has one pulse at this level. Below about
	// 0.4 the UI should ask the user to check.
	Confidence float64 `json:"confidence"`
	// Steady is false when the tempo changes noticeably through the recording (a live
	// drummer, a tempo change, a DJ mix); BPM is then the dominant tempo.
	Steady bool `json:"steady"`
	// Alternatives are other readings, best first, usually including the half and the
	// double of BPM.
	Alternatives []Candidate `json:"alternatives"`
	// Range is the tempo range the estimate preferred, from Options.
	Range [2]float64 `json:"range"`
	// Seconds is how much audio was analysed.
	Seconds float64 `json:"seconds"`
}

// Analyze estimates the tempo of mono samples (any scale; clipped at ±1 is fine) at
// sampleRate Hz. It honours ctx between processing stages.
func Analyze(ctx context.Context, samples []float32, sampleRate int, opt Options) (Result, error) {
	return analyze(ctx, samples, sampleRate, opt)
}
