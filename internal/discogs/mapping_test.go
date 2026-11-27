package discogs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ljagiello/crate-labels/internal/layout"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "discogs", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func release(t *testing.T, name string) *Release {
	t.Helper()
	var r Release
	if err := json.Unmarshal(fixture(t, name), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestGenresAndStylesKeepReleaseOrderUncut(t *testing.T) {
	styles := Strings{"Hardcore", " Breakbeat ", "", "Jungle", "hardcore", "Happy Hardcore"}
	d := Map(&Release{ID: 1, MasterID: 2, Genres: Strings{"Electronic", "Electronic"}, Styles: styles}, MapOptions{})
	if want := []string{"Hardcore", "Breakbeat", "Jungle", "Happy Hardcore"}; !reflect.DeepEqual(d.Styles, want) {
		t.Errorf("styles = %v, want %v", d.Styles, want)
	}
	if !reflect.DeepEqual(d.Genres, []string{"Electronic"}) {
		t.Errorf("genres = %v", d.Genres)
	}
	// The release and master ids play no part: the same lists map the same way.
	if other := Map(&Release{ID: 99, Styles: styles}, MapOptions{}); !reflect.DeepEqual(other.Styles, d.Styles) {
		t.Errorf("styles depend on the ids: %v", other.Styles)
	}
	if got := DistinctValues(nil); got == nil || len(got) != 0 {
		t.Errorf("DistinctValues(nil) = %#v, want empty non-nil", got)
	}
}

func TestDiscSize(t *testing.T) {
	vinyl := func(desc ...string) Format { return Format{Name: "Vinyl", Qty: "1", Descriptions: desc} }
	cases := []struct {
		formats []Format
		want    string
	}{
		{[]Format{vinyl(`12"`, "33 ⅓ RPM", "EP")}, `12"`},
		{[]Format{vinyl("LP", "Album")}, "LP"},
		{[]Format{vinyl("LP", `10"`, "Album")}, `10"`}, // a size beats LP
		{[]Format{vinyl(`7"`, "45 RPM", "Single")}, `7"`},
		{[]Format{{Name: "vinyl", Descriptions: Strings{` 10" `}}}, `10"`},
		{[]Format{vinyl(`8"`)}, `8"`},
		{[]Format{vinyl(`6½"`, "78 RPM")}, `6½"`},
		{[]Format{vinyl("LP", "25cm")}, "25cm"},
		{[]Format{vinyl("5cm", "123cm", `100"`, `0"`)}, ""},
		{[]Format{{Name: "Edison Disc", Descriptions: Strings{`10"`, "80 RPM"}}}, `10"`},
		{[]Format{{Name: "Laserdisc", Descriptions: Strings{`12"`}}}, ""},
		{[]Format{vinyl("Album")}, ""},         // a release type, not a disc
		{[]Format{vinyl(`12"/7"`)}, ""},        // not a size value
		{[]Format{vinyl(`12`, `12 inch`)}, ""}, // nor these
		{[]Format{vinyl()}, ""},
		{[]Format{{Name: "CD", Descriptions: Strings{"Album"}}}, ""},
		{[]Format{{Name: "CD", Descriptions: Strings{"Album"}}, vinyl(`7"`)}, `7"`},
		{[]Format{vinyl(`7"`, "45 RPM"), vinyl(`12"`)}, `7"`}, // the first listed medium
		{[]Format{{Name: "Cassette"}, vinyl("LP"), vinyl(`12"`)}, `12"`},
		{[]Format{{Name: "Acetate", Descriptions: Strings{`10"`, "Single Sided"}}}, `10"`},
		{[]Format{{Name: "Lathe Cut", Descriptions: Strings{`7"`}}}, `7"`},
		{[]Format{{Name: "Flexi-disc", Descriptions: Strings{`7"`}}}, `7"`},
		{[]Format{{Name: "Shellac", Descriptions: Strings{`10"`, "78 RPM"}}}, `10"`},
		{[]Format{{Name: "File", Descriptions: Strings{`12"`}}}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := DiscSize(c.formats); got != c.want {
			t.Errorf("DiscSize(%v) = %q, want %q", c.formats, got, c.want)
		}
	}
}

func TestStripAndJoinArtists(t *testing.T) {
	if got := StripDisambiguator("Paul Bradley (10)"); got != "Paul Bradley" {
		t.Errorf("strip = %q", got)
	}
	if got := StripDisambiguator("Brisk"); got != "Brisk" {
		t.Errorf("strip = %q", got)
	}
	artists := []Artist{{Name: "A (2)", Join: "&"}, {Name: "B (3)", Join: ","}, {Name: "C", Join: "feat."}, {Name: "D", Join: "/"}}
	if got := ArtistLine(artists, false); got != "A & B, C feat. D" {
		t.Errorf("ArtistLine = %q", got)
	}
	if got := ArtistLine([]Artist{{Name: " (4)"}, {Name: "X"}}, false); got != "X" {
		t.Errorf("empty names must be skipped: %q", got)
	}
	semi := []Artist{{Name: "A", Join: ";"}, {Name: "B", Join: ""}, {Name: "C", Join: "Vs."}, {Name: "D", Join: "+"}}
	if got := ArtistLine(semi, false); got != "A; B, C Vs. D" {
		t.Errorf("ArtistLine = %q", got)
	}
	// A skipped artist's join goes with it.
	if got := ArtistLine([]Artist{{Name: "A", Join: "&"}, {Name: "(3)", Join: "/"}, {Name: "B"}}, false); got != "A & B" {
		t.Errorf("ArtistLine with a skipped artist = %q", got)
	}
}

func tracks(rows ...layout.Track) []layout.Track { return rows }

// Worked example 1 of docs/DISCOGS-MAPPING.md.
func TestKniteforceKF039TwelveInch(t *testing.T) {
	got := Map(release(t, "release-118638.json"), MapOptions{})
	want := layout.LabelData{
		Artist: "DJ Force & The Evolution", Title: "Simply Electric / Out Of Control",
		Label: "Kniteforce Records", Catno: "KF039", Year: "1995", Country: "UK", Format: `12"`,
		Genres: []string{"Electronic"}, Styles: []string{"Breakbeat", "Happy Hardcore"},
		Tracks: tracks(
			layout.Track{Position: "A", Title: "Simply Electric", Duration: "4:39", Included: true},
			layout.Track{Position: "B", Title: "Out Of Control", Duration: "5:05", Included: true},
		),
		QRURL: "https://www.discogs.com/release/118638", ReleaseID: 118638,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Map =\n%+v\nwant\n%+v", got, want)
	}
	s := Summarize(release(t, "release-118638.json"))
	if s.Format != `Vinyl, 12", 45 RPM` || s.MasterID == nil || *s.MasterID != 4589 || s.Tracks != 2 {
		t.Errorf("summary = %+v", s)
	}
	if s.URL != "https://www.discogs.com/release/118638-DJ-Force-The-Evolution-Simply-Electric-Out-Of-Control" {
		t.Errorf("summary url = %q", s.URL)
	}
	if s.Cover == "" || s.Cover[:22] != "https://i.discogs.com/" {
		t.Errorf("cover = %q", s.Cover)
	}
}

// Worked example 2: four artists joined with "/" and an ANV.
func TestMultiArtistJoinsAndANV(t *testing.T) {
	r := release(t, "release-10550772.json")
	d := Map(r, MapOptions{})
	if d.Artist != "Sanxion / Dj Nicky Allen / TNO Project / Mannik" {
		t.Errorf("artist = %q", d.Artist)
	}
	if anv := Map(r, MapOptions{PreferANV: true}); anv.Artist != "Sanxion / Nicky Allen / TNO Project / Mannik" {
		t.Errorf("prefer_anv artist = %q", anv.Artist)
	} else if anv.Tracks[1].Artist != "Nicky Allen" {
		t.Errorf("prefer_anv must reach the track artists too: %q", anv.Tracks[1].Artist)
	}
	if d.Format != `12"` || d.Catno != "KF70" || d.Year != "2017" || !reflect.DeepEqual(d.Styles, []string{"Breakbeat", "Hardcore"}) {
		t.Errorf("data = %+v", d)
	}
	want0 := layout.Track{Position: "A1", Title: "Waiting On My Feelings", Artist: "Sanxion", Included: true}
	if d.Tracks[0] != want0 || d.Tracks[1].Artist != "Dj Nicky Allen" {
		t.Errorf("tracks = %+v", d.Tracks[:2])
	}
}

// Worked example 3: a CD by "Paul Bradley (10)": no disc size, no country, no master.
func TestCDOnlyReleaseHasNoDiscSizeOrCountry(t *testing.T) {
	d := Map(release(t, "release-10953799.json"), MapOptions{})
	if d.Artist != "Paul Bradley" || d.Label != "Knitebreed" || d.Catno != "BREEDCD01" || d.Format != "" || d.Country != "" {
		t.Errorf("data = %+v", d)
	}
	if len(d.Tracks) != 8 {
		t.Fatalf("tracks = %d", len(d.Tracks))
	}
	for i, tr := range d.Tracks {
		if tr.Position != string(rune('1'+i)) || tr.Duration != "" {
			t.Errorf("track %d = %+v", i, tr)
		}
	}
}

// Worked example 4: a 3×LP compilation, second label ignored, track artists stripped.
func TestCompilationLPTrackArtistsAreStripped(t *testing.T) {
	r := release(t, "release-89569.json")
	d := Map(r, MapOptions{})
	if d.Artist != "Various" || d.Label != "Kniteforce Records" || d.Catno != "KF LP 002" || d.Format != "LP" {
		t.Errorf("data = %+v", d)
	}
	first := d.Tracks[0]
	if first.Position != "F1" || first.Title != "Sumaki (Jimmy J & Cru-L-T Mix)" || first.Artist != "DJ Desire" {
		t.Errorf("first track = %+v", first)
	}
	if len(d.Tracks) != len(r.Tracklist) {
		t.Errorf("tracks = %d, tracklist = %d", len(d.Tracks), len(r.Tracklist))
	}
	if s := Summarize(r); s.Format != "3×Vinyl, LP, Compilation" {
		t.Errorf("summary format = %q", s.Format)
	}
}

func TestTracklistSkipsHeadingsAndUntitledRows(t *testing.T) {
	r := &Release{ID: 1, Tracklist: []TrackEntry{
		{Type: "heading", Title: "Side A"},
		{Position: " A1 ", Type: "track", Title: "One", Duration: " 5:01 "},
		{Position: "A2", Type: "track", Title: "  "},
		{Position: "", Type: "index", Title: "Medley"},
	}}
	got := Tracks(r, MapOptions{})
	want := []layout.Track{
		{Position: "A1", Title: "One", Duration: "5:01", Included: true},
		{Title: "Medley", Included: true}, // an index row without sub-tracks stays one row
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tracks = %+v", got)
	}
}

// Index rows give one row per sub-track. The row is synthetic (no fixture has one), in
// the tracklist shape the API returns: type_ "index" with a sub_tracks array.
func TestIndexRowsExpandToTheirSubTracks(t *testing.T) {
	var r Release
	in := `{"id": 2, "tracklist": [
	  {"position": "A1", "type_": "track", "title": "Intro"},
	  {"position": "A2", "type_": "index", "title": "Megamix", "artists": [{"name": "DJ Ham", "join": ""}],
	   "sub_tracks": [
	     {"position": "A2a", "type_": "track", "title": "Part One", "duration": "2:10"},
	     {"position": "", "type_": "track", "title": "Part Two", "artists": [{"name": "Brisk", "join": "&"}, {"name": "Ham (2)", "anv": "Hamster"}]},
	     {"position": "A2c", "type_": "track", "title": " "}]},
	  {"position": "", "type_": "heading", "title": "Side B"},
	  {"position": "B1", "type_": "track", "title": "Outro"}]}`
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	got := Tracks(&r, MapOptions{})
	want := []layout.Track{
		{Position: "A1", Title: "Intro", Included: true},
		{Position: "A2a", Title: "Part One", Artist: "DJ Ham", Duration: "2:10", Included: true},
		{Position: "A2", Title: "Part Two", Artist: "Brisk & Ham", Included: true},
		{Position: "B1", Title: "Outro", Included: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tracks =\n%+v\nwant\n%+v", got, want)
	}
	if anv := Tracks(&r, MapOptions{PreferANV: true}); anv[2].Artist != "Brisk & Hamster" {
		t.Errorf("prefer_anv sub-track artist = %q", anv[2].Artist)
	}
}

func TestTrackArtistsUseTheirJoins(t *testing.T) {
	r := &Release{ID: 3, Tracklist: []TrackEntry{{Position: "A", Title: "Split", Artists: []Artist{
		{Name: "Sanxion", Join: "vs."}, {Name: "Mannik (3)", Join: ","}, {Name: "Brisk"}}}}}
	if got := Tracks(r, MapOptions{})[0].Artist; got != "Sanxion vs. Mannik, Brisk" {
		t.Errorf("track artist = %q", got)
	}
}

func TestCatnoNoneAndYearZero(t *testing.T) {
	r := &Release{ID: 3, Year: 0, Labels: []Label{{Name: "White (2)", Catno: "NONE"}}}
	d := Map(r, MapOptions{})
	if d.Catno != "" || d.Year != "" || d.Label != "White" || d.Genres == nil || d.Styles == nil || d.Tracks == nil {
		t.Errorf("data = %#v", d)
	}
}

func TestArtworkURLOrder(t *testing.T) {
	r := &Release{Images: []Image{{Type: "secondary", URI: "s"}, {Type: "primary", URI: "p"}}, CoverImage: "c", Thumb: "t"}
	if got := ArtworkURL(r); got != "p" {
		t.Errorf("primary: %q", got)
	}
	r.Images[1].Type = "secondary"
	if got := ArtworkURL(r); got != "s" {
		t.Errorf("first image: %q", got)
	}
	r.Images = nil
	if got := ArtworkURL(r); got != "c" {
		t.Errorf("cover: %q", got)
	}
	r.CoverImage = ""
	if got := ArtworkURL(r); got != "t" {
		t.Errorf("thumb: %q", got)
	}
}

func TestMasterFixtureMainRelease(t *testing.T) {
	var m Master
	if err := json.Unmarshal(fixture(t, "master-4589.json"), &m); err != nil {
		t.Fatal(err)
	}
	if m.MainRelease != 118638 {
		t.Errorf("main_release = %d", m.MainRelease)
	}
}

// Synthetic rows, shaped like the public API docs.
var searchFixture = `{"pagination": {"page": 1, "pages": 1, "per_page": 30, "items": 1},
 "results": [{"id": 118638, "type": "release", "master_id": 4589,
   "title": "DJ Force & The Evolution (2) - Simply Electric / Out Of Control", "year": "1995", "country": "UK",
   "format": ["Vinyl", "12\"", "45 RPM"], "label": ["Kniteforce Records (3)", "Other"], "catno": "KF039",
   "thumb": "https://i.discogs.com/thumb.jpeg", "barcode": ["1", "2", "3", "4"]},
   {"id": 5, "title": "No Separator", "year": 1990, "label": []}]}`

var collectionFixture = `{"pagination": {"page": 1, "pages": 1, "per_page": 50, "items": 1},
 "releases": [{"id": 10550772, "instance_id": 99001, "folder_id": 1, "date_added": "2026-11-01T10:00:00-08:00",
   "rating": 5, "notes": [{"field_id": 3, "value": " plays great "}, {"field_id": 4, "value": ""}, {"field_id": 5, "value": "B2!"}],
   "basic_information": {"id": 10550772, "master_id": 1228832, "title": "Death To Digital EP (Volume One)", "year": 2017,
     "thumb": "https://i.discogs.com/t.jpeg", "cover_image": "https://i.discogs.com/c.jpeg",
     "formats": [{"name": "Vinyl", "qty": "1", "descriptions": ["12\"", "EP"]}],
     "labels": [{"name": "Kniteforce Records", "catno": "KF70"}],
     "artists": [{"name": "Sanxion", "join": "/"}, {"name": "Mannik", "join": ""}],
     "genres": ["Electronic"], "styles": ["Breakbeat", "Hardcore"]}}]}`

func TestSearchAndCollectionSummaries(t *testing.T) {
	var page SearchPage
	if err := json.Unmarshal([]byte(searchFixture), &page); err != nil {
		t.Fatal(err)
	}
	h := Hit(page.Results[0])
	if h.Artist != "DJ Force & The Evolution" || h.Title != "Simply Electric / Out Of Control" || h.Catno != "KF039" {
		t.Errorf("hit = %+v", h)
	}
	if h.Label != "Kniteforce Records" || h.Format != `Vinyl, 12", 45 RPM` || len(h.Barcode) != 3 || h.Year != "1995" {
		t.Errorf("hit = %+v", h)
	}
	h2 := Hit(page.Results[1])
	if h2.Artist != "" || h2.Title != "No Separator" || h2.Year != "1990" || h2.Type != "release" || h2.Barcode == nil {
		t.Errorf("hit 2 = %+v", h2)
	}
	var coll CollectionPage
	if err := json.Unmarshal([]byte(collectionFixture), &coll); err != nil {
		t.Fatal(err)
	}
	it := Item(coll.Releases[0])
	if it.Artist != "Sanxion / Mannik" || it.Notes != "plays great\nB2!" || it.InstanceID != 99001 || it.DiscSize != `12"` {
		t.Errorf("item = %+v", it)
	}
	if it.URL != "https://www.discogs.com/release/10550772" || it.Rating != 5 || it.Year != "2017" || it.Cover != "https://i.discogs.com/c.jpeg" {
		t.Errorf("item = %+v", it)
	}
	b, _ := json.Marshal(it)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	for _, k := range []string{"id", "instance_id", "folder_id", "master_id", "disc_size", "date_added", "printed"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("CollectionItem JSON lacks %q", k)
		}
	}
}

func TestLenientDecoding(t *testing.T) {
	var r Release
	in := `{"id": "77", "master_id": null, "year": "1994", "formats": [{"name": "Vinyl", "qty": 2, "descriptions": null}],
		"labels": [{"name": "L", "catno": 123}], "genres": null, "styles": ["A", 5]}`
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != 77 || r.MasterID != 0 || r.Year != 1994 || r.Formats[0].Qty != "2" || r.Labels[0].Catno != "123" {
		t.Errorf("release = %+v", r)
	}
	if !reflect.DeepEqual([]string(r.Styles), []string{"A", "5"}) {
		t.Errorf("styles = %v", r.Styles)
	}
}
