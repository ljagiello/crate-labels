package discogs

import "testing"

func TestParseRef(t *testing.T) {
	cases := []struct {
		text string
		want Ref
		ok   bool
	}{
		{"118638", Ref{KindRelease, 118638}, true},
		{"  r118638 ", Ref{KindRelease, 118638}, true},
		{"[m4589]", Ref{KindMaster, 4589}, true},
		{"[ R 42 ]", Ref{KindRelease, 42}, true},
		{"https://www.discogs.com/release/118638-DJ-Force-The-Evolution-Simply-Electric", Ref{KindRelease, 118638}, true},
		{"https://www.discogs.com/de/release/118638", Ref{KindRelease, 118638}, true},
		{"www.discogs.com/DJ-Force-The-Evolution-Simply-Electric/release/118638", Ref{KindRelease, 118638}, true},
		{"https://www.discogs.com/master/4589-DJ-Force-The-Evolution", Ref{KindMaster, 4589}, true},
		{"https://www.discogs.com/sell/release/118638?ev=rb", Ref{KindRelease, 118638}, true},
		{"https://api.discogs.com/releases/118638", Ref{KindRelease, 118638}, true},
		{"https://api.discogs.com/masters/4589", Ref{KindMaster, 4589}, true},
		{"look at https://www.discogs.com/release/89569 please", Ref{KindRelease, 89569}, true},
		{"discogs.com/release/1 and discogs.com/master/2", Ref{KindRelease, 1}, true}, // the first link counts
		{"discogs.com/master/2 and discogs.com/release/1", Ref{KindMaster, 2}, true},
		{"https://www.discogs.com/release/5#images", Ref{KindRelease, 5}, true},
		{"https://www.discogs.com/search?q=release/7", Ref{}, false},
		{"0", Ref{}, false},
		{"", Ref{}, false},
		{"https://example.com/release/1", Ref{}, false},
		{"Kniteforce", Ref{}, false},
		{"12345678901", Ref{}, false},
	}
	for _, c := range cases {
		got, ok := ParseRef(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v, %v", c.text, got, ok, c.want, c.ok)
		}
	}
	if u := (Ref{KindMaster, 4589}).URL(); u != "https://www.discogs.com/master/4589" {
		t.Errorf("URL = %q", u)
	}
}
