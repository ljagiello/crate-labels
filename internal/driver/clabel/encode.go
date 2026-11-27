package clabel

import (
	"image"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/raster"
)

// EncodeJob is everything the printer receives for one label, without a link: the
// head-width raster (honouring extra_rotate_cw, offset_dots, fixed_edge_mm) and its TSPL
// pages (honouring density and speed). label is in design orientation, white = 255.
func EncodeJob(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) (*Job, error) {
	layout, err := HeadLayout(m, s, settings)
	if err != nil {
		return nil, err
	}
	ps, err := ReadPageSettings(settings)
	if err != nil {
		return nil, err
	}
	turn, err := thermal.Rotation(s, settings)
	if err != nil {
		return nil, err
	}
	l, err := raster.Render(raster.ToGray(label), turn, raster.Frame{Width: layout.WidthDots, Rows: layout.Rows,
		Lane: raster.LaneAt(layout.WidthDots, layout.ContentDots, layout.OffsetDots)})
	if err != nil {
		return nil, err
	}
	packed := raster.PackRows(l.Bits)
	pages, err := Pages(m, s, ps, packed, l.Layout.Width)
	if err != nil {
		return nil, err
	}
	return &Job{Layout: layout, Raster: packed, Pages: pages, label: l}, nil
}
