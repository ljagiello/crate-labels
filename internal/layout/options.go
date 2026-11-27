package layout

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Option types of OptionSpec.Type.
const (
	OptionBool   = "bool"
	OptionInt    = "int"
	OptionFloat  = "float"
	OptionChoice = "choice"
	OptionText   = "text"
)

// OptionSpec is one user-adjustable template option, rendered as a form control by the UI.
// After CoerceOptions a value is a bool, int, float64 or string, per Type.
type OptionSpec struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Type         string   `json:"type"` // bool|int|float|choice|text
	Default      any      `json:"default"`
	Min          *float64 `json:"min"`
	Max          *float64 `json:"max"`
	Choices      []string `json:"choices"`
	ChoiceLabels []string `json:"choice_labels"` // same length as Choices, or empty
	Help         string   `json:"help"`
}

var (
	truthy = map[string]bool{"1": true, "true": true, "yes": true, "on": true, "y": true, "t": true}
	falsy  = map[string]bool{"0": true, "false": true, "no": true, "off": true, "n": true, "f": true, "": true}
)

// Coerce returns value as this option's type, clamped to Min/Max; the default when the
// value is unusable. Strings from forms and the CLI are converted.
func (o OptionSpec) Coerce(value any) any {
	switch o.Type {
	case OptionBool:
		switch v := value.(type) {
		case bool:
			return v
		case string:
			s := strings.ToLower(strings.TrimSpace(v))
			if truthy[s] {
				return true
			}
			if falsy[s] {
				return false
			}
		default:
			if n, ok := toFloat(value); ok {
				return n != 0
			}
		}
		return o.Default
	case OptionInt, OptionFloat:
		n, ok := toFloat(value)
		if !ok || math.IsNaN(n) {
			return o.Default
		}
		if o.Min != nil {
			n = math.Max(*o.Min, n)
		}
		if o.Max != nil {
			n = math.Min(*o.Max, n)
		}
		if math.IsInf(n, 0) {
			return o.Default
		}
		if o.Type == OptionInt {
			return int(math.RoundToEven(n))
		}
		return n
	case OptionChoice:
		text := ""
		if value != nil {
			text = fmt.Sprint(value)
		}
		for _, c := range o.Choices {
			if c == text {
				return c
			}
		}
		want := strings.ToLower(strings.TrimSpace(text))
		for _, c := range o.Choices {
			if strings.ToLower(c) == want {
				return c
			}
		}
		return o.Default
	case OptionText:
		switch v := value.(type) {
		case nil:
			return ""
		case string:
			return v
		default:
			return fmt.Sprint(v)
		}
	}
	return o.Default
}

// toFloat reads numbers as JSON, TOML, forms and Go callers send them.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// CoerceOptions returns a value for every option of t: the default, overridden by a usable
// value from given. Unknown keys are dropped.
func CoerceOptions(t Template, given map[string]any) map[string]any {
	out := map[string]any{}
	for _, spec := range t.Options() {
		if v, ok := given[spec.ID]; ok {
			out[spec.ID] = spec.Coerce(v)
		} else {
			out[spec.ID] = spec.Default
		}
	}
	return out
}

func floatPtr(v float64) *float64 { return &v }

func choiceOption(id, label, def string, choices, labels []string, help string) OptionSpec {
	if labels == nil {
		labels = []string{}
	}
	return OptionSpec{ID: id, Label: label, Type: OptionChoice, Default: def,
		Choices: choices, ChoiceLabels: labels, Help: help}
}

func boolOption(id, label string, def bool) OptionSpec {
	return OptionSpec{ID: id, Label: label, Type: OptionBool, Default: def,
		Choices: []string{}, ChoiceLabels: []string{}}
}

func textOption(id, label, help string) OptionSpec {
	return OptionSpec{ID: id, Label: label, Type: OptionText, Default: "",
		Choices: []string{}, ChoiceLabels: []string{}, Help: help}
}

func fontOption() OptionSpec {
	var ids, names []string
	for _, f := range fontFamilies {
		ids = append(ids, f.ID)
		names = append(names, f.Name)
	}
	return choiceOption("font", "Font", DefaultFontFamily, ids, names, "")
}

// Font scale bounds. Text always shrinks to fit its area, so the scale only moves the
// ceiling each element may grow to: below 0.8 the hierarchy flattens into one size,
// above 1.6 every element is already as large as its area allows.
const (
	fontScaleMin = 0.8
	fontScaleMax = 1.6
)

func fontScaleOption() OptionSpec {
	return OptionSpec{ID: "font_scale", Label: "Text size", Type: OptionFloat, Default: 1.0,
		Min: floatPtr(fontScaleMin), Max: floatPtr(fontScaleMax), Choices: []string{}, ChoiceLabels: []string{},
		Help: "How large text may grow; it still shrinks to fit."}
}

func qrOption(def bool) OptionSpec {
	return boolOption("show_qr", "QR code to the Discogs page", def)
}

func keyNotationOption() OptionSpec {
	return choiceOption("key_notation", "Key notation", KeyCamelot,
		[]string{KeyCamelot, KeyMusical, KeyBoth, KeyOff},
		[]string{"Camelot (8A)", "Musical (Am)", "Both (8A Am)", "Hidden"}, "")
}

// bpmOff is the bpm_range choice that prints tempos as detected.
const bpmOff = "off"

func bpmRangeOption() OptionSpec {
	choices := []string{bpmOff}
	labels := []string{"As detected"}
	for _, r := range BPMRanges {
		choices = append(choices, r.ID)
		labels = append(labels, r.Label+" BPM")
	}
	return choiceOption("bpm_range", "Tempo range", bpmOff, choices, labels,
		"Halves or doubles each tempo until it lands in the range.")
}

func trackOption() OptionSpec {
	return textOption("track", "Track", "A position such as B1, or a number. Blank picks the first included track.")
}

func djOptions(withTrack, qrDefault bool) []OptionSpec {
	opts := []OptionSpec{qrOption(qrDefault), keyNotationOption(), bpmRangeOption()}
	if withTrack {
		opts = append(opts, trackOption())
	}
	return append(opts, fontOption(), fontScaleOption())
}

// opts is typed read access to coerced options; missing or mistyped values give defaults.
type opts map[string]any

func (o opts) text(key, def string) string {
	if v, ok := o[key].(string); ok {
		return v
	}
	return def
}

func (o opts) number(key string, def float64) float64 {
	if _, isBool := o[key].(bool); isBool {
		return def
	}
	if _, isStr := o[key].(string); isStr {
		return def
	}
	if n, ok := toFloat(o[key]); ok {
		return n
	}
	return def
}

func (o opts) integer(key string, def int) int {
	switch v := o[key].(type) {
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return def
		}
		return n
	case bool:
		return def
	}
	if n, ok := toFloat(o[key]); ok {
		return int(n)
	}
	return def
}

func (o opts) flag(key string, def bool) bool {
	if v, ok := o[key].(bool); ok {
		return v
	}
	return def
}

func (o opts) faces() faces { return familyFaces(o.text("font", DefaultFontFamily)) }

// userScale is the font_scale option, clamped to its bounds.
func (o opts) userScale() float64 {
	return math.Min(fontScaleMax, math.Max(fontScaleMin, o.number("font_scale", 1)))
}

// bpmLow is the low end of the tempo window (0 = print as detected).
func (o opts) bpmLow() int { return bpmRangeLow(o.text("bpm_range", bpmOff)) }

func (o opts) notation() string { return o.text("key_notation", KeyCamelot) }

// qrLink is the URL the QR encodes, or "" when there is none or it is switched off.
func qrLink(d *LabelData, o opts) string {
	if !o.flag("show_qr", true) {
		return ""
	}
	return strings.TrimSpace(d.QRURL)
}
