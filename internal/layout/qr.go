package layout

import (
	"fmt"
	"image"
	"math"

	"rsc.io/qr"
)

// QR codes. Codes use error correction level M and the smallest version that holds the
// URL. Each module is a whole number of pixels so its edges stay sharp on a thermal
// head, and no quiet zone is drawn into the bitmap: every template keeps at least a
// gutter of white paper around the code instead.

// DiscogsURL is the page a label's QR opens: https://www.discogs.com/release/{id}, or
// /master/{id} when master is true. No third-party redirector is ever used.
func DiscogsURL(id int, master bool) string {
	kind := "release"
	if master {
		kind = "master"
	}
	return fmt.Sprintf("https://www.discogs.com/%s/%d", kind, id)
}

// qrMatrix is a code's modules without any quiet zone; true is dark.
type qrMatrix struct {
	n    int
	dark []bool
}

func (m *qrMatrix) at(x, y int) bool { return m.dark[y*m.n+x] }

// encodeQR encodes text at level M; nil when text is empty or too long. Encoding a
// Discogs URL takes microseconds, so codes are not cached.
func encodeQR(text string) *qrMatrix {
	if text == "" {
		return nil
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil
	}
	m := &qrMatrix{n: code.Size, dark: make([]bool, code.Size*code.Size)}
	for y := range code.Size {
		for x := range code.Size {
			m.dark[y*code.Size+x] = code.Black(x, y)
		}
	}
	return m
}

// minModulePx is the smallest module a code may use: 0.25 mm and at least 2 pixels.
// Phone cameras read 0.25 mm modules at arm's length; a 1-dot module smears on a
// thermal head.
func minModulePx(dpmm float64) int {
	return max(2, int(math.Ceil(0.25*dpmm-1e-9)))
}

// qrPixels is the side in pixels of text's code in a square of maxSide pixels, using
// the largest whole module that fits; 0 when the module would be under minModule.
func qrPixels(text string, maxSide, minModule int) int {
	m := encodeQR(text)
	if m == nil {
		return 0
	}
	k := maxSide / m.n
	if k < minModule {
		return 0
	}
	return k * m.n
}

// qrBitmap renders text's code as large as fits maxSide pixels (see qrPixels), with
// the module size it used; nil when it does not fit.
func qrBitmap(text string, maxSide, minModule int) (*image.Gray, int) {
	m := encodeQR(text)
	if m == nil {
		return nil, 0
	}
	k := maxSide / m.n
	if k < minModule {
		return nil, 0
	}
	side := m.n * k
	img := image.NewGray(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = white
	}
	for y := range m.n {
		for x := range m.n {
			if !m.at(x, y) {
				continue
			}
			for py := y * k; py < (y+1)*k; py++ {
				row := img.Pix[py*img.Stride:]
				for px := x * k; px < (x+1)*k; px++ {
					row[px] = black
				}
			}
		}
	}
	return img, k
}
