package service

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// calibrationKeys are the per-printer settings the UI edits, with their types as the
// API reports them. Drivers read them from printer.Settings.
var calibrationKeys = map[string]string{
	"extra_rotate_cw": "int",   // 0/90/180/270 on top of the stock's rotation
	"offset_dots":     "int",   // shift across the head, in dots
	"darkness":        "int",   // Phomemo, Munbyn
	"paper_align":     "str",   // Phomemo: where narrow paper sits on the head, "right" or "center"
	"ble_gap_ms":      "float", // pause between BLE chunks
	"speed":           "int",   // Clabel
	"density":         "int",   // Clabel's darkness (0-15)
}

func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// CleanCalibration validates calibration settings: empty and null values are dropped,
// known keys are coerced to their type and checked, and unknown keys pass through.
func CleanCalibration(in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for key, value := range in {
		if value == nil {
			continue
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		kind, known := calibrationKeys[key]
		if !known {
			out[key] = value
			continue
		}
		switch kind {
		case "int":
			f, ok := toNumber(value)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, Errorf("%s must be a whole number.", key)
			}
			if s, isStr := value.(string); isStr && strings.ContainsAny(s, ".eE") {
				return nil, Errorf("%s must be a whole number.", key)
			}
			n := int(math.Trunc(f))
			if key == "extra_rotate_cw" && !slices.Contains([]int{0, 90, 180, 270}, n) {
				return nil, Errorf("extra_rotate_cw must be 0, 90, 180 or 270.")
			}
			out[key] = n
		case "float":
			f, ok := toNumber(value)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, Errorf("%s must be a number.", key)
			}
			if key == "ble_gap_ms" && (f < 0 || f > 1000) {
				return nil, Errorf("ble_gap_ms must be between 0 and 1000.")
			}
			out[key] = f
		case "str":
			s, ok := value.(string)
			if !ok {
				s = fmt.Sprint(value)
			}
			s = strings.TrimSpace(s)
			if key == "paper_align" && s != "right" && s != "center" {
				return nil, Errorf("paper_align must be right or center.")
			}
			out[key] = s
		}
	}
	return out, nil
}

// CalibrationKeys returns the calibration keys and their types ("int", "float", "str").
func CalibrationKeys() map[string]string {
	out := make(map[string]string, len(calibrationKeys))
	for k, v := range calibrationKeys {
		out[k] = v
	}
	return out
}
