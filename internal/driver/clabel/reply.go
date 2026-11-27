package clabel

import (
	"bytes"
	"regexp"
)

// ReplyKind says what a control frame from the printer is.
type ReplyKind uint8

// Reply kinds.
const (
	ReplyOther    ReplyKind = iota // a frame the driver does not use
	ReplyIdentity                  // the answer to IdentityQuery
	ReplyStatus                    // the status byte, sent unasked
	ReplySetting                   // the answer to a setting read or write
)

var replyKindNames = [...]string{"other", "identity", "status", "setting"}

// String names the kind, for tests and logs.
func (k ReplyKind) String() string {
	if int(k) < len(replyKindNames) {
		return replyKindNames[k]
	}
	return "unknown"
}

// Reply is a decoded control frame.
type Reply struct {
	Kind          ReplyKind
	Function, Sub byte

	// ReplySetting. HasValue is false when the printer answers without a value ("no
	// such setting").
	Setting  Setting
	Value    byte
	HasValue bool

	// ReplyStatus.
	Status StatusBits

	// ReplyIdentity: the printable text fields, in order, and the firmware version
	// found among them ("" when none looks like one).
	Fields   []string
	Firmware string
}

// StatusBits is the printer's status byte.
type StatusBits byte

// statusNames are the bits whose meaning is known.
var statusNames = []struct {
	bit  uint
	name string
}{{0, "lid-open"}, {2, "no-paper"}, {7, "head-hot"}}

// Conditions names the known bits that are set, lowest bit first.
func (s StatusBits) Conditions() []string {
	var out []string
	for _, b := range statusNames {
		if s>>b.bit&1 != 0 {
			out = append(out, b.name)
		}
	}
	return out
}

// versionPattern finds a firmware version such as "V1.0.2" or "V2".
var versionPattern = regexp.MustCompile(`\b[Vv]\d+(?:\.\d+)*\b`)

func textFields(content []byte) []string {
	var out []string
	for _, f := range bytes.FieldsFunc(content, func(r rune) bool { return r < 0x20 || r > 0x7e }) {
		out = append(out, string(f))
	}
	return out
}

func knownSetting(s Setting) bool { return s == SettingPaperType || s == SettingLanguage }

// Decode reads a control frame. Replies have answerBit set in sub, over the item they
// answer.
func Decode(f RawFrame) Reply {
	r := Reply{Function: f.Function, Sub: f.Sub}
	if f.Sub&answerBit == 0 {
		return r
	}
	item := f.Sub &^ answerBit
	switch {
	case f.Function == fnSettings && item == itemIdentity:
		r.Kind, r.Fields = ReplyIdentity, textFields(f.Content)
		for _, field := range r.Fields {
			if v := versionPattern.FindString(field); v != "" {
				r.Firmware = v
				break
			}
		}
	case f.Function == fnStatus && item == itemStatus && len(f.Content) == 1:
		r.Kind, r.Status = ReplyStatus, StatusBits(f.Content[0])
	case f.Function == fnSettings && knownSetting(Setting(item)) && len(f.Content) <= 1:
		r.Kind, r.Setting = ReplySetting, Setting(item)
		if len(f.Content) == 1 {
			r.Value, r.HasValue = f.Content[0], true
		}
	}
	return r
}
