package tempo

import (
	"context"
	"math"
	"sort"
)

// The estimator, in four steps:
//
//  1. Onset envelopes (onset.go) at ~100 frames/s.
//  2. Periodicity: the autocorrelation of the whole envelope, read as a comb over the
//     first four multiples of each candidate beat period, gives a salience curve over
//     40–300 BPM. Its peaks are the candidate tempi.
//  3. Metrical level: a learned linear score (level.go) over per-band periodicity at
//     each candidate's beat, half-beat and two-beat lags, its place on the tempo scale,
//     onset density and the tempo range the release's styles imply (styles.go) picks
//     174 over 87 for drum & bass and 87 over 174 for hip hop.
//  4. Refinement: the chosen tempo is pinned by maximising the phase-coherent energy of
//     the envelope at the beat frequency and its harmonics over the whole recording (a
//     long DFT), which resolves a few hundredths of a BPM on a steady track. Overlapping
//     windows are refined the same way to tell whether the tempo holds.

const (
	minBPM     = 40.0
	maxBPM     = 300.0
	gridStep   = 1.002 // coarse BPM grid ratio
	combTeeth  = 4
	harmonics  = 4
	winSeconds = 20.0
	hopSeconds = 10.0
)

func analyze(ctx context.Context, samples []float32, sampleRate int, opt Options) (Result, error) {
	lo, hi := StyleRange(opt)
	res := Result{Range: [2]float64{lo, hi}}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if sampleRate < 8000 || len(samples) < 8*sampleRate {
		return res, ErrTooShort
	}
	res.Seconds = float64(len(samples)) / float64(sampleRate)

	env, err := onsets(ctx, samples, sampleRate)
	if err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	o := emphasise(env.all, env.fps)
	bs := newBands(env)
	if bs == nil {
		return res, ErrNoPulse
	}
	sal := bs.all
	cands := candidates(sal)
	if len(cands) == 0 {
		return res, ErrNoPulse
	}

	// Metrical level: a learned score over beat-level evidence and the style priors.
	sp := stylePriors(opt)
	best, bestScore := 0, math.Inf(-1)
	for i, c := range cands {
		if s := bs.level(c.BPM, sp); s > bestScore {
			best, bestScore = i, s
		}
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}

	bpm := refine(o, env.fps, cands[best].BPM, 0.012)
	steady, local := steadiness(o, env.fps, bpm)
	if !steady && local > 0 {
		bpm = local
	}
	res.BPM = round2(bpm)
	res.Steady = steady
	res.Alternatives, res.Confidence = readings(bs, cands, bpm, sp)
	if !steady {
		res.Confidence *= 0.7
	}
	return res, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// salience is the comb-filtered autocorrelation of an onset envelope.
type salience struct {
	acf []float64 // smoothed, normalised so lag 0 = 1
	fps float64
}

func newSalience(o []float64, fps float64) *salience {
	maxLag := int(fps * 60 / minBPM * combTeeth)
	if maxLag >= len(o)/2 {
		maxLag = len(o)/2 - 1
	}
	if maxLag < int(fps*60/maxBPM)*2 {
		return nil
	}
	a := autocorr(o, maxLag)
	if a[0] <= 0 {
		return nil
	}
	// Smooth with a small Gaussian so the coarse grid doesn't fall between sharp peaks.
	const sigma = 1.0
	k := []float64{}
	for i := -3; i <= 3; i++ {
		k = append(k, math.Exp(-0.5*float64(i*i)/(sigma*sigma)))
	}
	sm := make([]float64, len(a))
	for i := range a {
		var s, w float64
		for j, kv := range k {
			if l := i + j - 3; l >= 0 && l < len(a) {
				s += kv * a[l]
				w += kv
			}
		}
		sm[i] = s / w / a[0]
	}
	return &salience{acf: sm, fps: fps}
}

// at is the autocorrelation at a fractional lag, or 0 past the end.
func (s *salience) at(lag float64) float64 {
	i := int(lag)
	if i < 0 || i+1 >= len(s.acf) {
		return 0
	}
	f := lag - float64(i)
	return s.acf[i]*(1-f) + s.acf[i+1]*f
}

// score is the comb salience of a tempo: the mean autocorrelation at its first teeth
// beat multiples that fit.
func (s *salience) score(bpm float64) float64 {
	tau := s.fps * 60 / bpm
	var sum float64
	n := 0
	for k := 1; k <= combTeeth; k++ {
		l := tau * float64(k)
		if l+1 >= float64(len(s.acf)) {
			break
		}
		sum += s.at(l)
		n++
	}
	if n == 0 {
		return 0
	}
	return math.Max(0, sum/float64(n))
}

// peaks returns up to n local maxima of the salience curve over minBPM..maxBPM, best
// first.
func (s *salience) peaks(n int) []Candidate {
	var bpms, vals []float64
	for b := minBPM; b <= maxBPM; b *= gridStep {
		bpms = append(bpms, b)
		vals = append(vals, s.score(b))
	}
	var out []Candidate
	for i := 1; i+1 < len(vals); i++ {
		if vals[i] > 0 && vals[i] >= vals[i-1] && vals[i] > vals[i+1] {
			// parabolic interpolation in log-BPM
			d := 0.0
			if den := vals[i-1] - 2*vals[i] + vals[i+1]; den < 0 {
				d = 0.5 * (vals[i-1] - vals[i+1]) / den
			}
			b := bpms[i] * math.Pow(gridStep, d)
			out = append(out, Candidate{BPM: b, Score: s.score(b)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// rangePrior weighs a tempo by how well it sits in [lo, hi]: 1 inside, falling off as a
// Gaussian in octaves outside.
func rangePrior(lo, hi float64) func(float64) float64 {
	const sigma = 0.12 // octaves
	return func(b float64) float64 {
		var d float64
		switch {
		case b < lo:
			d = math.Log2(lo / b)
		case b > hi:
			d = math.Log2(b / hi)
		}
		return math.Exp(-0.5 * d * d / (sigma * sigma))
	}
}

// coherence is the phase-coherent energy of o at the beat frequency of bpm and its first
// harmonics: how well one fixed grid at that tempo lines up with the onsets.
func coherence(o []float64, fps, bpm float64, nh int) float64 {
	var rot [harmonics]complex128
	var w [harmonics]complex128
	var acc [harmonics]complex128
	for h := 0; h < nh; h++ {
		s, c := math.Sincos(-2 * math.Pi * float64(h+1) * bpm / 60 / fps)
		rot[h] = complex(c, s)
		w[h] = 1
	}
	for n, v := range o {
		if v != 0 {
			for h := 0; h < nh; h++ {
				acc[h] += complex(v, 0) * w[h]
			}
		}
		for h := 0; h < nh; h++ {
			w[h] *= rot[h]
		}
		if n&1023 == 1023 { // keep the phasors on the unit circle
			for h := 0; h < nh; h++ {
				a := math.Hypot(real(w[h]), imag(w[h]))
				w[h] /= complex(a, 0)
			}
		}
	}
	var e float64
	for h := 0; h < nh; h++ {
		e += real(acc[h])*real(acc[h]) + imag(acc[h])*imag(acc[h])
	}
	return e
}

// refine finds the tempo within ±span (a fraction) of bpm whose beat grid is most
// coherent with o.
func refine(o []float64, fps, bpm, span float64) float64 {
	beats := float64(len(o)) / fps * bpm / 60
	if beats < 4 {
		return bpm
	}
	// Coarse pass: a step that drifts a quarter beat over the recording at the first
	// harmonic.
	step := bpm / beats / 4
	best, bestE := bpm, -1.0
	for b := bpm * (1 - span); b <= bpm*(1+span); b += step {
		if e := coherence(o, fps, b, 2); e > bestE {
			best, bestE = b, e
		}
	}
	// Fine pass with all harmonics.
	fstep := step / 8
	var bs, es []float64
	for b := best - step; b <= best+step+fstep/2; b += fstep {
		bs = append(bs, b)
		es = append(es, coherence(o, fps, b, harmonics))
	}
	i := 0
	for j := range es {
		if es[j] > es[i] {
			i = j
		}
	}
	b := bs[i]
	if i > 0 && i+1 < len(es) {
		if den := es[i-1] - 2*es[i] + es[i+1]; den < 0 {
			b += 0.5 * (es[i-1] - es[i+1]) / den * fstep
		}
	}
	return b
}

// steadiness refines the tempo in overlapping windows. It reports whether they agree
// with bpm and, if not, the strength-weighted median of the local tempi.
func steadiness(o []float64, fps, bpm float64) (bool, float64) {
	w, hop := int(winSeconds*fps), int(hopSeconds*fps)
	if len(o) < w+hop {
		return true, bpm
	}
	type local struct{ bpm, weight float64 }
	var ls []local
	for start := 0; start+w <= len(o); start += hop {
		seg := o[start : start+w]
		var energy float64
		for _, v := range seg {
			energy += v * v
		}
		if energy == 0 {
			continue
		}
		b := refine(seg, fps, bpm, 0.12)
		strength := coherence(seg, fps, b, harmonics) / (energy * float64(len(seg)))
		ls = append(ls, local{b, strength})
	}
	if len(ls) < 2 {
		return true, bpm
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].bpm < ls[j].bpm })
	var total float64
	for _, l := range ls {
		total += l.weight
	}
	if total == 0 {
		return true, bpm
	}
	// weighted median and the share of weight within 0.5% of bpm
	var acc, median, near float64
	for _, l := range ls {
		if acc < total/2 && acc+l.weight >= total/2 {
			median = l.bpm
		}
		acc += l.weight
		if math.Abs(l.bpm/bpm-1) < 0.005 {
			near += l.weight
		}
	}
	return near/total >= 0.6, median
}
