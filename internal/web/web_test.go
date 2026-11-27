package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/service/servicetest"
	"github.com/ljagiello/crate-labels/internal/store"
)

type env struct {
	*servicetest.Env
	srv  *Server
	http *httptest.Server
}

func newEnv(t *testing.T, d Discovery) *env {
	t.Helper()
	e := servicetest.New(t)
	srv := New(e.Service, Options{Discovery: d, DataDir: filepath.Join(e.Dir, "data")})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Stop()
		hs.Close()
	})
	return &env{Env: e, srv: srv, http: hs}
}

type reply struct {
	status int
	body   []byte
	header http.Header
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("not JSON (%d): %s", r.status, r.body)
	}
	return m
}

func (r reply) errorText(t *testing.T) string {
	t.Helper()
	msg, _ := r.json(t)["error"].(string)
	if msg == "" {
		t.Fatalf("no error message in %d %s", r.status, r.body)
	}
	return msg
}

func (e *env) do(t *testing.T, method, path string, body any, headers ...string) reply {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.http.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil || (method != "GET" && method != "HEAD") {
		req.Header.Set("Content-Type", "application/json") // as app.js sends every write
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: raw, header: resp.Header}
}

func (e *env) ok(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	r := e.do(t, method, path, body)
	if r.status != 200 {
		t.Fatalf("%s %s = %d %s", method, path, r.status, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s %s Content-Type = %q", method, path, ct)
	}
	return r.json(t)
}

func (e *env) status(t *testing.T, want int, method, path string, body any) string {
	t.Helper()
	r := e.do(t, method, path, body)
	if r.status != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, r.status, r.body, want)
	}
	return r.errorText(t)
}

func (e *env) addPrinter(t *testing.T, name, address string, settings map[string]any) string {
	t.Helper()
	p := e.ok(t, "POST", "/api/printers", map[string]any{"name": name, "model_id": servicetest.Model,
		"address": address, "settings": settings})
	return p["id"].(string)
}

func (e *env) waitJob(t *testing.T, id float64) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j := e.ok(t, "GET", "/api/jobs/"+num(id), nil)
		if s := j["status"]; s != "queued" && s != "printing" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %v never finished: %v", id, j)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func num(f float64) string { b, _ := json.Marshal(int64(f)); return string(b) }

func TestIndexAndStaticFiles(t *testing.T) {
	e := newEnv(t, Discovery{})
	r := e.do(t, "GET", "/", nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-cache" || !bytes.Contains(r.body, []byte("/static/app.js")) {
		t.Fatalf("index = %d %v", r.status, r.header)
	}
	want, _ := os.ReadFile("static/index.html")
	if !bytes.Equal(r.body, want) {
		t.Fatal("index.html is not served unchanged")
	}
	for path, ct := range map[string]string{"/static/app.js": "javascript", "/static/style.css": "text/css"} {
		r := e.do(t, "GET", path, nil)
		if r.status != 200 || !strings.Contains(r.header.Get("Content-Type"), ct) {
			t.Errorf("%s = %d %q", path, r.status, r.header.Get("Content-Type"))
		}
	}
	for _, path := range []string{"/static/", "/static/nope.js", "/static/../server.go"} {
		if r := e.do(t, "GET", path, nil); r.status != 404 {
			t.Errorf("%s = %d", path, r.status)
		}
	}
	// The UI uses the system's fonts: the label fonts are only for server-side rendering
	// and are not served.
	css, err := staticFiles.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(css, []byte("@font-face")) || bytes.Contains(css, []byte("/fonts/")) {
		t.Error("style.css loads web fonts")
	}
	if r := e.do(t, "GET", "/fonts/plexcond_400.ttf", nil); r.status != 404 {
		t.Errorf("a label font is served: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/nope", nil); r.status != 404 || r.errorText(t) == "" {
		t.Errorf("unknown API path = %d", r.status)
	}
}

func TestHealthTemplatesModelsStocks(t *testing.T) {
	e := newEnv(t, Discovery{})
	h := e.ok(t, "GET", "/api/health", nil)
	if h["ok"] != true || h["version"] != "0.1.0" || h["templates"] != 3.0 || h["drivers_error"] != nil {
		t.Fatalf("health = %v", h)
	}
	if drivers, _ := h["drivers"].([]any); len(drivers) == 0 || h["data_dir"] == "" {
		t.Fatalf("health = %v", h)
	}
	tpl := e.ok(t, "GET", "/api/templates", nil)["templates"].([]any)
	big := tpl[2].(map[string]any)
	if big["id"] != "big-only" || len(big["stocks"].([]any)) != 1 || big["needs_artwork"] != false || big["max_tracks"] != 0.0 {
		t.Fatalf("template = %v", big)
	}
	opt := big["options"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "label", "type", "default", "min", "max", "choices", "choice_labels", "help"} {
		if _, ok := opt[k]; !ok {
			t.Errorf("option lacks %q", k)
		}
	}
	if opt["choices"] == nil || opt["choice_labels"] == nil {
		t.Errorf("option lists are null: %v", opt)
	}
	models := e.ok(t, "GET", "/api/models", nil)["models"].([]any)
	var test map[string]any
	for _, m := range models {
		if mm := m.(map[string]any); mm["id"] == servicetest.Model {
			test = mm
		}
	}
	if test == nil || test["dpi"] != 203.2 || test["head_dots"] != 384.0 || test["options"].(map[string]any)["darkness"] != 8.0 {
		t.Fatalf("model = %v", test)
	}
	all := e.ok(t, "GET", "/api/stocks", nil)["stocks"].([]any)
	first := all[0].(map[string]any)
	if first["id"] != "50x80-two-track" || len(all) != 14 {
		t.Fatalf("stocks = %v", all)
	}
	for _, s := range all {
		st := s.(map[string]any)
		if st["id"] == "d30-15mm-tape" && st["feed_mm"].([]any)[1] != nil {
			t.Errorf("continuous feed length = %v", st["feed_mm"])
		}
		if st["id"] == "avery-5160" && st["sheet"].(map[string]any)["rows"] != 10.0 {
			t.Errorf("sheet = %v", st["sheet"])
		}
	}
	model := e.ok(t, "GET", "/api/stocks?model_id="+servicetest.Model, nil)["stocks"].([]any)
	if model[0].(map[string]any)["id"] != "40x30" || model[1].(map[string]any)["id"] != "50x30" {
		t.Fatalf("model stocks = %v", model)
	}
	e.status(t, 404, "GET", "/api/stocks?printer_id=ghost", nil)
	id := e.addPrinter(t, "Bench", "", nil)
	if got := e.ok(t, "GET", "/api/stocks?printer_id="+id, nil)["stocks"].([]any); len(got) != len(model) {
		t.Fatalf("printer stocks = %v", got)
	}
}

func TestEmptyListsAreNeverNull(t *testing.T) {
	e := newEnv(t, Discovery{})
	for path, key := range map[string]string{"/api/printers": "printers", "/api/crates": "crates",
		"/api/history": "history", "/api/jobs": "jobs"} {
		r := e.do(t, "GET", path, nil)
		if !bytes.Contains(r.body, []byte(`"`+key+`":[]`)) {
			t.Errorf("%s = %s", path, r.body)
		}
	}
	if r := e.do(t, "GET", "/api/printers", nil); !bytes.Contains(r.body, []byte(`"default":null`)) {
		t.Errorf("printers = %s", r.body)
	}
	c := e.ok(t, "POST", "/api/crates", map[string]any{})
	if r := e.do(t, "GET", "/api/crates/"+c["id"].(string), nil); !bytes.Contains(r.body, []byte(`"records":[]`)) {
		t.Errorf("crate = %s", r.body)
	}
}

func TestTokenLifecycleNeverEchoesTheToken(t *testing.T) {
	e := newEnv(t, Discovery{})
	_ = e.Tokens.Delete(context.Background())
	s := e.ok(t, "GET", "/api/settings", nil)
	if d := s["discogs"].(map[string]any); d["token_set"] != false || d["source"] != "none" || d["username"] != nil {
		t.Fatalf("discogs = %v", d)
	}
	e.status(t, 400, "POST", "/api/settings/discogs-token/test", nil)
	msg := e.status(t, 400, "PUT", "/api/settings/discogs-token", map[string]any{"token": "not a token"})
	if !strings.Contains(msg, "Settings → Developers") {
		t.Errorf("hint missing: %q", msg)
	}
	e.status(t, 422, "PUT", "/api/settings/discogs-token", map[string]any{})
	token := strings.Repeat("Q", 40)
	r := e.do(t, "PUT", "/api/settings/discogs-token", map[string]any{"token": token})
	if r.status != 200 || bytes.Contains(r.body, []byte(token)) {
		t.Fatalf("set = %d %s", r.status, r.body)
	}
	if d := r.json(t); d["token_set"] != true || d["source"] != "keychain" || d["username"] != "testuser" {
		t.Fatalf("status = %v", d)
	}
	for _, path := range []string{"/api/settings", "/api/health"} {
		if r := e.do(t, "GET", path, nil); bytes.Contains(r.body, []byte(token)) {
			t.Fatalf("%s echoes the token", path)
		}
	}
	if d := e.ok(t, "POST", "/api/settings/discogs-token/test", nil); d["username"] != "testuser" {
		t.Fatalf("test = %v", d)
	}
	if d := e.ok(t, "DELETE", "/api/settings/discogs-token", nil); d["token_set"] != false || d["username"] != nil {
		t.Fatalf("delete = %v", d)
	}
	// A token Discogs rejects is a 401.
	e.Stub.Extra = map[string]func(*http.Request) (int, string){"/oauth/identity": func(*http.Request) (int, string) {
		return 401, `{"message": "Invalid consumer token."}`
	}}
	e.status(t, 401, "PUT", "/api/settings/discogs-token", map[string]any{"token": token})
}

func TestSettingsValues(t *testing.T) {
	e := newEnv(t, Discovery{})
	v := e.ok(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{
		"ui.template": "dj-single", "ui.options": map[string]any{"dj-single": map[string]any{"show_qr": false}}, "theme": "dark"}})
	values := v["values"].(map[string]any)
	if values["ui.template"] != "dj-single" || values["theme"] != "dark" {
		t.Fatalf("values = %v", values)
	}
	v = e.ok(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{"theme": nil}})
	if _, ok := v["values"].(map[string]any)["theme"]; ok {
		t.Fatal("null did not delete")
	}
	for _, key := range []string{"discogs.token", "my_secret", "api.password", "key_material", "Upper", "1abc", "a b"} {
		e.status(t, 400, "PUT", "/api/settings", map[string]any{"values": map[string]any{key: "x"}})
	}
	e.status(t, 422, "PUT", "/api/settings", map[string]any{"vals": map[string]any{}})
	e.status(t, 422, "PUT", "/api/settings", "{nope")
	s := e.ok(t, "GET", "/api/settings", nil)
	if s["labels_printed"] != 0.0 || s["data_dir"] == "" {
		t.Fatalf("settings = %v", s)
	}
}

func TestLookupSearchCollection(t *testing.T) {
	e := newEnv(t, Discovery{})
	l := e.ok(t, "GET", "/api/discogs/lookup?q="+urlEscape("https://www.discogs.com/master/4589-x"), nil)
	if ref := l["ref"].(map[string]any); ref["kind"] != "master" || ref["id"] != 4589.0 {
		t.Fatalf("ref = %v", ref)
	}
	rel := l["release"].(map[string]any)
	if rel["id"] != 118638.0 || rel["format"] != `Vinyl, 12", 45 RPM` || rel["printed"] != false || rel["master_id"] != 4589.0 {
		t.Fatalf("release = %v", rel)
	}
	data := l["data"].(map[string]any)
	tr := data["tracks"].([]any)[0].(map[string]any)
	if data["release_id"] != 118638.0 || data["qr_url"] != "https://www.discogs.com/release/118638" || tr["bpm"] != nil ||
		tr["key"] != nil || tr["included"] != true {
		t.Fatalf("data = %v", data)
	}
	msg := e.status(t, 400, "GET", "/api/discogs/lookup?q=Kniteforce", nil)
	if msg != "Paste a Discogs release or master link, or a release id." {
		t.Errorf("lookup message = %q", msg)
	}
	e.status(t, 422, "GET", "/api/discogs/lookup", nil)
	e.status(t, 404, "GET", "/api/discogs/release/999", nil)
	e.status(t, 422, "GET", "/api/discogs/release/abc", nil)
	if r := e.ok(t, "GET", "/api/discogs/release/10953799?refresh=true", nil); r["data"].(map[string]any)["artist"] != "Paul Bradley" {
		t.Fatalf("release = %v", r)
	}
	e.ok(t, "PUT", "/api/printed/118638", map[string]any{"override": "printed"})
	res := e.ok(t, "GET", "/api/discogs/search?q=kf039&format=", nil)
	hit := res["results"].([]any)[0].(map[string]any)
	if hit["artist"] != "DJ Force & The Evolution" || hit["printed"] != true || res["pagination"].(map[string]any)["items"] != 1.0 {
		t.Fatalf("search = %v", res)
	}
	var searchQuery string
	for _, r := range e.Stub.All() {
		if r.URL.Path == "/database/search" {
			searchQuery = r.URL.RawQuery
		}
	}
	if strings.Contains(searchQuery, "format=") || !strings.Contains(searchQuery, "q=kf039") {
		t.Errorf(`format="" must turn the Vinyl filter off: %s`, searchQuery)
	}
	lastQuery := func() url.Values { all := e.Stub.All(); return all[len(all)-1].URL.Query() }
	e.ok(t, "GET", "/api/discogs/search?barcode=5012345678900", nil)
	if q := lastQuery(); q.Has("format") || q.Get("type") != "release" || q.Get("per_page") != "50" {
		t.Errorf("search defaults: %s", q.Encode())
	}
	e.ok(t, "GET", "/api/discogs/search?q=kf039&format=Vinyl", nil)
	if lastQuery().Get("format") != "Vinyl" {
		t.Error("format=Vinyl was not passed on")
	}
	e.status(t, 400, "GET", "/api/discogs/search", nil)
	e.status(t, 422, "GET", "/api/discogs/search?q=abc&type=artist", nil)
	e.status(t, 422, "GET", "/api/discogs/search?q=abc&per_page=101", nil)
	e.status(t, 422, "GET", "/api/discogs/search?q=abc&page=0", nil)
	folders := e.ok(t, "GET", "/api/discogs/collection/folders", nil)["folders"].([]any)
	if len(folders) != 2 || folders[0].(map[string]any)["name"] != "All" {
		t.Fatalf("folders = %v", folders)
	}
	coll := e.ok(t, "GET", "/api/discogs/collection?folder_id=0&page=1&sort=added&sort_order=desc", nil)
	it := coll["items"].([]any)[0].(map[string]any)
	if it["artist"] != "Sanxion / Mannik" || it["notes"] != "plays great" || it["disc_size"] != `12"` || it["instance_id"] != 99001.0 {
		t.Fatalf("item = %v", it)
	}
	e.status(t, 422, "GET", "/api/discogs/collection?sort=bpm", nil)
	for query, want := range map[string]string{
		"":                            "artist asc",
		"&sort=added":                 "added desc",
		"&sort=rating":                "rating desc",
		"&sort=year":                  "year asc",
		"&sort=title&sort_order=desc": "title desc",
	} {
		e.ok(t, "GET", "/api/discogs/collection?refresh=true"+query, nil)
		q := lastQuery()
		if got := q.Get("sort") + " " + q.Get("sort_order"); got != want || q.Get("per_page") != "100" {
			t.Errorf("collection%s asked Discogs for %s", query, q.Encode())
		}
	}
	if w := e.ok(t, "GET", "/api/discogs/wantlist", nil); len(w["items"].([]any)) != 1 {
		t.Fatalf("wantlist = %v", w)
	}
	img := e.do(t, "GET", "/api/discogs/image?url="+urlEscape("https://i.discogs.com/abc.jpeg"), nil)
	if img.status != 200 || img.header.Get("Content-Type") != "image/jpeg" ||
		img.header.Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("image = %d %v", img.status, img.header)
	}
	for _, r := range e.Stub.All() {
		if r.URL.Host == "i.discogs.com" && r.Header.Get("Authorization") != "" {
			t.Fatal("the image request carried the token")
		}
	}
	e.status(t, 502, "GET", "/api/discogs/image?url="+urlEscape("https://evil.example.com/x.jpeg"), nil)
	e.status(t, 502, "GET", "/api/discogs/image?url="+urlEscape("https://api.discogs.com/releases/1"), nil)
}

func urlEscape(s string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F", "?", "%3F", "&", "%26").Replace(s)
}

func TestSearchWithoutTokenIs401(t *testing.T) {
	e := newEnv(t, Discovery{})
	_ = e.Tokens.Delete(context.Background())
	msg := e.status(t, 401, "GET", "/api/discogs/search?q=kniteforce", nil)
	if !strings.Contains(msg, "Add it in Settings") {
		t.Errorf("message = %q", msg)
	}
	e.status(t, 401, "GET", "/api/discogs/collection/folders", nil)
}

func TestPreviewPNG(t *testing.T) {
	e := newEnv(t, Discovery{})
	body := map[string]any{"data": map[string]any{"artist": "A", "title": "T", "styles": "Jungle, Hardcore",
		"tracks": []any{map[string]any{"position": "A1", "title": "x", "bpm": 172, "key": "8A", "bogus": 1}}},
		"template": "dj-single", "stock": "40x30", "printer_id": nil, "model_id": servicetest.Model, "options": map[string]any{}}
	r := e.do(t, "POST", "/api/preview", body)
	if r.status != 200 || r.header.Get("Content-Type") != "image/png" {
		t.Fatalf("preview = %d %s", r.status, r.body)
	}
	want := map[string]string{"X-Canvas-Width": "320", "X-Canvas-Height": "240", "X-Canvas-Dpi": "203.2",
		"X-Model-Id": servicetest.Model, "X-Design-Mm": "40.0x30.0", "Cache-Control": "no-store"}
	for k, v := range want {
		if got := r.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	img, err := png.Decode(bytes.NewReader(r.body))
	if err != nil || img.Bounds().Dx() != 320 {
		t.Fatalf("png = %v %v", img, err)
	}
	got := e.Layouts.Rendered()
	last := got[len(got)-1]
	if len(last.Styles) != 2 || last.Tracks[0].BPM != 172 || last.Tracks[0].Key != "8A" || !last.Tracks[0].Included {
		t.Fatalf("rendered data = %+v", last)
	}
	// No printer and no model: the first model that lists the stock.
	if r := e.do(t, "POST", "/api/preview", map[string]any{"release_id": 118638, "template": "dj-single", "stock": "40x30"}); r.status != 200 {
		t.Fatalf("by release = %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/preview", map[string]any{"ref": "[r118638]", "template": "test-pattern", "stock": "40x30",
		"model_id": servicetest.Model}); r.status != 200 {
		t.Fatalf("by ref = %d %s", r.status, r.body)
	}
	e.status(t, 400, "POST", "/api/preview", map[string]any{"data": map[string]any{}, "template": "dj-single", "stock": "70x80", "model_id": servicetest.Model})
	e.status(t, 400, "POST", "/api/preview", map[string]any{"data": map[string]any{}, "template": "nope", "stock": "40x30"})
	e.status(t, 400, "POST", "/api/preview", map[string]any{"template": "dj-single", "stock": "40x30", "model_id": servicetest.Model})
	e.status(t, 422, "POST", "/api/preview", map[string]any{"data": map[string]any{}, "stock": "40x30"})
	e.status(t, 422, "POST", "/api/preview", map[string]any{"data": map[string]any{"artist": map[string]any{}}, "template": "dj-single", "stock": "40x30"})
	e.status(t, 422, "POST", "/api/preview", map[string]any{"release_id": "abc", "template": "dj-single", "stock": "40x30"})
	e.status(t, 422, "POST", "/api/preview", "not json")
}

func TestPrintPollHistoryAndReprint(t *testing.T) {
	e := newEnv(t, Discovery{})
	out := filepath.Join(e.Dir, "printer.bin")
	pid := e.addPrinter(t, "Bench", out, map[string]any{"darkness": 10})
	job := e.ok(t, "POST", "/api/print", map[string]any{"items": []any{map[string]any{"release_id": 118638},
		map[string]any{"ref": "https://www.discogs.com/release/89569"}}, "template": "dj-single", "stock": "40x30",
		"printer_id": pid, "copies": 2, "options": map[string]any{"show_qr": false}, "print_options": map[string]any{"darkness": 12}})
	if job["status"] != "queued" || job["total"] != 4.0 || job["records"] != 2.0 || job["source"] != "ui" ||
		job["title"] != "DJ Force & The Evolution – Simply Electric / Out Of Control" || job["catno"] != "KF039" {
		t.Fatalf("job = %v", job)
	}
	for _, k := range []string{"started_at", "finished_at", "error"} {
		if v, ok := job[k]; !ok || v != nil {
			t.Errorf("%s = %v", k, v)
		}
	}
	done := e.waitJob(t, job["id"].(float64))
	if done["status"] != "done" || done["printed"] != 4.0 || done["progress"] != 1.0 || done["finished_at"] == nil {
		t.Fatalf("done = %v", done)
	}
	if b, _ := os.ReadFile(out); bytes.Count(b, []byte("LBL 320x240 d=12\n")) != 4 {
		t.Fatal("printer output missing")
	}
	jobs := e.ok(t, "GET", "/api/jobs?limit=12", nil)["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v", jobs)
	}
	if a := e.ok(t, "GET", "/api/jobs?active=true", nil)["jobs"].([]any); len(a) != 0 {
		t.Fatalf("active = %v", a)
	}
	e.status(t, 422, "GET", "/api/jobs?limit=201", nil)
	e.status(t, 404, "GET", "/api/jobs/999", nil)
	hist := e.ok(t, "GET", "/api/history?limit=40", nil)
	entries := hist["history"].([]any)
	if len(entries) != 2 || hist["labels_printed"] != 4.0 {
		t.Fatalf("history = %v", hist)
	}
	h := entries[0].(map[string]any)
	if h["title"] != "The Future Sound Of Hardcore" || h["copies"] != 2.0 || h["printer"] != "Bench" ||
		h["options"].(map[string]any)["show_qr"] != false || h["data"].(map[string]any)["release_id"] != 89569.0 {
		t.Fatalf("entry = %v", h)
	}
	states := e.ok(t, "GET", "/api/printed?release_ids=118638,89569,1,1", nil)["releases"].(map[string]any)
	if got := states["118638"].(map[string]any); got["printed"] != true || got["labels"] != 2.0 || got["override"] != "auto" ||
		got["last_printed_at"] == nil || len(states) != 3 {
		t.Fatalf("printed = %v", states)
	}
	if got := states["1"].(map[string]any); got["printed"] != false || got["labels"] != 0.0 || got["last_printed_at"] != nil {
		t.Fatalf("never printed = %v", got)
	}
	e.status(t, 422, "GET", "/api/printed?release_ids=1,x", nil)
	again := e.ok(t, "POST", "/api/history/"+num(h["id"].(float64))+"/reprint", map[string]any{"printer_id": pid})
	if again["source"] != "reprint:"+num(h["id"].(float64)) || again["total"] != 2.0 {
		t.Fatalf("reprint = %v", again)
	}
	e.waitJob(t, again["id"].(float64))
	e.status(t, 422, "POST", "/api/history/1/reprint", map[string]any{"copies": 0})
	e.status(t, 404, "POST", "/api/history/999/reprint", map[string]any{"printer_id": pid})
	retried := e.ok(t, "POST", "/api/jobs/"+num(job["id"].(float64))+"/retry", nil)
	if retried["source"] != "retry:"+num(job["id"].(float64)) {
		t.Fatalf("retry = %v", retried)
	}
	e.waitJob(t, retried["id"].(float64))
	// The reprint replaced the entry (same data, stock, template and options).
	latest := e.ok(t, "GET", "/api/history", nil)["history"].([]any)
	if len(latest) != 2 {
		t.Fatalf("history after reprint = %v", latest)
	}
	hid := num(latest[0].(map[string]any)["id"].(float64))
	if d := e.ok(t, "DELETE", "/api/history/"+hid, nil); d["deleted"] != latest[0].(map[string]any)["id"] {
		t.Fatalf("delete = %v", d)
	}
	e.status(t, 404, "DELETE", "/api/history/"+hid, nil)
}

func TestPrintErrors(t *testing.T) {
	e := newEnv(t, Discovery{})
	pid := e.addPrinter(t, "Bench", "", nil)
	item := []any{map[string]any{"data": map[string]any{"title": "x"}}}
	base := func(over map[string]any) map[string]any {
		b := map[string]any{"items": item, "template": "dj-single", "stock": "40x30", "printer_id": pid}
		for k, v := range over {
			b[k] = v
		}
		return b
	}
	if msg := e.status(t, 400, "POST", "/api/print", base(map[string]any{"printer_id": "ghost"})); !strings.Contains(msg, "No saved printer called") {
		t.Errorf("message = %q", msg)
	}
	e.status(t, 400, "POST", "/api/print", base(map[string]any{"stock": "70x80"}))
	e.status(t, 400, "POST", "/api/print", base(map[string]any{"template": "nope"}))
	e.status(t, 422, "POST", "/api/print", base(map[string]any{"copies": 100}))
	e.status(t, 422, "POST", "/api/print", base(map[string]any{"copies": 0}))
	e.status(t, 422, "POST", "/api/print", base(map[string]any{"copies": "two"}))
	e.status(t, 422, "POST", "/api/print", base(map[string]any{"items": []any{}}))
	e.status(t, 422, "POST", "/api/print", map[string]any{"items": item})
	many := make([]any, 501)
	for i := range many {
		many[i] = item[0]
	}
	e.status(t, 422, "POST", "/api/print", base(map[string]any{"items": many}))
	e.status(t, 404, "POST", "/api/jobs/1/retry", nil)
}

func TestCancelQueuedJob(t *testing.T) {
	e := newEnv(t, Discovery{})
	pid := e.addPrinter(t, "Slow", "", map[string]any{"hold_ms": 200})
	body := map[string]any{"items": []any{map[string]any{"data": map[string]any{"title": "x"}}}, "template": "dj-single",
		"stock": "40x30", "printer_id": pid}
	first := e.ok(t, "POST", "/api/print", body)
	second := e.ok(t, "POST", "/api/print", body)
	c := e.ok(t, "POST", "/api/jobs/"+num(second["id"].(float64))+"/cancel", nil)
	if c["status"] != "cancelled" {
		t.Fatalf("cancel = %v", c)
	}
	e.status(t, 400, "POST", "/api/jobs/"+num(first["id"].(float64))+"/retry", nil)
	e.waitJob(t, first["id"].(float64))
}

func TestPrintersCRUDAndCalibration(t *testing.T) {
	e := newEnv(t, Discovery{})
	p := e.ok(t, "POST", "/api/printers", map[string]any{"name": "Desk", "model_id": servicetest.Model,
		"settings": map[string]any{"darkness": "12", "speed": "", "custom": 1}})
	id := p["id"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id) || p["model"] != "App test printer" || p["family"] != servicetest.Family ||
		p["link"] != "file" || p["missing_model"] != false || p["address"] != "" {
		t.Fatalf("printer = %v", p)
	}
	if s := p["settings"].(map[string]any); s["darkness"] != 12.0 || s["custom"] != 1.0 || len(s) != 2 {
		t.Fatalf("settings = %v", s)
	}
	list := e.ok(t, "GET", "/api/printers", nil)
	if list["default"] != id || len(list["calibration_keys"].(map[string]any)) != 7 ||
		list["calibration_keys"].(map[string]any)["ble_gap_ms"] != "float" {
		t.Fatalf("list = %v", list)
	}
	second := e.addPrinter(t, "Spare", "", nil)
	if e.ok(t, "GET", "/api/printers", nil)["default"] != id {
		t.Fatal("the default moved to the second printer")
	}
	e.status(t, 400, "POST", "/api/printers", map[string]any{"name": "X", "model_id": "no-such-model"})
	e.status(t, 400, "POST", "/api/printers", map[string]any{"name": "X", "model_id": servicetest.Model, "settings": map[string]any{"extra_rotate_cw": 45}})
	e.status(t, 422, "POST", "/api/printers", map[string]any{"name": "", "model_id": servicetest.Model})
	e.status(t, 422, "POST", "/api/printers", map[string]any{"name": strings.Repeat("x", 81), "model_id": servicetest.Model})
	e.status(t, 422, "POST", "/api/printers", map[string]any{"model_id": servicetest.Model})
	p = e.ok(t, "PATCH", "/api/printers/"+id, map[string]any{"name": "Desk M110", "address": " /tmp/x ",
		"settings": map[string]any{"darkness": nil, "offset_dots": 4, "paper_align": "center"}})
	if s := p["settings"].(map[string]any); p["name"] != "Desk M110" || p["address"] != "/tmp/x" || s["darkness"] != nil ||
		s["offset_dots"] != 4.0 || s["custom"] != 1.0 {
		t.Fatalf("patched = %v", p)
	}
	e.status(t, 400, "PATCH", "/api/printers/"+id, map[string]any{"settings": map[string]any{"paper_align": "rod"}})
	e.status(t, 404, "PATCH", "/api/printers/ghost", map[string]any{"name": "x"})
	job := e.ok(t, "POST", "/api/printers/"+id+"/test", map[string]any{"stock": "40x30"})
	if job["template"] != "test-pattern" || job["source"] != "test" || job["title"] != "crate-labels – Test pattern" {
		t.Fatalf("test job = %v", job)
	}
	e.waitJob(t, job["id"].(float64))
	e.status(t, 404, "POST", "/api/printers/ghost/test", map[string]any{"stock": "40x30"})
	e.status(t, 422, "POST", "/api/printers/"+id+"/test", map[string]any{})
	// A printer whose model left the catalog still lists.
	gone := printer.Target{ID: second, Name: "Spare", ModelID: "gone"}
	_ = e.Store.SavePrinter(context.Background(), &gone)
	for _, raw := range e.ok(t, "GET", "/api/printers", nil)["printers"].([]any) {
		if pp := raw.(map[string]any); pp["id"] == second && (pp["missing_model"] != true || pp["model"] != "gone" || pp["family"] != nil) {
			t.Fatalf("missing model = %v", pp)
		}
	}
	if d := e.ok(t, "DELETE", "/api/printers/"+id, nil); d["deleted"] != id {
		t.Fatalf("delete = %v", d)
	}
	if e.ok(t, "GET", "/api/printers", nil)["default"] != nil {
		t.Fatal("deleting the default printer did not clear default_printer")
	}
	e.status(t, 404, "DELETE", "/api/printers/"+id, nil)
}

func TestDiscoveryEndpoints(t *testing.T) {
	e := newEnv(t, Discovery{})
	for _, c := range []struct{ method, path string }{{"GET", "/api/printers/ble/scan"}, {"GET", "/api/printers/ipp/discover"},
		{"GET", "/api/printers/cups/queues"}} {
		if msg := e.status(t, 501, c.method, c.path, nil); !strings.Contains(msg, "is not available in this build yet (") {
			t.Errorf("%s: %q", c.path, msg)
		}
	}
	e.status(t, 501, "POST", "/api/printers/ble/probe", map[string]any{"address": "x"})

	fam := "phomemo"
	var gotTimeout time.Duration
	var gotAll bool
	d := Discovery{
		BLEScan: func(_ context.Context, timeout time.Duration, showAll bool) ([]BLEDevice, error) {
			gotTimeout, gotAll = timeout, showAll
			return []BLEDevice{{Address: "UUID-1", Name: "Q199E1234", RSSI: -60, MaybeFamily: &fam}}, nil
		},
		BLEProbe: func(_ context.Context, addr string, _ time.Duration) (BLEProbe, error) {
			return BLEProbe{Services: []string{"ff00"}, Family: &fam}, nil
		},
		IPPDiscover: func(context.Context, time.Duration) ([]IPPPrinter, error) {
			return []IPPPrinter{
				{Name: "Brother", URI: "ipp://10.0.0.5:631/ipp/print", Printable: true,
					Kind: []string{"document", "envelope", "label", "postcard"}},
				{Name: "Zebra", URI: "ipp://10.0.0.6:631/ipp/print", Kind: []string{"label", "roll"}},
			}, nil
		},
		CUPSQueues: func(context.Context) ([]CUPSQueue, error) { return nil, nil },
	}
	e2 := newEnv(t, d)
	scan := e2.ok(t, "GET", "/api/printers/ble/scan?timeout=2&show_all=true", nil)
	dev := scan["devices"].([]any)[0].(map[string]any)
	if dev["maybe_family"] != "phomemo" || dev["model_ids"] == nil || dev["service_uuids"] == nil || gotTimeout != 2*time.Second || !gotAll {
		t.Fatalf("scan = %v (timeout %v, all %v)", scan, gotTimeout, gotAll)
	}
	e2.status(t, 422, "GET", "/api/printers/ble/scan?timeout=31", nil)
	probe := e2.ok(t, "POST", "/api/printers/ble/probe", map[string]any{"address": "UUID-1"})["probe"].(map[string]any)
	if probe["family"] != "phomemo" || probe["model_id"] != nil || probe["handshake"] != nil {
		t.Fatalf("probe = %v", probe)
	}
	e2.status(t, 422, "POST", "/api/printers/ble/probe", map[string]any{"address": "x", "timeout": 0})
	found := e2.ok(t, "GET", "/api/printers/ipp/discover?timeout=1", nil)["printers"].([]any)
	ipp := found[0].(map[string]any)
	if ipp["uri"] != "ipp://10.0.0.5:631/ipp/print" || ipp["formats"] == nil || ipp["txt"] == nil || ipp["hostname_uri"] != nil ||
		ipp["suggested_model_id"] != "ipp-sheet" {
		t.Fatalf("ipp = %v", ipp)
	}
	if z := found[1].(map[string]any); z["suggested_model_id"] != "ipp-label" {
		t.Fatalf("label printer = %v", z)
	}
	if r := e2.do(t, "GET", "/api/printers/cups/queues", nil); !bytes.Contains(r.body, []byte(`"queues":[]`)) {
		t.Fatalf("cups = %s", r.body)
	}
	d.CUPSQueues = func(context.Context) ([]CUPSQueue, error) { return nil, errors.New("lpstat failed") }
	e3 := newEnv(t, d)
	e3.status(t, 500, "GET", "/api/printers/cups/queues", nil)
}

func TestCratesAndPrintingThem(t *testing.T) {
	e := newEnv(t, Discovery{})
	pid := e.addPrinter(t, "Bench", "", nil)
	c := e.ok(t, "POST", "/api/crates", map[string]any{"name": "Happy HC"})
	id := c["id"].(string)
	if c["name"] != "Happy HC" || c["record_count"] != 0.0 || len(c) != 4 {
		t.Fatalf("crate = %v", c)
	}
	e.status(t, 400, "POST", "/api/crates", map[string]any{"name": strings.Repeat("x", 41)})
	e.status(t, 400, "POST", "/api/crates", map[string]any{"name": "happy hc"})
	e.status(t, 422, "POST", "/api/crates", map[string]any{"name": 7})
	if blank := e.ok(t, "POST", "/api/crates", map[string]any{"name": "  "}); blank["name"] != "Untitled crate" {
		t.Fatalf("blank name = %v", blank)
	}
	if none := e.ok(t, "POST", "/api/crates", nil); none["name"] != "Untitled crate 2" {
		t.Fatalf("no body = %v", none)
	}
	d := e.ok(t, "POST", "/api/crates/"+id+"/records", map[string]any{"release_id": 89569})
	a := d["records"].([]any)[0].(map[string]any)
	if a["artist"] != "Various" || a["catno"] != "KF LP 002" || a["year"] != 1996.0 || a["data"] != nil || a["edited_at"] != nil ||
		!strings.HasPrefix(a["thumb"].(string), "https://i.discogs.com/") || a["printed"] != false || d["record_count"] != 1.0 {
		t.Fatalf("record = %v", a)
	}
	e.status(t, 400, "POST", "/api/crates/"+id+"/records", map[string]any{"ref": "https://www.discogs.com/release/89569"})
	d = e.ok(t, "POST", "/api/crates/"+id+"/records", map[string]any{"data": map[string]any{"artist": "DJ Force",
		"title": "Edited title", "year": "1995", "release_id": "118638", "styles": []any{}, "genres": []any{"Electronic"}}})
	b := d["records"].([]any)[1].(map[string]any)
	if b["data"].(map[string]any)["title"] != "Edited title" || b["year"] != 1995.0 || b["edited_at"] == nil {
		t.Fatalf("edited record = %v", b)
	}
	e.status(t, 400, "POST", "/api/crates/"+id+"/records", map[string]any{"data": map[string]any{"title": "no release"}})
	e.status(t, 404, "POST", "/api/crates/nope/records", map[string]any{"release_id": 89569})
	aid, bid := num(a["id"].(float64)), num(b["id"].(float64))
	d = e.ok(t, "PUT", "/api/crates/"+id+"/records/"+aid+"/data", map[string]any{"data": map[string]any{"title": "Saved", "release_id": 89569}})
	if r := d["records"].([]any)[0].(map[string]any); r["data"].(map[string]any)["title"] != "Saved" || r["edited_at"] == nil {
		t.Fatalf("edited = %v", r)
	}
	d = e.ok(t, "PUT", "/api/crates/"+id+"/records/"+aid+"/data", map[string]any{"data": nil})
	if r := d["records"].([]any)[0].(map[string]any); r["data"] != nil || r["edited_at"] != nil {
		t.Fatalf("undone = %v", r)
	}
	e.status(t, 422, "PUT", "/api/crates/"+id+"/records/"+aid+"/data", map[string]any{})
	e.status(t, 422, "PUT", "/api/crates/"+id+"/records/"+aid+"/data", map[string]any{"data": "text"})
	other := e.ok(t, "POST", "/api/crates", map[string]any{"name": "Other"})["id"].(string)
	e.status(t, 404, "PUT", "/api/crates/"+other+"/records/"+aid+"/data", map[string]any{"data": nil})
	e.status(t, 404, "DELETE", "/api/crates/"+other+"/records/"+aid, nil)
	e.status(t, 400, "PUT", "/api/crates/"+id+"/order", map[string]any{"record_ids": []any{a["id"]}})
	e.status(t, 422, "PUT", "/api/crates/"+id+"/order", map[string]any{})
	d = e.ok(t, "PUT", "/api/crates/"+id+"/order", map[string]any{"record_ids": []any{b["id"], a["id"]}})
	if first := d["records"].([]any)[0].(map[string]any); first["id"] != b["id"] || first["position"] != 0.0 {
		t.Fatalf("order = %v", d["records"])
	}
	// By artist, "DJ Force" files before "Various" whatever the crate order.
	_ = e.ok(t, "PUT", "/api/crates/"+id+"/order", map[string]any{"record_ids": []any{a["id"], b["id"]}})
	job := e.ok(t, "POST", "/api/crates/"+id+"/print", map[string]any{"template": "dj-single", "stock": "40x30", "printer_id": pid,
		"record_ids": nil, "order": "artist", "copies": 1})
	if job["source"] != "crate:Happy HC" || job["records"] != 2.0 || job["title"] != "DJ Force – Edited title" {
		t.Fatalf("crate job = %v", job)
	}
	if done := e.waitJob(t, job["id"].(float64)); done["status"] != "done" {
		t.Fatalf("crate job = %v", done)
	}
	detail := e.ok(t, "GET", "/api/crates/"+id, nil)
	for _, raw := range detail["records"].([]any) {
		if r := raw.(map[string]any); r["printed"] != true {
			t.Fatalf("printed dots = %v", r)
		}
	}
	for _, order := range []string{"title", "year", "manual", ""} {
		e.status(t, 422, "POST", "/api/crates/"+id+"/print", map[string]any{"template": "dj-single", "stock": "40x30", "printer_id": pid, "order": order})
	}
	e.status(t, 400, "POST", "/api/crates/"+id+"/print", map[string]any{"template": "dj-single", "stock": "40x30", "printer_id": pid, "record_ids": []any{}})
	if r := e.ok(t, "PATCH", "/api/crates/"+id, map[string]any{"name": "Renamed"}); r["name"] != "Renamed" || r["record_count"] != 2.0 {
		t.Fatalf("rename = %v", r)
	}
	e.status(t, 400, "PATCH", "/api/crates/"+id, map[string]any{"name": " "})
	e.status(t, 400, "PATCH", "/api/crates/"+id, map[string]any{"name": "other"})
	e.ok(t, "DELETE", "/api/crates/"+id+"/records/"+bid, nil)
	if list := e.ok(t, "GET", "/api/crates", nil)["crates"].([]any); len(list) != 4 || list[0].(map[string]any)["id"] != id {
		t.Fatalf("crates = %v", list)
	}
	if r := e.ok(t, "DELETE", "/api/crates/"+id, nil); r["deleted"] != id {
		t.Fatalf("delete = %v", r)
	}
	e.status(t, 404, "GET", "/api/crates/"+id, nil)
}

func TestPrintedOverride(t *testing.T) {
	e := newEnv(t, Discovery{})
	r := e.ok(t, "PUT", "/api/printed/42", map[string]any{"override": "printed"})
	if r["release_id"] != 42.0 || r["printed"] != true || r["override"] != "printed" || r["labels"] != 0.0 {
		t.Fatalf("override = %v", r)
	}
	if s := e.ok(t, "GET", "/api/printed?release_ids=42", nil)["releases"].(map[string]any)["42"].(map[string]any); s["printed"] != true || s["override"] != "printed" {
		t.Fatalf("state = %v", s)
	}
	if r := e.ok(t, "PUT", "/api/printed/42", map[string]any{"override": "auto"}); r["printed"] != false || r["override"] != "auto" {
		t.Fatalf("auto = %v", r)
	}
	if r := e.ok(t, "GET", "/api/printed", nil)["releases"].(map[string]any); len(r) != 0 {
		t.Fatalf("no ids = %v", r)
	}
	e.status(t, 422, "PUT", "/api/printed/42", map[string]any{})
	e.status(t, 422, "PUT", "/api/printed/42", map[string]any{"override": true})
	e.status(t, 422, "PUT", "/api/printed/42", map[string]any{"override": "yes"})
	e.status(t, 400, "PUT", "/api/printed/0", map[string]any{"override": "printed"})
	many := make([]string, 501)
	for i := range many {
		many[i] = strconv.Itoa(i + 1)
	}
	e.status(t, 422, "GET", "/api/printed?release_ids="+strings.Join(many, ","), nil)
}

func TestHostAndOriginGuards(t *testing.T) {
	e := newEnv(t, Discovery{})
	for _, host := range []string{"evil.example.com", "evil.example.com:8787", "127.0.0.1.evil.com", "192.168.1.5:8787"} {
		if r := e.do(t, "GET", "/api/health", nil, "Host", host); r.status != 400 || r.errorText(t) == "" {
			t.Errorf("Host %s = %d", host, r.status)
		}
	}
	for _, host := range []string{"localhost:8787", "127.0.0.1", "[::1]:8787", "LOCALHOST"} {
		if r := e.do(t, "GET", "/api/health", nil, "Host", host); r.status != 200 {
			t.Errorf("Host %s = %d", host, r.status)
		}
	}
	body := map[string]any{} // crate names are unique, so let the server number them
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(e.http.URL, "http://"))
	other := "9999"
	if port == other {
		other = "9998"
	}
	for origin, want := range map[string]int{
		"https://evil.example.com": 403, "http://localhost.evil.com": 403, "null": 403,
		"http://127.0.0.1:" + other: 403, "http://localhost:" + other: 403, "https://127.0.0.1:" + port: 403,
		"http://localhost:" + port: 200, "http://127.0.0.1:" + port: 200, "http://[::1]:" + port: 200,
	} {
		r := e.do(t, "POST", "/api/crates", body, "Origin", origin)
		if r.status != want {
			t.Errorf("Origin %s = %d", origin, r.status)
		}
		if want == 403 && r.errorText(t) != "Cross-site requests are not allowed." {
			t.Errorf("message = %s", r.body)
		}
	}
	for site, want := range map[string]int{"cross-site": 403, "same-site": 403, "same-origin": 200, "none": 200} {
		if r := e.do(t, "POST", "/api/crates", body, "Sec-Fetch-Site", site); r.status != want {
			t.Errorf("Sec-Fetch-Site %s = %d", site, r.status)
		}
	}
	// A cross-site form or text/plain post cannot carry JSON's content type.
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		r := e.do(t, "POST", "/api/crates", `{"name":"x"}`, "Content-Type", ct)
		if r.status != 415 || r.errorText(t) == "" {
			t.Errorf("Content-Type %q = %d", ct, r.status)
		}
	}
	if r := e.do(t, "POST", "/api/crates", body, "Content-Type", "application/json; charset=utf-8"); r.status != 200 {
		t.Errorf("JSON with charset = %d", r.status)
	}
	if r := e.do(t, "DELETE", "/api/crates/nope", nil, "Content-Type", "text/plain"); r.status != 415 {
		t.Errorf("DELETE as text/plain = %d", r.status)
	}
	if r := e.do(t, "GET", "/api/crates", nil, "Origin", "https://evil.example.com", "Sec-Fetch-Site", "cross-site"); r.status != 200 {
		t.Errorf("a cross-site GET = %d", r.status)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	e := newEnv(t, Discovery{})
	for _, c := range []struct{ method, path, host string }{
		{"GET", "/", ""}, {"GET", "/static/app.js", ""}, {"GET", "/static/style.css", ""},
		{"GET", "/api/health", ""}, {"GET", "/api/nope", ""}, {"GET", "/api/health", "evil.example.com"},
		{"GET", "/api/discogs/image?url=" + urlEscape("https://i.discogs.com/a.jpeg"), ""},
		{"POST", "/api/preview", ""},
	} {
		var r reply
		if c.host != "" {
			r = e.do(t, c.method, c.path, nil, "Host", c.host)
		} else {
			r = e.do(t, c.method, c.path, nil)
		}
		for k, v := range map[string]string{"X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'",
			"X-Content-Type-Options": "nosniff"} {
			if got := r.header.Get(k); got != v {
				t.Errorf("%s %s (%d): %s = %q", c.method, c.path, r.status, k, got)
			}
		}
	}
}

func TestJobEventsStream(t *testing.T) {
	e := newEnv(t, Discovery{})
	pid := e.addPrinter(t, "Bench", "", nil)
	resp, err := http.Get(e.http.URL + "/api/jobs/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	rd := bufio.NewReader(resp.Body)
	first, _ := rd.ReadString('\n')
	if first != "retry: 2000\n" {
		t.Fatalf("first line = %q", first)
	}
	job := e.ok(t, "POST", "/api/print", map[string]any{"items": []any{map[string]any{"data": map[string]any{"title": "x"}}},
		"template": "dj-single", "stock": "40x30", "printer_id": pid})
	seen := map[string]bool{}
	deadline := time.Now().Add(10 * time.Second)
	for !seen["done"] && time.Now().Before(deadline) {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var j map[string]any
			if err := json.Unmarshal([]byte(data), &j); err != nil {
				t.Fatalf("event data = %q", data)
			}
			if j["id"] != job["id"] {
				t.Fatalf("event for job %v", j["id"])
			}
			seen[j["status"].(string)] = true
		} else if line != "event: job\n" && line != "\n" {
			t.Fatalf("unexpected line %q", line)
		}
	}
	if !seen["queued"] || !seen["printing"] || !seen["done"] {
		t.Fatalf("statuses seen = %v", seen)
	}
}

func TestListenAndServeShutsDownWithAnOpenStream(t *testing.T) {
	e := servicetest.New(t)
	srv := New(e.Service, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- ListenAndServe(ctx, srv, 0, func(a string) error { addr <- a; return nil }) }()
	a := <-addr
	if !strings.HasPrefix(a, "127.0.0.1:") {
		t.Fatalf("listening on %s", a)
	}
	resp, err := http.Get("http://" + a + "/api/jobs/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = bufio.NewReader(resp.Body).ReadString('\n')
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("shutdown was held up by the event stream")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("shutdown was slow")
	}
}

func TestServerStartMarksLeftoverJobsInterrupted(t *testing.T) {
	e := servicetest.New(t)
	ctx := context.Background()
	stale, _ := e.Store.CreateJob(ctx, store.NewJob{PrinterID: "p", Template: "dj-single", Stock: "40x30"})
	if n, err := e.Service.Recover(ctx); err != nil || n != 1 {
		t.Fatalf("recover = %d %v", n, err)
	}
	srv := New(e.Service, Options{})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/jobs/" + num(float64(stale.ID)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&j)
	if j["status"] != "interrupted" || j["error"] != store.InterruptedMessage || j["title"] != "" {
		t.Fatalf("job = %v", j)
	}
}

func TestPyFloat(t *testing.T) {
	for f, want := range map[float64]string{203.2: "203.2", 300: "300.0", 48: "48.0", 50.8: "50.8", 0.5: "0.5"} {
		if got := headerNumber(f); got != want {
			t.Errorf("headerNumber(%v) = %q", f, got)
		}
	}
}

func TestASecondServerLeavesTheLiveServersJobsAlone(t *testing.T) {
	e := servicetest.New(t)
	ctx := context.Background()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port
	queued, _ := e.Store.CreateJob(ctx, store.NewJob{PrinterID: "p", Template: "dj-single", Stock: "40x30"})
	ran := false
	err = ListenAndServe(ctx, New(e.Service, Options{}), port, func(string) error {
		ran = true
		_, err := e.Service.Recover(ctx)
		return err
	})
	if err == nil || ran {
		t.Fatalf("second server: err %v, ready ran %v", err, ran)
	}
	if j, _ := e.Store.Job(ctx, queued.ID); j.Status != store.JobQueued {
		t.Fatalf("the live server's job became %s", j.Status)
	}
	// A failing ready hook closes the socket and is returned.
	boom := errors.New("boom")
	if err := ListenAndServe(ctx, New(e.Service, Options{}), 0, func(string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("ready error = %v", err)
	}
}

func TestSuggestIPPModel(t *testing.T) {
	cases := map[string]string{
		"document,envelope,label,postcard": "ipp-sheet", // Brother HL-L3290CDW
		"label":                            "ipp-label",
		"roll":                             "ipp-label",
		"Label,Roll":                       "ipp-label",
		"document,photo":                   "ipp-sheet",
		"labels":                           "ipp-sheet", // the keyword is singular
		"":                                 "ipp-sheet",
	}
	for kinds, want := range cases {
		var kind []string
		if kinds != "" {
			kind = strings.Split(kinds, ",")
		}
		if got := SuggestIPPModel(kind); got != want {
			t.Errorf("SuggestIPPModel(%q) = %q, want %q", kinds, got, want)
		}
	}
}

func TestBLEProbeIsRefusedWhileTheDevicePrints(t *testing.T) {
	probed := 0
	e := newEnv(t, Discovery{BLEProbe: func(context.Context, string, time.Duration) (BLEProbe, error) {
		probed++
		return BLEProbe{}, nil
	}})
	ctx := context.Background()
	// A BLE printer (real catalog model) with a job printing on it, as another process would.
	p := printer.Target{Name: "Desk M110", ModelID: "phomemo-m110", Address: "4B2F-UUID"}
	if err := e.Store.SavePrinter(ctx, &p); err != nil {
		t.Fatal(err)
	}
	j, _ := e.Store.CreateJob(ctx, store.NewJob{PrinterID: p.ID, PrinterName: p.Name, Template: "dj-single", Stock: "40x30"})
	if _, ok, err := e.Store.TransitionJob(ctx, j.ID, store.JobQueued, store.JobPrinting, "Sending"); !ok || err != nil {
		t.Fatal(err)
	}
	msg := e.status(t, 409, "POST", "/api/printers/ble/probe", map[string]any{"address": "4b2f-uuid"})
	if !strings.Contains(msg, "Desk M110 is printing job") || probed != 0 {
		t.Fatalf("message = %q, probed %d", msg, probed)
	}
	e.ok(t, "POST", "/api/printers/ble/probe", map[string]any{"address": "OTHER-UUID"})
	done := store.JobDone
	_, _ = e.Store.UpdateJob(ctx, j.ID, store.JobUpdate{Status: &done})
	e.ok(t, "POST", "/api/printers/ble/probe", map[string]any{"address": "4B2F-UUID"})
	if probed != 2 {
		t.Fatalf("probed %d", probed)
	}
}

func TestPutTokenRefusesARejectedTokenAndWarnsWhenOffline(t *testing.T) {
	e := newEnv(t, Discovery{})
	ctx := context.Background()
	_ = e.Tokens.Delete(ctx)
	e.Stub.Extra = map[string]func(*http.Request) (int, string){"/oauth/identity": func(*http.Request) (int, string) {
		return 401, `{"message": "Invalid consumer token."}`
	}}
	msg := e.status(t, 401, "PUT", "/api/settings/discogs-token", map[string]any{"token": strings.Repeat("R", 40)})
	if !strings.Contains(msg, "Discogs rejected this token; nothing was saved") {
		t.Fatalf("message = %q", msg)
	}
	if d := e.ok(t, "GET", "/api/settings", nil)["discogs"].(map[string]any); d["token_set"] != false {
		t.Fatalf("a rejected token was saved: %v", d)
	}
	e.Stub.Down = true
	r := e.ok(t, "PUT", "/api/settings/discogs-token", map[string]any{"token": strings.Repeat("O", 40)})
	if r["token_set"] != true || r["username"] != nil || !strings.HasPrefix(r["warning"].(string), "Saved, but couldn't reach Discogs") {
		t.Fatalf("offline = %v", r)
	}
	e.Stub.Down = false
	e.Stub.Extra = nil
	if r := e.ok(t, "PUT", "/api/settings/discogs-token", map[string]any{"token": strings.Repeat("G", 40)}); r["username"] != "testuser" {
		t.Fatalf("good token = %v", r)
	} else if _, has := r["warning"]; has {
		t.Fatalf("a checked token carries a warning: %v", r)
	}
}
