package service_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ljagiello/crate-labels/internal/layout"
	"github.com/ljagiello/crate-labels/internal/service"
)

func TestLabelJSONIsLenientInAndCanonicalOut(t *testing.T) {
	l, err := service.ParseLabel(json.RawMessage(`{"artist": "X", "title": "Y", "styles": "Jungle, Hardcore,", "genres": null,
		"release_id": "42", "year": 1994, "bogus": 1,
		"tracks": [{"position": "A1", "title": "T", "bpm": 172, "key": "8A", "extra": "ignored"},
		           {"title": "U", "bpm": null, "key": "", "included": false}, {"title": "V", "bpm": "128"},
		           {"title": "W", "bpm": 128.5}, {"title": "X", "bpm": 0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(l.Styles, []string{"Jungle", "Hardcore"}) || len(l.Genres) != 0 || l.RID() != 42 || l.Year != "1994" {
		t.Fatalf("label = %+v", l)
	}
	if *l.Tracks[0].BPM != 172 || *l.Tracks[0].Key != "8A" || !l.Tracks[0].Included {
		t.Fatalf("track 0 = %+v", l.Tracks[0])
	}
	if l.Tracks[1].BPM != nil || l.Tracks[1].Key != nil || l.Tracks[1].Included {
		t.Fatalf("track 1 = %+v", l.Tracks[1])
	}
	if *l.Tracks[2].BPM != 128 || !l.Tracks[2].Included {
		t.Fatalf("track 2 = %+v", l.Tracks[2])
	}
	if *l.Tracks[3].BPM != 129 || l.Tracks[4].BPM != nil {
		t.Fatalf("fractional and zero bpm = %+v %+v", l.Tracks[3], l.Tracks[4])
	}
	out := string(l.JSON())
	for _, want := range []string{`"genres":[]`, `"release_id":42`, `"bpm":null`, `"key":null`, `"qr_url":""`, `"condition":""`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON lacks %s: %s", want, out)
		}
	}
	again, _ := service.ParseLabel(l.JSON())
	if string(again.JSON()) != out {
		t.Fatal("canonical JSON does not round-trip")
	}
	empty, _ := service.ParseLabel(json.RawMessage(`{}`))
	if got := string(empty.JSON()); got != `{"artist":"","title":"","label":"","catno":"","year":"","country":"","format":"",`+
		`"genres":[],"styles":[],"tracks":[],"notes":"","price":"","condition":"","qr_url":"","release_id":null}` {
		t.Fatalf("empty = %s", got)
	}
	for _, bad := range []string{`{"artist": {}}`, `{"tracks": "A1"}`, `{"release_id": 1.5}`, `{"tracks": [{"included": "yes"}]}`, `[]`} {
		_, err := service.ParseLabel(json.RawMessage(bad))
		var fe *service.FieldError
		if !errors.As(err, &fe) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestLabelLayoutRoundTrip(t *testing.T) {
	d := layout.LabelData{Artist: "A", Title: "T", Genres: []string{"Electronic"}, ReleaseID: 7,
		Tracks: []layout.Track{{Position: "A", Title: "x", BPM: 150, Key: "8A", Included: true}, {Title: "y"}}}
	l := service.FromLayout(d)
	if l.RID() != 7 || *l.Tracks[0].BPM != 150 || l.Tracks[1].BPM != nil || l.Tracks[1].Key != nil {
		t.Fatalf("wire = %+v", l)
	}
	back := l.ToLayout()
	if back.ReleaseID != 7 || back.Tracks[0] != d.Tracks[0] || back.Tracks[1] != d.Tracks[1] || back.Styles == nil {
		t.Fatalf("back = %+v", back)
	}
}
