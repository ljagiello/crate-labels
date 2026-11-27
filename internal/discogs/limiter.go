package discogs

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Discogs's published API limits (https://www.discogs.com/developers, "Rate Limiting"):
// requests are counted over a moving 60-second window, 60 a minute for authenticated
// requests and 25 for unauthenticated ones, and a window with no requests for 60 s resets.
// Every response reports the window in X-Discogs-Ratelimit (the limit),
// X-Discogs-Ratelimit-Used and X-Discogs-Ratelimit-Remaining.
const (
	AuthenticatedPerMinute = 60
	AnonymousPerMinute     = 25
	RateWindow             = time.Minute
)

// DefaultBurst is how many requests may start back to back from an idle limiter. Three
// covers the most a single user action costs at once (a master link is master + release;
// the first collection page is identity + folders + page), so those never wait.
const DefaultBurst = 3

// Back-off after a 429 that carries no Retry-After: 5 s, doubling for each 429 in a row,
// never longer than RateWindow, since a full window without requests resets the count.
// At the documented rate a slot frees about every second, so 5 s frees several; the
// doubling covers another client on the same address that keeps spending the budget.
const backoffBase = 5 * time.Second

// Limiter paces requests to api.discogs.com. It is a token bucket kept as a theoretical
// arrival time (tat): each request moves tat one refill interval forward, and a request
// may start while tat is less than a full bucket ahead of now.
//
// The bucket holds Burst tokens and refills at (limit − Burst) per RateWindow, so burst
// plus refill never adds up to more than limit starts in any window. limit is the
// documented figure for the request's kind until Discogs reports its own in
// X-Discogs-Ratelimit. X-Discogs-Ratelimit-Remaining and 429s only ever lower what is
// left (see Observe).
//
// A nil *Limiter lets every request through at once; tests use that.
type Limiter struct {
	Burst int                                              // tokens when idle; 0 = DefaultBurst
	Now   func() time.Time                                 // nil = time.Now
	Sleep func(ctx context.Context, d time.Duration) error // nil = a timer that honours ctx
	// Jitter returns a random wait in [0, d). It spreads a 429 back-off by up to a fifth.
	// nil = math/rand.
	Jitter func(d time.Duration) time.Duration

	mu         sync.Mutex
	advertised [2]int    // X-Discogs-Ratelimit per kind (index: authenticated); 0 = unseen
	tat        time.Time // the bucket is full again at tat; each start adds one interval
	holdUntil  time.Time // after a 429, nothing starts before this
	strikes    int       // 429s in a row
}

// NewLimiter returns a limiter on the real clock with the default burst.
func NewLimiter() *Limiter { return &Limiter{Burst: DefaultBurst} }

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Limiter) sleep(ctx context.Context, d time.Duration) error {
	if l.Sleep != nil {
		return l.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Limiter) jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if l.Jitter != nil {
		return l.Jitter(d)
	}
	return rand.N(d)
}

func kindIndex(authenticated bool) int {
	if authenticated {
		return 1
	}
	return 0
}

// Limit is the per-window limit the limiter works to for a kind of request.
func (l *Limiter) Limit(authenticated bool) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit(authenticated)
}

func (l *Limiter) limit(authenticated bool) int {
	if n := l.advertised[kindIndex(authenticated)]; n > 0 {
		return n
	}
	if authenticated {
		return AuthenticatedPerMinute
	}
	return AnonymousPerMinute
}

// shape is the bucket for a kind of request: its size and its refill interval. The burst
// is at most half the limit, so most of the window is paced even for a tiny limit.
func (l *Limiter) shape(authenticated bool) (burst int, every time.Duration) {
	limit := l.limit(authenticated)
	burst = l.Burst
	if burst <= 0 {
		burst = DefaultBurst
	}
	burst = max(1, min(burst, limit/2))
	return burst, RateWindow / time.Duration(limit-burst)
}

// Wait blocks until a request may start, or ctx ends.
func (l *Limiter) Wait(ctx context.Context, authenticated bool) error {
	if l == nil {
		return ctx.Err()
	}
	for {
		l.mu.Lock()
		now := l.now()
		burst, every := l.shape(authenticated)
		next := l.tat
		if next.Before(now) {
			next = now
		}
		// next is a whole bucket ahead of now when no token is left.
		wait := next.Sub(now) - time.Duration(burst-1)*every
		if hold := l.holdUntil.Sub(now); hold > wait {
			wait = hold
		}
		if wait <= 0 {
			l.tat = next.Add(every)
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// HeldFor is how long the limiter still holds every request back after a 429.
func (l *Limiter) HeldFor() time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return max(0, l.holdUntil.Sub(l.now()))
}

// Observe takes in one Discogs response and returns the hold a 429 started (0 otherwise).
//
//   - X-Discogs-Ratelimit replaces the documented limit for this kind of request.
//   - X-Discogs-Ratelimit-Remaining caps the tokens left. Discogs reports the count from
//     before the request it answers (the first request after an idle minute reads used 0,
//     remaining = limit), so one more is held back. It never adds tokens.
//   - A 429 holds every request for Retry-After, or for the back-off above, capped at
//     RateWindow. When the hold ends the bucket has one token and refills from there.
func (l *Limiter) Observe(h http.Header, status int, authenticated bool) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if n, ok := headerInt(h, "X-Discogs-Ratelimit"); ok && n >= 2 && n <= 10000 {
		l.advertised[kindIndex(authenticated)] = n
	}
	burst, every := l.shape(authenticated)
	if n, ok := headerInt(h, "X-Discogs-Ratelimit-Remaining"); ok {
		if left := max(0, n-1); left < burst {
			l.pushTAT(now.Add(time.Duration(burst-left) * every))
		}
	}
	if status != http.StatusTooManyRequests {
		l.strikes = 0
		return 0
	}
	l.strikes++
	hold, ok := parseRetryAfter(h.Get("Retry-After"), now)
	if !ok {
		step := backoffBase << min(l.strikes-1, 4)
		hold = step + l.jitter(step/5)
	}
	hold = min(max(hold, 0), RateWindow)
	if until := now.Add(hold); until.After(l.holdUntil) {
		l.holdUntil = until
	}
	l.pushTAT(l.holdUntil.Add(time.Duration(burst-1) * every))
	return hold
}

func (l *Limiter) pushTAT(t time.Time) {
	if t.After(l.tat) {
		l.tat = t
	}
}

func headerInt(h http.Header, name string) (int, bool) {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// parseRetryAfter reads Retry-After as delay-seconds or an HTTP-date (RFC 9110,
// section 10.2.3), capping delay-seconds at RateWindow.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(min(n, int64(RateWindow/time.Second))) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now), true
	}
	return 0, false
}
