package phomemo

import (
	"encoding/hex"
	"slices"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// The printer sends one notification as soon as the host subscribes to it: its hello.
// Each model lists the hello frames it is known to send, as hex, under the
// hello_frames option. Several models may send the same frame.

// sendsHello reports whether m lists frame among its hello frames.
func sendsHello(m *printer.Model, frame []byte) bool {
	if len(frame) == 0 {
		return false
	}
	want := hex.EncodeToString(frame)
	return slices.ContainsFunc(stringList(m.Options[SettingHelloFrames]), func(s string) bool {
		return strings.EqualFold(strings.TrimSpace(s), want)
	})
}

// stringList reads a TOML or JSON list of strings; anything else is empty.
func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		var out []string
		for _, e := range l {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func namedBy(m *printer.Model, name string) bool {
	return name != "" && m.BLE != nil && m.BLE.MatchesName(name)
}

// IdentifyModel names a printer's model from what it showed when it connected: its
// hello frame and its advertised name. models are the family's models in catalog
// order.
//
// The model saved for the printer (known) wins when it is one of models; neither the
// hello nor the name overrides a choice the user made. Without one, the first model
// whose name pattern matches decides. Failing that, the hello decides when exactly one
// model sends it. Anything else leaves the printer unidentified (nil), and the user
// picks.
func IdentifyModel(hello []byte, name string, known *printer.Model, models []*printer.Model) *printer.Model {
	if known != nil && slices.ContainsFunc(models, func(m *printer.Model) bool { return m.ID == known.ID }) {
		return known
	}
	if i := slices.IndexFunc(models, func(m *printer.Model) bool { return namedBy(m, name) }); i >= 0 {
		return models[i]
	}
	var senders []*printer.Model
	for _, m := range models {
		if sendsHello(m, hello) {
			senders = append(senders, m)
		}
	}
	if len(senders) == 1 {
		return senders[0]
	}
	return nil
}

// otherSuspects lists, in catalog order, the models the hello frame or the advertised
// name points to when that evidence leaves out the configured model. It is empty when
// the evidence agrees with the configuration or there is none.
func otherSuspects(hello []byte, name string, configured *printer.Model, models []*printer.Model) []string {
	pointsElsewhere := func(match func(*printer.Model) bool) []*printer.Model {
		hits := slices.DeleteFunc(slices.Clone(models), func(m *printer.Model) bool { return !match(m) })
		if slices.ContainsFunc(hits, func(m *printer.Model) bool { return m.ID == configured.ID }) {
			return nil
		}
		return hits
	}
	byHello := pointsElsewhere(func(m *printer.Model) bool { return sendsHello(m, hello) })
	byName := pointsElsewhere(func(m *printer.Model) bool { return namedBy(m, name) })
	var out []string
	for _, m := range models {
		if slices.Contains(byHello, m) || slices.Contains(byName, m) {
			out = append(out, m.ID)
		}
	}
	return out
}
