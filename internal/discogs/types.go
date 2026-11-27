package discogs

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Int is a JSON integer that also accepts a numeric string, a float with no fraction,
// or null (0). Discogs is mostly consistent, but one odd field must not sink a release.
type Int int64

// UnmarshalJSON implements json.Unmarshaler.
func (n *Int) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			*n = 0
			return nil
		}
		*n = Int(v)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		*n = 0
		return nil
	}
	if f != math.Trunc(f) {
		f = math.Trunc(f)
	}
	*n = Int(f)
	return nil
}

// Text is a JSON string that also accepts a number, a bool or null ("").
type Text string

// UnmarshalJSON implements json.Unmarshaler.
func (t *Text) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		*t = ""
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = Text(s)
	default:
		*t = Text(string(b))
	}
	return nil
}

// Strings is a JSON array of strings that tolerates null and non-string entries.
type Strings []string

// UnmarshalJSON implements json.Unmarshaler.
func (s *Strings) UnmarshalJSON(b []byte) error {
	var raw []Text
	if err := json.Unmarshal(b, &raw); err != nil {
		*s = nil
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, string(r))
	}
	*s = out
	return nil
}

// Artist is a credited artist (release `artists[]` or track `artists[]`).
type Artist struct {
	ID   Int    `json:"id"`
	Name string `json:"name"`
	ANV  string `json:"anv"`
	Join string `json:"join"`
}

// Label is a release label with its catalogue number.
type Label struct {
	Name  string `json:"name"`
	Catno Text   `json:"catno"`
}

// Format is one `formats[]` entry, e.g. {name: "Vinyl", qty: "1", descriptions: ["12\""]}.
type Format struct {
	Name         string  `json:"name"`
	Qty          Text    `json:"qty"`
	Descriptions Strings `json:"descriptions"`
}

// TrackEntry is one `tracklist[]` row. type_ is "track", "heading" or "index"; an index
// row holds its pieces in sub_tracks.
type TrackEntry struct {
	Position  string       `json:"position"`
	Type      string       `json:"type_"`
	Title     string       `json:"title"`
	Duration  string       `json:"duration"`
	Artists   []Artist     `json:"artists"`
	SubTracks []TrackEntry `json:"sub_tracks"`
}

// Image is one `images[]` entry.
type Image struct {
	Type string `json:"type"`
	URI  string `json:"uri"`
}

// Release is the part of `GET /releases/{id}` crate-labels reads.
type Release struct {
	ID         Int          `json:"id"`
	MasterID   Int          `json:"master_id"`
	Title      string       `json:"title"`
	Year       Int          `json:"year"`
	Country    string       `json:"country"`
	URI        string       `json:"uri"`
	Thumb      string       `json:"thumb"`
	CoverImage string       `json:"cover_image"`
	Artists    []Artist     `json:"artists"`
	Labels     []Label      `json:"labels"`
	Formats    []Format     `json:"formats"`
	Genres     Strings      `json:"genres"`
	Styles     Strings      `json:"styles"`
	Tracklist  []TrackEntry `json:"tracklist"`
	Images     []Image      `json:"images"`
	Videos     []Video      `json:"videos"`
}

// Video is one `videos[]` entry: a link (usually YouTube) someone attached to the release.
type Video struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Duration    Int    `json:"duration"` // seconds, 0 = unknown
}

// Master is the part of `GET /masters/{id}` crate-labels reads.
type Master struct {
	ID          Int    `json:"id"`
	MainRelease Int    `json:"main_release"`
	Title       string `json:"title"`
}

// Identity is `GET /oauth/identity`.
type Identity struct {
	ID       Int    `json:"id"`
	Username string `json:"username"`
}

// SearchResult is one `GET /database/search` result.
type SearchResult struct {
	ID         Int     `json:"id"`
	Type       string  `json:"type"`
	MasterID   Int     `json:"master_id"`
	Title      string  `json:"title"`
	Year       Text    `json:"year"`
	Country    string  `json:"country"`
	Format     Strings `json:"format"`
	Label      Strings `json:"label"`
	Catno      Text    `json:"catno"`
	Thumb      string  `json:"thumb"`
	CoverImage string  `json:"cover_image"`
	Barcode    Strings `json:"barcode"`
}

// SearchPage is a page of search results.
type SearchPage struct {
	Pagination json.RawMessage `json:"pagination"`
	Results    []SearchResult  `json:"results"`
}

// BasicInformation is the release summary inside a collection or wantlist row.
type BasicInformation struct {
	ID         Int      `json:"id"`
	MasterID   Int      `json:"master_id"`
	Title      string   `json:"title"`
	Year       Int      `json:"year"`
	Thumb      string   `json:"thumb"`
	CoverImage string   `json:"cover_image"`
	Formats    []Format `json:"formats"`
	Labels     []Label  `json:"labels"`
	Artists    []Artist `json:"artists"`
	Genres     Strings  `json:"genres"`
	Styles     Strings  `json:"styles"`
}

// Note is a collection custom-field value.
type Note struct {
	FieldID Int  `json:"field_id"`
	Value   Text `json:"value"`
}

// CollectionRow is one collection (`releases[]`) or wantlist (`wants[]`) row.
type CollectionRow struct {
	ID               Int              `json:"id"`
	InstanceID       Int              `json:"instance_id"`
	FolderID         Int              `json:"folder_id"`
	DateAdded        string           `json:"date_added"`
	Rating           Int              `json:"rating"`
	Notes            []Note           `json:"notes"`
	BasicInformation BasicInformation `json:"basic_information"`
}

// CollectionPage is a page of the collection or the wantlist.
type CollectionPage struct {
	Pagination json.RawMessage `json:"pagination"`
	Releases   []CollectionRow `json:"releases"`
	Wants      []CollectionRow `json:"wants"`
}

// Folder is a collection folder.
type Folder struct {
	ID    Int    `json:"id"`
	Name  string `json:"name"`
	Count Int    `json:"count"`
}
