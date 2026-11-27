package tempo

import (
	"math"
	"math/bits"
	"math/cmplx"
)

// fft is an in-place iterative radix-2 FFT for one power-of-two size.
type fft struct {
	n       int
	twiddle []complex128 // e^{-2πik/n}, k < n/2
	rev     []int
}

func newFFT(n int) *fft {
	if n < 2 || n&(n-1) != 0 {
		panic("tempo: FFT size must be a power of two")
	}
	f := &fft{n: n, twiddle: make([]complex128, n/2), rev: make([]int, n)}
	for k := range f.twiddle {
		s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(n))
		f.twiddle[k] = complex(c, s)
	}
	shift := 64 - bits.TrailingZeros(uint(n))
	for i := range f.rev {
		f.rev[i] = int(bits.Reverse64(uint64(i)) >> shift)
	}
	return f
}

// transform replaces x (len n) with its DFT.
func (f *fft) transform(x []complex128) {
	for i, j := range f.rev {
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= f.n; size <<= 1 {
		half, step := size/2, f.n/size
		for start := 0; start < f.n; start += size {
			for k := 0; k < half; k++ {
				t := f.twiddle[k*step] * x[start+k+half]
				x[start+k+half] = x[start+k] - t
				x[start+k] += t
			}
		}
	}
}

// realPair transforms two real sequences a and b (len n) at once and writes the
// magnitudes of bins 0..n/2 to ma and mb.
func (f *fft) realPair(buf []complex128, a, b []float64, ma, mb []float64) {
	for i := range buf {
		buf[i] = complex(a[i], b[i])
	}
	f.transform(buf)
	n := f.n
	for k := 0; k <= n/2; k++ {
		zk := buf[k%n]
		zn := cmplx.Conj(buf[(n-k)%n])
		ma[k] = cmplx.Abs((zk + zn) / 2)
		mb[k] = cmplx.Abs((zk - zn) / 2)
	}
}

// autocorr is the unbiased autocorrelation of x for lags 0..maxLag.
func autocorr(x []float64, maxLag int) []float64 {
	n := 1
	for n < 2*len(x) {
		n <<= 1
	}
	buf := make([]complex128, n)
	for i, v := range x {
		buf[i] = complex(v, 0)
	}
	f := newFFT(n)
	f.transform(buf)
	for i, v := range buf {
		buf[i] = complex(real(v)*real(v)+imag(v)*imag(v), 0)
	}
	// inverse via conjugation
	for i := range buf {
		buf[i] = cmplx.Conj(buf[i])
	}
	f.transform(buf)
	if maxLag >= len(x) {
		maxLag = len(x) - 1
	}
	out := make([]float64, maxLag+1)
	for lag := range out {
		out[lag] = real(buf[lag]) / float64(n) / float64(len(x)-lag)
	}
	return out
}
