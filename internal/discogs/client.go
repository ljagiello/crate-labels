package discogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// API endpoints and client identity.
const (
	APIBase   = "https://api.discogs.com"
	APIHost   = "api.discogs.com"
	UserAgent = "crate-labels/0.1"
)

// Whole-request timeouts. API answers are small JSON documents; 20 s allows for a slow
// link without leaving the UI hanging. Covers can be several megabytes, so they get 45 s.
const (
	APITimeout   = 20 * time.Second
	ImageTimeout = 45 * time.Second
)

// DefaultPatience is the longest one call waits out 429 holds, in total, before it gives
// up with a 429 of its own (the hold still applies to later calls).
const DefaultPatience = 20 * time.Second

// Cache lifetimes per endpoint (docs/API.md "Discogs"). Identity is never cached.
const (
	TTLRelease    = 30 * 24 * time.Hour
	TTLMaster     = 30 * 24 * time.Hour
	TTLSearch     = time.Hour
	TTLCollection = 5 * time.Minute
)

// Default size caps: a release with a huge tracklist is well under the API cap.
const (
	DefaultMaxBodyBytes  = 16 << 20
	DefaultMaxImageBytes = 20 << 20
)

// Error is a Discogs failure with a message fit for the user. Status is the HTTP status
// crate-labels replies with: 401 (token missing or rejected), 404, 429 or 502.
type Error struct {
	Status   int
	Upstream int // the status Discogs sent, 0 if it was never reached
	Msg      string
}

func (e *Error) Error() string { return e.Msg }

// IsNotFound reports whether err is a Discogs 404.
func IsNotFound(err error) bool {
	var de *Error
	return errors.As(err, &de) && de.Status == http.StatusNotFound
}

// Cache stores response bodies (implemented by store.Store).
type Cache interface {
	CacheGet(ctx context.Context, url string, maxAge time.Duration) ([]byte, bool, error)
	CachePut(ctx context.Context, url string, body []byte) error
}

// ImageCache stores cover images (implemented by store.Store).
type ImageCache interface {
	ImageGet(ctx context.Context, url string) (contentType string, body []byte, ok bool, err error)
	ImagePut(ctx context.Context, url, contentType string, body []byte) error
}

// tokenKey carries a candidate token that replaces Token for one call (CheckToken).
type tokenKey struct{}

// tokenFor is the token a request made with ctx carries.
func (c *Client) tokenFor(ctx context.Context) string {
	if t, ok := ctx.Value(tokenKey{}).(string); ok {
		return t
	}
	return c.Token(ctx)
}

// Client calls api.discogs.com. Build it with NewClient; the exported fields may be
// changed before first use.
type Client struct {
	Token      func(ctx context.Context) string // "" = no token
	Cache      Cache                            // nil = no response cache
	Images     ImageCache                       // nil = no image cache
	Limiter    *Limiter                         // nil = no pacing (tests)
	MaxRetries int                              // after a 429
	Patience   time.Duration                    // see DefaultPatience
	// OnUnauthorized runs when Discogs rejects a request's token (401 or 403), before a
	// single retry if Token then returns a different token. The service drops its
	// cached Keychain token here.
	OnUnauthorized func(ctx context.Context)
	// Size caps; a response or image over its cap is an error, never truncated.
	MaxBodyBytes, MaxImageBytes int64
	Log                         *slog.Logger

	api *http.Client
	web *http.Client

	mu        sync.Mutex
	username  string
	remaining int // last X-Discogs-Ratelimit-Remaining, -1 if unknown
}

// NewClient returns a client. transport may be nil for http.DefaultTransport; tests
// pass a RoundTripper that serves fixtures, so the host checks still run.
func NewClient(token func(ctx context.Context) string, transport http.RoundTripper) *Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	if token == nil {
		token = func(context.Context) string { return "" }
	}
	c := &Client{Token: token, Limiter: NewLimiter(), MaxRetries: 2, Patience: DefaultPatience,
		Log: slog.Default(), remaining: -1, MaxBodyBytes: DefaultMaxBodyBytes, MaxImageBytes: DefaultMaxImageBytes}
	c.api = &http.Client{
		Transport: transport,
		Timeout:   APITimeout,
		// The token must reach api.discogs.com only: never follow a redirect elsewhere.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || req.URL.Host != APIHost || len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	c.web = &http.Client{
		Transport: transport,
		Timeout:   ImageTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !IsImageURL(req.URL.String()) || len(via) >= 5 {
				return errors.New("the image redirected away from Discogs")
			}
			req.Header.Del("Authorization")
			return nil
		},
	}
	return c
}

// RateLimitRemaining is the last X-Discogs-Ratelimit-Remaining seen, or -1.
func (c *Client) RateLimitRemaining() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remaining
}

// HasToken reports whether a token is available.
func (c *Client) HasToken(ctx context.Context) bool { return c.Token(ctx) != "" }

// ForgetIdentity drops the cached username (after the token changes).
func (c *Client) ForgetIdentity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.username = ""
}

// IsImageURL reports whether url is an https URL on *.discogs.com other than the API.
func IsImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != APIHost && (host == "discogs.com" || strings.HasSuffix(host, ".discogs.com"))
}

// observe passes a response's rate-limit headers to the limiter and remembers Remaining.
func (c *Client) observe(h http.Header, status int, authenticated bool) time.Duration {
	if n, ok := headerInt(h, "X-Discogs-Ratelimit-Remaining"); ok {
		c.mu.Lock()
		c.remaining = n
		c.mu.Unlock()
	}
	return c.Limiter.Observe(h, status, authenticated)
}

// maxDetail bounds how much of a non-JSON error body reaches a message: one line of
// at most this many characters.
const maxDetail = 120

// errorDetail is Discogs' "message" field, else the first line of the body, shortened.
func errorDetail(body []byte) string {
	var msg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &msg) == nil {
		return strings.TrimSpace(msg.Message)
	}
	line, _, _ := strings.Cut(strings.ToValidUTF8(string(body), ""), "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > maxDetail {
		line = string(r[:maxDetail]) + "…"
	}
	return line
}

func (c *Client) statusError(status int, body []byte, what string, hadToken bool) *Error {
	detail := errorDetail(body)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if hadToken {
			return &Error{Status: 401, Upstream: status,
				Msg: fmt.Sprintf("Discogs rejected the token for %s (%d). Check it in Settings.", what, status)}
		}
		return &Error{Status: 401, Upstream: status,
			Msg: fmt.Sprintf("Discogs needs your personal access token for %s. Add it in Settings.", what)}
	case status == http.StatusNotFound:
		return &Error{Status: 404, Upstream: status, Msg: fmt.Sprintf("Discogs has no %s.", what)}
	case status == http.StatusTooManyRequests:
		return &Error{Status: 429, Upstream: status, Msg: "Discogs is rate limiting crate-labels; try again in a minute."}
	}
	return &Error{Status: 502, Upstream: status,
		Msg: strings.TrimSpace(fmt.Sprintf("Discogs returned %d for %s: %s", status, what, detail))}
}

// apiURL builds the request URL. url.Values.Encode sorts the query, so it doubles as
// the cache key (it never contains the token, which travels in a header).
func apiURL(path string, params url.Values) string {
	clean := url.Values{}
	for k, vs := range params {
		for _, v := range vs {
			if v != "" {
				clean.Add(k, v)
			}
		}
	}
	u := APIBase + path
	if q := clean.Encode(); q != "" {
		u += "?" + q
	}
	return u
}

// GetJSON fetches path from api.discogs.com through the cache and the gate and returns
// the raw body. ttl 0 disables caching; refresh skips the cache read.
func (c *Client) GetJSON(ctx context.Context, path string, params url.Values, what string, ttl time.Duration, refresh bool) ([]byte, error) {
	key := apiURL(path, params)
	if ttl > 0 && !refresh && c.Cache != nil {
		if body, ok, err := c.Cache.CacheGet(ctx, key, ttl); err == nil && ok {
			return body, nil
		}
	}
	status, body, used, err := c.send(ctx, key, path, what)
	if err != nil {
		return nil, err
	}
	_, checking := ctx.Value(tokenKey{}).(string)
	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && c.OnUnauthorized != nil && !checking {
		// The token may have been changed by another process (`crate-labels discogs token
		// set`): drop the cached one and, if the store now holds another, retry once.
		c.OnUnauthorized(ctx)
		if now := c.Token(ctx); now != used {
			c.ForgetIdentity()
			if status, body, used, err = c.send(ctx, key, path, what); err != nil {
				return nil, err
			}
		}
	}
	if status >= 400 {
		return nil, c.statusError(status, body, what, used != "")
	}
	if !json.Valid(body) {
		return nil, &Error{Status: 502, Upstream: status, Msg: fmt.Sprintf("Discogs sent something that is not JSON for %s.", what)}
	}
	if ttl > 0 && c.Cache != nil {
		if err := c.Cache.CachePut(ctx, key, body); err != nil {
			c.Log.Warn("could not cache a Discogs response", "path", path, "err", err)
		}
	}
	return body, nil
}

// send makes the request and retries a 429 up to MaxRetries times, as long as the holds
// it waits out stay within Patience in total. used is the token the last attempt carried
// ("" for none).
func (c *Client) send(ctx context.Context, key, path, what string) (status int, body []byte, used string, err error) {
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		held := c.Limiter.HeldFor()
		if waited+held > c.Patience {
			secs := int((held + time.Second - 1) / time.Second)
			return 0, nil, "", &Error{Status: http.StatusTooManyRequests, Upstream: http.StatusTooManyRequests,
				Msg: fmt.Sprintf("Discogs is rate limiting crate-labels; try again in %d s.", secs)}
		}
		waited += held
		var header http.Header
		status, header, body, used, err = c.once(ctx, key)
		if err != nil {
			return 0, nil, used, err
		}
		hold := c.observe(header, status, used != "")
		if status != http.StatusTooManyRequests {
			return status, body, used, nil
		}
		c.Log.Warn("Discogs rate limit; holding requests", "path", path, "hold", hold)
		if attempt >= c.MaxRetries {
			return 0, nil, used, c.statusError(status, body, what, used != "")
		}
	}
}

// once waits for the limiter, sends one request and reads the whole body.
func (c *Client) once(ctx context.Context, rawURL string) (status int, header http.Header, body []byte, used string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, nil, "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	if req.URL.Scheme == "https" && req.URL.Host == APIHost {
		if token := c.tokenFor(ctx); token != "" {
			req.Header.Set("Authorization", "Discogs token="+token)
			used = token
		}
	}
	if err := c.Limiter.Wait(ctx, used != ""); err != nil {
		return 0, nil, nil, used, err
	}
	resp, err := c.api.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, used, ctx.Err()
		}
		return 0, nil, nil, used, &Error{Status: 502, Msg: "Could not reach Discogs: " + transportReason(err)}
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, c.MaxBodyBytes+1))
	if err != nil {
		return 0, nil, nil, used, &Error{Status: 502, Msg: "Discogs stopped answering: " + transportReason(err)}
	}
	if int64(len(body)) > c.MaxBodyBytes {
		return 0, nil, nil, used, &Error{Status: 502, Msg: "Discogs sent an answer too large to read."}
	}
	return resp.StatusCode, resp.Header, body, used, nil
}

// transportReason strips the URL from a *url.Error, so messages stay short.
func transportReason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

func decode[T any](body []byte, what string) (*T, error) {
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, &Error{Status: 502, Msg: fmt.Sprintf("Discogs sent an unexpected answer for %s.", what)}
	}
	return &v, nil
}

// --- endpoints --------------------------------------------------------------------------

// Identity is `GET /oauth/identity`: whom the token belongs to. Never cached.
func (c *Client) Identity(ctx context.Context) (*Identity, error) {
	body, err := c.GetJSON(ctx, "/oauth/identity", nil, "your identity", 0, true)
	if err != nil {
		return nil, err
	}
	id, err := decode[Identity](body, "your identity")
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.username = id.Username
	c.mu.Unlock()
	return id, nil
}

// Username is the token's account, asking Discogs once.
func (c *Client) Username(ctx context.Context) (string, error) {
	c.mu.Lock()
	u := c.username
	c.mu.Unlock()
	if u != "" {
		return u, nil
	}
	id, err := c.Identity(ctx)
	if err != nil {
		return "", err
	}
	if id.Username == "" {
		return "", &Error{Status: 401, Msg: "Discogs did not say which account the token belongs to."}
	}
	return id.Username, nil
}

// ReleaseRaw is `GET /releases/{id}` as raw JSON.
func (c *Client) ReleaseRaw(ctx context.Context, id int64, refresh bool) ([]byte, error) {
	return c.GetJSON(ctx, fmt.Sprintf("/releases/%d", id), nil, fmt.Sprintf("release %d", id), TTLRelease, refresh)
}

// Release is `GET /releases/{id}`.
func (c *Client) Release(ctx context.Context, id int64, refresh bool) (*Release, error) {
	what := fmt.Sprintf("release %d", id)
	body, err := c.ReleaseRaw(ctx, id, refresh)
	if err != nil {
		return nil, err
	}
	r, err := decode[Release](body, what)
	if err != nil {
		return nil, err
	}
	if r.ID == 0 {
		r.ID = Int(id)
	}
	return r, nil
}

// Master is `GET /masters/{id}`.
func (c *Client) Master(ctx context.Context, id int64, refresh bool) (*Master, error) {
	what := fmt.Sprintf("master %d", id)
	body, err := c.GetJSON(ctx, fmt.Sprintf("/masters/%d", id), nil, what, TTLMaster, refresh)
	if err != nil {
		return nil, err
	}
	return decode[Master](body, what)
}

// SearchParams are the `GET /database/search` filters crate-labels uses.
type SearchParams struct {
	Query, Barcode, Catno, Label, Artist, Title string
	Type                                        string // "release" or "master"
	Format                                      string // e.g. "Vinyl"; "" = any
	Page, PerPage                               int
	Refresh                                     bool
}

// Search is `GET /database/search` (Discogs needs a token for it). The barcode is
// reduced to its digits and the title goes in `release_title`. Page and PerPage are sent
// only when set, so Discogs' own pagination defaults apply otherwise.
func (c *Client) Search(ctx context.Context, p SearchParams) (*SearchPage, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, p.Barcode)
	q := url.Values{
		"q":             {strings.TrimSpace(p.Query)},
		"type":          {p.Type},
		"barcode":       {digits},
		"catno":         {strings.TrimSpace(p.Catno)},
		"label":         {strings.TrimSpace(p.Label)},
		"artist":        {strings.TrimSpace(p.Artist)},
		"release_title": {strings.TrimSpace(p.Title)},
		"format":        {p.Format},
	}
	paginate(q, p.Page, p.PerPage)
	body, err := c.GetJSON(ctx, "/database/search", q, "search", TTLSearch, p.Refresh)
	if err != nil {
		return nil, err
	}
	return decode[SearchPage](body, "search")
}

// CollectionFolders is `GET /users/{u}/collection/folders`.
func (c *Client) CollectionFolders(ctx context.Context, refresh bool) ([]Folder, error) {
	user, err := c.Username(ctx)
	if err != nil {
		return nil, err
	}
	body, err := c.GetJSON(ctx, "/users/"+url.PathEscape(user)+"/collection/folders", nil,
		"collection folders", TTLCollection, refresh)
	if err != nil {
		return nil, err
	}
	page, err := decode[struct {
		Folders []Folder `json:"folders"`
	}](body, "collection folders")
	if err != nil {
		return nil, err
	}
	return page.Folders, nil
}

// CollectionParams page through a collection folder.
type CollectionParams struct {
	FolderID        int64
	Page, PerPage   int
	Sort, SortOrder string
	Refresh         bool
	Username        string // "" = the token's account
}

// Collection is `GET /users/{u}/collection/folders/{f}/releases`.
func (c *Client) Collection(ctx context.Context, p CollectionParams) (*CollectionPage, error) {
	user := p.Username
	if user == "" {
		var err error
		if user, err = c.Username(ctx); err != nil {
			return nil, err
		}
	}
	q := url.Values{"sort": {p.Sort}, "sort_order": {p.SortOrder}}
	paginate(q, p.Page, p.PerPage)
	path := fmt.Sprintf("/users/%s/collection/folders/%d/releases", url.PathEscape(user), p.FolderID)
	body, err := c.GetJSON(ctx, path, q, "your collection", TTLCollection, p.Refresh)
	if err != nil {
		return nil, err
	}
	return decode[CollectionPage](body, "your collection")
}

// Wantlist is `GET /users/{u}/wants`.
func (c *Client) Wantlist(ctx context.Context, page, perPage int, refresh bool) (*CollectionPage, error) {
	user, err := c.Username(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	paginate(q, page, perPage)
	body, err := c.GetJSON(ctx, "/users/"+url.PathEscape(user)+"/wants", q, "your wantlist", TTLCollection, refresh)
	if err != nil {
		return nil, err
	}
	return decode[CollectionPage](body, "your wantlist")
}

// paginate adds page and per_page when they are set (> 0). Discogs shows 50 items per
// page by default and at most 100; a larger per_page is sent as 100.
func paginate(q url.Values, page, perPage int) {
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if perPage > 0 {
		q.Set("per_page", strconv.Itoa(min(perPage, 100)))
	}
}

// FetchImage downloads a cover image from Discogs' image host, through the image cache.
// It never sends the token and refuses anything but https *.discogs.com (not the API).
func (c *Client) FetchImage(ctx context.Context, rawURL string) (contentType string, body []byte, err error) {
	if !IsImageURL(rawURL) {
		return "", nil, &Error{Status: 502, Msg: "Only Discogs image URLs can be fetched."}
	}
	if c.Images != nil {
		if ct, b, ok, err := c.Images.ImageGet(ctx, rawURL); err == nil && ok {
			return ct, b, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, &Error{Status: 502, Msg: "Only Discogs image URLs can be fetched."}
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.web.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, &Error{Status: 502, Msg: "Could not download the cover image: " + transportReason(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", nil, &Error{Status: 502, Upstream: resp.StatusCode,
			Msg: fmt.Sprintf("The cover image returned %d.", resp.StatusCode)}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		ct = mt
	}
	if !strings.HasPrefix(ct, "image/") {
		return "", nil, &Error{Status: 502, Msg: "Discogs did not send an image."}
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, c.MaxImageBytes+1))
	if err != nil {
		return "", nil, &Error{Status: 502, Msg: "The cover image download broke off: " + transportReason(err)}
	}
	if int64(len(body)) > c.MaxImageBytes {
		// Never cache (or serve) a truncated image.
		return "", nil, &Error{Status: 502, Msg: "The cover image is too large to show."}
	}
	if c.Images != nil {
		if err := c.Images.ImagePut(ctx, rawURL, ct, body); err != nil {
			c.Log.Warn("could not cache a cover image", "err", err)
		}
	}
	return ct, body, nil
}

// CheckToken asks Discogs whom token belongs to, without storing or caching anything.
// A 401 means Discogs rejected the token; any other error means it could not be checked.
func (c *Client) CheckToken(ctx context.Context, token string) (*Identity, error) {
	ctx = context.WithValue(ctx, tokenKey{}, token)
	body, err := c.GetJSON(ctx, "/oauth/identity", nil, "your identity", 0, true)
	if err != nil {
		return nil, err
	}
	return decode[Identity](body, "your identity")
}

// IsRejected reports whether err is Discogs refusing the token (it answered 401 or 403).
func IsRejected(err error) bool {
	var de *Error
	return errors.As(err, &de) && (de.Upstream == http.StatusUnauthorized || de.Upstream == http.StatusForbidden)
}
