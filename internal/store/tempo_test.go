package store

import (
	"testing"
	"time"
)

func TestTempoCacheRoundTripAndPrune(t *testing.T) {
	s := open(t)
	now := time.UnixMilli(1_800_000_000_000)
	s.Now = func() time.Time { return now }
	if _, ok, err := s.TempoGet(ctx, "k1"); err != nil || ok {
		t.Fatalf("empty cache: %v %v", ok, err)
	}
	check(t, s.TempoPut(ctx, "k1", []byte(`{"bpm":174.02}`)))
	body, ok, err := s.TempoGet(ctx, "k1")
	check(t, err)
	if !ok || string(body) != `{"bpm":174.02}` {
		t.Fatalf("hit = %q %v", body, ok)
	}
	check(t, s.TempoPut(ctx, "k1", []byte(`{"bpm":87.01}`)))
	if body, _, _ = s.TempoGet(ctx, "k1"); string(body) != `{"bpm":87.01}` {
		t.Fatalf("overwrite = %q", body)
	}

	// Pruned by age, then by size, newest kept.
	s.TempoMaxAge, s.TempoMaxBytes = time.Hour, 30
	now = now.Add(2 * time.Hour)
	for _, k := range []string{"a", "b", "c"} {
		check(t, s.TempoPut(ctx, k, []byte(`{"bpm":120.00}`))) // 14 bytes
		now = now.Add(time.Second)
	}
	check(t, s.Prune(ctx))
	for k, want := range map[string]bool{"k1": false, "a": false, "b": true, "c": true} {
		if _, ok, _ := s.TempoGet(ctx, k); ok != want {
			t.Errorf("%s kept = %v", k, ok)
		}
	}
}
