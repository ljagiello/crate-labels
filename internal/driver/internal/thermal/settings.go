// Package thermal holds the calibration and message helpers that the thermal drivers
// share: typed, validated settings, the total rotation and the offset rule, so every
// family reads extra_rotate_cw and offset_dots the same way.
package thermal

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// number reads key as a float64. ok is false when the key is missing or null.
func number(s printer.Settings, key string) (v float64, ok bool, err error) {
	raw, present := s[key]
	if !present || raw == nil {
		return 0, false, nil
	}
	switch x := raw.(type) {
	case int:
		return float64(x), true, nil
	case int64:
		return float64(x), true, nil
	case float64:
		return x, true, nil
	case string:
		if f, perr := strconv.ParseFloat(strings.TrimSpace(x), 64); perr == nil {
			return f, true, nil
		}
	}
	return 0, false, printer.Errorf("Setting %q must be a number, not %v.", key, raw)
}

// Int returns key as a whole number in [lo, hi], or def when it is not set.
func Int(s printer.Settings, key string, def, lo, hi int) (int, error) {
	v, ok, err := number(s, key)
	if err != nil || !ok {
		return def, err
	}
	if v != math.Trunc(v) || v < float64(lo) || v > float64(hi) {
		return 0, printer.Errorf("Setting %q must be a whole number from %d to %d, not %g.", key, lo, hi, v)
	}
	return int(v), nil
}

// ClampedInt returns key as a whole number clamped into [lo, hi], or def when it is not
// set. Out-of-range values are clamped rather than rejected; only a value that is not a
// whole number is an error.
func ClampedInt(s printer.Settings, key string, def, lo, hi int) (int, error) {
	v, ok, err := number(s, key)
	if err != nil || !ok {
		return def, err
	}
	if v != math.Trunc(v) {
		return 0, printer.Errorf("Setting %q must be a whole number, not %g.", key, v)
	}
	return int(math.Max(float64(lo), math.Min(float64(hi), v))), nil
}

// OffsetDots is the offset_dots calibration clamped into [lo, hi], or def when unset.
// Munbyn and Clabel pass [0, head]; a family that allows negative offsets passes its
// own lower bound.
func OffsetDots(s printer.Settings, def, lo, hi int) (int, error) {
	return ClampedInt(s, "offset_dots", def, lo, hi)
}

// Float returns key as a number in [lo, hi], or def when it is not set.
func Float(s printer.Settings, key string, def, lo, hi float64) (float64, error) {
	v, ok, err := number(s, key)
	if err != nil || !ok {
		return def, err
	}
	if v < lo || v > hi {
		return 0, printer.Errorf("Setting %q must be from %g to %g, not %g.", key, lo, hi, v)
	}
	return v, nil
}

// ExtraRotation is the extra_rotate_cw calibration: 0 when unset, else exactly 0, 90,
// 180 or 270. Anything else is a user error.
func ExtraRotation(s printer.Settings) (int, error) {
	v, ok, err := number(s, "extra_rotate_cw")
	if err != nil || !ok {
		return 0, err
	}
	switch v {
	case 0, 90, 180, 270:
		return int(v), nil
	}
	return 0, printer.Errorf("Setting \"extra_rotate_cw\" must be 0, 90, 180 or 270, not %g.", v)
}

// Rotation is the total clockwise turn: the stock's own plus the extra_rotate_cw
// calibration.
func Rotation(stock *printer.Stock, s printer.Settings) (int, error) {
	extra, err := ExtraRotation(s)
	if err != nil {
		return 0, err
	}
	return (stock.RotateCW + extra) % 360, nil
}

// AcrossMM is the printable design width across the head, in mm.
func AcrossMM(stock *printer.Stock) float64 {
	if stock.RotateCW == 90 || stock.RotateCW == 270 {
		return stock.DesignMM[1]
	}
	return stock.DesignMM[0]
}

// IsContinuous reports whether the stock has no fixed length along feed.
func IsContinuous(stock *printer.Stock) bool {
	return stock.Media == printer.MediaContinuous || stock.FeedMM[1] <= 0
}

// Labels counts labels for a message: "1 label", "3 labels".
func Labels(n int) string {
	if n == 1 {
		return "1 label"
	}
	return fmt.Sprintf("%d labels", n)
}
