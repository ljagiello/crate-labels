package service

import (
	"context"
	"encoding/json"
	"image"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/ljagiello/crate-labels/internal/audio"
	"github.com/ljagiello/crate-labels/internal/discogs"
	"github.com/ljagiello/crate-labels/internal/layout"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/store"
	"github.com/ljagiello/crate-labels/internal/tempo"
)

// Version is the crate-labels version reported by the API and `crate-labels version`.
// Release builds set it with
// -ldflags "-X github.com/ljagiello/crate-labels/internal/service.Version=…" (see
// .goreleaser.yaml); a plain `go build` keeps this default.
var Version = "0.1.0"

// Layouts is the label layout engine. Production uses internal/layout; tests swap in
// doubles.
type Layouts interface {
	Templates() []layout.Template
	Get(id string) (layout.Template, error)
	// Render coerces and defaults opts, then draws d on exactly c's pixels.
	Render(id string, d *layout.LabelData, c printer.Canvas, s *printer.Stock, opts map[string]any) (*image.Gray, error)
}

// BuiltinLayouts are the templates in internal/layout.
type BuiltinLayouts struct{}

// Templates lists the built-in templates.
func (BuiltinLayouts) Templates() []layout.Template { return layout.Templates() }

// Get looks a built-in template up.
func (BuiltinLayouts) Get(id string) (layout.Template, error) { return layout.Get(id) }

// Render draws a label with a built-in template.
func (BuiltinLayouts) Render(id string, d *layout.LabelData, c printer.Canvas, s *printer.Stock, opts map[string]any) (*image.Gray, error) {
	return layout.Render(id, d, c, s, opts)
}

// Config builds a Service. Store and Catalog are required.
type Config struct {
	Store      *store.Store
	Catalog    *printer.Catalog
	TokenStore discogs.TokenStore // nil = the macOS Keychain
	Discogs    *discogs.Client    // nil = a client using TokenStore, the env and Store's caches
	Layouts    Layouts            // nil = BuiltinLayouts
	Log        *slog.Logger
}

// Service is the application layer shared by the web server and the CLI.
type Service struct {
	Store      *store.Store
	Catalog    *printer.Catalog
	TokenStore discogs.TokenStore
	Tokens     *discogs.TokenProvider
	Discogs    *discogs.Client
	Layouts    Layouts
	Log        *slog.Logger

	// Owner identifies this process on the jobs it queues ("<pid>:<start>"), and
	// OwnerAlive says whether another owner still runs. Tests may replace them.
	Owner      string
	OwnerAlive func(owner string) bool

	// AnalyzeTempo estimates the tempo of mono samples: tempo.Analyze, unless a test
	// replaces it. DetectTempo runs at most maxTempoAnalyses at once.
	AnalyzeTempo func(ctx context.Context, samples []float32, sampleRate int, opt tempo.Options) (tempo.Result, error)
	// DecodeVideo fetches and decodes a YouTube video's audio: audio.DecodeYouTube, unless a
	// test replaces it.
	DecodeVideo func(ctx context.Context, id string, rate int, maxSeconds float64) ([]float32, error)
	tempoSlots  chan struct{}

	ctx    context.Context // cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	queues     map[string]*printQueue
	running    map[int64]context.CancelFunc
	userCancel map[int64]bool
	done       map[int64]chan struct{}
	subs       map[int]chan store.Job
	nextSub    int
	closed     bool
}

// New builds a Service from cfg.
func New(cfg Config) *Service {
	s := &Service{Store: cfg.Store, Catalog: cfg.Catalog, TokenStore: cfg.TokenStore, Discogs: cfg.Discogs,
		Layouts: cfg.Layouts, Log: cfg.Log}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.TokenStore == nil {
		s.TokenStore = discogs.NewKeychain()
	}
	// One Keychain read per process, not one `security` run per Discogs request; the
	// item keeps its default access control.
	if _, cached := s.TokenStore.(*discogs.CachedTokens); !cached {
		s.TokenStore = discogs.NewCachedTokens(s.TokenStore)
	}
	s.Tokens = &discogs.TokenProvider{Store: s.TokenStore, UseEnv: true}
	if s.Discogs == nil {
		s.Discogs = discogs.NewClient(s.Tokens.Token, nil)
		s.Discogs.Cache, s.Discogs.Images, s.Discogs.Log = s.Store, s.Store, s.Log
	}
	if s.Discogs.OnUnauthorized == nil {
		// On a 401, re-read the Keychain: another process may have set or cleared the token.
		s.Discogs.OnUnauthorized = func(context.Context) {
			if c, ok := s.TokenStore.(*discogs.CachedTokens); ok {
				c.Invalidate()
			}
		}
	}
	if s.Layouts == nil {
		s.Layouts = BuiltinLayouts{}
	}
	s.Owner, s.OwnerAlive = processOwner(), ownerAlive
	s.AnalyzeTempo, s.tempoSlots = tempo.Analyze, make(chan struct{}, maxTempoAnalyses)
	s.DecodeVideo = audio.DecodeYouTube
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.queues = map[string]*printQueue{}
	s.running = map[int64]context.CancelFunc{}
	s.userCancel = map[int64]bool{}
	s.done = map[int64]chan struct{}{}
	s.subs = map[int]chan store.Job{}
	return s
}

// Recover marks queued or printing jobs whose owning process has died as interrupted.
// The server calls it once it owns its port. Jobs of a live process (a CLI print, or
// another server) are left alone.
func (s *Service) Recover(ctx context.Context) (int, error) {
	ids, err := s.Store.InterruptStaleJobs(ctx, func(owner string) bool {
		return owner != s.Owner && !s.OwnerAlive(owner)
	})
	if len(ids) > 0 {
		s.Log.Info("marked unfinished print jobs as interrupted", "count", len(ids))
	}
	return len(ids), err
}

// Close stops the print workers and waits for them. The job a worker was printing, and
// every job this process still had queued, become interrupted.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, ch := range s.subs {
		close(ch)
	}
	s.subs = map[int]chan store.Job{}
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	bg := context.Background()
	if _, err := s.Store.InterruptStaleJobs(bg, func(owner string) bool { return owner == s.Owner }); err != nil {
		s.Log.Error("could not mark unfinished jobs as interrupted", "err", err)
	}
	s.mu.Lock()
	for id, ch := range s.done {
		close(ch)
		delete(s.done, id)
	}
	s.mu.Unlock()
}

// --- Discogs token ----------------------------------------------------------------------

// TokenStatus is the API's `discogs` status object. The token itself never appears.
type TokenStatus struct {
	TokenSet bool    `json:"token_set"`
	Source   string  `json:"source"`
	Username *string `json:"username"`
	// Warning is set when a token was saved without Discogs confirming it.
	Warning string `json:"warning,omitempty"`
}

// TokenStatus reports whether a token is set, where it comes from and its account.
func (s *Service) TokenStatus(ctx context.Context) TokenStatus {
	token, source := s.Tokens.Resolve(ctx)
	st := TokenStatus{TokenSet: token != "", Source: source}
	if user, _, ok, err := s.Store.DiscogsAccount(ctx); err == nil && ok && user != "" {
		st.Username = &user
	}
	return st
}

// SetToken checks token with Discogs first and stores it in the Keychain only if
// Discogs accepts it, or could not be reached to say (then the status carries a
// Warning). A token Discogs rejects is not stored: 401 "nothing was saved".
func (s *Service) SetToken(ctx context.Context, token string) (TokenStatus, error) {
	token, err := discogs.ValidateToken(token)
	if err != nil {
		return TokenStatus{}, err
	}
	id, checkErr := s.Discogs.CheckToken(ctx, token)
	switch {
	case checkErr == nil:
	case discogs.IsRejected(checkErr):
		return TokenStatus{}, &Error{Status: 401, Msg: "Discogs rejected this token; nothing was saved. " +
			"Copy it again from discogs.com → Settings → Developers."}
	case ctx.Err() != nil:
		return TokenStatus{}, ctx.Err()
	}
	if err := s.TokenStore.Set(ctx, token); err != nil {
		return TokenStatus{}, err
	}
	s.Discogs.ForgetIdentity()
	if checkErr != nil {
		// Stored unchecked: the previous account's name no longer applies.
		if err := s.Store.ClearDiscogsAccount(ctx); err != nil {
			return TokenStatus{}, err
		}
		st := s.TokenStatus(ctx)
		s.Log.Info("saved a Discogs token without checking it", "err", checkErr)
		st.Warning = "Saved, but couldn't reach Discogs to check it. Use Test once Discogs answers."
		return st, nil
	}
	if id.Username != "" {
		if err := s.Store.SetDiscogsAccount(ctx, id.Username, discogs.KeychainRef); err != nil {
			return TokenStatus{}, err
		}
	}
	return s.TokenStatus(ctx), nil
}

// TestToken asks Discogs whom the token belongs to and records the username.
func (s *Service) TestToken(ctx context.Context) (TokenStatus, error) {
	if !s.Discogs.HasToken(ctx) {
		return TokenStatus{}, Errorf("No Discogs token is set.")
	}
	s.Discogs.ForgetIdentity()
	id, err := s.Discogs.Identity(ctx)
	if err != nil {
		return TokenStatus{}, err
	}
	if id.Username != "" {
		if err := s.Store.SetDiscogsAccount(ctx, id.Username, discogs.KeychainRef); err != nil {
			return TokenStatus{}, err
		}
	}
	return s.TokenStatus(ctx), nil
}

// ClearToken deletes the Keychain item and the stored username.
func (s *Service) ClearToken(ctx context.Context) (TokenStatus, error) {
	if err := s.TokenStore.Delete(ctx); err != nil {
		return TokenStatus{}, err
	}
	if err := s.Store.ClearDiscogsAccount(ctx); err != nil {
		return TokenStatus{}, err
	}
	s.Discogs.ForgetIdentity()
	return s.TokenStatus(ctx), nil
}

// --- Discogs lookups -----------------------------------------------------------------------

// Lookup is what the paste box shows: the reference, the release and its label data.
type Lookup struct {
	Ref     discogs.Ref            `json:"ref"`
	Release discogs.ReleaseSummary `json:"release"`
	Data    Label                  `json:"data"`
}

// ResolveRef turns a master into its main release.
func (s *Service) ResolveRef(ctx context.Context, ref discogs.Ref) (int64, error) {
	if ref.Kind == discogs.KindRelease {
		return ref.ID, nil
	}
	m, err := s.Discogs.Master(ctx, ref.ID, false)
	if err != nil {
		return 0, err
	}
	if m.MainRelease <= 0 {
		return 0, Errorf("Master %d has no main release; open a specific pressing.", ref.ID)
	}
	return int64(m.MainRelease), nil
}

func (s *Service) mapOptions(ctx context.Context) discogs.MapOptions {
	return discogs.MapOptions{PreferANV: s.Store.SettingBool(ctx, "discogs.prefer_anv")}
}

// LookupText parses pasted text (an id, a release or master URL, "[r123]") and loads it.
func (s *Service) LookupText(ctx context.Context, text string, refresh bool) (Lookup, error) {
	ref, ok := discogs.ParseRef(text)
	if !ok {
		return Lookup{}, Errorf("Paste a Discogs release or master link, or a release id.")
	}
	return s.lookup(ctx, ref, refresh)
}

// LookupRelease loads one release by id.
func (s *Service) LookupRelease(ctx context.Context, id int64, refresh bool) (Lookup, error) {
	if id <= 0 {
		return Lookup{}, Errorf("Paste a Discogs release or master link, or a release id.")
	}
	return s.lookup(ctx, discogs.Ref{Kind: discogs.KindRelease, ID: id}, refresh)
}

func (s *Service) lookup(ctx context.Context, ref discogs.Ref, refresh bool) (Lookup, error) {
	id, err := s.ResolveRef(ctx, ref)
	if err != nil {
		return Lookup{}, err
	}
	r, err := s.Discogs.Release(ctx, id, refresh)
	if err != nil {
		return Lookup{}, err
	}
	sum := discogs.Summarize(r)
	sum.Printed = s.printed(ctx, sum.ID)
	return Lookup{Ref: ref, Release: sum, Data: FromLayout(discogs.Map(r, s.mapOptions(ctx)))}, nil
}

// ReleaseLabel fetches a release and maps it to label data.
func (s *Service) ReleaseLabel(ctx context.Context, id int64) (Label, *discogs.Release, error) {
	r, err := s.Discogs.Release(ctx, id, false)
	if err != nil {
		return Label{}, nil, err
	}
	return FromLayout(discogs.Map(r, s.mapOptions(ctx))), r, nil
}

// printed reports whether a release has a label. A store error reads as "no", so a
// lookup never fails over the printed dot.
func (s *Service) printed(ctx context.Context, releaseID int64) bool {
	return s.printedMany(ctx, []int64{releaseID})[releaseID]
}

// printedMany says which of ids have a label.
func (s *Service) printedMany(ctx context.Context, ids []int64) map[int64]bool {
	out := map[int64]bool{}
	states, err := s.Store.PrintedStates(ctx, ids)
	if err != nil {
		s.Log.Warn("could not read which records have labels", "err", err)
		return out
	}
	for id, st := range states {
		out[id] = st.Printed
	}
	return out
}

// SearchResults is a page of search hits.
type SearchResults struct {
	Pagination json.RawMessage     `json:"pagination"`
	Results    []discogs.SearchHit `json:"results"`
}

// Search runs a Discogs database search and marks hits that already have a label.
func (s *Service) Search(ctx context.Context, p discogs.SearchParams) (SearchResults, error) {
	fields := []string{p.Query, p.Barcode, p.Catno, p.Label, p.Artist, p.Title}
	if !slices.ContainsFunc(fields, func(v string) bool { return strings.TrimSpace(v) != "" }) {
		return SearchResults{}, Errorf("Type something to search for.")
	}
	if q := strings.TrimSpace(p.Query); q != "" && len([]rune(q)) < 2 &&
		strings.TrimSpace(p.Barcode+p.Catno+p.Label+p.Artist+p.Title) == "" {
		return SearchResults{}, Errorf("Type at least 2 characters.")
	}
	page, err := s.Discogs.Search(ctx, p)
	if err != nil {
		return SearchResults{}, err
	}
	out := SearchResults{Pagination: pagination(page.Pagination), Results: []discogs.SearchHit{}}
	var ids []int64
	for _, r := range page.Results {
		h := discogs.Hit(r)
		out.Results = append(out.Results, h)
		ids = append(ids, h.ID)
	}
	printed := s.printedMany(ctx, ids)
	for i := range out.Results {
		out.Results[i].Printed = printed[out.Results[i].ID]
	}
	return out, nil
}

func pagination(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

// FolderView is a collection folder as the API returns it.
type FolderView struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// Folders lists the account's collection folders.
func (s *Service) Folders(ctx context.Context) ([]FolderView, error) {
	folders, err := s.Discogs.CollectionFolders(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []FolderView{}
	for _, f := range folders {
		out = append(out, FolderView{ID: int64(f.ID), Name: f.Name, Count: int64(f.Count)})
	}
	return out, nil
}

// CollectionResults is a page of collection or wantlist rows.
type CollectionResults struct {
	Pagination json.RawMessage          `json:"pagination"`
	Items      []discogs.CollectionItem `json:"items"`
}

func (s *Service) collectionResults(ctx context.Context, page *discogs.CollectionPage, rows []discogs.CollectionRow) CollectionResults {
	out := CollectionResults{Pagination: pagination(page.Pagination), Items: []discogs.CollectionItem{}}
	var ids []int64
	for _, r := range rows {
		it := discogs.Item(r)
		out.Items = append(out.Items, it)
		ids = append(ids, it.ID)
	}
	printed := s.printedMany(ctx, ids)
	for i := range out.Items {
		out.Items[i].Printed = printed[out.Items[i].ID]
	}
	return out
}

// Collection is one page of a collection folder.
func (s *Service) Collection(ctx context.Context, p discogs.CollectionParams) (CollectionResults, error) {
	page, err := s.Discogs.Collection(ctx, p)
	if err != nil {
		return CollectionResults{}, err
	}
	return s.collectionResults(ctx, page, page.Releases), nil
}

// Wantlist is one page of the wantlist.
func (s *Service) Wantlist(ctx context.Context, page, perPage int, refresh bool) (CollectionResults, error) {
	p, err := s.Discogs.Wantlist(ctx, page, perPage, refresh)
	if err != nil {
		return CollectionResults{}, err
	}
	return s.collectionResults(ctx, p, p.Wants), nil
}

// PrintedView is whether a release has a label, as the API returns it.
type PrintedView struct {
	Printed       bool   `json:"printed"`
	Override      string `json:"override"`        // "auto", "printed" or "not_printed"
	Labels        int    `json:"labels"`          // labels printed for it
	LastPrintedAt *int64 `json:"last_printed_at"` // null when it was never printed
}

func printedView(st store.PrintedState) PrintedView {
	return PrintedView{Printed: st.Printed, Override: st.Override, Labels: st.Labels, LastPrintedAt: st.LastPrintedAt}
}

// Printed returns the state of every release in ids that is a valid id.
func (s *Service) Printed(ctx context.Context, ids []int64) (map[int64]PrintedView, error) {
	states, err := s.Store.PrintedStates(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]PrintedView, len(states))
	for id, st := range states {
		out[id] = printedView(st)
	}
	return out, nil
}

// SetPrinted sets a release's override: store.OverridePrinted, OverrideNotPrinted, or
// OverrideAuto to let the prints decide again.
func (s *Service) SetPrinted(ctx context.Context, releaseID int64, override string) (PrintedView, error) {
	st, err := s.Store.SetPrintedOverride(ctx, releaseID, override)
	if err != nil {
		return PrintedView{}, err
	}
	return printedView(st), nil
}
