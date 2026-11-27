//go:build tempotrain

// Fitting levelWeights. Not part of the normal test run:
//
//	TEMPO_TRAIN=train.tsv TEMPO_HELDOUT=test.tsv go test -tags tempotrain -run TestTrainLevel -timeout 2h ./internal/tempo
//
// Each list is TSV "bpm<TAB>ignored<TAB>path[<TAB>comma-separated styles]" with reference
// tempi (for example from a DJ library's beat grids). Audio is decoded with ffmpeg. The
// test prints weights to paste into level.go and the held-out selection accuracy.

package tempo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type trainTrack struct {
	Ref    float64
	Feats  [][nFeatures]float64
	BPMs   []float64
	Label  int
	Styles []string
}

// withStyles fills the style-prior features, which are not cached.
func withStyles(tracks []trainTrack, useStyles bool) []trainTrack {
	out := make([]trainTrack, len(tracks))
	for i, tr := range tracks {
		var sp priors
		if useStyles {
			sp = stylePriors(Options{Styles: tr.Styles})
		}
		c := tr
		c.Feats = append([][nFeatures]float64(nil), tr.Feats...)
		for j := range c.Feats {
			c.Feats[j][15] = at(sp.specific, tr.BPMs[j])
			c.Feats[j][19] = at(sp.broad, tr.BPMs[j])
		}
		out[i] = c
	}
	return out
}

func loadTrain(t *testing.T, path string) []trainTrack {
	cache := path + ".features2.gob"
	if f, err := os.Open(cache); err == nil {
		defer f.Close()
		var out []trainTrack
		if gob.NewDecoder(f).Decode(&out) == nil {
			return out
		}
	}
	out := loadTrainUncached(t, path)
	if f, err := os.Create(cache); err == nil {
		gob.NewEncoder(f).Encode(out)
		f.Close()
	}
	return out
}

func loadTrainUncached(t *testing.T, path string) []trainTrack {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type line struct {
		ref    float64
		path   string
		styles []string
	}
	var lines []line
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) < 3 {
			continue
		}
		ref, err := strconv.ParseFloat(p[0], 64)
		if err != nil {
			continue
		}
		l := line{ref: ref, path: p[2]}
		if len(p) > 3 && p[3] != "" {
			l.styles = strings.Split(p[3], ",")
		}
		lines = append(lines, l)
	}
	out := make([]trainTrack, len(lines))
	var wg sync.WaitGroup
	next := make(chan int)
	for range 10 {
		wg.Go(func() {
			for i := range next {
				out[i] = trainOne(lines[i].ref, lines[i].path, lines[i].styles)
			}
		})
	}
	for i := range lines {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

func trainOne(ref float64, path string, styles []string) trainTrack {
	tr := trainTrack{Ref: ref, Label: -1, Styles: styles}
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-i", path,
		"-t", "900", "-ac", "1", "-ar", strconv.Itoa(SampleRate), "-f", "f32le", "-")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if cmd.Run() != nil {
		return tr
	}
	b := buf.Bytes()
	x := make([]float32, len(b)/4)
	for i := range x {
		x[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	env, err := onsets(context.Background(), x, SampleRate)
	if err != nil {
		return tr
	}
	bs := newBands(env)
	if bs == nil {
		return tr
	}
	best := 0.03
	for i, c := range candidates(bs.all) {
		tr.BPMs = append(tr.BPMs, c.BPM)
		tr.Feats = append(tr.Feats, bs.features(c.BPM, priors{}))
		if d := math.Abs(c.BPM/ref - 1); d < best {
			best, tr.Label = d, i
		}
	}
	return tr
}

// fitLogit fits a conditional logit with L2 on standardised features and returns raw-scale
// weights.
func fitLogit(tracks []trainTrack, l2 float64, iters int) [nFeatures]float64 {
	var mean, std [nFeatures]float64
	n := 0
	for _, tr := range tracks {
		for _, f := range tr.Feats {
			for j := range f {
				mean[j] += f[j]
			}
			n++
		}
	}
	for j := range mean {
		mean[j] /= float64(n)
	}
	for _, tr := range tracks {
		for _, f := range tr.Feats {
			for j := range f {
				d := f[j] - mean[j]
				std[j] += d * d
			}
		}
	}
	for j := range std {
		std[j] = math.Sqrt(std[j] / float64(n))
		if std[j] < 1e-9 {
			std[j] = 1
		}
	}
	var w, m, v [nFeatures]float64
	const lr, b1, b2 = 0.05, 0.9, 0.999
	for it := 1; it <= iters; it++ {
		var g [nFeatures]float64
		for _, tr := range tracks {
			scores := make([]float64, len(tr.Feats))
			for i, f := range tr.Feats {
				for j := range f {
					scores[i] += w[j] * (f[j] - mean[j]) / std[j]
				}
			}
			p := softmax(scores)
			for i, f := range tr.Feats {
				c := p[i]
				if i == tr.Label {
					c -= 1
				}
				for j := range f {
					g[j] += c * (f[j] - mean[j]) / std[j]
				}
			}
		}
		for j := range g {
			g[j] = g[j]/float64(len(tracks)) + l2*w[j]
			m[j] = b1*m[j] + (1-b1)*g[j]
			v[j] = b2*v[j] + (1-b2)*g[j]*g[j]
			mh := m[j] / (1 - math.Pow(b1, float64(it)))
			vh := v[j] / (1 - math.Pow(b2, float64(it)))
			w[j] -= lr * mh / (math.Sqrt(vh) + 1e-8)
		}
	}
	var raw [nFeatures]float64
	for j := range w {
		raw[j] = w[j] / std[j]
	}
	return raw
}

func selectionAccuracy(tracks []trainTrack, w [nFeatures]float64) (hit, reachable, total int) {
	for _, tr := range tracks {
		if len(tr.Feats) == 0 {
			continue
		}
		total++
		if tr.Label < 0 {
			continue
		}
		reachable++
		best, bs := 0, math.Inf(-1)
		for i, f := range tr.Feats {
			var s float64
			for j := range f {
				s += w[j] * f[j]
			}
			if s > bs {
				best, bs = i, s
			}
		}
		if best == tr.Label {
			hit++
		}
	}
	return
}

func TestTrainLevel(t *testing.T) {
	trainPath, heldPath := os.Getenv("TEMPO_TRAIN"), os.Getenv("TEMPO_HELDOUT")
	if trainPath == "" {
		t.Skip("set TEMPO_TRAIN")
	}
	var train []trainTrack
	for _, p := range strings.Split(trainPath, ",") {
		train = append(train, loadTrain(t, p)...)
	}
	train = withStyles(train, true)
	drop := map[int]bool{}
	for _, s := range strings.Split(os.Getenv("TEMPO_DROP"), ",") {
		if i, err := strconv.Atoi(s); err == nil {
			drop[i] = true
		}
	}
	for i := range train {
		for j := range train[i].Feats {
			for k := range drop {
				train[i].Feats[j][k] = 0
			}
		}
	}
	var usable []trainTrack
	for _, tr := range train {
		if tr.Label >= 0 {
			usable = append(usable, tr)
		}
	}
	var held []trainTrack
	if heldPath != "" {
		held = withStyles(loadTrain(t, heldPath), true)
	}
	for _, l2 := range []float64{0.001, 0.01, 0.03} {
		w := fitLogit(usable, l2, 1500)
		h, r, n := selectionAccuracy(train, w)
		fmt.Printf("l2=%g train: %d/%d reachable picked (%.1f%%), %d/%d reachable\n", l2, h, r, 100*float64(h)/float64(r), r, n)
		if held != nil {
			h, r, n := selectionAccuracy(held, w)
			fmt.Printf("l2=%g held-out: %d/%d reachable picked (%.1f%%), %d/%d reachable, %.1f%% of all\n", l2, h, r, 100*float64(h)/float64(r), r, n, 100*float64(h)/float64(n))
		}
		fmt.Printf("weights: %#v\n", w)
	}
	h, r, n := selectionAccuracy(held, levelWeights)
	fmt.Printf("current levelWeights held-out: %d/%d reachable, %d total\n", h, r, n)
}
