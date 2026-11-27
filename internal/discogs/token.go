package discogs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Where the token lives in the macOS Keychain (a generic password).
const (
	KeychainService = "crate-labels.discogs"
	KeychainAccount = "personal-access-token"
	KeychainRef     = "keychain:" + KeychainService + "/" + KeychainAccount
	EnvVar          = "DISCOGS_TOKEN"
)

// Token sources reported by TokenProvider.Resolve.
const (
	SourceEnv      = "env"
	SourceKeychain = "keychain"
	SourceNone     = "none"
)

// Discogs personal tokens are 40 alphanumerics. Allow some slack, but never quotes or
// spaces, which would break the `security -i` command line.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{10,128}$`)

// TokenError says the token could not be checked, stored or read. Its message is fit
// for the user (HTTP 400).
type TokenError struct{ Msg string }

func (e *TokenError) Error() string { return e.Msg }

// ValidateToken trims token and checks it looks like a personal access token.
func ValidateToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if !tokenRE.MatchString(token) {
		return "", &TokenError{Msg: "That does not look like a Discogs personal access token (letters and digits, " +
			"usually 40 characters). Generate one at discogs.com → Settings → Developers."}
	}
	return token, nil
}

// TokenStore keeps the token somewhere safe. Get returns "" when none is stored.
type TokenStore interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, token string) error
	Delete(ctx context.Context) error
}

// Runner runs a command with stdin and returns its stdout, stderr and exit code. err is
// set only when the command could not run at all.
type Runner func(ctx context.Context, stdin string, name string, args ...string) (stdout, stderr string, code int, err error)

// ExecRunner runs commands with os/exec, passing arguments as a list, never via a shell.
func ExecRunner(ctx context.Context, stdin string, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), errb.String(), exit.ExitCode(), nil
	}
	if err != nil {
		return "", "", -1, err
	}
	return out.String(), errb.String(), 0, nil
}

// Keychain stores the token in the login keychain through /usr/bin/security. The token
// never goes into argv: writes pipe an `add-generic-password` command into `security -i`.
type Keychain struct {
	Service, Account string
	Security         string // path to the security tool
	Run              Runner
}

// NewKeychain returns the crate-labels keychain item.
func NewKeychain() *Keychain {
	return &Keychain{Service: KeychainService, Account: KeychainAccount, Security: "/usr/bin/security", Run: ExecRunner}
}

func (k *Keychain) run(ctx context.Context, timeout time.Duration, stdin string, args ...string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return k.Run(ctx, stdin, k.Security, args...)
}

// Get reads the token. A missing item (exit code 44) or any failure gives "".
func (k *Keychain) Get(ctx context.Context) (string, error) {
	out, _, code, err := k.run(ctx, 10*time.Second, "", "find-generic-password", "-s", k.Service, "-a", k.Account, "-w")
	if err != nil || code != 0 {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// Set stores (or replaces) the token.
func (k *Keychain) Set(ctx context.Context, token string) error {
	token, err := ValidateToken(token)
	if err != nil {
		return err
	}
	command := fmt.Sprintf("add-generic-password -U -s %q -a %q -l %q -w %q\n",
		k.Service, k.Account, "crate-labels Discogs token", token)
	_, stderr, code, err := k.run(ctx, 15*time.Second, command, "-i")
	if err != nil {
		return &TokenError{Msg: "Could not reach the macOS Keychain: " + err.Error()}
	}
	if code != 0 || strings.Contains(strings.ToLower(stderr), "error") {
		return &TokenError{Msg: "The macOS Keychain refused to store the token."}
	}
	return nil
}

// Delete removes the item. A missing item is not an error.
func (k *Keychain) Delete(ctx context.Context) error {
	_, _, _, _ = k.run(ctx, 10*time.Second, "", "delete-generic-password", "-s", k.Service, "-a", k.Account)
	return nil
}

// MemoryTokens is a process-local TokenStore for tests and non-macOS runs.
type MemoryTokens struct {
	mu    sync.Mutex
	token string
}

// Get returns the stored token.
func (m *MemoryTokens) Get(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token, nil
}

// Set validates and stores token.
func (m *MemoryTokens) Set(_ context.Context, token string) error {
	token, err := ValidateToken(token)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = token
	return nil
}

// Delete forgets the token.
func (m *MemoryTokens) Delete(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = ""
	return nil
}

// TokenProvider resolves the token on every call, so a newly saved token applies at
// once: the DISCOGS_TOKEN environment variable first, then the store.
type TokenProvider struct {
	Store  TokenStore
	UseEnv bool
}

// Resolve returns the token and where it came from ("env", "keychain" or "none").
func (p *TokenProvider) Resolve(ctx context.Context) (token, source string) {
	if p.UseEnv {
		if env := strings.TrimSpace(os.Getenv(EnvVar)); env != "" {
			return env, SourceEnv
		}
	}
	if p.Store != nil {
		if t, err := p.Store.Get(ctx); err == nil && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t), SourceKeychain
		}
	}
	return "", SourceNone
}

// Token returns just the token ("" for none).
func (p *TokenProvider) Token(ctx context.Context) string {
	t, _ := p.Resolve(ctx)
	return t
}

// CachedTokens remembers what a slower TokenStore (the Keychain, one `security` run per
// read) returned, so a Discogs request does not run a process. Set and Delete update the
// cache. The store is read again once a found token is FoundTTL old, or "no token" is
// EmptyTTL old, so `crate-labels discogs token set` or `clear` run from another process
// is noticed; Invalidate (called on a 401) forces the next Get to read it.
type CachedTokens struct {
	Store    TokenStore
	FoundTTL time.Duration
	EmptyTTL time.Duration
	Now      func() time.Time

	mu       sync.Mutex
	token    string
	loaded   bool
	loadedAt time.Time
}

// NewCachedTokens wraps store.
func NewCachedTokens(store TokenStore) *CachedTokens {
	return &CachedTokens{Store: store, FoundTTL: 5 * time.Minute, EmptyTTL: 30 * time.Second, Now: time.Now}
}

// Get returns the cached token, reading the store when the cache is empty or stale.
func (c *CachedTokens) Get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.EmptyTTL
	if c.token != "" {
		ttl = c.FoundTTL
	}
	if c.loaded && c.Now().Sub(c.loadedAt) < ttl {
		return c.token, nil
	}
	t, err := c.Store.Get(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.loaded, c.loadedAt = strings.TrimSpace(t), true, c.Now()
	return c.token, nil
}

// Invalidate drops the cached value, so the next Get reads the store.
func (c *CachedTokens) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
}

// Set stores token and caches it.
func (c *CachedTokens) Set(ctx context.Context, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Store.Set(ctx, token); err != nil {
		c.loaded = false // the store's state is unknown now
		return err
	}
	c.token, c.loaded, c.loadedAt = strings.TrimSpace(token), true, c.Now()
	return nil
}

// Delete removes the token and caches its absence.
func (c *CachedTokens) Delete(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.Store.Delete(ctx)
	c.token, c.loaded, c.loadedAt = "", err == nil, c.Now()
	return err
}
