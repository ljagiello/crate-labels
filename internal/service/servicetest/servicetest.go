// Package servicetest holds test doubles for the service and web tests: a FILE-link
// printer driver, a catalog with a test model, fake templates and a Discogs stub that
// serves the recorded fixtures in testdata/discogs. Import it from tests only.
package servicetest

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/discogs"
	"github.com/ljagiello/crate-labels/internal/layout"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/store"
)

// Test catalog ids.
const (
	Family = "crate-labels-app-test"
	Model  = "app-test-file"
	Token  = "abcdefghijklmnopqrstuvwxyz0123456789ABCD"
)

// Driver writes "LBL <w>x<h> d=<darkness>\n" plus the 1-bit rows of each label.
// Settings: fail_after=n stops after n labels with OK=false; error_after=n stops after n
// labels with a partial Result and a *printer.Error; hold_ms=n waits up to n ms before
// each label, giving up when the context is cancelled.
type Driver struct{}

// Family implements printer.Driver.
func (Driver) Family() string { return Family }

// Links implements printer.Driver.
func (Driver) Links() []printer.LinkKind { return []printer.LinkKind{printer.LinkFile} }

// Canvas implements printer.Driver.
func (Driver) Canvas(m *printer.Model, s *printer.Stock) printer.Canvas {
	return printer.DefaultCanvas(m, s)
}

// Preview thresholds the label at 128.
func (Driver) Preview(_ *printer.Model, _ *printer.Stock, label *image.Gray, _ printer.Settings) (*image.Gray, error) {
	out := image.NewGray(label.Bounds())
	for i, v := range label.Pix {
		if v >= 128 {
			out.Pix[i] = 255
		}
	}
	return out, nil
}

// Send implements printer.Driver.
func (Driver) Send(ctx context.Context, pc *printer.PrintContext, labels []*image.Gray) (printer.Result, error) {
	failAfter := pc.Settings.Int("fail_after", -1)
	errorAfter := pc.Settings.Int("error_after", -1)
	hold := time.Duration(pc.Settings.Int("hold_ms", 0)) * time.Millisecond
	printed := 0
	for i, l := range labels {
		if failAfter >= 0 && i >= failAfter {
			return printer.Result{OK: false, Printed: printed, Message: "Out of labels"}, nil
		}
		if errorAfter >= 0 && i >= errorAfter {
			// The driver contract: a partial Result together with a user-facing error.
			return printer.Result{Printed: printed}, printer.Errorf("The printer reports a paper jam. Clear it and retry.")
		}
		if hold > 0 {
			select {
			case <-time.After(hold):
			case <-ctx.Done():
				return printer.Result{Printed: printed}, ctx.Err()
			}
		}
		var b bytes.Buffer
		fmt.Fprintf(&b, "LBL %dx%d d=%d\n", l.Rect.Dx(), l.Rect.Dy(), pc.Settings.Int("darkness", -1))
		for _, v := range l.Pix {
			if v < 128 {
				b.WriteByte(1)
			} else {
				b.WriteByte(0)
			}
		}
		if err := pc.Link.Write(ctx, b.Bytes(), "data", false); err != nil {
			return printer.Result{Printed: printed}, err
		}
		printed++
		pc.Report(float64(printed)/float64(len(labels)), fmt.Sprintf("Printed %d of %d", printed, len(labels)))
	}
	return printer.Result{OK: true, Printed: printed, Message: fmt.Sprintf("Printed %d", printed)}, nil
}

var registerOnce sync.Once

// Register installs Driver (once per test binary).
func Register() { registerOnce.Do(func() { printer.Register(Driver{}) }) }

// Catalog loads the built-in catalog plus the test model, which takes 40x30 and 50x30.
func Catalog(t testing.TB) *printer.Catalog {
	t.Helper()
	Register()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "printers"), 0o755); err != nil {
		t.Fatal(err)
	}
	toml := fmt.Sprintf(`
[[model]]
id = %q
label = "App test printer"
family = %q
link = "file"
dpi = 203.2
head_dots = 384
paper_width_mm = [20, 50]
stocks = ["40x30", "50x30"]
options = { darkness = 8 }
`, Model, Family)
	if err := os.WriteFile(filepath.Join(dir, "printers", "app-test.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := printer.LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// Template is a fake layout.Template that draws a border and the artist's length as a bar.
type Template struct {
	TID, TName, TDesc string
	Allowed           []string // stock ids; empty = every stock
}

// ID implements layout.Template.
func (t Template) ID() string { return t.TID }

// Name implements layout.Template.
func (t Template) Name() string { return t.TName }

// Description implements layout.Template.
func (t Template) Description() string { return t.TDesc }

// Supports implements layout.Template.
func (t Template) Supports(s *printer.Stock) bool {
	if len(t.Allowed) == 0 {
		return true
	}
	for _, id := range t.Allowed {
		if id == s.ID {
			return true
		}
	}
	return false
}

// Options implements layout.Template.
func (t Template) Options() []layout.OptionSpec {
	return []layout.OptionSpec{{ID: "show_qr", Label: "QR code", Type: layout.OptionBool, Default: true}}
}

// Render implements layout.Template.
func (t Template) Render(d *layout.LabelData, c printer.Canvas, _ *printer.Stock, _ map[string]any) (*image.Gray, error) {
	img := image.NewGray(image.Rect(0, 0, c.Width, c.Height))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for x := 0; x < c.Width; x++ {
		img.SetGray(x, 0, color.Gray{0})
		img.SetGray(x, c.Height-1, color.Gray{0})
	}
	for x := 0; x < min(c.Width, len(d.Artist)+len(d.Title)); x++ {
		img.SetGray(x, c.Height/2, color.Gray{0})
	}
	return img, nil
}

// Layouts serves the fake templates and records the options of each render.
type Layouts struct {
	mu   sync.Mutex
	List []layout.Template
	Opts []map[string]any
	Data []layout.LabelData
}

// NewLayouts returns dj-single, test-pattern and big-only (70x80 only).
func NewLayouts() *Layouts {
	return &Layouts{List: []layout.Template{
		Template{TID: "dj-single", TName: "DJ single", TDesc: "One track"},
		Template{TID: "test-pattern", TName: "Test pattern", TDesc: "Calibration"},
		Template{TID: "big-only", TName: "Big only", Allowed: []string{"70x80"}},
	}}
}

// Templates implements service.Layouts.
func (l *Layouts) Templates() []layout.Template { return append([]layout.Template(nil), l.List...) }

// Get implements service.Layouts.
func (l *Layouts) Get(id string) (layout.Template, error) {
	for _, t := range l.List {
		if t.ID() == id {
			return t, nil
		}
	}
	return nil, printer.Errorf("unknown template %q", id)
}

// Render implements service.Layouts.
func (l *Layouts) Render(id string, d *layout.LabelData, c printer.Canvas, s *printer.Stock, opts map[string]any) (*image.Gray, error) {
	t, err := l.Get(id)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.Opts = append(l.Opts, opts)
	l.Data = append(l.Data, *d)
	l.mu.Unlock()
	return t.Render(d, c, s, opts)
}

// Rendered returns the label data of every render so far.
func (l *Layouts) Rendered() []layout.LabelData {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]layout.LabelData(nil), l.Data...)
}

// FixturesDir is testdata/discogs at the repository root.
func FixturesDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "discogs")
}

// SearchResult is a synthetic /database/search reply.
const SearchResult = `{"pagination": {"page": 1, "pages": 1, "per_page": 30, "items": 1},
 "results": [{"id": 118638, "type": "release", "master_id": 4589,
   "title": "DJ Force & The Evolution - Simply Electric / Out Of Control", "year": "1995", "country": "UK",
   "format": ["Vinyl", "12\"", "45 RPM"], "label": ["Kniteforce Records"], "catno": "KF039",
   "thumb": "https://i.discogs.com/thumb.jpeg", "barcode": []}]}`

// CollectionPage is a synthetic collection page.
const CollectionPage = `{"pagination": {"page": 1, "pages": 1, "per_page": 50, "items": 1},
 "releases": [{"id": 10550772, "instance_id": 99001, "folder_id": 1, "date_added": "2026-11-01T10:00:00-08:00",
   "rating": 5, "notes": [{"field_id": 3, "value": " plays great "}, {"field_id": 4, "value": ""}],
   "basic_information": {"id": 10550772, "master_id": 1228832, "title": "Death To Digital EP (Volume One)", "year": 2017,
     "thumb": "https://i.discogs.com/t.jpeg", "cover_image": "https://i.discogs.com/c.jpeg",
     "formats": [{"name": "Vinyl", "qty": "1", "descriptions": ["12\"", "EP"]}],
     "labels": [{"name": "Kniteforce Records", "catno": "KF70"}],
     "artists": [{"name": "Sanxion", "join": "/"}, {"name": "Mannik", "join": ""}],
     "genres": ["Electronic"], "styles": ["Breakbeat", "Hardcore"]}}]}`

// Stub is a RoundTripper serving recorded releases and masters plus a few synthetic
// endpoints. Identity needs a token; everything else answers without one.
type Stub struct {
	mu       sync.Mutex
	Requests []*http.Request
	Username string
	// Extra handlers by path prefix, consulted first.
	Extra map[string]func(*http.Request) (int, string)
	// Down makes api.discogs.com unreachable (a network error).
	Down bool
}

func reply(status int, body, contentType string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}},
		Body: io.NopCloser(strings.NewReader(body))}
}

// TinyJPEG is an 8×8 red JPEG.
func TinyJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+3] = 200, 255
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

// RoundTrip implements http.RoundTripper.
func (s *Stub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.Requests = append(s.Requests, r)
	user := s.Username
	down := s.Down
	s.mu.Unlock()
	if user == "" {
		user = "testuser"
	}
	if down && r.URL.Host == discogs.APIHost {
		return nil, fmt.Errorf("dial tcp: lookup api.discogs.com: no such host")
	}
	path := r.URL.Path
	for prefix, h := range s.Extra {
		if strings.HasPrefix(path, prefix) {
			status, body := h(r)
			return reply(status, body, "application/json"), nil
		}
	}
	if r.URL.Host == "i.discogs.com" {
		return reply(200, string(TinyJPEG()), "image/jpeg"), nil
	}
	if r.URL.Host != discogs.APIHost {
		return nil, fmt.Errorf("stub: unexpected host %s", r.URL.Host)
	}
	switch {
	case path == "/oauth/identity":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Discogs token=") {
			return reply(401, `{"message": "You must authenticate."}`, "application/json"), nil
		}
		return reply(200, fmt.Sprintf(`{"id": 1, "username": %q}`, user), "application/json"), nil
	case path == "/database/search":
		if r.Header.Get("Authorization") == "" {
			return reply(401, `{"message": "You must authenticate."}`, "application/json"), nil
		}
		return reply(200, SearchResult, "application/json"), nil
	case strings.HasSuffix(path, "/collection/folders"):
		return reply(200, `{"folders": [{"id": 0, "name": "All", "count": 2}, {"id": 1, "name": "Uncategorized", "count": 2}]}`,
			"application/json"), nil
	case strings.Contains(path, "/collection/folders/") && strings.HasSuffix(path, "/releases"):
		return reply(200, CollectionPage, "application/json"), nil
	case strings.HasSuffix(path, "/wants"):
		return reply(200, strings.Replace(CollectionPage, `"releases"`, `"wants"`, 1), "application/json"), nil
	}
	for _, kind := range []string{"releases", "masters"} {
		prefix := "/" + kind + "/"
		if id, ok := strings.CutPrefix(path, prefix); ok {
			b, err := os.ReadFile(filepath.Join(FixturesDir(), strings.TrimSuffix(kind, "s")+"-"+id+".json"))
			if err != nil {
				return reply(404, `{"message": "Release not found."}`, "application/json"), nil
			}
			return reply(200, string(b), "application/json"), nil
		}
	}
	return reply(404, `{"message": "not stubbed"}`, "application/json"), nil
}

// Count returns how many requests went to a path.
func (s *Stub) Count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if r.URL.Path == path {
			n++
		}
	}
	return n
}

// All returns every request so far.
func (s *Stub) All() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.Requests...)
}

// Env is a service wired to the doubles.
type Env struct {
	Service *service.Service
	Store   *store.Store
	Stub    *Stub
	Tokens  *discogs.MemoryTokens
	Layouts *Layouts
	Dir     string
}

// New builds a Service on a temp data dir, with no env token, the stub, an in-memory
// token store holding Token, and no pacing between Discogs requests.
func New(t testing.TB) *Env {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CRATE_LABELS_DATA", filepath.Join(dir, "data"))
	t.Setenv("CRATE_LABELS_CONFIG", filepath.Join(dir, "config"))
	t.Setenv(discogs.EnvVar, "")
	st, err := store.Open(filepath.Join(dir, "svc.db"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := &discogs.MemoryTokens{}
	if err := tokens.Set(context.Background(), Token); err != nil {
		t.Fatal(err)
	}
	stub := &Stub{}
	// The client reads the token through the service, as in production, so the
	// service's Keychain cache is in the path.
	var svc *service.Service
	client := discogs.NewClient(func(ctx context.Context) string { return svc.Tokens.Token(ctx) }, stub)
	client.Cache, client.Images = st, st
	client.Limiter = nil // no pacing: the stub answers at once
	lay := NewLayouts()
	svc = service.New(service.Config{Store: st, Catalog: Catalog(t), TokenStore: tokens, Discogs: client, Layouts: lay})
	t.Cleanup(func() {
		svc.Close()
		st.Close()
	})
	return &Env{Service: svc, Store: st, Stub: stub, Tokens: tokens, Layouts: lay, Dir: dir}
}

// AddPrinter saves a printer of the test model.
func (e *Env) AddPrinter(t testing.TB, name, address string, settings printer.Settings) printer.Target {
	t.Helper()
	p := printer.Target{Name: name, ModelID: Model, Address: address, Settings: settings}
	if err := e.Store.SavePrinter(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
