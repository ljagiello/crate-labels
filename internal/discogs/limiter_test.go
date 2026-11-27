package discogs

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"sync"
	"testing"
	"time"
)

// The bucket's promise: however requests arrive, no 60 s window holds more starts than
// the limit, for the documented limits and for whatever Discogs advertises.
func TestNoWindowEverHoldsMoreThanTheLimit(t *testing.T) {
	for _, c := range []struct {
		name       string
		auth       bool
		advertised string
		limit      int
	}{
		{"authenticated", true, "", AuthenticatedPerMinute},
		{"anonymous", false, "", AnonymousPerMinute},
		{"advertised 10", true, "10", 10},
		{"advertised 2", true, "2", 2},
		{"advertised 240", false, "240", 240},
	} {
		t.Run(c.name, func(t *testing.T) {
			clock := newFakeClock()
			l := testLimiter(clock)
			if c.advertised != "" {
				l.Observe(http.Header{"X-Discogs-Ratelimit": {c.advertised}}, 200, c.auth)
			}
			if got := l.Limit(c.auth); got != c.limit {
				t.Fatalf("limit = %d, want %d", got, c.limit)
			}
			rng := rand.New(rand.NewPCG(1, uint64(c.limit)))
			var starts []time.Time
			for range 6 * c.limit {
				// Mostly back to back, sometimes after a pause long enough to refill.
				if rng.IntN(8) == 0 {
					_ = clock.Sleep(ctx, time.Duration(rng.Int64N(int64(2*RateWindow))))
				}
				if err := l.Wait(ctx, c.auth); err != nil {
					t.Fatal(err)
				}
				starts = append(starts, clock.Now())
			}
			for i, s := range starts {
				n := 0
				for _, u := range starts[i:] {
					if u.Sub(s) < RateWindow {
						n++
					}
				}
				if n > c.limit {
					t.Fatalf("%d starts in the 60 s from start %d, limit %d", n, i, c.limit)
				}
			}
		})
	}
}

func TestRemainingHeaderOnlyLowersTheBudget(t *testing.T) {
	every := interval(AuthenticatedPerMinute)
	for _, c := range []struct {
		remaining string
		free      int // requests that start without waiting after the response
	}{{"0", 0}, {"1", 0}, {"2", 1}, {"3", 2}, {"50", DefaultBurst}, {"junk", DefaultBurst}} {
		clock := newFakeClock()
		l := testLimiter(clock)
		l.Observe(http.Header{"X-Discogs-Ratelimit-Remaining": {c.remaining}}, 200, true)
		for range c.free {
			_ = l.Wait(ctx, true)
		}
		if s := clock.Sleeps(); len(s) != 0 {
			t.Errorf("remaining %s: the first %d waited: %v", c.remaining, c.free, s)
		}
		_ = l.Wait(ctx, true)
		if s := clock.Sleeps(); len(s) != 1 || s[0] != every {
			t.Errorf("remaining %s: next request slept %v, want [%v]", c.remaining, s, every)
		}
	}
}

func TestAdvertisedLimitReshapesTheBucketPerKind(t *testing.T) {
	clock := newFakeClock()
	l := testLimiter(clock)
	l.Observe(http.Header{"X-Discogs-Ratelimit": {"30"}}, 200, true)
	if l.Limit(true) != 30 || l.Limit(false) != AnonymousPerMinute {
		t.Fatalf("limits = %d, %d", l.Limit(true), l.Limit(false))
	}
	for _, bad := range []string{"0", "1", "-5", "abc", "100000"} {
		l.Observe(http.Header{"X-Discogs-Ratelimit": {bad}}, 200, true)
	}
	if l.Limit(true) != 30 {
		t.Fatalf("an implausible limit was taken: %d", l.Limit(true))
	}
	for range DefaultBurst + 1 {
		_ = l.Wait(ctx, true)
	}
	if s := clock.Sleeps(); len(s) != 1 || s[0] != interval(30) {
		t.Errorf("sleeps = %v, want [%v]", s, interval(30))
	}
}

func TestA429HoldEndsWithOneTokenThenPaces(t *testing.T) {
	clock := newFakeClock()
	l := testLimiter(clock)
	if h := l.Observe(http.Header{"Retry-After": {"7"}}, http.StatusTooManyRequests, true); h != 7*time.Second {
		t.Fatalf("hold = %v", h)
	}
	_ = l.Wait(ctx, true)
	_ = l.Wait(ctx, true)
	every := interval(AuthenticatedPerMinute)
	if s := clock.Sleeps(); len(s) != 2 || s[0] != 7*time.Second || s[1] != every {
		t.Errorf("sleeps = %v, want [7s %v]", s, every)
	}
}

func TestBackoffDoublesToTheWindowWithJitter(t *testing.T) {
	clock := newFakeClock()
	l := testLimiter(clock)
	var asked []time.Duration
	l.Jitter = func(d time.Duration) time.Duration { asked = append(asked, d); return d - 1 }
	var holds []time.Duration
	for range 7 {
		holds = append(holds, l.Observe(http.Header{}, http.StatusTooManyRequests, true))
	}
	want := []time.Duration{6 * time.Second, 12 * time.Second, 24 * time.Second, 48 * time.Second,
		RateWindow, RateWindow, RateWindow}
	for i := range want {
		if i < 4 {
			want[i] -= time.Nanosecond // the largest jitter: just under a fifth
		}
		if holds[i] != want[i] {
			t.Errorf("hold %d = %v, want %v", i+1, holds[i], want[i])
		}
	}
	if asked[0] != time.Second || asked[4] != 16*time.Second {
		t.Errorf("jitter ranges = %v", asked)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		v    string
		want time.Duration
		ok   bool
	}{
		{"5", 5 * time.Second, true},
		{" 0 ", 0, true},
		{"9999", RateWindow, true},
		{"Thu, 26 Nov 2026 12:00:30 GMT", 30 * time.Second, true},
		{"", 0, false},
		{"-1", 0, false},
		{"1.5", 0, false},
		{"soon", 0, false},
	} {
		got, ok := parseRetryAfter(c.v, now)
		if got != c.want || ok != c.ok {
			t.Errorf("parseRetryAfter(%q) = %v, %v; want %v, %v", c.v, got, ok, c.want, c.ok)
		}
	}
	// An HTTP-date in the past holds nothing; one far ahead is capped at the window.
	l := testLimiter(newFakeClock())
	l.Now = func() time.Time { return now }
	if h := l.Observe(http.Header{"Retry-After": {"Mon, 02 Jan 2006 15:04:05 GMT"}}, 429, true); h != 0 {
		t.Errorf("past date hold = %v", h)
	}
	if h := l.Observe(http.Header{"Retry-After": {"Fri, 01 Jan 2100 00:00:00 GMT"}}, 429, true); h != RateWindow {
		t.Errorf("far date hold = %v", h)
	}
}

func TestCancelledWaitReturnsAndTheLimiterStaysUsable(t *testing.T) {
	clock := newFakeClock()
	l := &Limiter{Burst: 1, Now: clock.Now, Sleep: func(ctx context.Context, d time.Duration) error {
		<-ctx.Done() // never wakes on its own
		return ctx.Err()
	}}
	l.Observe(http.Header{"Retry-After": {"30"}}, 429, true)
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- l.Wait(cctx, true) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	_ = clock.Sleep(ctx, 31*time.Second) // past the hold: the next wait needs no sleep
	if err := l.Wait(ctx, true); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
}

func TestNilLimiterLetsEverythingThrough(t *testing.T) {
	var l *Limiter
	if err := l.Wait(ctx, true); err != nil || l.HeldFor() != 0 || l.Observe(http.Header{}, 429, true) != 0 || l.Limit(true) != 0 {
		t.Fatal("a nil limiter must be a no-op")
	}
}

// Concurrent callers on the real clock with a fast bucket: nothing races or wedges.
func TestConcurrentWaiters(t *testing.T) {
	l := &Limiter{Burst: 4}
	for _, auth := range []bool{true, false} {
		l.Observe(http.Header{"X-Discogs-Ratelimit": {"6000"}}, 200, auth)
	}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Wait(ctx, i%2 == 0); err != nil {
				t.Error(err)
			}
			l.Observe(http.Header{"X-Discogs-Ratelimit-Remaining": {"5000"}}, 200, i%2 == 0)
		}()
	}
	wg.Wait()
}
