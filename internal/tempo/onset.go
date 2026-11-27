package tempo

import (
	"context"
	"math"
	"sort"
)

// Onset strength: a log-frequency spectrogram and its half-wave-rectified flux, with
// SuperFlux's maximum filter across frequency so vibrato and pitch glides don't count as
// onsets (Böck & Widmer, DAFx 2013). Bands are summed into three groups — low (kick and
// bass), mid and high (snare, hats) — because the groups pulse at different metrical
// levels and the octave decision looks at them separately.

const (
	frameRate     = 100.0 // target onset frames per second
	windowSeconds = 0.046 // analysis window, ~1024 samples at 22.05 kHz
	bandsPerOct   = 12
	minFreq       = 30.0
	maxFreq       = 11000.0
	lowSplit      = 200.0  // Hz: kick and bass below
	highSplit     = 2500.0 // Hz: hats and snare noise above
)

// envelope holds the onset strength signals at fps frames per second.
type envelope struct {
	fps            float64
	low, mid, high []float64
	all            []float64 // low + mid + high
	loudness       []float64 // per-frame log energy, for finding silent stretches
}

// filterbank maps FFT bins to log-spaced triangular bands. Each band covers at least one
// bin, so the bottom octaves get fewer, wider bands.
type filterbank struct {
	centres []float64 // Hz
	weights [][]float64
	first   []int
}

func newFilterbank(nfft int, sr float64) filterbank {
	binHz := sr / float64(nfft)
	top := math.Min(maxFreq, sr/2*0.95)
	var bins []int
	for f := minFreq; f <= top; f *= math.Pow(2, 1.0/bandsPerOct) {
		b := int(math.Round(f / binHz))
		if len(bins) == 0 || b > bins[len(bins)-1] {
			bins = append(bins, b)
		}
	}
	var fb filterbank
	for i := 1; i+1 < len(bins); i++ {
		lo, mid, hi := bins[i-1], bins[i], bins[i+1]
		w := make([]float64, hi-lo+1)
		sum := 0.0
		for b := lo; b <= hi; b++ {
			var v float64
			switch {
			case b <= mid && mid > lo:
				v = float64(b-lo) / float64(mid-lo)
			case b > mid && hi > mid:
				v = float64(hi-b) / float64(hi-mid)
			}
			if b == mid {
				v = 1
			}
			w[b-lo] = v
			sum += v
		}
		for j := range w {
			w[j] /= sum
		}
		fb.centres = append(fb.centres, float64(mid)*binHz)
		fb.weights = append(fb.weights, w)
		fb.first = append(fb.first, lo)
	}
	return fb
}

// onsets computes the onset envelopes of x at sample rate sr.
func onsets(ctx context.Context, x []float32, sr int) (*envelope, error) {
	hop := int(math.Round(float64(sr) / frameRate))
	nfft := 1
	for float64(nfft) < windowSeconds*float64(sr) {
		nfft <<= 1
	}
	frames := (len(x) - nfft) / hop
	if frames < 2 {
		return nil, ErrTooShort
	}
	// Normalise the level so the log compression behaves the same for quiet and loud
	// masters.
	var ss float64
	for _, v := range x {
		ss += float64(v) * float64(v)
	}
	rms := math.Sqrt(ss / float64(len(x)))
	if rms < 1e-5 {
		return nil, ErrNoPulse
	}
	gain := 0.1 / rms

	win := make([]float64, nfft)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(nfft))
	}
	fb := newFilterbank(nfft, float64(sr))
	nb := len(fb.centres)
	f := newFFT(nfft)
	buf := make([]complex128, nfft)
	a, b := make([]float64, nfft), make([]float64, nfft)
	ma, mb := make([]float64, nfft/2+1), make([]float64, nfft/2+1)

	spec := make([][]float32, frames) // log band energies
	loud := make([]float64, frames)
	fill := func(dst []float64, t int) {
		off := t * hop
		for i := range dst {
			dst[i] = float64(x[off+i]) * gain * win[i]
		}
	}
	bands := func(m []float64, t int) {
		row := make([]float32, nb)
		var e float64
		for j := 0; j < nb; j++ {
			var s float64
			for k, w := range fb.weights[j] {
				s += w * m[fb.first[j]+k]
			}
			row[j] = float32(math.Log1p(s))
			e += s * s
		}
		spec[t] = row
		loud[t] = math.Log10(1e-9 + e)
	}
	for t := 0; t < frames; t += 2 {
		if t%2000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		fill(a, t)
		if t+1 < frames {
			fill(b, t+1)
		} else {
			clear(b)
		}
		f.realPair(buf, a, b, ma, mb)
		bands(ma, t)
		if t+1 < frames {
			bands(mb, t+1)
		}
	}

	env := &envelope{fps: float64(sr) / float64(hop), loudness: loud,
		low: make([]float64, frames), mid: make([]float64, frames), high: make([]float64, frames), all: make([]float64, frames)}
	for t := 1; t < frames; t++ {
		prev, cur := spec[t-1], spec[t]
		for j := 0; j < nb; j++ {
			ref := prev[j]
			if j > 0 && prev[j-1] > ref {
				ref = prev[j-1]
			}
			if j+1 < nb && prev[j+1] > ref {
				ref = prev[j+1]
			}
			d := float64(cur[j] - ref)
			if d <= 0 {
				continue
			}
			switch c := fb.centres[j]; {
			case c < lowSplit:
				env.low[t] += d
			case c < highSplit:
				env.mid[t] += d
			default:
				env.high[t] += d
			}
		}
		env.all[t] = env.low[t] + env.mid[t] + env.high[t]
	}
	return env, nil
}

// emphasise prepares an onset signal for periodicity analysis: subtracts a moving average
// (so sustained energy doesn't count), half-wave rectifies and scales to unit RMS.
func emphasise(x []float64, fps float64) []float64 {
	w := int(fps * 0.4)
	if w < 3 {
		w = 3
	}
	out := make([]float64, len(x))
	var sum float64
	for i := 0; i < len(x); i++ {
		sum += x[i]
		if i >= w {
			sum -= x[i-w]
		}
		n := min(i+1, w)
		// centre the window: compare x[i-w/2] with the mean around it
		j := i - w/2
		if j >= 0 {
			if v := x[j] - sum/float64(n); v > 0 {
				out[j] = v
			}
		}
	}
	var ss float64
	for _, v := range out {
		ss += v * v
	}
	if ss > 0 {
		g := 1 / math.Sqrt(ss/float64(len(out)))
		for i := range out {
			out[i] *= g
		}
	}
	return out
}

// quantile is the q-quantile of x (x is not modified).
func quantile(x []float64, q float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	i := int(q * float64(len(s)-1))
	return s[i]
}
