package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"slices"
	"strings"

	"github.com/ljagiello/crate-labels/internal/discogs"
	"github.com/ljagiello/crate-labels/internal/layout"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/store"
)

// Template returns a template, or a 400 naming the known ones.
func (s *Service) Template(id string) (layout.Template, error) {
	t, err := s.Layouts.Get(id)
	if err == nil && t != nil {
		return t, nil
	}
	var ids []string
	for _, t := range s.Layouts.Templates() {
		ids = append(ids, t.ID())
	}
	return nil, Errorf("Unknown template %q (known: %s).", id, strings.Join(ids, ", "))
}

// Stocks lists every stock in catalog file order.
func (s *Service) Stocks() []*printer.Stock { return s.Catalog.StockList() }

// TemplateStocks lists the ids of the stocks t supports, in catalog order.
func (s *Service) TemplateStocks(t layout.Template) []string {
	out := []string{}
	for _, st := range s.Stocks() {
		if t.Supports(st) {
			out = append(out, st.ID)
		}
	}
	return out
}

// StocksForModel lists the stocks a model takes: its listed ones first, then the rest
// that fit.
func (s *Service) StocksForModel(id string) ([]*printer.Stock, error) {
	m, err := s.Catalog.Model(id)
	if err != nil {
		return nil, err
	}
	return s.Catalog.StocksFor(m), nil
}

// ModelFor picks the model a preview renders for (API.md "Preview"): the printer's
// model; else modelID; else the default printer's model if it takes the stock; else
// the first model that lists the stock; else the first that fits.
func (s *Service) ModelFor(ctx context.Context, printerID, modelID, stockID string) (*printer.Model, *printer.Target, error) {
	if printerID != "" {
		t, err := s.Store.FindPrinter(ctx, printerID)
		if err != nil {
			if store.IsNotFound(err) {
				return nil, nil, Errorf("No saved printer called %q.", printerID)
			}
			return nil, nil, err
		}
		m, err := s.Catalog.Model(t.ModelID)
		if err != nil {
			return nil, nil, Errorf("Printer %s uses model %q, which is not in the catalog.", t.Name, t.ModelID)
		}
		return m, &t, nil
	}
	if modelID != "" {
		m, err := s.Catalog.Model(modelID)
		return m, nil, err
	}
	st, err := s.Catalog.Stock(stockID)
	if err != nil {
		return nil, nil, err
	}
	if def := s.Store.SettingString(ctx, "default_printer"); def != "" {
		if t, err := s.Store.Printer(ctx, def); err == nil {
			if m, err := s.Catalog.Model(t.ModelID); err == nil && s.Catalog.Fits(m, st) {
				return m, nil, nil
			}
		}
	}
	// Models whose driver is in this build come first, so a preview never needs a
	// missing driver while another one could render it.
	var usable, rest []*printer.Model
	for _, m := range s.Catalog.ModelList() {
		if _, err := printer.DriverFor(m.Family); err == nil {
			usable = append(usable, m)
		} else {
			rest = append(rest, m)
		}
	}
	for _, models := range [][]*printer.Model{usable, rest} {
		for _, m := range models {
			if slices.Contains(m.Stocks, st.ID) {
				return m, nil, nil
			}
		}
		for _, m := range models {
			if s.Catalog.Fits(m, st) {
				return m, nil, nil
			}
		}
	}
	return nil, nil, Errorf("No printer model takes %s.", st.Name)
}

// Source is what to print: an edited label, a release id, or pasted text, in that
// order of precedence.
type Source struct {
	Data      *Label
	ReleaseID *int64
	Ref       string
}

// Resolve turns a source into label data.
func (s *Service) Resolve(ctx context.Context, src Source) (Label, error) {
	switch {
	case src.Data != nil:
		return src.Data.normalized(), nil
	case src.ReleaseID != nil:
		if *src.ReleaseID <= 0 {
			return Label{}, Errorf("Give a release_id, a Discogs link (ref) or label data.")
		}
		l, _, err := s.ReleaseLabel(ctx, *src.ReleaseID)
		return l, err
	case strings.TrimSpace(src.Ref) != "":
		ref, ok := discogs.ParseRef(src.Ref)
		if !ok {
			return Label{}, Errorf("Paste a Discogs release or master link, or a release id.")
		}
		id, err := s.ResolveRef(ctx, ref)
		if err != nil {
			return Label{}, err
		}
		l, _, err := s.ReleaseLabel(ctx, id)
		return l, err
	}
	return Label{}, Errorf("Give a release_id, a Discogs link (ref) or label data.")
}

// checkFit returns the template and stock, after the model-takes-stock and
// template-supports-stock rules.
func (s *Service) checkFit(m *printer.Model, templateID, stockID string) (layout.Template, *printer.Stock, error) {
	t, err := s.Template(templateID)
	if err != nil {
		return nil, nil, err
	}
	st, err := s.Catalog.Stock(stockID)
	if err != nil {
		return nil, nil, err
	}
	if !s.Catalog.Fits(m, st) {
		return nil, nil, Errorf("%s can't print on %s.", m.Label, st.Name)
	}
	if !t.Supports(st) {
		return nil, nil, Errorf("The %s template does not fit %s.", t.Name(), st.Name)
	}
	return t, st, nil
}

// render draws one label for model m.
func (s *Service) render(ctx context.Context, data Label, t layout.Template, m *printer.Model, st *printer.Stock, opts map[string]any) (*image.Gray, printer.Canvas, error) {
	canvas, err := s.Catalog.CanvasFor(m.ID, st.ID)
	if err != nil {
		return nil, canvas, err
	}
	d := data.ToLayout()
	if opts == nil {
		opts = map[string]any{}
	}
	img, err := s.Layouts.Render(t.ID(), &d, canvas, st, opts)
	if err != nil {
		return nil, canvas, err
	}
	return img, canvas, nil
}

// PreviewRequest asks for a preview.
type PreviewRequest struct {
	Source    Source
	Template  string
	Stock     string
	PrinterID string
	ModelID   string
	Options   map[string]any
}

// Preview is the PNG the UI shows: exactly what the driver will print.
type Preview struct {
	PNG     []byte
	Canvas  printer.Canvas
	ModelID string
	Stock   *printer.Stock
}

// Preview renders and dithers a label as the chosen printer will print it.
func (s *Service) Preview(ctx context.Context, r PreviewRequest) (Preview, error) {
	m, target, err := s.ModelFor(ctx, r.PrinterID, r.ModelID, r.Stock)
	if err != nil {
		return Preview{}, err
	}
	t, st, err := s.checkFit(m, r.Template, r.Stock)
	if err != nil {
		return Preview{}, err
	}
	data, err := s.Resolve(ctx, r.Source)
	if err != nil {
		return Preview{}, err
	}
	img, canvas, err := s.render(ctx, data, t, m, st, r.Options)
	if err != nil {
		return Preview{}, err
	}
	if target == nil {
		target = &printer.Target{ModelID: m.ID}
	}
	shown, err := s.Catalog.Preview(target, st.ID, img, nil)
	if err != nil {
		return Preview{}, err
	}
	b, err := EncodePNG(shown)
	if err != nil {
		return Preview{}, err
	}
	return Preview{PNG: b, Canvas: canvas, ModelID: m.ID, Stock: st}, nil
}

// EncodePNG writes img as a 1-bit PNG when it only holds black and white, else as 8-bit
// grey.
func EncodePNG(img *image.Gray) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	bilevel := true
	for _, v := range img.Pix {
		if v != 0 && v != 255 {
			bilevel = false
			break
		}
	}
	if !bilevel {
		err := enc.Encode(&buf, img)
		return buf.Bytes(), err
	}
	b := img.Bounds()
	pal := image.NewPaletted(b, color.Palette{color.Gray{0}, color.Gray{255}})
	for y := b.Min.Y; y < b.Max.Y; y++ {
		src := img.Pix[(y-b.Min.Y)*img.Stride:]
		dst := pal.Pix[(y-b.Min.Y)*pal.Stride:]
		for x := 0; x < b.Dx(); x++ {
			if src[x] == 255 {
				dst[x] = 1
			}
		}
	}
	err := enc.Encode(&buf, pal)
	return buf.Bytes(), err
}
