package discogs

import (
	"reflect"
	"testing"
)

func TestYouTubeID(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=nMP1QjHNDeA":            "nMP1QjHNDeA",
		"http://youtube.com/watch?v=nMP1QjHNDeA&t=30":            "nMP1QjHNDeA",
		"https://m.youtube.com/watch?v=nMP1QjHNDeA":              "nMP1QjHNDeA",
		"https://youtu.be/nMP1QjHNDeA":                           "nMP1QjHNDeA",
		"https://www.youtube.com/embed/nMP1QjHNDeA":              "nMP1QjHNDeA",
		"https://www.youtube.com/shorts/nMP1QjHNDeA":             "nMP1QjHNDeA",
		"https://www.youtube.com/watch?v=short":                  "",
		"https://www.youtube.com/watch?v=--rm-rf-xx":             "",
		"https://evil.example/watch?v=nMP1QjHNDeA":               "",
		"javascript:alert(1)":                                    "",
		"https://vimeo.com/123456":                               "",
		"https://www.youtube.com/watch?v=nMP1QjHNDeA%20--exec=x": "",
	} {
		if got := YouTubeID(in); got != want {
			t.Errorf("YouTubeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReleaseVideos(t *testing.T) {
	track := func(pos, title, dur string) TrackEntry {
		return TrackEntry{Position: pos, Type: "track", Title: title, Duration: dur}
	}
	video := func(id, title string, dur Int) Video {
		return Video{URI: "https://www.youtube.com/watch?v=" + id, Title: title, Duration: dur}
	}
	cases := []struct {
		name string
		r    Release
		want map[string]string // video id → track position
	}{
		{
			// A white label: untitled tracks, videos named by side, in order.
			name: "untitled white label",
			r: Release{Title: "Vol 022", Artists: []Artist{{Name: "Fin Deejay Series"}},
				Tracklist: []TrackEntry{track("A", "Untitled", ""), track("B1", "Untitled", ""), track("B2", "Untitled", "")},
				Videos: []Video{video("aaaaaaaaaaa", "Fin Vol 22   A   Waterfall", 384),
					video("bbbbbbbbbbb", "Fin Vol 22   B   Can You Feel It", 372),
					video("ccccccccccc", "Fin Vol 22   C   Its Not Over", 416)}},
			want: map[string]string{"aaaaaaaaaaa": "A", "bbbbbbbbbbb": "B1", "ccccccccccc": "B2"},
		},
		{
			// Titles decide, whatever order the videos were added in; a mix is skipped.
			name: "by title",
			r: Release{Title: "Remixes", Artists: []Artist{{Name: "DJ Example"}},
				Tracklist: []TrackEntry{track("A1", "Bassline Pressure", "5:01"), track("A2", "Lost In Time", "6:10"), track("B", "Night Shift (Dub)", "7:00")},
				Videos: []Video{video("xxxxxxxxxxx", "DJ Example - Night Shift (Dub Mix)", 421),
					video("yyyyyyyyyyy", "DJ Example - Bassline Pressure", 300),
					video("zzzzzzzzzzz", "DJ Example - Remixes (full EP)", 3600)}},
			want: map[string]string{"xxxxxxxxxxx": "B", "yyyyyyyyyyy": "A1"},
		},
		{
			// A video of another version (minutes longer) is not this track.
			name: "duration mismatch",
			r: Release{Title: "Single",
				Tracklist: []TrackEntry{track("A", "Rush", "4:00"), track("B", "Rush (Instrumental)", "4:05")},
				Videos:    []Video{video("rrrrrrrrrrr", "Rush (12\" Extended)", 480)}},
			want: map[string]string{"rrrrrrrrrrr": ""},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]string{}
			for _, v := range ReleaseVideos(&c.r) {
				got[v.ID] = v.Track
				if v.URL != "https://www.youtube.com/watch?v="+v.ID {
					t.Errorf("URL = %q", v.URL)
				}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("matches = %v, want %v", got, c.want)
			}
		})
	}
}
