package layout

import (
	"math"
	"slices"
	"strconv"
	"testing"
)

func TestParseKeyAndDisplay(t *testing.T) {
	cases := []struct{ in, camelot, musical string }{
		{"8A", "8A", "Am"},
		{"08a", "8A", "Am"},
		{"8b", "8B", "C"},
		{"1m", "8A", "Am"}, // Open Key
		{"1d", "8B", "C"},
		{"6m", "1A", "A♭m"},
		{"Am", "8A", "Am"},
		{"a", "8A", "Am"},
		{"C", "8B", "C"},
		{"F#m", "11A", "F♯m"},
		{"F♯m", "11A", "F♯m"},
		{"Gb", "2B", "F♯"},
		{"Bbm", "3A", "B♭m"},
		{"B♭ minor", "3A", "B♭m"},
		{"Db", "3B", "D♭"},
		{"C#", "3B", "D♭"},
		{"Ebm", "2A", "E♭m"},
		{"Ab", "4B", "A♭"},
		{"E major", "12B", "E"},
		{"Emaj", "12B", "E"},
		{"Cb", "1B", "B"},
		{"B#m", "5A", "Cm"},
		{"12A", "12A", "D♭m"},
	}
	for _, c := range cases {
		got, ok := ParseKey(c.in)
		if !ok || got != c.camelot {
			t.Errorf("ParseKey(%q) = %q, %v; want %q", c.in, got, ok, c.camelot)
		}
		if got := KeyDisplay(c.in, KeyMusical); got != c.musical {
			t.Errorf("KeyDisplay(%q, musical) = %q, want %q", c.in, got, c.musical)
		}
		if got := KeyDisplay(c.in, KeyCamelot); got != c.camelot {
			t.Errorf("KeyDisplay(%q, camelot) = %q", c.in, got)
		}
		if got := KeyDisplay(c.in, KeyBoth); got != c.camelot+" "+c.musical {
			t.Errorf("KeyDisplay(%q, both) = %q", c.in, got)
		}
		if got := KeyDisplay(c.in, KeyOff); got != "" {
			t.Errorf("KeyDisplay(%q, off) = %q", c.in, got)
		}
	}
	if got := KeyDisplay(" weird ", KeyCamelot); got != "weird" {
		t.Errorf("unparsed key printed as %q", got)
	}
	for _, bad := range []string{"13A", "0B", "H", "", "8C"} {
		if _, ok := ParseKey(bad); ok {
			t.Errorf("ParseKey(%q) accepted", bad)
		}
	}
}

// TestCamelotRoundTrip checks the wheel arithmetic: every code maps to a tonic whose name
// parses back to the same code, and neighbours on the wheel are a fifth apart.
func TestCamelotRoundTrip(t *testing.T) {
	for n := 1; n <= 12; n++ {
		for _, letter := range []string{"A", "B"} {
			code := strconv.Itoa(n) + letter
			name := musicalName(code)
			if got, ok := ParseKey(name); !ok || got != code {
				t.Errorf("%s → %q → %q, %v", code, name, got, ok)
			}
			next := (n % 12) + 1
			a, b := tonicOf(n, letter == "A"), tonicOf(next, letter == "A")
			if (b-a+12)%12 != 7 {
				t.Errorf("%s → %d%s is not a fifth", code, next, letter)
			}
		}
		if (tonicOf(n, false)-tonicOf(n, true)+12)%12 != 3 {
			t.Errorf("%dA is not the relative minor of %dB", n, n)
		}
	}
}

func TestFoldBPM(t *testing.T) {
	cases := []struct {
		bpm  float64
		low  int
		want int
	}{
		{128, 0, 128},
		{127.5, 0, 128},
		{174, 85, 87},
		{70, 85, 140},
		{140, 85, 140},
		{170, 85, 85}, // exactly 2·low folds down
		{339.6, 85, 85},
		{86, 95, 172},
		{43, 95, 172},
		{190, 95, 95},
		{189.6, 95, 95}, // rounds onto 2·low, prints as low
		{60, 60, 60},
		{1000, 75, 125},
		{-5, 75, -5},
		{0, 75, 0},
		{math.NaN(), 75, 0},
	}
	for _, c := range cases {
		if got := FoldBPM(c.bpm, c.low); got != c.want {
			t.Errorf("FoldBPM(%v, %d) = %d, want %d", c.bpm, c.low, got, c.want)
		}
	}
	for _, r := range BPMRanges {
		for bpm := 20.0; bpm < 400; bpm += 0.7 {
			if got := FoldBPM(bpm, r.Low); got < r.Low || got >= 2*r.Low {
				t.Fatalf("FoldBPM(%v, %d) = %d leaves the window", bpm, r.Low, got)
			}
		}
		if bpmRangeLow(r.ID) != r.Low {
			t.Errorf("bpmRangeLow(%q)", r.ID)
		}
	}
	if bpmRangeLow(bpmOff) != 0 || bpmRangeLow("88") != 0 {
		t.Error("off and unknown ranges must not fold")
	}
}

func TestFormatDisplay(t *testing.T) {
	cases := map[string]string{
		`12"`: "12″", "12": "12″", "7 EP": "7″ EP", `7"EP`: "7″ EP", "10in": "10″",
		"12-inch": "12″", "12 inch Single": "12″ Single", "LP": "LP", "123": "123",
		"7″": "7″", "  10″ ": "10″", "": "", "2LP": "2LP", `8"`: "8″", `6½"`: "6½″",
		"25cm": "25cm", "12345": "12345", "11": "11", `11"`: "11″",
	}
	for in, want := range cases {
		if got := FormatDisplay(in); got != want {
			t.Errorf("FormatDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseCondition(t *testing.T) {
	cases := []struct{ in, disc, cover string }{
		{"VG+", "VG+", ""},
		{"NM / VG+", "NM", "VG+"},
		{"Very Good Plus (VG+) / Very Good (VG)", "VG+", "VG"},
		{"Near Mint (NM or M-) / Generic", "NM", ""},
		{"M-, EX", "NM", "VG+"},
		{"mint|poor", "M", "P"},
		{"Generic", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := parseCondition(c.in); got.disc != c.disc || got.cover != c.cover {
			t.Errorf("parseCondition(%q) = %+v, want %q/%q", c.in, got, c.disc, c.cover)
		}
	}
}

func TestFormatPrice(t *testing.T) {
	cases := []struct{ in, currency, want string }{
		{"18.50", "USD", "$18.50"},
		{"18.5", "USD", "$18.50"},
		{"18", "EUR", "€18"},
		{"$12", "EUR", "$12"},
		{"£15.00", "USD", "£15"},
		{"20 EUR", "USD", "€20"},
		{"12,50 €", "USD", "€12.50"},
		{"1,500", "JPY", "¥1,500"},
		{"1500.4", "JPY", "¥1,500"},
		{"1.249,00", "EUR", "€1,249"},
		{"CA$1,249.50", "USD", "CA$1,249.50"},
		{"A$30", "USD", "A$30"},
		{"40", "PLN", "40 zł"},
		{"40 zł", "USD", "40 zł"},
		{"30 CHF", "USD", "CHF 30"},
		{"1234567", "USD", "$1,234,567"},
		{"", "USD", ""},
		{"free", "USD", ""},
	}
	for _, c := range cases {
		if got := FormatPrice(c.in, c.currency); got != c.want {
			t.Errorf("FormatPrice(%q, %s) = %q, want %q", c.in, c.currency, got, c.want)
		}
	}
	if currencyMark("PLN") != "zł" || currencyMark("GBP") != "£" || currencyMark("CHF") != "CHF" {
		t.Error("currency marks")
	}
}

func TestNamesAndStyles(t *testing.T) {
	d := Sample("ep12")
	if got := artistName(d); got != "Moth Signal" {
		t.Errorf("artistName %q", got)
	}
	if got := styleLine(d, 2); got != "Techno · Electro" {
		t.Errorf("styleLine %q", got)
	}
	if got := styleLine(&LabelData{Genres: []string{"Funk / Soul", " "}, Styles: []string{" "}}, 3); got != "Funk / Soul" {
		t.Errorf("genres as a fallback: %q", got)
	}
	if got := joinFacts(" A ", "", "B"); got != "A · B" {
		t.Errorf("joinFacts %q", got)
	}
	if got := StripDiscogsSuffix("Moth Signal (2) "); got != "Moth Signal" {
		t.Errorf("StripDiscogsSuffix %q", got)
	}
}

func TestPickTrack(t *testing.T) {
	d := Sample("longform")
	cases := map[string]string{"": "A1", "b2": "B2", "3": "A3", "99": "A1", "B3": "B3", "x": "A1"}
	for choice, want := range cases {
		if got := pickTrack(d, choice); got == nil || got.Position != want {
			t.Errorf("pickTrack(%q) = %v, want %s", choice, got, want)
		}
	}
	none := &LabelData{Tracks: []Track{{Position: "A", Included: false}}}
	if got := pickTrack(none, ""); got == nil || got.Position != "A" {
		t.Errorf("no included tracks: %v, want the first", got)
	}
	if pickTrack(&LabelData{}, "") != nil {
		t.Error("no tracks: want nil")
	}
}

func TestSidesAndDiscs(t *testing.T) {
	side, tracks := sideTracks(Sample("ep12"), "b")
	if side != "B" || len(tracks) != 2 || tracks[0].Position != "B1" {
		t.Errorf("side %q tracks %v", side, tracks)
	}
	side, tracks = sideTracks(Sample("ep12"), "")
	if side != "A" || len(tracks) != 2 {
		t.Errorf("default side %q, %d tracks", side, len(tracks))
	}
	side, tracks = sideTracks(Sample("ep12"), "Z")
	if side != "A" || len(tracks) != 2 {
		t.Errorf("unknown side: %q, %d tracks", side, len(tracks))
	}
	numbered := &LabelData{Tracks: []Track{{Position: "1", Included: true}, {Position: "2", Included: true}}}
	if side, tracks := sideTracks(numbered, "c"); side != "C" || len(tracks) != 2 {
		t.Errorf("numbered tracks: side %q, %d tracks", side, len(tracks))
	}
	for _, c := range []struct {
		positions []string
		want      int
	}{
		{[]string{"A1", "B2"}, 1},
		{[]string{"A1", "D2"}, 2},
		{[]string{"E1", "f2"}, 3},
		{[]string{"2-5"}, 2},
		{[]string{"1", "2"}, 1},
		{nil, 1},
	} {
		d := &LabelData{}
		for _, p := range c.positions {
			d.Tracks = append(d.Tracks, Track{Position: p})
		}
		if got := discsOf(d); got != c.want {
			t.Errorf("discsOf(%v) = %d, want %d", c.positions, got, c.want)
		}
	}
	if !slices.Equal([]string{sideLetters("b2"), sideLetters("AA1"), sideLetters("3")}, []string{"B", "AA", ""}) {
		t.Error("sideLetters")
	}
}
