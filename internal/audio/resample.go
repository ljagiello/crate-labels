package audio

import "math"

// The resampler is a polyphase windowed-sinc (Kaiser) low-pass. Its pass band ends at
// 0.4 and its stop band starts at 0.5 of the lower of the two rates, so nothing above
// the new Nyquist frequency folds back: it is at least stopbandDB down.
const (
	stopbandDB = 80
	// halfWidth is the kernel's half length in samples of the lower rate, from Kaiser's
	// formula for stopbandDB and a transition of 0.1 of that rate.
	halfWidth = 26
	// maxPhases caps the filter table. Ratios needing more phases (odd rates such as
	// 44056 Hz) use the nearest of maxPhases, a timing error under 1/2048 sample.
	maxPhases = 1024
)

type resampler struct {
	l, m     int64 // output rate / input rate = l / m, in lowest terms
	taps     int   // per phase
	phases   int64
	table    []float32 // phases × taps
	buf      []float32 // input from absolute index bufStart
	bufStart int64
	in       int64 // input samples pushed
	n        int64 // next output index
	out      []float32
}

func newResampler(inRate, outRate int) *resampler {
	g := gcd(inRate, outRate)
	r := &resampler{l: int64(outRate / g), m: int64(inRate / g)}
	low := float64(min(inRate, outRate))
	fc := 0.45 * low / float64(inRate)        // cut-off, cycles per input sample
	half := halfWidth * float64(inRate) / low // kernel half length, input samples
	r.taps = 2 * int(math.Ceil(half))
	r.phases = min(r.l, maxPhases)
	beta := 0.1102 * (stopbandDB - 8.7)
	r.table = make([]float32, int(r.phases)*r.taps)
	row := make([]float64, r.taps)
	for p := range int(r.phases) {
		frac := float64(p) / float64(r.phases)
		var sum float64
		for k := range r.taps {
			// Tap k multiplies input index base-taps/2+1+k, at distance d from the output.
			d := frac + float64(r.taps/2-1-k)
			v := 0.0
			if math.Abs(d) < half {
				v = 2 * fc * sinc(2*fc*d) * bessel0(beta*math.Sqrt(1-(d/half)*(d/half))) / bessel0(beta)
			}
			row[k], sum = v, sum+v
		}
		for k, v := range row {
			r.table[p*r.taps+k] = float32(v / sum) // unity gain at DC in every phase
		}
	}
	// Zeros before the first sample, so the first outputs have a full window.
	r.bufStart = -int64(r.taps/2 - 1)
	r.buf = make([]float32, r.taps/2-1, 1<<16)
	return r
}

// outputs is how many samples n inputs become.
func (r *resampler) outputs(n int64) int64 { return (n*r.l + r.m - 1) / r.m }

func (r *resampler) push(x []float32) {
	r.buf = append(r.buf, x...)
	r.in += int64(len(x))
	r.run(math.MaxInt64)
	// Drop input no future output reaches.
	need := r.n*r.m/r.l - int64(r.taps/2) + 1
	if drop := need - r.bufStart; drop > 1<<16 {
		r.buf = r.buf[:copy(r.buf, r.buf[drop:])]
		r.bufStart = need
	}
}

// finish flushes the tail (padding with silence) and returns every output.
func (r *resampler) finish() []float32 {
	r.buf = append(r.buf, make([]float32, r.taps/2+1)...)
	r.run(r.outputs(r.in))
	return r.out
}

// run produces outputs, up to index limit, while their whole window is buffered.
func (r *resampler) run(limit int64) {
	avail := r.bufStart + int64(len(r.buf))
	half := int64(r.taps / 2)
	for r.n < limit {
		num := r.n * r.m
		base, p := num/r.l, num%r.l
		if r.phases != r.l {
			p = (p*r.phases + r.l/2) / r.l
			if p == r.phases {
				p, base = 0, base+1
			}
		}
		first := base - half + 1
		if first+int64(r.taps) > avail {
			return
		}
		row := r.table[int(p)*r.taps : int(p+1)*r.taps]
		src := r.buf[first-r.bufStart:]
		src = src[:len(row)]
		var acc float32
		for k, c := range row {
			acc += c * src[k]
		}
		r.out = append(r.out, acc)
		r.n++
	}
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// bessel0 is the modified Bessel function of the first kind, order 0.
func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 64; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < sum*1e-15 {
			break
		}
	}
	return sum
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
