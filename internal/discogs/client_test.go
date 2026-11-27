package discogs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "abcdefghijklmnopqrstuvwxyz0123456789ABCD"

// fakeClock is a clock whose sleep advances time at once and records each wait.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// roundTrip turns a function into an http.RoundTripper.
type roundTrip func(*http.Request) *http.Response

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func respond(status int, body string, header ...string) *http.Response {
	h := http.Header{"Content-Type": {"application/json"}}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// recorder keeps every request a client sends.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
	at   []time.Time
}

func (r *recorder) add(req *http.Request, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	r.at = append(r.at, at)
}

func (r *recorder) all() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.reqs...)
}

func newTestClient(clock *fakeClock, token string, rec *recorder, handler func(*http.Request) *http.Response) *Client {
	c := NewClient(func(context.Context) string { return token }, roundTrip(func(r *http.Request) *http.Response {
		rec.add(r, clock.Now())
		return handler(r)
	}))
	c.Limiter = testLimiter(clock)
	return c
}

// testLimiter is the default limiter on the fake clock, without jitter.
func testLimiter(clock *fakeClock) *Limiter {
	return &Limiter{Burst: DefaultBurst, Now: clock.Now, Sleep: clock.Sleep, Jitter: func(time.Duration) time.Duration { return 0 }}
}

// interval is the steady spacing of a bucket with the default burst.
func interval(limit int) time.Duration { return RateWindow / time.Duration(limit-DefaultBurst) }

func okJSON(r *http.Request) *http.Response {
	return respond(200, `{"id": 1, "path": "`+r.URL.Path+`"}`)
}

type memCache struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (m *memCache) CacheGet(_ context.Context, url string, _ time.Duration) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[url]
	return b, ok, nil
}

func (m *memCache) CachePut(_ context.Context, url string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string][]byte{}
	}
	m.rows[url] = body
	return nil
}

var ctx = context.Background()

func TestRequestsBurstThenArePacedAndCarryTokenAndUA(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	c := newTestClient(clock, testToken, rec, okJSON)
	for id := range int64(6) {
		if _, err := c.Release(ctx, id+1, false); err != nil {
			t.Fatal(err)
		}
	}
	// A full bucket lets three go at once; after that one start per refill interval.
	every := interval(AuthenticatedPerMinute)
	if s := clock.Sleeps(); len(s) != 3 || s[0] != every || s[1] != every || s[2] != every {
		t.Fatalf("sleeps = %v, want three of %v", s, every)
	}
	for _, r := range rec.all() {
		if r.URL.Host != "api.discogs.com" || r.URL.Scheme != "https" {
			t.Errorf("host = %s", r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Discogs token="+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != DefaultUserAgent {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("Accept"); got != MediaType {
			t.Errorf("Accept = %q", got)
		}
	}
}

// Discogs rejects unauthenticated calls from 1 December 2026, so without a token nothing
// is sent at all: not even a cached answer is used.
func TestNoTokenNeverCallsDiscogs(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	c := newTestClient(clock, "", rec, okJSON)
	c.Cache = &memCache{rows: map[string][]byte{APIBase + "/releases/5": []byte(`{"id": 5}`)}}
	for _, call := range []func() error{
		func() error { _, err := c.Release(ctx, 5, false); return err },
		func() error { _, err := c.Master(ctx, 6, false); return err },
		func() error { _, err := c.Search(ctx, SearchParams{Query: "kf039"}); return err },
		func() error { _, err := c.Identity(ctx); return err },
	} {
		err := call()
		var de *Error
		if !errors.As(err, &de) || de.Status != http.StatusUnauthorized || de.Upstream != 0 ||
			!strings.Contains(de.Msg, "needs your personal access token") {
			t.Fatalf("err = %#v", err)
		}
		if IsRejected(err) {
			t.Fatal("a missing token reported as rejected by Discogs")
		}
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("%d requests sent without a token", n)
	}
	if s := clock.Sleeps(); len(s) != 0 {
		t.Fatalf("sleeps = %v", s)
	}
}

func Test429WithoutRetryAfterBacksOffAndDoubles(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	calls := 0
	c := newTestClient(clock, testToken, rec, func(r *http.Request) *http.Response {
		calls++
		if calls <= 2 {
			return respond(429, `{"message": "slow down"}`)
		}
		return okJSON(r)
	})
	r, err := c.Release(ctx, 7, false)
	if err != nil || r.ID != 1 {
		t.Fatalf("release = %+v, %v", r, err)
	}
	if g1, g2 := rec.at[1].Sub(rec.at[0]), rec.at[2].Sub(rec.at[1]); g1 != 5*time.Second || g2 != 10*time.Second {
		t.Fatalf("retries after %v and %v, want 5 s then 10 s", g1, g2)
	}
	// A success ends the streak: the next 429 starts again at 5 s.
	c.Limiter.Observe(http.Header{}, 429, true)
	if h := c.Limiter.HeldFor(); h != 5*time.Second {
		t.Errorf("hold after a success = %v", h)
	}
}

func Test429HonoursRetryAfterAndGivesUp(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	c := newTestClient(clock, testToken, rec, func(*http.Request) *http.Response {
		return respond(429, ``, "Retry-After", "5")
	})
	_, err := c.Release(ctx, 7, false)
	var de *Error
	if !errors.As(err, &de) || de.Status != 429 {
		t.Fatalf("err = %v", err)
	}
	if len(rec.at) != 3 {
		t.Fatalf("requests = %d, want 3 (1 + 2 retries)", len(rec.at))
	}
	for i := 1; i < 3; i++ {
		if gap := rec.at[i].Sub(rec.at[i-1]); gap != 5*time.Second {
			t.Errorf("gap %d = %v", i, gap)
		}
	}
	if b := c.Limiter.HeldFor(); b != 5*time.Second {
		t.Errorf("still held for %v after giving up, want 5 s", b)
	}
}

func TestA429HoldBeyondPatienceFailsFast(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	c := newTestClient(clock, testToken, rec, func(*http.Request) *http.Response {
		return respond(429, ``, "Retry-After", "45")
	})
	_, err := c.Release(ctx, 7, false)
	var de *Error
	if !errors.As(err, &de) || de.Status != 429 || !strings.Contains(de.Msg, "try again in 45 s") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.all()) != 1 {
		t.Fatalf("requests = %d: a 45 s hold must not be waited out", len(rec.all()))
	}
	// Later calls fail fast too, until the hold is short enough to wait.
	if _, err := c.Release(ctx, 8, false); !errors.As(err, &de) || de.Status != 429 || len(rec.all()) != 1 {
		t.Fatalf("second call: %v, %d requests", err, len(rec.all()))
	}
	_ = clock.Sleep(ctx, 30*time.Second)
	_, _ = c.Release(ctx, 9, false) // 15 s left: waited out, then 429 again
	if n := len(rec.all()); n != 2 {
		t.Fatalf("requests after the hold shrank = %d", n)
	}
}

func TestRateLimitRemainingIsRemembered(t *testing.T) {
	c := newTestClient(newFakeClock(), testToken, &recorder{}, func(*http.Request) *http.Response {
		return respond(200, `{}`, "X-Discogs-Ratelimit-Remaining", "42")
	})
	if got := c.RateLimitRemaining(); got != -1 {
		t.Errorf("before = %d", got)
	}
	_, _ = c.Release(ctx, 1, false)
	if got := c.RateLimitRemaining(); got != 42 {
		t.Errorf("after = %d", got)
	}
}

func TestCacheHitsSkipNetworkAndRefreshBypasses(t *testing.T) {
	clock := newFakeClock()
	n := 0
	c := newTestClient(clock, testToken, &recorder{}, func(*http.Request) *http.Response {
		n++
		return respond(200, `{"id": `+string(rune('0'+n))+`}`)
	})
	cache := &memCache{}
	c.Cache = cache
	if r, _ := c.Release(ctx, 9, false); r.ID != 1 {
		t.Fatalf("first = %d", r.ID)
	}
	if r, _ := c.Release(ctx, 9, false); r.ID != 1 {
		t.Fatalf("cached = %d", r.ID)
	}
	if len(clock.Sleeps()) != 0 {
		t.Fatal("a cache hit waited for the limiter")
	}
	if r, _ := c.Release(ctx, 9, true); r.ID != 2 {
		t.Fatalf("refresh = %d", r.ID)
	}
	if len(cache.rows) != 1 || cache.rows["https://api.discogs.com/releases/9"] == nil {
		t.Fatalf("cache keys = %v", cache.rows)
	}
}

func TestCacheKeyHasSortedQueryAndNoToken(t *testing.T) {
	cache := &memCache{}
	c := newTestClient(newFakeClock(), testToken, &recorder{}, func(*http.Request) *http.Response {
		return respond(200, `{"results": []}`)
	})
	c.Cache = cache
	if _, err := c.Search(ctx, SearchParams{Query: "kf039", Type: "release", Format: "Vinyl"}); err != nil {
		t.Fatal(err)
	}
	for k := range cache.rows {
		if k != "https://api.discogs.com/database/search?format=Vinyl&q=kf039&type=release" {
			t.Errorf("cache key = %q", k)
		}
		if strings.Contains(k, testToken) {
			t.Error("cache key holds the token")
		}
	}
}

func TestIdentityIsNotCachedAndErrorsExplain(t *testing.T) {
	cache := &memCache{}
	unauth := func(*http.Request) *http.Response { return respond(401, `{"message": "You must authenticate."}`) }
	c := newTestClient(newFakeClock(), testToken, &recorder{}, unauth)
	c.Cache = cache
	_, err := c.Identity(ctx)
	var de *Error
	if !errors.As(err, &de) || de.Status != 401 || !strings.Contains(de.Msg, "Check it in Settings") {
		t.Fatalf("with token: %v", err)
	}
	c = newTestClient(newFakeClock(), "", &recorder{}, unauth)
	c.Cache = cache
	_, err = c.Search(ctx, SearchParams{Query: "kniteforce"})
	if !errors.As(err, &de) || de.Status != 401 || !strings.Contains(de.Msg, "Add it in Settings") {
		t.Fatalf("without token: %v", err)
	}
	if len(cache.rows) != 0 {
		t.Fatal("an error was cached")
	}
	for status, want := range map[int]int{403: 401, 404: 404, 500: 502, 418: 502} {
		c = newTestClient(newFakeClock(), testToken, &recorder{}, func(*http.Request) *http.Response {
			return respond(status, `{"message": "nope"}`)
		})
		_, err := c.Release(ctx, 1, false)
		if !errors.As(err, &de) || de.Status != want {
			t.Errorf("upstream %d gave %v", status, err)
		}
	}
	c = newTestClient(newFakeClock(), testToken, &recorder{}, func(*http.Request) *http.Response {
		return respond(200, `<html>`)
	})
	if _, err := c.Release(ctx, 1, false); !errors.As(err, &de) || de.Status != 502 {
		t.Errorf("not JSON: %v", err)
	}
}

func TestTransportErrorIs502(t *testing.T) {
	c := NewClient(func(context.Context) string { return testToken }, errTransport{})
	c.Limiter = nil
	_, err := c.Release(ctx, 1, false)
	var de *Error
	if !errors.As(err, &de) || de.Status != 502 || !strings.HasPrefix(de.Msg, "Could not reach Discogs") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(de.Msg, "api.discogs.com/releases") {
		t.Errorf("message repeats the URL: %q", de.Msg)
	}
}

type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no route to host")
}

type memImages struct {
	ct   string
	body []byte
	puts int
}

func (m *memImages) ImageGet(context.Context, string) (string, []byte, bool, error) {
	return m.ct, m.body, m.body != nil, nil
}

func (m *memImages) ImagePut(_ context.Context, _, ct string, body []byte) error {
	m.ct, m.body = ct, body
	m.puts++
	return nil
}

func TestImagesNeverGetTheTokenAndAreCached(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(newFakeClock(), testToken, rec, func(*http.Request) *http.Response {
		return respond(200, "\xff\xd8jpeg", "Content-Type", "image/jpeg; charset=binary")
	})
	images := &memImages{}
	c.Images = images
	ct, body, err := c.FetchImage(ctx, "https://i.discogs.com/abc/R-1.jpeg")
	if err != nil || ct != "image/jpeg" || !bytes.HasPrefix(body, []byte{0xff, 0xd8}) {
		t.Fatalf("image = %q %q %v", ct, body, err)
	}
	if _, _, err := c.FetchImage(ctx, "https://i.discogs.com/abc/R-1.jpeg"); err != nil || images.puts != 1 {
		t.Fatalf("second fetch: %v, puts %d", err, images.puts)
	}
	for _, bad := range []string{
		"https://evil.example.com/x.jpeg", "https://api.discogs.com/releases/1", "http://i.discogs.com/x.jpeg",
		"https://discogs.com.evil.net/x.jpeg", "https://user@i.discogs.com/x.jpeg", "javascript:alert(1)",
	} {
		_, _, err := c.FetchImage(ctx, bad)
		var de *Error
		if !errors.As(err, &de) || de.Status != 502 || !strings.Contains(de.Msg, "Only Discogs image URLs") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	reqs := rec.all()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if _, ok := reqs[0].Header["Authorization"]; ok {
		t.Fatal("the image request carried the token")
	}
}

func TestImageRedirectAwayFromDiscogsIsRefused(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(newFakeClock(), testToken, rec, func(r *http.Request) *http.Response {
		if r.URL.Host == "i.discogs.com" {
			return respond(302, "", "Location", "https://evil.example.com/x.jpeg")
		}
		return respond(200, "img", "Content-Type", "image/jpeg")
	})
	if _, _, err := c.FetchImage(ctx, "https://i.discogs.com/x.jpeg"); err == nil {
		t.Fatal("followed a redirect off Discogs")
	}
	for _, r := range rec.all() {
		if r.URL.Host == "evil.example.com" {
			t.Fatal("request reached evil.example.com")
		}
	}
	c = newTestClient(newFakeClock(), testToken, &recorder{}, func(*http.Request) *http.Response {
		return respond(200, "<html>", "Content-Type", "text/html")
	})
	if _, _, err := c.FetchImage(ctx, "https://i.discogs.com/x.jpeg"); err == nil {
		t.Fatal("accepted a non-image")
	}
}

func TestAPIRedirectOffHostIsNotFollowed(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(newFakeClock(), testToken, rec, func(r *http.Request) *http.Response {
		if r.URL.Host == APIHost {
			return respond(301, "", "Location", "https://elsewhere.example.com/releases/1")
		}
		return okJSON(r)
	})
	if _, err := c.Release(ctx, 1, false); err == nil {
		t.Fatal("a redirect off api.discogs.com was followed")
	}
	if n := len(rec.all()); n != 1 {
		t.Fatalf("requests = %d", n)
	}
}

func TestSearchAndCollectionPaths(t *testing.T) {
	rec := &recorder{}
	c := newTestClient(newFakeClock(), testToken, rec, func(r *http.Request) *http.Response {
		if r.URL.Path == "/oauth/identity" {
			return respond(200, `{"username": "dig ger"}`)
		}
		return respond(200, `{"results": [], "releases": [], "wants": [], "folders": [{"id": 0, "name": "All", "count": 2}]}`)
	})
	if _, err := c.Search(ctx, SearchParams{Barcode: "5 012345 678900", Type: "release", Format: "Vinyl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, SearchParams{Query: "kf039", Catno: "KF039", Label: "Kniteforce Records", Title: "Simply", Page: 3, PerPage: 500}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Collection(ctx, CollectionParams{FolderID: 0, Page: 2, PerPage: 50, Sort: "added", SortOrder: "desc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Wantlist(ctx, 1, 50, false); err != nil {
		t.Fatal(err)
	}
	folders, err := c.CollectionFolders(ctx, false)
	if err != nil || len(folders) != 1 || folders[0].Name != "All" {
		t.Fatalf("folders = %+v, %v", folders, err)
	}
	reqs := rec.all()
	q := reqs[0].URL.Query()
	// Unset paging is left to Discogs' defaults.
	if len(q) != 3 || q.Get("barcode") != "5012345678900" || q.Get("type") != "release" || q.Get("format") != "Vinyl" {
		t.Errorf("barcode search query = %v", q)
	}
	q = reqs[1].URL.Query()
	if q.Get("catno") != "KF039" || q.Get("label") != "Kniteforce Records" || q.Get("release_title") != "Simply" || q.Has("title") ||
		q.Get("page") != "3" || q.Get("per_page") != "100" {
		t.Errorf("search query = %v", q)
	}
	if reqs[2].URL.Path != "/oauth/identity" {
		t.Errorf("identity first: %s", reqs[2].URL.Path)
	}
	if !strings.HasPrefix(reqs[3].URL.EscapedPath(), "/users/dig%20ger/collection/folders/0/releases") ||
		reqs[3].URL.Query().Get("page") != "2" {
		t.Errorf("collection = %s", reqs[3].URL)
	}
	if reqs[4].URL.Path != "/users/dig ger/wants" {
		t.Errorf("wantlist = %s", reqs[4].URL.Path)
	}
	if len(reqs) != 6 {
		t.Errorf("identity was not remembered: %d requests", len(reqs))
	}
}

func TestKeychainKeepsTheTokenOutOfArgv(t *testing.T) {
	type call struct {
		stdin string
		args  []string
	}
	var calls []call
	k := NewKeychain()
	k.Run = func(_ context.Context, stdin, name string, args ...string) (string, string, int, error) {
		calls = append(calls, call{stdin, append([]string{name}, args...)})
		if len(args) > 0 && args[0] == "find-generic-password" {
			return testToken + "\n", "", 0, nil
		}
		return "", "", 0, nil
	}
	if err := k.Set(ctx, testToken); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls[0].args, " ") != "/usr/bin/security -i" {
		t.Errorf("set argv = %v", calls[0].args)
	}
	want := `add-generic-password -U -s "crate-labels.discogs" -a "personal-access-token" -l "crate-labels Discogs token" -w "` + testToken + "\"\n"
	if calls[0].stdin != want {
		t.Errorf("stdin = %q", calls[0].stdin)
	}
	if got, _ := k.Get(ctx); got != testToken {
		t.Errorf("Get = %q", got)
	}
	if err := k.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if calls[len(calls)-1].args[1] != "delete-generic-password" {
		t.Errorf("delete argv = %v", calls[len(calls)-1].args)
	}
	for _, c := range calls {
		if strings.Contains(strings.Join(c.args, " "), testToken) {
			t.Fatalf("token in argv: %v", c.args)
		}
	}
	var te *TokenError
	if err := k.Set(ctx, `bad" token; rm -rf /`); !errors.As(err, &te) {
		t.Fatalf("bad token: %v", err)
	}
	k.Run = func(context.Context, string, string, ...string) (string, string, int, error) {
		return "", "security: SecKeychainItemCreateFromContent: error", 0, nil
	}
	if err := k.Set(ctx, testToken); !errors.As(err, &te) {
		t.Fatalf("keychain refusal: %v", err)
	}
}

func TestKeychainMissingItemIsEmpty(t *testing.T) {
	k := NewKeychain()
	k.Run = func(context.Context, string, string, ...string) (string, string, int, error) {
		return "", "The specified item could not be found in the keychain.", 44, nil
	}
	if got, err := k.Get(ctx); got != "" || err != nil {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestEnvTokenWinsOverTheStore(t *testing.T) {
	store := &MemoryTokens{}
	if err := store.Set(ctx, testToken); err != nil {
		t.Fatal(err)
	}
	p := &TokenProvider{Store: store, UseEnv: true}
	t.Setenv(EnvVar, "")
	if tok, src := p.Resolve(ctx); tok != testToken || src != SourceKeychain {
		t.Errorf("keychain: %q %q", tok, src)
	}
	t.Setenv(EnvVar, " envtoken1234567890 ")
	if tok, src := p.Resolve(ctx); tok != "envtoken1234567890" || src != SourceEnv {
		t.Errorf("env: %q %q", tok, src)
	}
	if tok, src := (&TokenProvider{Store: &MemoryTokens{}, UseEnv: true}).Resolve(ctx); tok != "envtoken1234567890" || src != SourceEnv {
		t.Errorf("env, empty store: %q %q", tok, src)
	}
	t.Setenv(EnvVar, "")
	if tok, src := (&TokenProvider{Store: &MemoryTokens{}, UseEnv: true}).Resolve(ctx); tok != "" || src != SourceNone {
		t.Errorf("none: %q %q", tok, src)
	}
}

func TestValidateToken(t *testing.T) {
	if got, err := ValidateToken("  " + testToken + "\n"); err != nil || got != testToken {
		t.Errorf("valid: %q %v", got, err)
	}
	for _, bad := range []string{"short", "has space in it 1234", `quote"1234567890`, strings.Repeat("a", 129)} {
		if _, err := ValidateToken(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

type countingTokens struct {
	MemoryTokens
	gets int
}

func (c *countingTokens) Get(ctx context.Context) (string, error) {
	c.gets++
	return c.MemoryTokens.Get(ctx)
}

func TestCachedTokensReadTheKeychainOnce(t *testing.T) {
	inner := &countingTokens{}
	now := time.Unix(0, 0)
	c := NewCachedTokens(inner)
	c.Now = func() time.Time { return now }
	for range 3 {
		if tok, _ := c.Get(ctx); tok != "" {
			t.Fatalf("token = %q", tok)
		}
	}
	if inner.gets != 1 {
		t.Fatalf("empty reads = %d", inner.gets)
	}
	// Another process saves a token: seen once the empty result expires.
	_ = inner.MemoryTokens.Set(ctx, testToken)
	now = now.Add(31 * time.Second)
	if tok, _ := c.Get(ctx); tok != testToken || inner.gets != 2 {
		t.Fatalf("after expiry: %q, %d reads", tok, inner.gets)
	}
	now = now.Add(4 * time.Minute)
	for range 5 {
		_, _ = c.Get(ctx)
	}
	if inner.gets != 2 {
		t.Fatalf("a fresh token was re-read: %d reads", inner.gets)
	}
	// Another process clears it: noticed once the found token is 5 minutes old.
	_ = inner.MemoryTokens.Delete(ctx)
	now = now.Add(time.Minute)
	if tok, _ := c.Get(ctx); tok != "" || inner.gets != 3 {
		t.Fatalf("after FoundTTL: %q, %d reads", tok, inner.gets)
	}
	_ = inner.MemoryTokens.Set(ctx, testToken)
	c.Invalidate()
	if tok, _ := c.Get(ctx); tok != testToken || inner.gets != 4 {
		t.Fatalf("after Invalidate: %q, %d reads", tok, inner.gets)
	}
	other := strings.Repeat("Y", 40)
	if err := c.Set(ctx, other); err != nil {
		t.Fatal(err)
	}
	if tok, _ := c.Get(ctx); tok != other || inner.gets != 4 {
		t.Fatalf("after Set: %q, %d reads", tok, inner.gets)
	}
	if err := c.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if tok, _ := c.Get(ctx); tok != "" || inner.gets != 4 {
		t.Fatalf("after Delete: %q, %d reads", tok, inner.gets)
	}
	if err := c.Set(ctx, "bad token"); err == nil {
		t.Fatal("bad token accepted")
	}
}

func TestOversizedBodiesAreErrorsNotTruncated(t *testing.T) {
	images := &memImages{}
	c := newTestClient(newFakeClock(), testToken, &recorder{}, func(r *http.Request) *http.Response {
		if r.URL.Host == "i.discogs.com" {
			return respond(200, strings.Repeat("x", 11), "Content-Type", "image/jpeg")
		}
		return respond(200, `{"id": 1, "title": "`+strings.Repeat("x", 100)+`"}`)
	})
	c.Images, c.MaxImageBytes, c.MaxBodyBytes = images, 10, 50
	var de *Error
	if _, _, err := c.FetchImage(ctx, "https://i.discogs.com/big.jpeg"); !errors.As(err, &de) || de.Status != 502 {
		t.Fatalf("oversized image: %v", err)
	}
	if images.puts != 0 {
		t.Fatal("a truncated image was cached")
	}
	if _, err := c.Release(ctx, 1, false); !errors.As(err, &de) || de.Status != 502 || !strings.Contains(de.Msg, "too large") {
		t.Fatalf("oversized body: %v", err)
	}
	c.MaxImageBytes = 11
	if _, body, err := c.FetchImage(ctx, "https://i.discogs.com/big.jpeg"); err != nil || len(body) != 11 || images.puts != 1 {
		t.Fatalf("image at the cap: %d bytes, %v", len(body), err)
	}
}

func TestA401RereadsTheTokenAndRetriesOnce(t *testing.T) {
	newToken := strings.Repeat("N", 40)
	token := testToken
	rec := &recorder{}
	c := NewClient(func(context.Context) string { return token }, roundTrip(func(r *http.Request) *http.Response {
		rec.add(r, time.Now())
		if r.Header.Get("Authorization") == "Discogs token="+newToken {
			return respond(200, `{"id": 5}`)
		}
		return respond(401, `{"message": "Invalid consumer token."}`)
	}))
	c.Limiter = nil
	refreshed := 0
	c.OnUnauthorized = func(context.Context) { refreshed++; token = newToken } // set by another process
	r, err := c.Release(ctx, 5, false)
	if err != nil || r.ID != 5 || refreshed != 1 || len(rec.all()) != 2 {
		t.Fatalf("release = %+v, %v; refreshed %d, requests %d", r, err, refreshed, len(rec.all()))
	}
	// The token did not change: no retry, and the 401 explains.
	c.OnUnauthorized = func(context.Context) { refreshed++ }
	token = testToken
	_, err = c.Release(ctx, 6, false)
	var de *Error
	if !errors.As(err, &de) || de.Status != 401 || !strings.Contains(de.Msg, "Check it in Settings") || len(rec.all()) != 3 {
		t.Fatalf("unchanged token: %v, requests %d", err, len(rec.all()))
	}
	// The new token is rejected too: still only one retry.
	c.OnUnauthorized = func(context.Context) { token = strings.Repeat("Z", 40) }
	if _, err := c.Release(ctx, 7, false); !errors.As(err, &de) || de.Status != 401 || len(rec.all()) != 5 {
		t.Fatalf("second 401: %v, requests %d", err, len(rec.all()))
	}
}

func TestCheckTokenSendsTheCandidateAndNeverRetries(t *testing.T) {
	candidate := strings.Repeat("C", 40)
	rec := &recorder{}
	c := newTestClient(newFakeClock(), testToken, rec, func(r *http.Request) *http.Response {
		if r.Header.Get("Authorization") == "Discogs token="+candidate {
			return respond(200, `{"id": 1, "username": "testuser"}`)
		}
		return respond(401, `{"message": "nope"}`)
	})
	called := 0
	c.OnUnauthorized = func(context.Context) { called++ }
	id, err := c.CheckToken(ctx, candidate)
	if err != nil || id.Username != "testuser" {
		t.Fatalf("check = %+v, %v", id, err)
	}
	_, err = c.CheckToken(ctx, strings.Repeat("X", 40))
	if !IsRejected(err) || called != 0 || len(rec.all()) != 2 {
		t.Fatalf("rejected check: %v, OnUnauthorized %d, requests %d", err, called, len(rec.all()))
	}
	if IsRejected(&Error{Status: 502, Msg: "Could not reach Discogs"}) {
		t.Fatal("a network error counts as a rejection")
	}
}

func TestErrorDetailIsOneShortLine(t *testing.T) {
	if got := errorDetail([]byte(`{"message": " Release not found. "}`)); got != "Release not found." {
		t.Errorf("json = %q", got)
	}
	long := strings.Repeat("é", maxDetail+10) + "\nsecond line"
	got := errorDetail([]byte(long))
	if want := strings.Repeat("é", maxDetail) + "…"; got != want {
		t.Errorf("long = %q", got)
	}
	if got := errorDetail([]byte("<html>\n<body>Bad Gateway</body>")); got != "<html>" {
		t.Errorf("html = %q", got)
	}
	if got := errorDetail([]byte("bad \xff byte")); got != "bad  byte" {
		t.Errorf("invalid UTF-8 = %q", got)
	}
}
