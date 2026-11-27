package raster

import (
	"fmt"
	"math"
)

// Grey levels are carried in sixteenths during dithering, so the error shares below
// are whole numbers and the result is the same on every machine.
const (
	sixteenths = 16
	white16    = 255 * sixteenths
	middle16   = white16 / 2 // 127.5 grey: a pixel at or above it stays paper
)

// FloydSteinberg dithers g into burn dots by Floyd–Steinberg error diffusion. Each
// pixel becomes black or white, whichever is nearer, and the difference goes to the
// pixels not yet visited: 7/16 to the right, 3/16 below left, 5/16 below and 1/16
// below right. Rows run top to bottom, each left to right. Shares that would fall off
// the image are dropped.
//
// The 3/16, 5/16 and 1/16 shares are rounded toward zero and the right-hand share
// takes what is left, so each pixel passes on its error in full. Input outside 0..255
// is clamped first.
func FloydSteinberg(g *Gray) *Bits {
	w, h := g.W, g.H
	level := make([]int32, w*h)
	for i, v := range g.Pix {
		level[i] = int32(math.Round(float64(min(max(v, 0), 255)) * sixteenths))
	}
	out := NewBits(w, h)
	for y := range h {
		for x := range w {
			i := y*w + x
			e := level[i] - white16
			if level[i] < middle16 {
				e = level[i]
				out.Pix[i] = true
			}
			down, downLeft, downRight := e*5/16, e*3/16, e/16
			right := e - down - downLeft - downRight
			if x+1 < w {
				level[i+1] += right
			}
			if y+1 < h {
				below := i + w
				level[below] += down
				if x > 0 {
					level[below-1] += downLeft
				}
				if x+1 < w {
					level[below+1] += downRight
				}
			}
		}
	}
	return out
}

// PackRows packs b MSB-first (leftmost dot in bit 7, 1 = black) into ceil(W/8) bytes per
// row, zero-padded.
func PackRows(b *Bits) []byte {
	bpr := (b.W + 7) / 8
	out := make([]byte, bpr*b.H)
	for y := range b.H {
		row := out[y*bpr : (y+1)*bpr]
		for x, burn := range b.Pix[y*b.W : (y+1)*b.W] {
			if burn {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	return out
}

// UnpackRows is the inverse of [PackRows] for a raster width dots wide.
func UnpackRows(data []byte, width int) (*Bits, error) {
	bpr := (width + 7) / 8
	if bpr == 0 || len(data)%bpr != 0 {
		return nil, fmt.Errorf("raster: %d bytes is not a whole number of %d-byte rows", len(data), bpr)
	}
	b := NewBits(width, len(data)/bpr)
	for y := range b.H {
		row := data[y*bpr : (y+1)*bpr]
		for x := range width {
			b.Pix[y*width+x] = row[x/8]&(0x80>>(x%8)) != 0
		}
	}
	return b, nil
}
