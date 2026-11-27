package clabel

import (
	"image"

	"github.com/ljagiello/crate-labels/internal/raster"
)

// Job is everything the printer receives for one label: one TSPL page on die-cut stock,
// or balanced pages of at most 190 mm on continuous stock.
type Job struct {
	Layout Layout
	Raster []byte // the whole head-width raster, packed rows, 1 = black
	Pages  []Page

	label *raster.Label // nil for a job built from a raster
}

// Preview is the dithered content box turned back to design orientation: 0 = burn,
// 255 = paper.
func (j *Job) Preview() *image.Gray {
	if j.label == nil {
		return nil
	}
	return j.label.Preview()
}
