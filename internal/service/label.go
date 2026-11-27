package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/layout"
)

// Track is a tracklist row as it travels over HTTP (API.md Track).
type Track struct {
	Position string  `json:"position"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Duration string  `json:"duration"`
	BPM      *int    `json:"bpm"`
	Key      *string `json:"key"`
	Included bool    `json:"included"`
}

// Label is LabelData as it travels over HTTP and is stored in jobs, crates and history
// (API.md LabelData). Unknown numbers are null on the wire; layout.LabelData uses 0 and "".
type Label struct {
	Artist    string   `json:"artist"`
	Title     string   `json:"title"`
	Label     string   `json:"label"`
	Catno     string   `json:"catno"`
	Year      string   `json:"year"`
	Country   string   `json:"country"`
	Format    string   `json:"format"`
	Genres    []string `json:"genres"`
	Styles    []string `json:"styles"`
	Tracks    []Track  `json:"tracks"`
	Notes     string   `json:"notes"`
	Price     string   `json:"price"`
	Condition string   `json:"condition"`
	QRURL     string   `json:"qr_url"`
	ReleaseID *int64   `json:"release_id"`
}

// RID is the release id, or 0.
func (l Label) RID() int64 {
	if l.ReleaseID == nil {
		return 0
	}
	return *l.ReleaseID
}

// normalized gives nil slices their empty JSON form.
func (l Label) normalized() Label {
	if l.Genres == nil {
		l.Genres = []string{}
	}
	if l.Styles == nil {
		l.Styles = []string{}
	}
	if l.Tracks == nil {
		l.Tracks = []Track{}
	}
	return l
}

// MarshalJSON writes the canonical form: every key, empty lists as [].
func (l Label) MarshalJSON() ([]byte, error) {
	type plain Label
	return json.Marshal(plain(l.normalized()))
}

// JSON is the canonical JSON of l, used for snapshots and history de-duplication.
func (l Label) JSON() json.RawMessage {
	b, _ := json.Marshal(l)
	return b
}

// ToLayout converts to what templates render.
func (l Label) ToLayout() layout.LabelData {
	l = l.normalized()
	d := layout.LabelData{
		Artist: l.Artist, Title: l.Title, Label: l.Label, Catno: l.Catno, Year: l.Year, Country: l.Country,
		Format: l.Format, Genres: append([]string{}, l.Genres...), Styles: append([]string{}, l.Styles...),
		Notes: l.Notes, Price: l.Price, Condition: l.Condition, QRURL: l.QRURL, ReleaseID: int(l.RID()),
		Tracks: make([]layout.Track, 0, len(l.Tracks)),
	}
	for _, t := range l.Tracks {
		lt := layout.Track{Position: t.Position, Title: t.Title, Artist: t.Artist, Duration: t.Duration, Included: t.Included}
		if t.BPM != nil {
			lt.BPM = *t.BPM
		}
		if t.Key != nil {
			lt.Key = *t.Key
		}
		d.Tracks = append(d.Tracks, lt)
	}
	return d
}

// FromLayout converts template data to the wire form.
func FromLayout(d layout.LabelData) Label {
	l := Label{
		Artist: d.Artist, Title: d.Title, Label: d.Label, Catno: d.Catno, Year: d.Year, Country: d.Country,
		Format: d.Format, Genres: append([]string{}, d.Genres...), Styles: append([]string{}, d.Styles...),
		Notes: d.Notes, Price: d.Price, Condition: d.Condition, QRURL: d.QRURL, Tracks: []Track{},
	}
	if d.ReleaseID > 0 {
		id := int64(d.ReleaseID)
		l.ReleaseID = &id
	}
	for _, t := range d.Tracks {
		wt := Track{Position: t.Position, Title: t.Title, Artist: t.Artist, Duration: t.Duration, Included: t.Included}
		if t.BPM > 0 {
			bpm := t.BPM
			wt.BPM = &bpm
		}
		if t.Key != "" {
			key := t.Key
			wt.Key = &key
		}
		l.Tracks = append(l.Tracks, wt)
	}
	return l
}

// FieldError says a LabelData field has a type that cannot be read (HTTP 422).
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

func isNull(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || string(raw) == "null"
}

// lenientText reads a string, number or bool as text; null is "".
func lenientText(raw json.RawMessage, field string) (string, error) {
	raw = bytes.TrimSpace(raw)
	if isNull(raw) {
		return "", nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", &FieldError{field, "not a string"}
		}
		return s, nil
	case '{', '[':
		return "", &FieldError{field, "must be text"}
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return strconv.FormatBool(b), nil
	}
	return "", &FieldError{field, "must be text"}
}

// lenientList reads a list of strings, or a comma-separated string.
func lenientList(raw json.RawMessage, field string) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	out := []string{}
	if isNull(raw) {
		return out, nil
	}
	if raw[0] == '"' {
		s, err := lenientText(raw, field)
		if err != nil {
			return nil, err
		}
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &FieldError{field, "must be a list of strings"}
	}
	for i, it := range items {
		s, err := lenientText(it, fmt.Sprintf("%s.%d", field, i))
		if err != nil {
			return nil, err
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// lenientInt reads a number or a numeric string; null, "" and values ≤ 0 are nil.
func lenientInt(raw json.RawMessage, field string) (*int64, error) {
	raw = bytes.TrimSpace(raw)
	if isNull(raw) {
		return nil, nil
	}
	var f float64
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, &FieldError{field, "not a number"}
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, &FieldError{field, "must be a whole number"}
		}
		f = v
	} else if err := json.Unmarshal(raw, &f); err != nil {
		return nil, &FieldError{field, "must be a whole number"}
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return nil, &FieldError{field, "must be a whole number"}
	}
	if f <= 0 {
		return nil, nil
	}
	v := int64(f)
	return &v, nil
}

// lenientBPM reads a tempo: a number or numeric string, rounded half up to a whole BPM
// (the UI's number field may send 128.5). null, "" and values ≤ 0 are nil.
func lenientBPM(raw json.RawMessage) (*int, error) {
	raw = bytes.TrimSpace(raw)
	if isNull(raw) {
		return nil, nil
	}
	var f float64
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, &FieldError{"tracks.bpm", "must be a number"}
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, &FieldError{"tracks.bpm", "must be a number"}
		}
		f = v
	} else if err := json.Unmarshal(raw, &f); err != nil {
		return nil, &FieldError{"tracks.bpm", "must be a number"}
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f > 10000 {
		return nil, &FieldError{"tracks.bpm", "must be a number"}
	}
	n := int(math.Floor(f + 0.5))
	if n <= 0 {
		return nil, nil
	}
	return &n, nil
}

// UnmarshalJSON reads a track leniently: unknown keys are ignored, a missing or null
// "included" means true, and bpm/key may be null.
func (t *Track) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return &FieldError{"tracks", "each track must be an object"}
	}
	var out Track
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{"position", &out.Position}, {"title", &out.Title}, {"artist", &out.Artist}, {"duration", &out.Duration}} {
		if *f.dst, err = lenientText(raw[f.key], "tracks."+f.key); err != nil {
			return err
		}
	}
	if out.BPM, err = lenientBPM(raw["bpm"]); err != nil {
		return err
	}
	if !isNull(raw["key"]) {
		key, err := lenientText(raw["key"], "tracks.key")
		if err != nil {
			return err
		}
		if key = strings.TrimSpace(key); key != "" {
			out.Key = &key
		}
	}
	out.Included = true
	if inc := raw["included"]; !isNull(inc) {
		if err := json.Unmarshal(inc, &out.Included); err != nil {
			return &FieldError{"tracks.included", "must be true or false"}
		}
	}
	*t = out
	return nil
}

// UnmarshalJSON reads LabelData leniently (API.md): unknown keys are ignored, missing
// keys take their defaults, styles and genres may be a comma-separated string, and
// release_id may be a numeric string.
func (l *Label) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return &FieldError{"data", "must be an object"}
	}
	var out Label
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"artist", &out.Artist}, {"title", &out.Title}, {"label", &out.Label}, {"catno", &out.Catno},
		{"year", &out.Year}, {"country", &out.Country}, {"format", &out.Format}, {"notes", &out.Notes},
		{"price", &out.Price}, {"condition", &out.Condition}, {"qr_url", &out.QRURL},
	} {
		if *f.dst, err = lenientText(raw[f.key], f.key); err != nil {
			return err
		}
	}
	if out.Genres, err = lenientList(raw["genres"], "genres"); err != nil {
		return err
	}
	if out.Styles, err = lenientList(raw["styles"], "styles"); err != nil {
		return err
	}
	out.Tracks = []Track{}
	if tr := raw["tracks"]; !isNull(tr) {
		if bytes.TrimSpace(tr)[0] != '[' {
			return &FieldError{"tracks", "must be a list"}
		}
		if err := json.Unmarshal(tr, &out.Tracks); err != nil {
			return err
		}
	}
	if out.ReleaseID, err = lenientInt(raw["release_id"], "release_id"); err != nil {
		return err
	}
	*l = out
	return nil
}

// ParseLabel decodes stored or posted LabelData.
func ParseLabel(raw json.RawMessage) (Label, error) {
	var l Label
	if isNull(raw) {
		return Label{}.normalized(), nil
	}
	err := json.Unmarshal(raw, &l)
	return l.normalized(), err
}
