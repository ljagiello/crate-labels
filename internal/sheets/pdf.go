// A minimal PDF writer (ISO 32000-1): one image XObject per page.

package sheets

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

const ptPerInch = 72.0

// pt formats a PDF number with at most four decimals and no trailing zeros.
func pt(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// PDFImage is one page image already Flate-compressed for a PDF (ISO 32000-1 section
// 7.4.4), so a caller can see how large each page will be before writing the file.
type PDFImage struct {
	Width, Height int
	Flate         []byte
}

// CompressForPDF prepares one page image for WritePDF.
func CompressForPDF(g *image.Gray) PDFImage {
	g = Normalize(g)
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
	_, _ = zw.Write(g.Pix)
	_ = zw.Close()
	return PDFImage{Width: g.Bounds().Dx(), Height: g.Bounds().Dy(), Flate: z.Bytes()}
}

// PDF writes pages as a minimal PDF; see WritePDF.
func PDF(pages []*image.Gray, paperMM [2]float64, dpi float64) []byte {
	imgs := make([]PDFImage, len(pages))
	for i, p := range pages {
		imgs[i] = CompressForPDF(p)
	}
	return WritePDF(imgs, paperMM, dpi)
}

// WritePDF writes a minimal PDF: one page per image, each exactly paperMM in points,
// holding one 8-bit DeviceGray image XObject.
//
// A page rendered at dpi that is within a pixel of paperMM fills the page exactly, so
// rounding the page to whole pixels does not change the PDF page size. Any other image
// is drawn at its physical size (pixels / dpi), centred. CUPS needs this writer because
// macOS's PNG→PDF filter sizes images by their DPI tag.
func WritePDF(pages []PDFImage, paperMM [2]float64, dpi float64) []byte {
	pw := paperMM[0] / printer.MMPerInch * ptPerInch
	ph := paperMM[1] / printer.MMPerInch * ptPerInch
	ew, eh := MMToPx(paperMM[0], dpi), MMToPx(paperMM[1], dpi)

	var buf bytes.Buffer
	var offsets []int
	obj := func(body string, stream []byte) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s", len(offsets), body)
		if stream != nil {
			buf.WriteString("\nstream\n")
			buf.Write(stream)
			buf.WriteString("\nendstream")
		}
		buf.WriteString("\nendobj\n")
	}

	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// Objects: 1 catalog, 2 pages, 3 info, then per page: page, contents, image.
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+3*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>", nil)
	obj(fmt.Sprintf("<< /Type /Pages /Kids [ %s ] /Count %d >>", strings.Join(kids, " "), len(pages)), nil)
	obj("<< /Title (crate-labels) /Producer (crate-labels) >>", nil)
	for i, p := range pages {
		w, h := p.Width, p.Height
		dw, dh, dx, dy := pw, ph, 0.0, 0.0
		if abs(w-ew) > 1 || abs(h-eh) > 1 {
			dw = float64(w) / dpi * ptPerInch
			dh = float64(h) / dpi * ptPerInch
			dx, dy = (pw-dw)/2, (ph-dh)/2
		}
		contents, img := 5+3*i, 6+3*i
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [ 0 0 %s %s ] /Resources << /XObject << /Im%d %d 0 R >> >> /Contents %d 0 R >>",
			pt(pw), pt(ph), i, img, contents), nil)
		draw := fmt.Sprintf("q %s 0 0 %s %s %s cm /Im%d Do Q", pt(dw), pt(dh), pt(dx), pt(dy), i)
		obj(fmt.Sprintf("<< /Length %d >>", len(draw)), []byte(draw))
		obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>",
			w, h, len(p.Flate)), p.Flate)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
