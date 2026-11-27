package printer

import (
	"fmt"
	"maps"
)

// Settings are tunables (darkness, paper_align, offsets…) merged from model defaults,
// per-printer calibration and per-job options. Values come from TOML or JSON, so
// numbers may arrive as int64 or float64; use the typed getters.
type Settings map[string]any

// Merge returns a new map with later layers overriding earlier ones.
func Merge(layers ...Settings) Settings {
	out := Settings{}
	for _, l := range layers {
		maps.Copy(out, l)
	}
	return out
}

// Int returns key as an int, or def when missing or not a number.
func (s Settings) Int(key string, def int) int {
	switch v := s[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

// Float returns key as a float64, or def when missing or not a number.
func (s Settings) Float(key string, def float64) float64 {
	switch v := s[key].(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return def
}

// String returns key as a string, or def when missing.
func (s Settings) String(key, def string) string {
	if v, ok := s[key]; ok {
		if str, ok := v.(string); ok {
			return str
		}
		return fmt.Sprint(v)
	}
	return def
}

// Bool returns key as a bool, or def when missing or not a bool.
func (s Settings) Bool(key string, def bool) bool {
	if v, ok := s[key].(bool); ok {
		return v
	}
	return def
}

// Map returns a nested table (e.g. options.handshake), or nil.
func (s Settings) Map(key string) map[string]any {
	if v, ok := s[key].(map[string]any); ok {
		return v
	}
	return nil
}
