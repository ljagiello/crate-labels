package munbyn

import (
	"math"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/raster"
)

// headWidthDots is the FM226 head, used when a model does not say.
const headWidthDots = 384

// Layout is where the label goes across the head.
type Layout struct {
	WidthDots   int // dots per raster row sent to the printer (a multiple of 8)
	ContentDots int // width of the box the label is fitted into
	OffsetDots  int // white dots between the low-x edge and the box
	Rows        int // raster rows along feed; 0 = as long as the fitted image (continuous)
}

// HeadLayout sizes the raster: the printable width in whole bytes, at most the head.
// The offset_dots calibration adds white dots on the low-x side and widens the raster
// by whole bytes, up to the head width.
func HeadLayout(m *printer.Model, s *printer.Stock, settings printer.Settings) (Layout, error) {
	head := m.HeadDots
	if head <= 0 {
		head = headWidthDots
	}
	content := min(raster.RoundHalfUp(float64(thermal.AcrossMM(s)*m.DotsPerMM())), head)
	content -= content % 8
	if content <= 0 {
		return Layout{}, printer.Errorf("%s is too narrow for the %s.", s.Name, m.Label)
	}
	offset, err := thermal.OffsetDots(settings, 0, 0, head)
	if err != nil {
		return Layout{}, err
	}
	width := min(head, (content+offset+7)/8*8)
	rows := 0
	if !thermal.IsContinuous(s) {
		rows = raster.RoundHalfUp(float64(s.FeedMM[1] * m.DotsPerMM()))
	}
	return Layout{WidthDots: width, ContentDots: content, OffsetDots: min(offset, width-content), Rows: rows}, nil
}

// PaperFor is the paper type a stock needs: continuous for continuous media, else gap.
func PaperFor(s *printer.Stock) PaperType {
	if s.Media == printer.MediaContinuous {
		return PaperContinuous
	}
	return PaperGap
}

// DarknessGamma maps the darkness calibration (1..15, default 8) to a gamma applied to
// gray tones before dithering. The FM226 protocol has no heat or darkness field, so 8
// leaves tones alone and each step of 4 doubles (darker) or halves (lighter) the gamma.
// Pure black and white never change.
func DarknessGamma(settings printer.Settings) (float64, error) {
	darkness, err := thermal.Float(settings, "darkness", 8, 1, 15)
	if err != nil {
		return 0, err
	}
	return math.Pow(2, (darkness-8)/4), nil
}
