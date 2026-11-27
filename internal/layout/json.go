package layout

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// JSON forms of Track and LabelData, per docs/API.md. Output uses null for an unknown
// bpm, key or release id and [] for empty lists. Input is lenient: unknown keys are
// ignored; missing keys take their defaults ("included" defaults to true); "styles" and
// "genres" may be a comma-separated string; "release_id", "bpm" and "year" may be numbers
// or numeric strings; a fractional bpm is rounded.

type trackJSON struct {
	Position string `json:"position"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Duration string `json:"duration"`
	BPM      any    `json:"bpm"`
	Key      any    `json:"key"`
	Included any    `json:"included"`
}

// MarshalJSON writes bpm 0 and an empty key as null.
func (t Track) MarshalJSON() ([]byte, error) {
	w := trackJSON{Position: t.Position, Title: t.Title, Artist: t.Artist, Duration: t.Duration,
		Included: t.Included}
	if t.BPM > 0 {
		w.BPM = t.BPM
	}
	if t.Key != "" {
		w.Key = t.Key
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads a track leniently; a missing or null "included" means true.
func (t *Track) UnmarshalJSON(data []byte) error {
	var w trackJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*t = Track{Position: w.Position, Title: w.Title, Artist: w.Artist, Duration: w.Duration,
		Key: looseString(w.Key), Included: true}
	if bpm, ok := looseNumber(w.BPM); ok && bpm > 0 {
		t.BPM = int(math.Floor(bpm + 0.5))
	}
	if w.Included != nil {
		t.Included = boolOption("included", "", true).Coerce(w.Included).(bool)
	}
	return nil
}

type labelDataJSON struct {
	Artist    string  `json:"artist"`
	Title     string  `json:"title"`
	Label     string  `json:"label"`
	Catno     string  `json:"catno"`
	Year      any     `json:"year"`
	Country   string  `json:"country"`
	Format    string  `json:"format"`
	Genres    any     `json:"genres"`
	Styles    any     `json:"styles"`
	Tracks    []Track `json:"tracks"`
	Notes     string  `json:"notes"`
	Price     string  `json:"price"`
	Condition string  `json:"condition"`
	QRURL     string  `json:"qr_url"`
	ReleaseID any     `json:"release_id"`
}

// MarshalJSON writes release id 0 as null and nil lists as [].
func (d LabelData) MarshalJSON() ([]byte, error) {
	w := labelDataJSON{Artist: d.Artist, Title: d.Title, Label: d.Label, Catno: d.Catno,
		Year: d.Year, Country: d.Country, Format: d.Format, Genres: nonNil(d.Genres),
		Styles: nonNil(d.Styles), Tracks: d.Tracks, Notes: d.Notes, Price: d.Price,
		Condition: d.Condition, QRURL: d.QRURL}
	if w.Tracks == nil {
		w.Tracks = []Track{}
	}
	if d.ReleaseID > 0 {
		w.ReleaseID = d.ReleaseID
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads a label leniently (see the package's JSON notes).
func (d *LabelData) UnmarshalJSON(data []byte) error {
	var w labelDataJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*d = LabelData{Artist: w.Artist, Title: w.Title, Label: w.Label, Catno: w.Catno,
		Year: looseString(w.Year), Country: w.Country, Format: w.Format,
		Genres: looseList(w.Genres), Styles: looseList(w.Styles), Tracks: w.Tracks,
		Notes: w.Notes, Price: w.Price, Condition: w.Condition, QRURL: w.QRURL}
	if id, ok := looseNumber(w.ReleaseID); ok && id > 0 && id < 1<<53 {
		d.ReleaseID = int(id)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// looseNumber reads a JSON number or numeric string.
func looseNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// looseString reads a JSON string or number as text; null is "".
func looseString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	}
	return ""
}

// looseList reads a JSON array of strings or a comma-separated string.
func looseList(v any) []string {
	switch l := v.(type) {
	case string:
		var out []string
		for _, p := range strings.Split(l, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range l {
			if s := strings.TrimSpace(looseString(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
