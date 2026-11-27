package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ljagiello/crate-labels/internal/service"
)

func (s *Server) templates(w http.ResponseWriter, r *http.Request) {
	out := []templateView{}
	for _, t := range s.svc.Layouts.Templates() {
		out = append(out, s.templateJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	out := []modelView{}
	for _, m := range s.svc.Catalog.ModelList() {
		out = append(out, modelJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

func (s *Server) stocks(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	modelID := q.str("model_id", "")
	if id := q.str("printer_id", ""); id != "" {
		t, err := s.svc.Store.Printer(r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		modelID = t.ModelID
	}
	if modelID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"stocks": stocksJSON(s.svc.Stocks())})
		return
	}
	list, err := s.svc.StocksForModel(modelID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stocks": stocksJSON(list)})
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		labelSource
		Template  *string        `json:"template"`
		Stock     *string        `json:"stock"`
		PrinterID *string        `json:"printer_id"`
		ModelID   *string        `json:"model_id"`
		Options   map[string]any `json:"options"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Template == nil || body.Stock == nil {
		s.fail(w, r, invalid("template and stock: required"))
		return
	}
	req := service.PreviewRequest{Source: body.source(), Template: *body.Template, Stock: *body.Stock, Options: body.Options}
	if body.PrinterID != nil {
		req.PrinterID = *body.PrinterID
	}
	if body.ModelID != nil {
		req.ModelID = *body.ModelID
	}
	p, err := s.svc.Preview(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Canvas-Width", strconv.Itoa(p.Canvas.Width))
	h.Set("X-Canvas-Height", strconv.Itoa(p.Canvas.Height))
	h.Set("X-Canvas-Dpi", headerNumber(p.Canvas.DPI))
	h.Set("X-Model-Id", p.ModelID)
	h.Set("X-Design-Mm", headerNumber(p.Stock.DesignMM[0])+"x"+headerNumber(p.Stock.DesignMM[1]))
	h.Set("Content-Length", strconv.Itoa(len(p.PNG)))
	_, _ = w.Write(p.PNG)
}

// copiesIn checks an optional copies field (1..99, default 1).
func copiesIn(c *int) (int, error) {
	if c == nil {
		return 1, nil
	}
	if *c < 1 || *c > service.MaxCopies {
		return 0, invalid("copies: must be between 1 and %d", service.MaxCopies)
	}
	return *c, nil
}

func (s *Server) print(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items        []labelSource  `json:"items"`
		Template     *string        `json:"template"`
		Stock        *string        `json:"stock"`
		PrinterID    *string        `json:"printer_id"`
		Copies       *int           `json:"copies"`
		Options      map[string]any `json:"options"`
		PrintOptions map[string]any `json:"print_options"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	switch {
	case body.Template == nil || body.Stock == nil || body.PrinterID == nil:
		s.fail(w, r, invalid("template, stock and printer_id: required"))
		return
	case len(body.Items) < 1 || len(body.Items) > service.MaxItems:
		s.fail(w, r, invalid("items: between 1 and %d records", service.MaxItems))
		return
	}
	copies, err := copiesIn(body.Copies)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var sources []service.Source
	for _, it := range body.Items {
		sources = append(sources, it.source())
	}
	j, err := s.svc.Print(r.Context(), service.PrintRequest{Sources: sources, Template: *body.Template, Stock: *body.Stock,
		Printer: *body.PrinterID, Copies: copies, Options: body.Options, PrintOptions: body.PrintOptions, Origin: "ui"})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(j))
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	active, limit := q.boolean("active", false), q.integer("limit", 30, 1, 200)
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	list, err := s.svc.Store.Jobs(r.Context(), int(limit), active)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobsJSON(list)})
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j, err := s.svc.Store.Job(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(j))
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j, err := s.svc.Cancel(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(j))
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j, err := s.svc.Retry(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(j))
}

// keepAlive is how often an idle event stream sends a comment.
const keepAlive = 15 * time.Second

// jobEvents is the SSE stream of job changes. It ends when the client goes away or the
// server shuts down, so Ctrl-C is not held up.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	events, unsubscribe := s.svc.Subscribe()
	defer unsubscribe()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 2000\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.stopping:
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		case j, ok := <-events:
			if !ok {
				return
			}
			b, err := jsonBytes(jobJSON(j))
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: job\ndata: %s\n\n", b); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
