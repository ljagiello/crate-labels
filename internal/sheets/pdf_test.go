package sheets

import (
	"bytes"
	"compress/zlib"
	"image"
	"io"
	"math"
	"regexp"
	"strconv"
	"testing"
)

var (
	mediaBoxRe = regexp.MustCompile(`/MediaBox \[ 0 0 ([\d.]+) ([\d.]+) \]`)
	imageRe    = regexp.MustCompile(`/Width (\d+) /Height (\d+) /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length (\d+) >>\nstream\n`)
	xrefRe     = regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n$`)
	objRe      = regexp.MustCompile(`(?m)^(\d+) 0 obj$`)
)

func mediaBoxes(t *testing.T, pdf []byte) [][2]float64 {
	t.Helper()
	var out [][2]float64
	for _, m := range mediaBoxRe.FindAllSubmatch(pdf, -1) {
		w, _ := strconv.ParseFloat(string(m[1]), 64)
		h, _ := strconv.ParseFloat(string(m[2]), 64)
		out = append(out, [2]float64{w, h})
	}
	return out
}

// images inflates every image XObject.
func images(t *testing.T, pdf []byte) []*image.Gray {
	t.Helper()
	var out []*image.Gray
	for _, loc := range imageRe.FindAllSubmatchIndex(pdf, -1) {
		w, _ := strconv.Atoi(string(pdf[loc[2]:loc[3]]))
		h, _ := strconv.Atoi(string(pdf[loc[4]:loc[5]]))
		n, _ := strconv.Atoi(string(pdf[loc[6]:loc[7]]))
		stream := pdf[loc[1] : loc[1]+n]
		if !bytes.HasPrefix(pdf[loc[1]+n:], []byte("\nendstream")) {
			t.Fatal("stream /Length does not end at endstream")
		}
		zr, err := zlib.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatal(err)
		}
		pix, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		g := image.NewGray(image.Rect(0, 0, w, h))
		if len(pix) != len(g.Pix) {
			t.Fatalf("image has %d bytes, want %d", len(pix), len(g.Pix))
		}
		copy(g.Pix, pix)
		out = append(out, g)
	}
	return out
}

func checkXref(t *testing.T, pdf []byte) {
	t.Helper()
	m := xrefRe.FindSubmatch(pdf)
	if m == nil {
		t.Fatal("no startxref")
	}
	at, _ := strconv.Atoi(string(m[1]))
	if !bytes.HasPrefix(pdf[at:], []byte("xref\n")) {
		t.Fatalf("startxref %d does not point at the xref table", at)
	}
	objs := objRe.FindAllSubmatchIndex(pdf, -1)
	lines := bytes.Split(pdf[at:], []byte("\n"))[3:] // xref, "0 N", free entry
	for i, o := range objs {
		off, _ := strconv.Atoi(string(lines[i][:10]))
		if off != o[0] {
			t.Fatalf("object %d at %d, xref says %d", i+1, o[0], off)
		}
	}
}

func TestPDFPageSizeIsExact(t *testing.T) {
	white, black := White(2550, 3300), image.NewGray(image.Rect(0, 0, 2550, 3300))
	pdf := PDF([]*image.Gray{white, black}, [2]float64{215.9, 279.4}, 300)
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4\n")) {
		t.Fatal("no header")
	}
	if got := mediaBoxes(t, pdf); len(got) != 2 || got[0] != [2]float64{612, 792} || got[1] != got[0] {
		t.Fatalf("media boxes %v", got)
	}
	imgs := images(t, pdf)
	if len(imgs) != 2 || !bytes.Equal(imgs[0].Pix, white.Pix) || !bytes.Equal(imgs[1].Pix, black.Pix) {
		t.Fatal("images do not inflate back to the pages")
	}
	if !bytes.Contains(pdf, []byte("q 612 0 0 792 0 0 cm /Im0 Do Q")) {
		t.Fatal("page 1 is not drawn full page")
	}
	checkXref(t, pdf)

	// A4 is not whole pixels at 300 dpi; the PDF page is still exactly A4.
	a4 := White(MMToPx(210, 300), MMToPx(297, 300))
	got := mediaBoxes(t, PDF([]*image.Gray{a4}, [2]float64{210, 297}, 300))
	if math.Abs(got[0][0]-210/25.4*72) > 1e-3 || math.Abs(got[0][1]-297/25.4*72) > 1e-3 {
		t.Fatalf("A4 box %v", got)
	}
}

func TestPDFOddSizedImageKeepsItsPhysicalSize(t *testing.T) {
	pdf := PDF([]*image.Gray{White(300, 150)}, [2]float64{215.9, 279.4}, 300)
	// 1×0.5 in = 72×36 pt, centred on 612×792.
	if !bytes.Contains(pdf, []byte("q 72 0 0 36 270 378 cm /Im0 Do Q")) {
		t.Fatalf("draw op not found in %q", pdf[:min(len(pdf), 600)])
	}
	checkXref(t, pdf)
}
