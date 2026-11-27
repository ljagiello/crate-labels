// Package raster is the image pipeline shared by the thermal printer drivers: grey
// conversion, area resampling, rotation, Floyd–Steinberg dithering and MSB-first bit
// packing.
//
// [Render] is the chain every thermal driver uses. It turns the design, scales it into
// the label's lane on the head with [Fit], and dithers it into burn dots on an
// otherwise blank head-width raster. [Label.Preview] turns the burnt dots back into
// the design orientation.
//
// Dithering works in integers, so a given image gives the same dots on every machine.
package raster

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
)

// Gray is a grey image in float32, row-major, with 0 = black and 255 = white.
// Values may leave 0..255 in intermediate steps.
type Gray struct {
	W, H int
	Pix  []float32 // len W*H
}

// NewGray returns a w×h image with every pixel set to fill.
func NewGray(w, h int, fill float32) *Gray {
	g := &Gray{W: w, H: h, Pix: make([]float32, w*h)}
	if fill != 0 {
		for i := range g.Pix {
			g.Pix[i] = fill
		}
	}
	return g
}

// At returns the pixel at (x, y).
func (g *Gray) At(x, y int) float32 { return g.Pix[y*g.W+x] }

// Set stores v at (x, y).
func (g *Gray) Set(x, y int, v float32) { g.Pix[y*g.W+x] = v }

// Clone returns a deep copy.
func (g *Gray) Clone() *Gray { return &Gray{W: g.W, H: g.H, Pix: slices.Clone(g.Pix)} }

// Image converts to 8-bit grey, rounding to the nearest level and clamping to 0..255.
// NaN becomes 0.
func (g *Gray) Image() *image.Gray {
	out := image.NewGray(image.Rect(0, 0, g.W, g.H))
	for y := range g.H {
		for x := range g.W {
			v := float64(g.Pix[y*g.W+x])
			if math.IsNaN(v) {
				v = 0
			}
			out.Pix[y*out.Stride+x] = uint8(math.Round(min(max(v, 0), 255)))
		}
	}
	return out
}

// Bits is a 1-bit raster, row-major, with true = a black (burnt) dot.
type Bits struct {
	W, H int
	Pix  []bool // len W*H
}

// NewBits returns an all-white w×h raster.
func NewBits(w, h int) *Bits { return &Bits{W: w, H: h, Pix: make([]bool, w*h)} }

// At reports whether the dot at (x, y) burns.
func (b *Bits) At(x, y int) bool { return b.Pix[y*b.W+x] }

// Set sets the dot at (x, y).
func (b *Bits) Set(x, y int, v bool) { b.Pix[y*b.W+x] = v }

// Crop returns a copy of the dots inside r, which is clipped to the raster.
func (b *Bits) Crop(r image.Rectangle) *Bits {
	r = r.Intersect(image.Rect(0, 0, b.W, b.H))
	out := NewBits(r.Dx(), r.Dy())
	for y := range out.H {
		copy(out.Pix[y*out.W:(y+1)*out.W], b.Pix[(y+r.Min.Y)*b.W+r.Min.X:])
	}
	return out
}

// RoundHalfUp rounds x to the nearest integer, halves towards +∞. Pass a value that
// is already rounded to float64 (e.g. float64(a*b)) so the compiler cannot fuse the
// product with the +0.5, which would make the result differ between machines.
func RoundHalfUp(x float64) int { return int(math.Floor(x + 0.5)) }

// Rec. 709 luma weights for gamma-encoded sRGB.
const (
	lumaR = 0.2126
	lumaG = 0.7152
	lumaB = 0.0722
)

// ToGray converts an image to grey on a 0..255 scale. An *image.Gray is copied as is.
// Anything else is composited over white paper (premultiplied: out = c + (1 − α)·white)
// and reduced to Rec. 709 luma, without rounding.
func ToGray(img image.Image) *Gray {
	b := img.Bounds()
	out := NewGray(b.Dx(), b.Dy(), 0)
	if src, ok := img.(*image.Gray); ok {
		for y := range out.H {
			row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := range out.W {
				out.Pix[y*out.W+x] = float32(row[x])
			}
		}
		return out
	}
	for y := range out.H {
		for x := range out.W {
			out.Pix[y*out.W+x] = lumaOverWhite(img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// lumaOverWhite is one pixel composited over white, as 0..255 luma.
func lumaOverWhite(c color.Color) float32 {
	r, g, b, a := c.RGBA() // premultiplied, 0..0xffff
	paper := float64(0xffff - a)
	// float64() around each product keeps it from fusing into the sum (see RoundHalfUp).
	y := float64(lumaR*(float64(r)+paper)) + float64(lumaG*(float64(g)+paper)) + float64(lumaB*(float64(b)+paper))
	return float32(y * 255 / 0xffff)
}

// BitsToImage is the preview of a raster: a burnt dot is 0, paper is 255.
func BitsToImage(b *Bits) *image.Gray {
	out := image.NewGray(image.Rect(0, 0, b.W, b.H))
	for y := range b.H {
		for x := range b.W {
			if !b.Pix[y*b.W+x] {
				out.Pix[y*out.Stride+x] = 255
			}
		}
	}
	return out
}

// RotateCW turns g clockwise by a multiple of 90 degrees (negative turns are
// anticlockwise). A quarter turn maps the source's left column to the top row: on a
// W×H image, out(row i, col j) = in(row H-1-j, col i).
func RotateCW(g *Gray, degrees int) (*Gray, error) {
	w, h, pix, err := rotate(g.W, g.H, g.Pix, degrees)
	if err != nil {
		return nil, err
	}
	return &Gray{W: w, H: h, Pix: pix}, nil
}

// RotateBitsCW turns b clockwise by a multiple of 90 degrees, like [RotateCW].
func RotateBitsCW(b *Bits, degrees int) (*Bits, error) {
	w, h, pix, err := rotate(b.W, b.H, b.Pix, degrees)
	if err != nil {
		return nil, err
	}
	return &Bits{W: w, H: h, Pix: pix}, nil
}

func rotate[T any](w, h int, pix []T, degrees int) (int, int, []T, error) {
	if degrees%90 != 0 {
		return 0, 0, nil, fmt.Errorf("raster: rotation must be a multiple of 90 degrees, got %d", degrees)
	}
	out := make([]T, len(pix))
	switch ((degrees/90)%4 + 4) % 4 {
	case 0:
		copy(out, pix)
		return w, h, out, nil
	case 1: // W×H → H×W, out(row i, col j) = in(row h-1-j, col i)
		for i := range w {
			for j := range h {
				out[i*h+j] = pix[(h-1-j)*w+i]
			}
		}
		return h, w, out, nil
	case 2:
		for i := range h {
			for j := range w {
				out[i*w+j] = pix[(h-1-i)*w+(w-1-j)]
			}
		}
		return w, h, out, nil
	default: // 270: out(row i, col j) = in(row j, col w-1-i)
		for i := range w {
			for j := range h {
				out[i*h+j] = pix[j*w+(w-1-i)]
			}
		}
		return h, w, out, nil
	}
}
