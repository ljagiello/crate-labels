package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/store"
)

func (s *Server) crates(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Store.Crates(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]crateView, 0, len(list))
	for _, c := range list {
		out = append(out, crateJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"crates": out})
}

func (s *Server) createCrate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name *string `json:"name"`
	}
	if err := decode(r, &body, true); err != nil {
		s.fail(w, r, err)
		return
	}
	name := ""
	if body.Name != nil {
		name = *body.Name
	}
	c, err := s.svc.Store.CreateCrate(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, crateJSON(c))
}

func (s *Server) writeCrate(w http.ResponseWriter, r *http.Request, id string) {
	d, err := s.crateDetail(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) crate(w http.ResponseWriter, r *http.Request) { s.writeCrate(w, r, r.PathValue("id")) }

func (s *Server) renameCrate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name *string `json:"name"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Name == nil {
		s.fail(w, r, invalid("name: required"))
		return
	}
	c, err := s.svc.Store.RenameCrate(r.Context(), r.PathValue("id"), *body.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, crateJSON(c))
}

func (s *Server) deleteCrate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.Store.DeleteCrate(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// addToCrate adds a release to a crate. Posted label data is kept as the record's edited
// label; a release id or link only stores what the crate list shows.
func (s *Server) addToCrate(w http.ResponseWriter, r *http.Request) {
	var body labelSource
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := s.svc.Store.Crate(ctx, id); err != nil {
		s.fail(w, r, err)
		return
	}
	data, err := s.svc.Resolve(ctx, body.source())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if data.RID() <= 0 {
		s.fail(w, r, badRequest("Only records from Discogs go in a crate; this label has no release id."))
		return
	}
	n := store.NewCrateRecord{ReleaseID: data.RID(), Artist: data.Artist, Title: data.Title, Catno: data.Catno}
	if body.Data != nil {
		n.Data = data.JSON()
	}
	if y, err := strconv.ParseInt(strings.TrimSpace(data.Year), 10, 64); err == nil && y > 0 {
		n.Year = &y
	}
	if rel, err := s.svc.Discogs.Release(ctx, data.RID(), false); err == nil {
		n.Thumb = rel.Thumb
	}
	if _, err := s.svc.Store.AddToCrate(ctx, id, n); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeCrate(w, r, id)
}

// recordInCrate returns the record id when it belongs to the crate in the path (404 if
// not).
func (s *Server) recordInCrate(r *http.Request) (int64, error) {
	recordID, err := pathInt(r, "recordId")
	if err != nil {
		return 0, err
	}
	rec, err := s.svc.Store.CrateRecord(r.Context(), recordID)
	if err != nil {
		return 0, err
	}
	if rec.CrateID != r.PathValue("id") {
		return 0, &store.NotFoundError{Msg: "Crate " + strconv.Quote(r.PathValue("id")) + " has no record " +
			strconv.FormatInt(recordID, 10) + "."}
	}
	return recordID, nil
}

// editCrateRecord saves (data) or drops (data: null) a record's edited label.
func (s *Server) editCrateRecord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data json.RawMessage `json:"data"` // absent = a mistake; null = undo the edits
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Data == nil {
		s.fail(w, r, invalid("data: required (label data, or null to undo the edits)"))
		return
	}
	var raw []byte
	if string(bytes.TrimSpace(body.Data)) != "null" {
		l, err := service.ParseLabel(body.Data)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		raw = l.JSON()
	}
	recordID, err := s.recordInCrate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.svc.Store.SetCrateRecordData(r.Context(), recordID, raw); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeCrate(w, r, r.PathValue("id"))
}

func (s *Server) removeFromCrate(w http.ResponseWriter, r *http.Request) {
	recordID, err := s.recordInCrate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.Store.RemoveFromCrate(r.Context(), recordID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeCrate(w, r, r.PathValue("id"))
}

func (s *Server) reorderCrate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RecordIDs *[]int64 `json:"record_ids"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.RecordIDs == nil {
		s.fail(w, r, invalid("record_ids: required"))
		return
	}
	if _, err := s.svc.Store.ReorderCrate(r.Context(), r.PathValue("id"), *body.RecordIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeCrate(w, r, r.PathValue("id"))
}

func (s *Server) printCrate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template     *string        `json:"template"`
		Stock        *string        `json:"stock"`
		PrinterID    *string        `json:"printer_id"`
		RecordIDs    *[]int64       `json:"record_ids"`
		Order        *string        `json:"order"`
		Copies       *int           `json:"copies"`
		Options      map[string]any `json:"options"`
		PrintOptions map[string]any `json:"print_options"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Template == nil || body.Stock == nil || body.PrinterID == nil {
		s.fail(w, r, invalid("template, stock and printer_id: required"))
		return
	}
	order := service.OrderCrate
	if body.Order != nil {
		order = *body.Order
	}
	if !slices.Contains(service.CrateOrders, order) {
		s.fail(w, r, invalid("order: must be one of %s", strings.Join(service.CrateOrders, ", ")))
		return
	}
	copies, err := copiesIn(body.Copies)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	req := service.CratePrintRequest{CrateID: r.PathValue("id"), Order: order, Template: *body.Template, Stock: *body.Stock,
		Printer: *body.PrinterID, Copies: copies, Options: body.Options, PrintOptions: body.PrintOptions}
	if body.RecordIDs != nil {
		req.RecordIDs = append([]int64{}, *body.RecordIDs...)
	}
	j, err := s.svc.PrintCrate(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(j))
}

// --- history and printed state ---------------------------------------------------------------

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	limit := q.integer("limit", 100, 1, 500)
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	ctx := r.Context()
	list, err := s.svc.Store.History(ctx, int(limit))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	total, err := s.svc.Store.LabelsPrinted(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]historyView, 0, len(list))
	for _, h := range list {
		out = append(out, historyJSON(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out, "labels_printed": total})
}

func (s *Server) reprint(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var body struct {
		PrinterID *string `json:"printer_id"`
		Copies    *int    `json:"copies"`
	}
	if err := decode(r, &body, true); err != nil {
		s.fail(w, r, err)
		return
	}
	copies := 0
	if body.Copies != nil {
		if copies, err = copiesIn(body.Copies); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	printerID := ""
	if body.PrinterID != nil {
		printerID = *body.PrinterID
	}
	j, err := s.svc.Reprint(r.Context(), id, printerID, copies)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(j))
}

func (s *Server) deleteHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.Store.DeleteHistory(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// maxPrintedIDs bounds one GET /api/printed: a crate (500 records) fits in one call.
const maxPrintedIDs = 500

// printed says which of the releases in ?release_ids= have a label.
func (s *Server) printed(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	for part := range strings.SplitSeq(r.URL.Query().Get("release_ids"), ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			s.fail(w, r, invalid("release_ids: %q is not a release id", part))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) > maxPrintedIDs {
		s.fail(w, r, invalid("release_ids: at most %d at a time", maxPrintedIDs))
		return
	}
	views, err := s.svc.Printed(r.Context(), ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make(map[string]service.PrintedView, len(views))
	for id, v := range views {
		out[strconv.FormatInt(id, 10)] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
}

// setPrinted sets or clears a release's printed override.
func (s *Server) setPrinted(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "release_id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var body struct {
		Override *string `json:"override"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	overrides := []string{store.OverrideAuto, store.OverridePrinted, store.OverrideNotPrinted}
	if body.Override == nil || !slices.Contains(overrides, *body.Override) {
		s.fail(w, r, invalid("override: must be one of %s", strings.Join(overrides, ", ")))
		return
	}
	v, err := s.svc.SetPrinted(r.Context(), id, *body.Override)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ReleaseID int64 `json:"release_id"`
		service.PrintedView
	}{id, v})
}
