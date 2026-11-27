package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/discogs"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/service"
)

// routes registers every endpoint of docs/API.md.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /static/{file...}", s.staticFile)

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("PUT /api/settings/discogs-token", s.setToken)
	mux.HandleFunc("POST /api/settings/discogs-token/test", s.testToken)
	mux.HandleFunc("DELETE /api/settings/discogs-token", s.deleteToken)

	mux.HandleFunc("GET /api/discogs/lookup", s.lookup)
	mux.HandleFunc("GET /api/discogs/release/{id}", s.release)
	mux.HandleFunc("GET /api/discogs/search", s.search)
	mux.HandleFunc("GET /api/discogs/collection/folders", s.folders)
	mux.HandleFunc("GET /api/discogs/collection", s.collection)
	mux.HandleFunc("GET /api/discogs/wantlist", s.wantlist)
	mux.HandleFunc("GET /api/discogs/image", s.image)

	mux.HandleFunc("GET /api/templates", s.templates)
	mux.HandleFunc("GET /api/models", s.models)
	mux.HandleFunc("GET /api/stocks", s.stocks)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("POST /api/print", s.print)
	mux.HandleFunc("POST "+tempoPath, s.detectTempo)
	mux.HandleFunc("POST /api/tempo/video", s.detectTempoVideo)

	mux.HandleFunc("GET /api/jobs", s.jobs)
	mux.HandleFunc("GET /api/jobs/events", s.jobEvents)
	mux.HandleFunc("GET /api/jobs/{id}", s.job)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", s.retryJob)

	mux.HandleFunc("GET /api/printers", s.printers)
	mux.HandleFunc("POST /api/printers", s.addPrinter)
	mux.HandleFunc("PATCH /api/printers/{id}", s.patchPrinter)
	mux.HandleFunc("DELETE /api/printers/{id}", s.deletePrinter)
	mux.HandleFunc("POST /api/printers/{id}/test", s.testPrint)
	mux.HandleFunc("GET /api/printers/ble/scan", s.bleScan)
	mux.HandleFunc("POST /api/printers/ble/probe", s.bleProbe)
	mux.HandleFunc("GET /api/printers/ipp/discover", s.ippDiscover)
	mux.HandleFunc("GET /api/printers/cups/queues", s.cupsQueues)

	mux.HandleFunc("GET /api/crates", s.crates)
	mux.HandleFunc("POST /api/crates", s.createCrate)
	mux.HandleFunc("GET /api/crates/{id}", s.crate)
	mux.HandleFunc("PATCH /api/crates/{id}", s.renameCrate)
	mux.HandleFunc("DELETE /api/crates/{id}", s.deleteCrate)
	mux.HandleFunc("POST /api/crates/{id}/records", s.addToCrate)
	mux.HandleFunc("PUT /api/crates/{id}/records/{recordId}/data", s.editCrateRecord)
	mux.HandleFunc("DELETE /api/crates/{id}/records/{recordId}", s.removeFromCrate)
	mux.HandleFunc("PUT /api/crates/{id}/order", s.reorderCrate)
	mux.HandleFunc("POST /api/crates/{id}/print", s.printCrate)

	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("POST /api/history/{id}/reprint", s.reprint)
	mux.HandleFunc("DELETE /api/history/{id}", s.deleteHistory)
	mux.HandleFunc("GET /api/printed", s.printed)
	mux.HandleFunc("PUT /api/printed/{release_id}", s.setPrinted)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "No such API endpoint: "+r.Method+" "+r.URL.Path)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Not found.")
	})
}

// --- static ------------------------------------------------------------------------------

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "The UI is missing from this build.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

func (s *Server) staticFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !fs.ValidPath(name) || name == "." {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}
	if info, err := fs.Stat(s.static, name); err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.static, name)
}

// --- health and settings -------------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": service.Version, "templates": len(s.svc.Layouts.Templates()),
		"drivers": printer.Families(), "drivers_error": nil, "data_dir": s.opts.DataDir,
	})
}

func (s *Server) settingsValues(ctx context.Context) (map[string]json.RawMessage, error) {
	return s.svc.Store.Settings(ctx)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	values, err := s.settingsValues(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	total, err := s.svc.Store.LabelsPrinted(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"discogs": s.svc.TokenStatus(ctx), "values": values,
		"labels_printed": total, "data_dir": s.opts.DataDir})
}

var settingKey = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Values == nil {
		s.fail(w, r, invalid("values: required"))
		return
	}
	for key := range body.Values {
		secret := false
		for _, word := range []string{"token", "secret", "password", "key_material"} {
			secret = secret || strings.Contains(key, word)
		}
		if !settingKey.MatchString(key) || secret {
			s.fail(w, r, badRequest("Setting %q cannot be stored here.", key))
			return
		}
	}
	ctx := r.Context()
	for key, value := range body.Values {
		if err := s.svc.Store.SetSetting(ctx, key, value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	values, err := s.settingsValues(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

func (s *Server) setToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token *string `json:"token"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Token == nil || *body.Token == "" || len(*body.Token) > 200 {
		s.fail(w, r, invalid("token: required (1 to 200 characters)"))
		return
	}
	st, err := s.svc.SetToken(r.Context(), *body.Token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) testToken(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.TestToken(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.ClearToken(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// --- Discogs ----------------------------------------------------------------------------------

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	text, refresh := q.str("q", ""), q.boolean("refresh", false)
	if q.err == nil && !q.has("q") {
		q.err = invalid("q: required")
	}
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	res, err := s.svc.LookupText(r.Context(), text, refresh)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := newQuery(r)
	refresh := q.boolean("refresh", false)
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	res, err := s.svc.LookupRelease(r.Context(), id, refresh)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// Page sizes. A search page matches Discogs' own default (50), so a query without
// per_page asks for what discogs.com would show. Collection and wantlist pages use the
// largest page Discogs allows (100): a collection is read front to back, and fewer pages
// spend less of the per-minute request budget.
const (
	searchPerPage     = 50
	collectionPerPage = 100
)

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	p := discogs.SearchParams{
		Query: q.str("q", ""), Barcode: q.str("barcode", ""), Catno: q.str("catno", ""), Label: q.str("label", ""),
		Artist: q.str("artist", ""), Title: q.str("title", ""),
		// Only releases can be printed, so they are the default; "master" is allowed for
		// finding a release through its master.
		Type: q.oneOf("type", "release", "release", "master"),
		// No format filter unless asked: the UI sends format=Vinyl while "Vinyl only" is on.
		Format:  q.str("format", ""),
		Page:    int(q.integer("page", 1, 1, 1<<20)),
		PerPage: int(q.integer("per_page", searchPerPage, 1, 100)),
		Refresh: q.boolean("refresh", false),
	}
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	res, err := s.svc.Search(r.Context(), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) folders(w http.ResponseWriter, r *http.Request) {
	f, err := s.svc.Folders(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": f})
}

func (s *Server) collection(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	// The default is filing order, artist A to Z. Without sort_order, "added" and
	// "rating" read newest or best first, every other key A to Z.
	sortKey := q.oneOf("sort", "artist", "artist", "title", "catno", "label", "year", "added", "rating")
	order := "asc"
	if sortKey == "added" || sortKey == "rating" {
		order = "desc"
	}
	p := discogs.CollectionParams{
		FolderID:  q.integer("folder_id", 0, 0, 1<<62), // 0 is Discogs' "All" folder
		Page:      int(q.integer("page", 1, 1, 1<<20)),
		PerPage:   int(q.integer("per_page", collectionPerPage, 1, 100)),
		Sort:      sortKey,
		SortOrder: q.oneOf("sort_order", order, "asc", "desc"),
		Refresh:   q.boolean("refresh", false),
	}
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	res, err := s.svc.Collection(r.Context(), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) wantlist(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	page, perPage := int(q.integer("page", 1, 1, 1<<20)), int(q.integer("per_page", collectionPerPage, 1, 100))
	refresh := q.boolean("refresh", false)
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	res, err := s.svc.Wantlist(r.Context(), page, perPage, refresh)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	if !q.has("url") {
		s.fail(w, r, invalid("url: required"))
		return
	}
	ct, body, err := s.svc.Discogs.FetchImage(r.Context(), q.str("url", ""))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
