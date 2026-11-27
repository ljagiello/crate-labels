package munbyn

import (
	"image"

	"github.com/ljagiello/crate-labels/internal/raster"
)

// Job is everything the printer receives for one label.
type Job struct {
	Paper    PaperType
	Layout   Layout
	Raster   []byte     // packed rows, Layout.WidthDots/8 bytes each, MSB-first, 1 = black
	Rows     int        // raster rows
	Sections [][][]byte // framed DEVICE_PRINT packages, grouped by section, in send order

	label *raster.Label // nil for a job built by JobFromRaster
}

// Packages returns every package frame in send order.
func (j *Job) Packages() [][]byte {
	var out [][]byte
	for _, s := range j.Sections {
		out = append(out, s...)
	}
	return out
}

// Preview is the dithered content box turned back to design orientation: 0 = burn,
// 255 = paper.
func (j *Job) Preview() *image.Gray {
	if j.label == nil {
		return nil
	}
	return j.label.Preview()
}

// JobFromRaster packages an already-rasterised label (packed rows of widthDots dots).
func JobFromRaster(paper PaperType, raster []byte, widthDots int) (*Job, error) {
	sections, err := Sections(raster, widthDots)
	if err != nil {
		return nil, err
	}
	return &Job{Paper: paper, Layout: Layout{WidthDots: widthDots, ContentDots: widthDots},
		Raster: raster, Rows: len(raster) / (widthDots / 8), Sections: sections}, nil
}
