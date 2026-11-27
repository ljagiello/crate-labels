package discogs

import (
	"regexp"
	"strconv"
	"strings"
)

// disambiguator is the " (n)" Discogs appends to artist and label names that are shared
// by several entities.
var disambiguator = regexp.MustCompile(`\s*\(\d+\)\s*$`)

// StripDisambiguator turns "Paul Bradley (10)" into "Paul Bradley".
func StripDisambiguator(name string) string {
	return strings.TrimSpace(disambiguator.ReplaceAllString(name, ""))
}

func artistName(a Artist, preferANV bool) string {
	name := a.Name
	if anv := strings.TrimSpace(a.ANV); preferANV && anv != "" {
		name = anv
	}
	return StripDisambiguator(name)
}

// ArtistLine is a credit line for release or track artists, e.g. "A / B & C, D feat. E".
// Each artist's join is the text Discogs puts between it and the next artist, used as
// given: "," and ";" follow the name directly, anything else ("&", "/", "vs.", "Feat.")
// stands between spaces, and a blank join between two names reads ", ". The last join
// is never printed, and artists whose name is empty after stripping are left out.
func ArtistLine(artists []Artist, preferANV bool) string {
	var b strings.Builder
	pending := "" // the join owed to the next printed name
	for _, a := range artists {
		name := artistName(a, preferANV)
		if name == "" {
			continue
		}
		b.WriteString(pending)
		b.WriteString(name)
		switch join := strings.TrimSpace(a.Join); join {
		case "", ",":
			pending = ", "
		case ";":
			pending = "; "
		default:
			pending = " " + join + " "
		}
	}
	return b.String()
}

// TrackArtists is the credit line for one track's artists; see ArtistLine.
func TrackArtists(artists []Artist, preferANV bool) string { return ArtistLine(artists, preferANV) }

// discFormats are the record formats on Discogs' formats list
// (https://www.discogs.com/help/formatslist): the cut or pressed discs, the media a size
// on a label means something for. Compared without case.
var discFormats = []string{"Vinyl", "Acetate", "Lathe Cut", "Flexi-disc", "Shellac", "Pathé Disc", "Edison Disc"}

// discSize matches a size description as the formats list writes them: whole inches,
// optionally with a half, and an ASCII double quote (`7"`, `12"`, `6½"`), or whole
// centimetres (`25cm`).
var discSize = regexp.MustCompile(`^(?:[1-9][0-9]?½?"|[1-9][0-9]cm)$`)

// DiscSize is the size a label prints for the record, from the release's formats[]:
//
//  1. Among the disc formats, in the order the release lists them, the first description
//     that is a size, exactly as written (`12"`, `7"`, `10"`; rarer sizes such as `8"`
//     or `25cm` too). The first listed medium is the main one, so a 12" with a bonus 7"
//     is `12"`.
//  2. Failing that, `LP` when a disc format lists it. Discogs' guideline 6.14 makes LP on
//     its own mean a 12" 33 rpm record and adds a size only when it differs.
//  3. Otherwise "". Release types such as Album or EP say nothing about the disc.
func DiscSize(formats []Format) string {
	var discs []Format
	for _, f := range formats {
		for _, name := range discFormats {
			if strings.EqualFold(strings.TrimSpace(f.Name), name) {
				discs = append(discs, f)
				break
			}
		}
	}
	for _, f := range discs {
		for _, d := range f.Descriptions {
			if d = strings.TrimSpace(d); discSize.MatchString(d) {
				return d
			}
		}
	}
	for _, f := range discs {
		for _, d := range f.Descriptions {
			if strings.TrimSpace(d) == "LP" {
				return "LP"
			}
		}
	}
	return ""
}

// DistinctValues is a genre or style list ready for a label: trimmed, without empty
// entries or repeats (compared without case), in the order the release lists them.
// Nothing is cut here: templates show as many as fit, and the editor shows them all.
func DistinctValues(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if k := strings.ToLower(v); v != "" && !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}

// FormatText is the human format line for lists, e.g. `3×Vinyl, LP, Compilation`.
func FormatText(formats []Format) string {
	var parts []string
	for _, f := range formats {
		qty := strings.TrimSpace(string(f.Qty))
		head := f.Name
		if qty != "" && qty != "1" {
			head = qty + "×" + f.Name
		}
		part := strings.Join(append([]string{head}, f.Descriptions...), ", ")
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "; ")
}

// FirstLabel is the first label's stripped name and its catno ("none" becomes "").
func FirstLabel(labels []Label) (name, catno string) {
	if len(labels) == 0 {
		return "", ""
	}
	catno = strings.TrimSpace(string(labels[0].Catno))
	if strings.EqualFold(catno, "none") {
		catno = ""
	}
	return StripDisambiguator(labels[0].Name), catno
}

// YearText is the year as a decimal string if it is > 0, else "".
func YearText(y Int) string {
	if y <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(y), 10)
}

// ArtworkURL is the primary image, else the first image, else cover_image, else thumb.
func ArtworkURL(r *Release) string {
	for _, im := range r.Images {
		if im.Type == "primary" && im.URI != "" {
			return im.URI
		}
	}
	for _, im := range r.Images {
		if im.URI != "" {
			return im.URI
		}
	}
	if r.CoverImage != "" {
		return r.CoverImage
	}
	return r.Thumb
}

func idPtr(v Int) *int64 {
	if v <= 0 {
		return nil
	}
	x := int64(v)
	return &x
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return append([]string{}, v...)
}

// ReleaseSummary is the release panel's facts (API.md ReleaseSummary).
type ReleaseSummary struct {
	ID       int64      `json:"id"`
	MasterID *int64     `json:"master_id"`
	Artist   string     `json:"artist"`
	Title    string     `json:"title"`
	Label    string     `json:"label"`
	Catno    string     `json:"catno"`
	Year     string     `json:"year"`
	Country  string     `json:"country"`
	Format   string     `json:"format"`
	Genres   []string   `json:"genres"`
	Styles   []string   `json:"styles"`
	Thumb    string     `json:"thumb"`
	Cover    string     `json:"cover"`
	URL      string     `json:"url"`
	Tracks   int        `json:"tracks"`
	Printed  bool       `json:"printed"`
	Videos   []VideoRef `json:"videos"` // YouTube links on the release, matched to tracks
}

// Summarize builds a ReleaseSummary. Printed is left false for the caller to fill.
func Summarize(r *Release) ReleaseSummary {
	label, catno := FirstLabel(r.Labels)
	u := r.URI
	if u == "" {
		u = ReleaseURL(int64(r.ID))
	}
	return ReleaseSummary{
		ID: int64(r.ID), MasterID: idPtr(r.MasterID),
		Artist: ArtistLine(r.Artists, false), Title: r.Title, Label: label, Catno: catno,
		Year: YearText(r.Year), Country: r.Country, Format: FormatText(r.Formats),
		Genres: orEmpty(r.Genres), Styles: orEmpty(r.Styles),
		Thumb: r.Thumb, Cover: ArtworkURL(r), URL: u, Tracks: len(r.Tracklist), Videos: videosOrEmpty(ReleaseVideos(r)),
	}
}

// SearchHit is one search result row (API.md SearchHit).
type SearchHit struct {
	ID       int64    `json:"id"`
	Type     string   `json:"type"`
	MasterID *int64   `json:"master_id"`
	Artist   string   `json:"artist"`
	Title    string   `json:"title"`
	Label    string   `json:"label"`
	Catno    string   `json:"catno"`
	Year     string   `json:"year"`
	Country  string   `json:"country"`
	Format   string   `json:"format"`
	Thumb    string   `json:"thumb"`
	Cover    string   `json:"cover"`
	Barcode  []string `json:"barcode"`
	Printed  bool     `json:"printed"`
}

// Hit builds a SearchHit. Discogs titles are "Artist - Title"; they are split on the
// first " - " and the artist loses its "(n)".
func Hit(r SearchResult) SearchHit {
	artist, title, found := strings.Cut(r.Title, " - ")
	if !found || title == "" {
		artist, title = "", r.Title
	} else {
		artist = StripDisambiguator(artist)
	}
	label := ""
	if len(r.Label) > 0 {
		label = StripDisambiguator(r.Label[0])
	}
	kind := r.Type
	if kind == "" {
		kind = KindRelease
	}
	barcode := orEmpty(r.Barcode)
	if len(barcode) > 3 {
		barcode = barcode[:3]
	}
	year := string(r.Year)
	if year == "0" {
		year = ""
	}
	return SearchHit{
		ID: int64(r.ID), Type: kind, MasterID: idPtr(r.MasterID), Artist: artist, Title: title,
		Label: label, Catno: string(r.Catno), Year: year, Country: r.Country,
		Format: strings.Join(r.Format, ", "), Thumb: r.Thumb, Cover: r.CoverImage, Barcode: barcode,
	}
}

// CollectionItem is one collection or wantlist row (API.md CollectionItem).
type CollectionItem struct {
	ID         int64    `json:"id"`
	InstanceID int64    `json:"instance_id"`
	FolderID   int64    `json:"folder_id"`
	MasterID   *int64   `json:"master_id"`
	Artist     string   `json:"artist"`
	Title      string   `json:"title"`
	Label      string   `json:"label"`
	Catno      string   `json:"catno"`
	Year       string   `json:"year"`
	Format     string   `json:"format"`
	DiscSize   string   `json:"disc_size"` // DiscSize of the formats
	Genres     []string `json:"genres"`
	Styles     []string `json:"styles"`
	Thumb      string   `json:"thumb"`
	Cover      string   `json:"cover"`
	DateAdded  string   `json:"date_added"`
	Rating     int64    `json:"rating"`
	Notes      string   `json:"notes"`
	URL        string   `json:"url"`
	Printed    bool     `json:"printed"`
}

// Item builds a CollectionItem. Notes holds the row's custom-field values, one per line
// in Discogs' field order, each trimmed; empty fields are left out.
func Item(row CollectionRow) CollectionItem {
	b := row.BasicInformation
	label, catno := FirstLabel(b.Labels)
	var notes []string
	for _, n := range row.Notes {
		if v := strings.TrimSpace(string(n.Value)); v != "" {
			notes = append(notes, v)
		}
	}
	id := int64(b.ID)
	if id == 0 {
		id = int64(row.ID)
	}
	return CollectionItem{
		ID: id, InstanceID: int64(row.InstanceID), FolderID: int64(row.FolderID), MasterID: idPtr(b.MasterID),
		Artist: ArtistLine(b.Artists, false), Title: b.Title, Label: label, Catno: catno, Year: YearText(b.Year),
		Format: FormatText(b.Formats), DiscSize: DiscSize(b.Formats),
		Genres: orEmpty(b.Genres), Styles: orEmpty(b.Styles), Thumb: b.Thumb, Cover: b.CoverImage,
		DateAdded: row.DateAdded, Rating: int64(row.Rating), Notes: strings.Join(notes, "\n"), URL: ReleaseURL(id),
	}
}

func videosOrEmpty(v []VideoRef) []VideoRef {
	if v == nil {
		return []VideoRef{}
	}
	return v
}
