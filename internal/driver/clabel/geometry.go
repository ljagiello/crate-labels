package clabel

import (
	"math"

	"github.com/ljagiello/crate-labels/internal/driver/clabel/tspl"
	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
)

// Page sizes the printer sees.
const (
	bandRows      = tspl.DefaultBandRows
	longestPageMM = 190 // a continuous label is cut into pages no longer than this
	gapTenths     = 20  // "GAP 2 mm,0 mm" on die-cut stock
)

// Layout is where the label goes across the head.
type Layout struct {
	WidthDots   int // dots per raster row sent to the printer: the whole head
	ContentDots int // width of the box the label is fitted into
	OffsetDots  int // white dots between the low-x edge and the box
	Rows        int // raster rows along feed; 0 = as long as the fitted image (continuous)
}

// RatedDPI is the resolution a model is sold at: the catalog's dpi rounded to a whole
// number, so 203 for an 8 dots/mm head and 300. The TSPL page text gives millimetres
// converted from dots at this resolution (the CT-320B's 576-dot head is "72.1 mm"). That
// is the text these printers have been sent so far. Changing it needs a test on the
// printer.
func RatedDPI(m *printer.Model) int { return int(math.Round(m.DPI)) }

// HeadLayout places the label across the head. Everything is in head dots at the
// catalog's dpi.
//
// The label's low edge sits fixed_edge_mm beyond dot 0 of the head. That is 0 on the
// CT-230B; on the CT-320B the roll guide holds the label 3 mm off the head. The content
// box is the stock's printable width, centred on the label. If the box would overhang
// either end of the head, it shrinks by the same amount on both sides, so it stays
// centred on the label. Its edges round to the nearest dot. A die-cut label has as many
// rows as its length in dots. The offset_dots calibration replaces the box's offset.
func HeadLayout(m *printer.Model, s *printer.Stock, settings printer.Settings) (Layout, error) {
	head := m.HeadDots
	if head <= 0 || head%8 != 0 {
		return Layout{}, printer.Errorf("%s: model %q needs head_dots, a multiple of 8", m.Source, m.ID)
	}
	fixedEdge, err := thermal.Float(settings, "fixed_edge_mm", 0, 0, 50)
	if err != nil {
		return Layout{}, err
	}
	perMM := m.DotsPerMM()
	centre := (s.FeedMM[0]/2 - fixedEdge) * perMM
	half := min(thermal.AcrossMM(s)*perMM/2, centre, float64(head)-centre)
	if half < 1 {
		return Layout{}, printer.Errorf("With fixed_edge_mm %g, the %s label does not reach the %s's print head.",
			fixedEdge, s.Name, m.Label)
	}
	low, high := int(math.Round(centre-half)), int(math.Round(centre+half))
	content := high - low
	offset, err := thermal.OffsetDots(settings, low, 0, head)
	if err != nil {
		return Layout{}, err
	}
	rows := 0
	if !thermal.IsContinuous(s) {
		rows = int(math.Round(s.FeedMM[1] * perMM))
	}
	return Layout{WidthDots: head, ContentDots: content, OffsetDots: min(offset, head-content), Rows: rows}, nil
}

// PageSettings are the per-page TSPL tunables.
type PageSettings struct {
	Density int // DENSITY, 0..15 (the "darkness" of this family)
	Speed   int // SPEED, 1..15
}

// ReadPageSettings reads density (default 8) and speed (default 2; the CT-320B's model
// options say 1).
func ReadPageSettings(settings printer.Settings) (PageSettings, error) {
	density, err := thermal.Int(settings, "density", 8, 0, 15)
	if err != nil {
		return PageSettings{}, err
	}
	speed, err := thermal.Int(settings, "speed", 2, 1, 15)
	if err != nil {
		return PageSettings{}, err
	}
	return PageSettings{Density: density, Speed: speed}, nil
}

// Page is one TSPL page as sent.
type Page struct {
	Data  []byte // the TSPL bytes
	Rows  int    // raster rows, which set the print time
	Paper Paper  // the paper type this page needs
}

// Pages turns a packed head-width raster (widthDots/8 bytes per row, 1 = black) into
// TSPL pages. Die-cut stock gives one page as long as the label. Continuous stock gives
// as few pages as keep each one within 190 mm, with the rows shared out evenly.
func Pages(m *printer.Model, s *printer.Stock, ps PageSettings, raster []byte, widthDots int) ([]Page, error) {
	bpr := widthDots / 8
	if widthDots <= 0 || widthDots%8 != 0 || len(raster) == 0 || len(raster)%bpr != 0 {
		return nil, printer.Errorf("clabel: %d raster bytes are not whole rows of %d dots", len(raster), widthDots)
	}
	rated := RatedDPI(m)
	rows := len(raster) / bpr
	page := tspl.Page{WidthTenths: tspl.Tenths(widthDots, rated), Density: ps.Density, Speed: ps.Speed,
		Mode: tspl.ModeDeflate}
	if !thermal.IsContinuous(s) {
		page.HeightTenths = int(math.Round(s.FeedMM[1] * 10))
		page.GapTenths = gapTenths
		data, err := page.Encode(raster, bpr)
		if err != nil {
			return nil, err
		}
		return []Page{{Data: data, Rows: rows, Paper: PaperGap}}, nil
	}
	// Measure the limit at the rated resolution, which the SIZE text also uses. Then
	// no page is longer than 190 mm in the printer's own terms: 1518 rows at 203 dpi,
	// 2244 at 300.
	limit := int(longestPageMM * float64(rated) / printer.MMPerInch)
	count := (rows + limit - 1) / limit
	pages := make([]Page, 0, count)
	for k := range count {
		top, bottom := rows*k/count, rows*(k+1)/count
		page.HeightTenths = tspl.Tenths(bottom-top, rated)
		data, err := page.Encode(raster[top*bpr:bottom*bpr], bpr)
		if err != nil {
			return nil, err
		}
		pages = append(pages, Page{Data: data, Rows: bottom - top, Paper: PaperContinuous})
	}
	return pages, nil
}
