package discogs

import (
	"strings"

	"github.com/ljagiello/crate-labels/internal/layout"
)

// MapOptions tune Map.
type MapOptions struct {
	// PreferANV prints each credit's name variation (Discogs `anv`, the name as printed
	// on that record) instead of the artist's canonical name, for the release artists
	// and the track artists alike (setting discogs.prefer_anv).
	PreferANV bool
}

// Map turns a release into the label data a template renders. The rules, with worked
// examples, are in docs/DISCOGS-MAPPING.md.
func Map(r *Release, o MapOptions) layout.LabelData {
	id := int64(r.ID)
	label, catno := FirstLabel(r.Labels)
	return layout.LabelData{
		Artist:    ArtistLine(r.Artists, o.PreferANV),
		Title:     strings.TrimSpace(r.Title),
		Label:     label,
		Catno:     catno,
		Year:      YearText(r.Year),
		Country:   strings.TrimSpace(r.Country),
		Format:    DiscSize(r.Formats),
		Genres:    DistinctValues(r.Genres),
		Styles:    DistinctValues(r.Styles),
		Tracks:    Tracks(r, o),
		QRURL:     ReleaseURL(id),
		ReleaseID: int(id),
	}
}

// Tracks is the release's playable rows, in tracklist order:
//
//   - heading rows (a side, disc or part name) are not tracks and are left out;
//   - a row with sub_tracks (an index row: one groove holding several pieces) gives one
//     row per sub-track; a sub-track with no position or no artists of its own takes
//     the index row's;
//   - rows with no title are left out;
//   - position and duration are Discogs' text, trimmed; the artist is the track's own
//     credit line (ArtistLine), "" when the track credits nobody.
func Tracks(r *Release, o MapOptions) []layout.Track {
	out := []layout.Track{}
	add := func(e TrackEntry) {
		title := strings.TrimSpace(e.Title)
		if title == "" {
			return
		}
		out = append(out, layout.Track{
			Position: strings.TrimSpace(e.Position),
			Title:    title,
			Artist:   ArtistLine(e.Artists, o.PreferANV),
			Duration: strings.TrimSpace(e.Duration),
			Included: true,
		})
	}
	for _, e := range r.Tracklist {
		if e.Type == "heading" {
			continue
		}
		if len(e.SubTracks) == 0 {
			add(e)
			continue
		}
		for _, sub := range e.SubTracks {
			if strings.TrimSpace(sub.Position) == "" {
				sub.Position = e.Position
			}
			if len(sub.Artists) == 0 {
				sub.Artists = e.Artists
			}
			add(sub)
		}
	}
	return out
}
