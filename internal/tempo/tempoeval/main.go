// Command tempoeval measures internal/tempo against reference tempi. It is a development
// tool and is not part of the crate-labels binary.
//
// The reference list is a TSV of "bpm<TAB>anything<TAB>path[<TAB>styles]" lines, for
// example tempi from a DJ library's beat grids. Audio is decoded with ffmpeg. Results go to
// stdout; per-file rows go to -out as CSV.
//
//	go run ./internal/tempo/tempoeval -list ref.tsv -n 300 -split test -styles -out rows.csv
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/tempo"
)

type item struct {
	ref    float64
	path   string
	styles []string
}

type row struct {
	item
	est      float64
	conf     float64
	steady   bool
	alts     []tempo.Candidate
	err      error
	duration time.Duration
}

func main() {
	list := flag.String("list", "", "reference TSV: bpm, ignored, path[, comma-separated styles]")
	n := flag.Int("n", 200, "number of files (0 = all)")
	split := flag.String("split", "all", "tune (even rows), test (odd rows) or all")
	excerpt := flag.Float64("excerpt", 0, "analyse only this many seconds from 40% into the track (0 = whole track)")
	maxSec := flag.Float64("max", 900, "decode at most this many seconds")
	workers := flag.Int("workers", 8, "parallel analyses")
	out := flag.String("out", "", "per-file CSV")
	lo := flag.Float64("min", 0, "override range low")
	hi := flag.Float64("max-bpm", 0, "override range high")
	useStyles := flag.Bool("styles", false, "pass the 4th TSV column as Options.Styles")
	flag.Parse()

	items, err := load(*list, *split, *n)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rows := make([]row, len(items))
	var wg sync.WaitGroup
	next := make(chan int)
	for range *workers {
		wg.Go(func() {
			for i := range next {
				rows[i] = analyse(items[i], *maxSec, *excerpt, tempo.Options{MinBPM: *lo, MaxBPM: *hi}, *useStyles)
			}
		})
	}
	for i := range items {
		next <- i
	}
	close(next)
	wg.Wait()
	report(rows)
	if *out != "" {
		writeCSV(*out, rows)
	}
}

func analyse(it item, maxSec, excerpt float64, opt tempo.Options, useStyles bool) row {
	r := row{item: it}
	x, err := decode(it.path, maxSec)
	if err != nil {
		r.err = err
		return r
	}
	if excerpt > 0 {
		span := int(excerpt * tempo.SampleRate)
		start := int(float64(len(x)) * 0.4)
		end := min(start+span, len(x))
		start = max(0, end-span)
		x = x[start:end]
	}
	if useStyles {
		opt.Styles = it.styles
	}
	t0 := time.Now()
	res, err := tempo.Analyze(context.Background(), x, tempo.SampleRate, opt)
	r.duration = time.Since(t0)
	r.err, r.est, r.conf, r.steady, r.alts = err, res.BPM, res.Confidence, res.Steady, res.Alternatives
	return r
}

func load(path, split string, n int) ([]item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items []item
	sc := bufio.NewScanner(f)
	i := -1
	for sc.Scan() {
		i++
		if (split == "tune" && i%2 == 1) || (split == "test" && i%2 == 0) {
			continue
		}
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) < 3 {
			continue
		}
		ref, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			continue
		}
		it := item{ref: ref, path: parts[2]}
		if len(parts) > 3 && parts[3] != "" {
			it.styles = strings.Split(parts[3], ",")
		}
		items = append(items, it)
		if n > 0 && len(items) == n {
			break
		}
	}
	return items, sc.Err()
}

func decode(path string, maxSec float64) ([]float32, error) {
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-i", path,
		"-t", strconv.FormatFloat(maxSec, 'f', 0, 64), "-ac", "1", "-ar", strconv.Itoa(tempo.SampleRate), "-f", "f32le", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	b := stdout.Bytes()
	x := make([]float32, len(b)/4)
	for i := range x {
		x[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return x, nil
}

// relation classifies est against ref: "exact" (within 0.25 BPM), "close" (within 4%), or
// the metrical error.
func relation(est, ref float64) string {
	switch {
	case est <= 0:
		return "none"
	case math.Abs(est-ref) <= 0.25:
		return "exact"
	case math.Abs(est/ref-1) <= 0.04:
		return "close"
	}
	for _, r := range []struct {
		f    float64
		name string
	}{{2, "double"}, {0.5, "half"}, {1.5, "three-halves"}, {2.0 / 3, "two-thirds"}, {3, "triple"}, {1.0 / 3, "third"}} {
		if math.Abs(est/(ref*r.f)-1) <= 0.04 {
			return r.name
		}
	}
	return "other"
}

func report(rows []row) {
	counts := map[string]int{}
	var errs, total, sameInt, octaveExact, acc2 int
	var absErr []float64
	var dur time.Duration
	byBin := map[int][2]int{}
	for _, r := range rows {
		if r.err != nil && r.est == 0 {
			errs++
			continue
		}
		total++
		dur += r.duration
		rel := relation(r.est, r.ref)
		counts[rel]++
		b := int(r.ref/10) * 10
		c := byBin[b]
		c[0]++
		if rel == "exact" {
			c[1]++
			absErr = append(absErr, math.Abs(r.est-r.ref))
		}
		byBin[b] = c
		if math.Floor(r.est+0.5) == math.Floor(r.ref+0.5) {
			sameInt++
		}
		for _, f := range []float64{1, 2, 0.5} {
			if math.Abs(r.est*f-r.ref) <= 0.25 {
				octaveExact++
				break
			}
		}
		for _, f := range []float64{1, 2, 0.5, 3, 1.0 / 3} {
			if math.Abs(r.est*f/r.ref-1) <= 0.04 {
				acc2++
				break
			}
		}
	}
	pct := func(a, b int) string {
		if b == 0 {
			return "-"
		}
		return fmt.Sprintf("%5.1f%%", 100*float64(a)/float64(b))
	}
	fmt.Printf("files: %d analysed, %d failed\n", total, errs)
	fmt.Printf("exact (|Δ| ≤ 0.25 BPM):      %s\n", pct(counts["exact"], total))
	fmt.Printf("same integer on the label:   %s\n", pct(sameInt, total))
	fmt.Printf("exact allowing ×2 / ÷2:      %s\n", pct(octaveExact, total))
	fmt.Printf("acc1 (4%%):                   %s\n", pct(counts["exact"]+counts["close"], total))
	fmt.Printf("acc2 (4%%, ×2 ÷2 ×3 ÷3):      %s\n", pct(acc2, total))
	if len(absErr) > 0 {
		sort.Float64s(absErr)
		fmt.Printf("|Δ| among exact: median %.3f, p90 %.3f BPM\n", absErr[len(absErr)/2], absErr[len(absErr)*9/10])
	}
	if total > 0 {
		fmt.Printf("time per file: %v\n", (dur / time.Duration(total)).Round(time.Millisecond))
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Print("outcomes:")
	for _, k := range keys {
		fmt.Printf(" %s=%d", k, counts[k])
	}
	fmt.Println()
	var bins []int
	for b := range byBin {
		bins = append(bins, b)
	}
	sort.Ints(bins)
	fmt.Print("exact by reference tempo:")
	for _, b := range bins {
		fmt.Printf(" %d:%s(%d)", b, strings.TrimSpace(pct(byBin[b][1], byBin[b][0])), byBin[b][0])
	}
	fmt.Println()
}

func writeCSV(path string, rows []row) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"ref", "est", "relation", "conf", "steady", "alts", "ms", "err", "path"})
	for _, r := range rows {
		var alts []string
		for _, a := range r.alts {
			alts = append(alts, fmt.Sprintf("%.2f:%.2f:%s", a.BPM, a.Score, a.Relation))
		}
		e := ""
		if r.err != nil {
			e = r.err.Error()
		}
		w.Write([]string{fmt.Sprintf("%.2f", r.ref), fmt.Sprintf("%.2f", r.est), relation(r.est, r.ref),
			fmt.Sprintf("%.3f", r.conf), strconv.FormatBool(r.steady), strings.Join(alts, " "),
			strconv.FormatInt(r.duration.Milliseconds(), 10), e, r.path})
	}
	w.Flush()
}
