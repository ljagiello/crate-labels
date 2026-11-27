package sheets

import (
	"image"
	"math"
)

// White returns a w×h page filled with white (255).
func White(w, h int) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for i := range g.Pix {
		g.Pix[i] = 0xff
	}
	return g
}

// Normalize returns g with bounds starting at (0, 0) and Stride equal to its width,
// copying only when it is not already in that form.
func Normalize(g *image.Gray) *image.Gray {
	b := g.Bounds()
	if b.Min == (image.Point{}) && g.Stride == b.Dx() && len(g.Pix) == b.Dx()*b.Dy() {
		return g
	}
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		copy(out.Pix[y*b.Dx():(y+1)*b.Dx()], g.Pix[g.PixOffset(b.Min.X, b.Min.Y+y):])
	}
	return out
}

// Rotate turns g clockwise by deg (0, 90, 180 or 270; other values are taken mod 360
// and anything else returns g unchanged). 90° maps source (x, y) to (H-1-y, x).
func Rotate(g *image.Gray, deg int) *image.Gray {
	g = Normalize(g)
	w, h := g.Bounds().Dx(), g.Bounds().Dy()
	var out *image.Gray
	switch ((deg % 360) + 360) % 360 {
	case 90:
		out = image.NewGray(image.Rect(0, 0, h, w))
		for y := range h {
			for x := range w {
				out.Pix[x*h+(h-1-y)] = g.Pix[y*w+x]
			}
		}
	case 180:
		out = image.NewGray(image.Rect(0, 0, w, h))
		n := len(g.Pix)
		for i, v := range g.Pix {
			out.Pix[n-1-i] = v
		}
	case 270:
		out = image.NewGray(image.Rect(0, 0, h, w))
		for y := range h {
			for x := range w {
				out.Pix[(w-1-x)*h+y] = g.Pix[y*w+x]
			}
		}
	default:
		return g
	}
	return out
}

// Paste copies src onto dst with its top-left at (x, y) in dst coordinates, clipped to
// dst. Offsets may be negative.
func Paste(dst, src *image.Gray, x, y int) {
	sb := src.Bounds()
	r := image.Rect(x, y, x+sb.Dx(), y+sb.Dy()).Intersect(dst.Bounds())
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		so := src.PixOffset(sb.Min.X+r.Min.X-x, sb.Min.Y+yy-y)
		do := dst.PixOffset(r.Min.X, yy)
		copy(dst.Pix[do:do+r.Dx()], src.Pix[so:so+r.Dx()])
	}
}

// Resize scales g to w×h. Whole-number enlargements replicate pixels exactly (nearest
// neighbour); anything else is an exact area-weighted box filter, rounded half up.
// The same image is returned when the size already matches.
func Resize(g *image.Gray, w, h int) *image.Gray {
	g = Normalize(g)
	sw, sh := g.Bounds().Dx(), g.Bounds().Dy()
	if sw == w && sh == h {
		return g
	}
	if w <= 0 || h <= 0 || sw == 0 || sh == 0 {
		return White(max(w, 0), max(h, 0))
	}
	if w >= sw && h >= sh && w%sw == 0 && h%sh == 0 {
		return replicate(g, w/sw, h/sh)
	}
	return box(g, w, h)
}

func replicate(g *image.Gray, fx, fy int) *image.Gray {
	sw, sh := g.Bounds().Dx(), g.Bounds().Dy()
	w := sw * fx
	out := image.NewGray(image.Rect(0, 0, w, sh*fy))
	line := make([]byte, w)
	for y := range sh {
		src := g.Pix[y*sw : (y+1)*sw]
		for x, v := range src {
			for k := range fx {
				line[x*fx+k] = v
			}
		}
		for k := range fy {
			copy(out.Pix[(y*fy+k)*w:], line)
		}
	}
	return out
}

// tap is one source sample's weight in an output sample.
type tap struct {
	i int
	w float64
}

// boxTaps gives, for each of n outputs, the source samples (of src) it covers and the
// fraction of the output footprint each covers.
func boxTaps(src, n int) [][]tap {
	scale := float64(src) / float64(n)
	out := make([][]tap, n)
	for o := range n {
		lo := float64(o) * scale
		hi := float64(o+1) * scale
		var taps []tap
		for i := int(math.Floor(lo)); i < src && float64(i) < hi; i++ {
			a := math.Max(lo, float64(i))
			b := math.Min(hi, float64(i+1))
			if b > a {
				taps = append(taps, tap{i, (b - a) / scale})
			}
		}
		out[o] = taps
	}
	return out
}

func box(g *image.Gray, w, h int) *image.Gray {
	sw := g.Bounds().Dx()
	xt := boxTaps(sw, w)
	yt := boxTaps(g.Bounds().Dy(), h)
	out := image.NewGray(image.Rect(0, 0, w, h))
	acc := make([]float64, sw)
	for oy, ys := range yt {
		clear(acc)
		for _, t := range ys {
			src := g.Pix[t.i*sw : (t.i+1)*sw]
			for x, v := range src {
				acc[x] += float64(float64(v) * t.w)
			}
		}
		dst := out.Pix[oy*w : (oy+1)*w]
		for ox, xs := range xt {
			var s float64
			for _, t := range xs {
				s += float64(acc[t.i] * t.w)
			}
			dst[ox] = byte(min(255, max(0, math.Floor(s+0.5))))
		}
	}
	return out
}
