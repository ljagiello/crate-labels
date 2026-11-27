package phomemo

import (
	"image"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/raster"
)

// DotsPerMM is the head resolution of every supported model (203.2 dpi).
const DotsPerMM = 8

func dots(mm float64) int { return raster.RoundHalfUp(mm * DotsPerMM) }

// Geometry is how one label is laid out for the printer.
type Geometry struct {
	TurnCW   int           // total clockwise turn of the design before rasterising
	Frame    raster.Frame  // the raster the design is fitted into
	Layout   raster.Layout // where the design landed in it
	Media    byte          // GapMedia or ContinuousMedia
	SkipFeed bool          // leave the feed command out of the footer
	TailRows int           // blank rows sent after the label
}

// LabelGeometry lays out a width×height design (design orientation) for m and s.
// settings are layered over m.Options.
//
// The raster is always the head's width. The label's lane is its printable width
// across the head, placed by paper_align or offset_dots; a stock wider than the head
// is cut to the head and the design scaled down to match. The extra_rotate_cw
// calibration turns the design but not the physical label, so a quarter turn scales
// the design down to fit.
func LabelGeometry(m *printer.Model, s *printer.Stock, width, height int, settings printer.Settings) (Geometry, error) {
	if s.Media == printer.MediaSheet {
		return Geometry{}, printer.Errorf("%s prints on label rolls, not on %s.", m.Label, s.Name)
	}
	opts := printer.Merge(m.Options, settings)
	turn, err := thermal.Rotation(s, opts)
	if err != nil {
		return Geometry{}, err
	}
	justify, err := paperAlignSetting(opts)
	if err != nil {
		return Geometry{}, err
	}
	offset, err := offsetSetting(opts)
	if err != nil {
		return Geometry{}, err
	}
	if width <= 0 || height <= 0 {
		return Geometry{}, printer.Errorf("The label image is empty (%d×%d px).", width, height)
	}
	if turn == 90 || turn == 270 {
		width, height = height, width
	}
	across, along := s.DesignMM[0], s.DesignMM[1] // the label as it leaves the printer
	if s.RotateCW == 90 || s.RotateCW == 270 {
		across, along = along, across
	}
	labelDots := dots(across)
	head := m.HeadDots
	if head <= 0 { // a model without a head width prints at the label's own width
		head = (labelDots + 7) / 8 * 8
	}
	lane := raster.JustifiedLane(head, labelDots, justify)
	if offset != nil {
		lane = raster.LaneAt(head, labelDots, *offset)
	}
	f := raster.Frame{Width: head, Lane: lane}
	g := Geometry{TurnCW: turn, Media: GapMedia, TailRows: extraInt(s, extraTailRows)}
	if s.Media == printer.MediaContinuous {
		f.MaxRows = extraInt(s, extraMaxLabelRows)
		g.Media = ContinuousMedia
	} else {
		f.Rows = dots(along)
	}
	layout, err := raster.Fit(width, height, f)
	if err != nil {
		return Geometry{}, printer.Errorf("%s cannot lay out %s: %v", m.Label, s.Name, err)
	}
	g.Frame, g.Layout = f, layout
	g.SkipFeed, _ = s.Extra[extraSkipFeed].(bool)
	return g, nil
}

func extraInt(s *printer.Stock, key string) int {
	n, _ := wholeNumber(s.Extra[key])
	return n
}

// renderLabel rasterises one label: the design is turned, scaled into its lane and
// dithered.
func renderLabel(m *printer.Model, s *printer.Stock, label image.Image, settings printer.Settings) (*raster.Label, Geometry, error) {
	b := label.Bounds()
	geo, err := LabelGeometry(m, s, b.Dx(), b.Dy(), settings)
	if err != nil {
		return nil, Geometry{}, err
	}
	l, err := raster.Render(raster.ToGray(label), geo.TurnCW, geo.Frame)
	if err != nil {
		return nil, Geometry{}, printer.Errorf("%s cannot rasterise %s: %v", m.Label, s.Name, err)
	}
	return l, geo, nil
}

// Encode is the pure encoder: one label image in design orientation becomes the jobs
// to send in order. settings are layered over m.Options. blockRows, when positive,
// cuts each job's raster into GS v 0 blocks of at most that many rows (see
// SettingLegacyBlockRows).
func Encode(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings, blockRows int) ([]Job, error) {
	opts := printer.Merge(m.Options, settings)
	l, geo, err := renderLabel(m, s, label, opts)
	if err != nil {
		return nil, err
	}
	speed, err := thermal.Int(opts, SettingSpeed, DefaultSpeed, 0, 255)
	if err != nil {
		return nil, err
	}
	darkness, err := thermal.Int(opts, SettingDarkness, DefaultDarkness, 0, 255)
	if err != nil {
		return nil, err
	}
	bytesPerRow := geo.Layout.Width / 8
	rows := append(raster.PackRows(l.Bits), make([]byte, bytesPerRow*geo.TailRows)...)
	return BuildJobs(rows, bytesPerRow, JobSettings{Speed: speed, Darkness: darkness,
		Media: geo.Media, SkipFeed: geo.SkipFeed, BlockRows: blockRows})
}

// preview is exactly the dots a job burns, cropped to the label's lane and turned back
// to design orientation.
func preview(m *printer.Model, s *printer.Stock, label image.Image, settings printer.Settings) (*image.Gray, error) {
	l, _, err := renderLabel(m, s, label, settings)
	if err != nil {
		return nil, err
	}
	return l.Preview(), nil
}
