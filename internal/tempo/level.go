package tempo

import (
	"math"
	"sort"
)

// Choosing the metrical level.
//
// Every candidate tempo is described by the periodicity of the onset envelope in four
// bands (all, low, mid, high) at its beat period, half its beat period and twice its beat
// period, by where it sits on the tempo scale, by onset density, and by how well it fits
// the tempo range of the release's styles. levelScore is a linear score over those
// features; the candidate with the highest score wins, and a softmax over the scores
// gives the confidence. The weights were fitted (conditional logit, train_test.go) to
// about 2,500 constant-tempo beat grids from a DJ library of hardcore, jungle, drum &
// bass, hip hop, breaks, techno and house, and checked on held-out tracks (docs/TEMPO.md).

const nFeatures = 20

// bands holds the salience of each onset band.
type bands struct {
	all, low, mid, high *salience
	// density is onsets per second; lowShare is the low band's share of onset energy.
	density, lowShare float64
}

func newBands(env *envelope) *bands {
	all := newSalience(emphasise(env.all, env.fps), env.fps)
	if all == nil {
		return nil
	}
	bs := &bands{all: all, density: onsetDensity(env.all, env.fps), lowShare: share(env.low, env.all)}
	bs.low = newSalience(emphasise(env.low, env.fps), env.fps)
	bs.mid = newSalience(emphasise(env.mid, env.fps), env.fps)
	bs.high = newSalience(emphasise(env.high, env.fps), env.fps)
	// A silent band (e.g. a filtered intro) has no salience; read it as zeros.
	for _, p := range []**salience{&bs.low, &bs.mid, &bs.high} {
		if *p == nil {
			*p = &salience{acf: make([]float64, len(all.acf)), fps: all.fps}
		}
	}
	return bs
}

// onsetDensity counts clear onsets per second: local maxima above the mean plus one
// standard deviation, at least 50 ms apart.
func onsetDensity(x []float64, fps float64) float64 {
	if len(x) < 3 {
		return 0
	}
	var sum, ss float64
	for _, v := range x {
		sum += v
		ss += v * v
	}
	n := float64(len(x))
	mean := sum / n
	thr := mean + math.Sqrt(math.Max(0, ss/n-mean*mean))
	gap := int(0.05 * fps)
	count, last := 0, -gap
	for i := 1; i+1 < len(x); i++ {
		if x[i] > thr && x[i] >= x[i-1] && x[i] > x[i+1] && i-last >= gap {
			count++
			last = i
		}
	}
	return float64(count) / (n / fps)
}

func share(part, whole []float64) float64 {
	var p, w float64
	for i := range whole {
		p += part[i]
		w += whole[i]
	}
	if w == 0 {
		return 0
	}
	return p / w
}

// priors are the style information for the level choice: a soft "inside the range" score
// from specific styles (or an explicit range), and one from broad styles and genres. A nil
// prior means no information and contributes nothing.
type priors struct {
	specific, broad func(float64) float64
	// explicit is a range the user set; it is not learned and weighs explicitWeight.
	explicit func(float64) float64
}

// explicitWeight makes a user-set range decisive unless the audio is overwhelmingly clear.
const explicitWeight = 8

func stylePriors(opt Options) priors {
	if opt.MinBPM > 0 && opt.MaxBPM > opt.MinBPM {
		return priors{explicit: rangePrior(opt.MinBPM, opt.MaxBPM)}
	}
	lo, hi, level := styleRanges(opt)
	switch level {
	case levelSpecific:
		return priors{specific: rangePrior(lo, hi)}
	case levelBroad:
		return priors{broad: rangePrior(lo, hi)}
	}
	return priors{}
}

func at(p func(float64) float64, bpm float64) float64 {
	if p == nil {
		return 0
	}
	return p(bpm)
}

// features describes candidate tempo bpm for levelScore.
func (bs *bands) features(bpm float64, p priors) [nFeatures]float64 {
	var f [nFeatures]float64
	tau := bs.all.fps * 60 / bpm
	i := 0
	for _, s := range []*salience{bs.all, bs.low, bs.mid, bs.high} {
		f[i] = s.at(tau)
		f[i+1] = s.at(tau / 2)
		f[i+2] = s.at(2 * tau)
		i += 3
	}
	f[12] = bs.all.score(bpm)
	x := math.Log2(bpm / 120)
	f[13] = x
	f[14] = x * x
	f[15] = at(p.specific, bpm)
	f[16] = x * x * x
	f[17] = math.Log2(math.Max(bs.density, 0.25)/4) * x
	f[18] = (bs.lowShare - 0.3) * x
	f[19] = at(p.broad, bpm)
	return f
}

// levelWeights are fitted by train_test.go; see the comment at the top of this file.
var levelWeights = [nFeatures]float64{
	2.0661, 4.1949, -0.7676, // all: τ, τ/2, 2τ
	1.8295, -0.5399, -2.3817, // low
	-0.7228, -0.2081, -1.5470, // mid
	0.5848, 5.8195, 3.7022, // high
	10.7461,         // comb
	2.2996, -3.1968, // log2(bpm/120), squared
	5,         // specific-style prior: set by hand above the fitted 1.3 (see docs/TEMPO.md)
	-0.0474,   // cubed
	0.6161, 0, // density × log2(bpm/120), low share × log2(bpm/120) (unused)
	1.4602, // broad-style prior
}

func levelScore(f [nFeatures]float64) float64 {
	var s float64
	for i, w := range levelWeights {
		s += w * f[i]
	}
	return s
}

// candidates are the salience peaks plus the half and double of the strongest ones, so
// the level choice always sees both octaves.
func candidates(sal *salience) []Candidate {
	peaks := sal.peaks(10)
	out := append([]Candidate(nil), peaks...)
	add := func(b float64) {
		if b < minBPM || b > maxBPM {
			return
		}
		for _, c := range out {
			if math.Abs(c.BPM/b-1) < 0.02 {
				return
			}
		}
		out = append(out, Candidate{BPM: b, Score: sal.score(b)})
	}
	for i := 0; i < len(peaks) && i < 4; i++ {
		add(peaks[i].BPM * 2)
		add(peaks[i].BPM / 2)
	}
	return out
}

// softmax turns level scores into shares that sum to 1.
func softmax(scores []float64) []float64 {
	m := math.Inf(-1)
	for _, s := range scores {
		m = math.Max(m, s)
	}
	out := make([]float64, len(scores))
	var sum float64
	for i, s := range scores {
		out[i] = math.Exp(s - m)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

// readings scores the chosen tempo, its relatives and the other candidates, and returns
// the alternatives and the chosen tempo's share as the confidence.
func readings(bs *bands, cands []Candidate, bpm float64, sp priors) ([]Candidate, float64) {
	type reading struct {
		bpm      float64
		relation string
	}
	rs := []reading{{bpm, "best"}}
	for _, r := range []struct {
		ratio float64
		name  string
	}{{0.5, "half"}, {2, "double"}, {2.0 / 3, "two-thirds"}, {1.5, "three-halves"}} {
		if b := bpm * r.ratio; b >= minBPM && b <= maxBPM {
			rs = append(rs, reading{b, r.name})
		}
	}
	for _, c := range cands {
		dup := false
		for _, r := range rs {
			if math.Abs(c.BPM/r.bpm-1) < 0.02 {
				dup = true
				break
			}
		}
		if !dup {
			rs = append(rs, reading{c.BPM, ""})
		}
	}
	scores := make([]float64, len(rs))
	for i, r := range rs {
		scores[i] = bs.level(r.bpm, sp)
	}
	p := softmax(scores)
	var out []Candidate
	for i, r := range rs[1:] {
		share := p[i+1]
		if r.relation != "half" && r.relation != "double" && share < 0.05 {
			continue
		}
		out = append(out, Candidate{BPM: round2(r.bpm), Score: math.Round(share*1000) / 1000, Relation: r.relation})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, math.Round(p[0]*1000) / 1000
}

// level is the score of reading the music at bpm: the learned score plus the weight of a
// user-set range.
func (bs *bands) level(bpm float64, sp priors) float64 {
	return levelScore(bs.features(bpm, sp)) + explicitWeight*at(sp.explicit, bpm)
}
