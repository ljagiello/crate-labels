package munbyn

import (
	"image"
	"math"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/raster"
)

// EncodeJob is everything the printer receives for one label, without a link: the
// paper type, the head-width raster (honouring extra_rotate_cw, offset_dots and
// darkness) and its framed packages. label is in design orientation, white = 255.
func EncodeJob(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) (*Job, error) {
	layout, err := HeadLayout(m, s, settings)
	if err != nil {
		return nil, err
	}
	gamma, err := DarknessGamma(settings)
	if err != nil {
		return nil, err
	}
	turn, err := thermal.Rotation(s, settings)
	if err != nil {
		return nil, err
	}
	gray := raster.ToGray(label)
	if gamma != 1 {
		applyGamma(gray, gamma) // on the design, before the turn
	}
	l, err := raster.Render(gray, turn, raster.Frame{Width: layout.WidthDots, Rows: layout.Rows,
		Lane: raster.LaneAt(layout.WidthDots, layout.ContentDots, layout.OffsetDots)})
	if err != nil {
		return nil, err
	}
	packed := raster.PackRows(l.Bits)
	sections, err := Sections(packed, l.Layout.Width)
	if err != nil {
		return nil, err
	}
	return &Job{Paper: PaperFor(s), Layout: layout, Raster: packed, Rows: l.Layout.Rows,
		Sections: sections, label: l}, nil
}

// applyGamma maps each tone t to (t/255)^gamma * 255, kept as float32 like the rest of
// the gray image. Pure black and white stay put.
func applyGamma(g *raster.Gray, gamma float64) {
	for i, v := range g.Pix {
		q := min(max(v, 0), 255) / 255
		p := float32(math.Pow(float64(q), gamma))
		g.Pix[i] = p * 255
	}
}
