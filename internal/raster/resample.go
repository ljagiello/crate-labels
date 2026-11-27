package raster

import "fmt"

// ResampleArea scales g to w×h by area averaging: each output pixel is the mean of the
// source area it covers, with a partly covered source pixel weighted by how much of
// it is covered. Scaling up by a whole factor repeats pixels; a same-size call returns
// a copy.
//
// The overlaps are exact integers. Along an axis of n source and m output pixels,
// measure in units of 1/(n·m) of the axis: source pixel i spans [i·m, (i+1)·m) and
// output pixel j spans [j·n, (j+1)·n), so each output pixel's weights add up to n.
// Each product is wrapped in float64() so that the compiler cannot fuse it into the
// sum, which keeps the result identical on every machine.
func ResampleArea(g *Gray, w, h int) (*Gray, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("raster: resample target must be positive, got %dx%d", w, h)
	}
	if g.W <= 0 || g.H <= 0 {
		return nil, fmt.Errorf("raster: cannot resample an empty %dx%d image", g.W, g.H)
	}
	if g.W == w && g.H == h {
		return g.Clone(), nil
	}
	across, down := coverage(g.W, w), coverage(g.H, h)

	// Vertical pass: one row per output row, still at the source width.
	rows := make([]float64, h*g.W)
	for j, taps := range down {
		dst := rows[j*g.W : (j+1)*g.W]
		for _, t := range taps {
			for x, v := range g.Pix[t.src*g.W : (t.src+1)*g.W] {
				dst[x] += float64(float64(t.weight) * float64(v))
			}
		}
	}
	// Horizontal pass, then divide by the total weight of an output pixel.
	norm := float64(g.W) * float64(g.H)
	out := NewGray(w, h, 0)
	for j := range h {
		src := rows[j*g.W : (j+1)*g.W]
		for i, taps := range across {
			var sum float64
			for _, t := range taps {
				sum += float64(float64(t.weight) * src[t.src])
			}
			out.Pix[j*w+i] = float32(sum / norm)
		}
	}
	return out, nil
}

// tap is one source pixel's share of an output pixel.
type tap struct {
	src, weight int
}

// coverage lists, for each of m output pixels along an axis, the n source pixels it
// overlaps and by how much.
func coverage(n, m int) [][]tap {
	out := make([][]tap, m)
	for j := range out {
		lo, hi := j*n, (j+1)*n
		for i := lo / m; i*m < hi; i++ {
			if overlap := min(hi, (i+1)*m) - max(lo, i*m); overlap > 0 {
				out[j] = append(out[j], tap{src: i, weight: overlap})
			}
		}
	}
	return out
}
