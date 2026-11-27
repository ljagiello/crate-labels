package web

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ljagiello/crate-labels/internal/layout"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/store"
)

// The JSON shapes of API.md "Data shapes". Lists are never null.

type jobView struct {
	ID          int64   `json:"id"`
	Status      string  `json:"status"`
	CreatedAt   int64   `json:"created_at"`
	StartedAt   *int64  `json:"started_at"`
	FinishedAt  *int64  `json:"finished_at"`
	PrinterID   string  `json:"printer_id"`
	PrinterName string  `json:"printer_name"`
	Template    string  `json:"template"`
	Stock       string  `json:"stock"`
	Copies      int     `json:"copies"`
	Total       int     `json:"total"`
	Printed     int     `json:"printed"`
	Progress    float64 `json:"progress"`
	Message     string  `json:"message"`
	Error       *string `json:"error"`
	Source      string  `json:"source"`
	Records     int     `json:"records"`
	Title       string  `json:"title"`
	Catno       string  `json:"catno"`
}

func jobJSON(j store.Job) jobView {
	v := jobView{ID: j.ID, Status: j.Status, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		PrinterID: j.PrinterID, PrinterName: j.PrinterName, Template: j.Template, Stock: j.Stock, Copies: j.Copies,
		Total: j.Total, Printed: j.Printed, Progress: j.Progress, Message: j.Message, Source: j.Source, Records: len(j.Records)}
	if j.Error != "" {
		e := j.Error
		v.Error = &e
	}
	if len(j.Records) > 0 {
		if first, err := service.ParseLabel(j.Records[0]); err == nil {
			var parts []string
			for _, p := range []string{first.Artist, first.Title} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			v.Title = strings.Join(parts, " – ")
			v.Catno = first.Catno
		}
	}
	return v
}

func jobsJSON(jobs []store.Job) []jobView {
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobJSON(j))
	}
	return out
}

type printerView struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	ModelID      string         `json:"model_id"`
	Model        string         `json:"model"`
	Family       *string        `json:"family"`
	Link         *string        `json:"link"`
	Address      string         `json:"address"`
	Settings     map[string]any `json:"settings"`
	MissingModel bool           `json:"missing_model"`
}

func (s *Server) printerJSON(t printer.Target) printerView {
	v := printerView{ID: t.ID, Name: t.Name, ModelID: t.ModelID, Model: t.ModelID, Address: t.Address,
		Settings: map[string]any(t.Settings), MissingModel: true}
	if v.Settings == nil {
		v.Settings = map[string]any{}
	}
	if m, ok := s.svc.Catalog.Models[t.ModelID]; ok {
		fam, link := m.Family, string(m.Link)
		v.Model, v.Family, v.Link, v.MissingModel = m.Label, &fam, &link, false
	}
	return v
}

type sheetView struct {
	Rows    int        `json:"rows"`
	Cols    int        `json:"cols"`
	PaperMM [2]float64 `json:"paper_mm"`
}

type stockView struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Media    string      `json:"media"`
	DesignMM [2]float64  `json:"design_mm"`
	FeedMM   [2]*float64 `json:"feed_mm"`
	RotateCW int         `json:"rotate_cw"`
	Round    bool        `json:"round"`
	Sheet    *sheetView  `json:"sheet"`
}

func stockJSON(st *printer.Stock) stockView {
	w := st.FeedMM[0]
	v := stockView{ID: st.ID, Name: st.Name, Media: string(st.Media), DesignMM: st.DesignMM,
		FeedMM: [2]*float64{&w, nil}, RotateCW: st.RotateCW, Round: st.Round}
	if st.FeedMM[1] > 0 {
		h := st.FeedMM[1]
		v.FeedMM[1] = &h
	}
	if st.Sheet != nil {
		v.Sheet = &sheetView{Rows: st.Sheet.Rows, Cols: st.Sheet.Cols, PaperMM: st.Sheet.PaperMM}
	}
	return v
}

func stocksJSON(list []*printer.Stock) []stockView {
	out := make([]stockView, 0, len(list))
	for _, st := range list {
		out = append(out, stockJSON(st))
	}
	return out
}

type modelView struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Family   string         `json:"family"`
	Link     string         `json:"link"`
	DPI      float64        `json:"dpi"`
	HeadDots *int           `json:"head_dots"`
	Stocks   []string       `json:"stocks"`
	Options  map[string]any `json:"options"`
}

func modelJSON(m *printer.Model) modelView {
	v := modelView{ID: m.ID, Label: m.Label, Family: m.Family, Link: string(m.Link), DPI: m.DPI,
		Stocks: append([]string{}, m.Stocks...), Options: map[string]any(m.Options)}
	if m.HeadDots > 0 {
		h := m.HeadDots
		v.HeadDots = &h
	}
	if v.Options == nil {
		v.Options = map[string]any{}
	}
	return v
}

type optionView struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Type         string   `json:"type"`
	Default      any      `json:"default"`
	Min          *float64 `json:"min"`
	Max          *float64 `json:"max"`
	Choices      []string `json:"choices"`
	ChoiceLabels []string `json:"choice_labels"`
	Help         string   `json:"help"`
}

type templateView struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Options      []optionView `json:"options"`
	Stocks       []string     `json:"stocks"`
	NeedsArtwork bool         `json:"needs_artwork"`
	MaxTracks    int          `json:"max_tracks"` // track rows it prints; 0 = not track-based
}

func (s *Server) templateJSON(t layout.Template) templateView {
	v := templateView{ID: t.ID(), Name: t.Name(), Description: t.Description(), Options: []optionView{},
		Stocks: s.svc.TemplateStocks(t), MaxTracks: layout.MaxTracks(t)} // no template draws cover art, so needs_artwork stays false
	for _, o := range t.Options() {
		ov := optionView{ID: o.ID, Label: o.Label, Type: o.Type, Default: o.Default, Min: o.Min, Max: o.Max,
			Choices: append([]string{}, o.Choices...), Help: o.Help, ChoiceLabels: []string{}}
		if len(o.ChoiceLabels) == len(o.Choices) {
			ov.ChoiceLabels = append(ov.ChoiceLabels, o.ChoiceLabels...)
		}
		if ov.Default == nil {
			switch o.Type {
			case layout.OptionBool:
				ov.Default = false
			case layout.OptionInt, layout.OptionFloat:
				ov.Default = 0
			default:
				ov.Default = ""
			}
		}
		v.Options = append(v.Options, ov)
	}
	return v
}

type crateView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreatedAt   int64  `json:"created_at"`
	RecordCount int    `json:"record_count"`
}

func crateJSON(c store.Crate) crateView {
	return crateView{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt, RecordCount: c.Records}
}

type crateRecordView struct {
	ID        int64          `json:"id"`
	Position  int            `json:"position"`
	ReleaseID int64          `json:"release_id"`
	Artist    string         `json:"artist"`
	Title     string         `json:"title"`
	Catno     string         `json:"catno"`
	Year      *int64         `json:"year"`
	Thumb     string         `json:"thumb"`
	Data      *service.Label `json:"data"`      // the edited label; null = prints from Discogs
	EditedAt  *int64         `json:"edited_at"` // when data was last saved
	AddedAt   int64          `json:"added_at"`
	Printed   bool           `json:"printed"`
}

type crateDetailView struct {
	crateView
	Records []crateRecordView `json:"records"`
}

func (s *Server) crateDetail(ctx context.Context, id string) (crateDetailView, error) {
	c, err := s.svc.Store.Crate(ctx, id)
	if err != nil {
		return crateDetailView{}, err
	}
	records, err := s.svc.Store.CrateRecords(ctx, id)
	if err != nil {
		return crateDetailView{}, err
	}
	ids := make([]int64, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.ReleaseID)
	}
	printed, err := s.svc.Printed(ctx, ids)
	if err != nil {
		return crateDetailView{}, err
	}
	out := crateDetailView{crateView: crateJSON(c), Records: []crateRecordView{}}
	for _, rec := range records {
		v := crateRecordView{ID: rec.ID, Position: rec.Position, ReleaseID: rec.ReleaseID, Artist: rec.Artist,
			Title: rec.Title, Catno: rec.Catno, Year: rec.Year, Thumb: rec.Thumb, EditedAt: rec.EditedAt,
			AddedAt: rec.AddedAt, Printed: printed[rec.ReleaseID].Printed}
		if len(rec.Data) > 0 {
			if l, err := service.ParseLabel(rec.Data); err == nil {
				v.Data = &l
			}
		}
		out.Records = append(out.Records, v)
	}
	return out, nil
}

type historyView struct {
	ID        int64           `json:"id"`
	PrintedAt int64           `json:"printed_at"`
	Stock     string          `json:"stock"`
	Template  string          `json:"template"`
	Printer   string          `json:"printer"`
	Artist    string          `json:"artist"`
	Title     string          `json:"title"`
	Catno     string          `json:"catno"`
	Year      string          `json:"year"`
	Label     string          `json:"label"`
	Format    string          `json:"format"`
	ReleaseID *int64          `json:"release_id"`
	Copies    int             `json:"copies"`
	Options   map[string]any  `json:"options"`
	Data      json.RawMessage `json:"data"`
}

func historyJSON(h store.HistoryEntry) historyView {
	data := h.Data
	if l, err := service.ParseLabel(h.Data); err == nil {
		data = l.JSON()
	}
	return historyView{ID: h.ID, PrintedAt: h.PrintedAt, Stock: h.Stock, Template: h.Template, Printer: h.Printer,
		Artist: h.Artist, Title: h.Title, Catno: h.Catno, Year: h.Year, Label: h.Label, Format: h.Format,
		ReleaseID: h.ReleaseID, Copies: h.Copies, Options: h.Options, Data: data}
}
