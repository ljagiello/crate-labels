package discogs

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Releases often carry YouTube links, one per track, that people added on Discogs. They
// are the audio crate-labels detects BPM from when the user has no file of the record.

var youTubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// YouTubeID is the video id in a YouTube link (watch?v=, youtu.be/, /embed/, /shorts/,
// /v/), or "" if it isn't one.
func YouTubeID(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	var id string
	switch host {
	case "youtube.com", "music.youtube.com", "youtube-nocookie.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else {
			for _, p := range []string{"/embed/", "/shorts/", "/v/", "/live/"} {
				if rest, ok := strings.CutPrefix(u.Path, p); ok {
					id, _, _ = strings.Cut(rest, "/")
				}
			}
		}
	case "youtu.be":
		id, _, _ = strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	}
	if !youTubeID.MatchString(id) {
		return ""
	}
	return id
}

// VideoRef is a release's video as the UI sees it, with the track it belongs to.
type VideoRef struct {
	URL      string `json:"url"`
	ID       string `json:"id"` // YouTube id
	Title    string `json:"title"`
	Duration int64  `json:"duration"` // seconds, 0 = unknown
	// Track is the position of the track this video most likely plays ("" = none).
	Track string `json:"track"`
}

// maxVideoSeconds skips long videos (full-album uploads, mixes): a track's tempo can't be
// told from them.
const maxVideoSeconds = 20 * 60

// ReleaseVideos lists the release's YouTube videos and matches them to its tracks.
//
// A video belongs to a track when its title names the track's title, or its position
// ("A1", "B2"), or its side when the side has one track; when the durations are both
// known they must roughly agree. Videos and tracks left over after that are paired in
// order when there are as many of each, since people usually add them in tracklist order.
func ReleaseVideos(r *Release) []VideoRef {
	var vids []VideoRef
	seen := map[string]bool{}
	for _, v := range r.Videos {
		id := YouTubeID(v.URI)
		if id == "" || seen[id] || (v.Duration > maxVideoSeconds) {
			continue
		}
		seen[id] = true
		vids = append(vids, VideoRef{URL: "https://www.youtube.com/watch?v=" + id, ID: id,
			Title: strings.TrimSpace(v.Title), Duration: int64(v.Duration)})
	}
	var tracks []TrackEntry
	for _, t := range r.Tracklist {
		if t.Type == "" || t.Type == "track" {
			tracks = append(tracks, t)
		}
	}
	for v, t := range matchVideos(tracks, vids, r.Title, ArtistLine(r.Artists, false)) {
		vids[v].Track = tracks[t].Position
	}
	return vids
}

// matchVideos returns video index → track index.
func matchVideos(tracks []TrackEntry, vids []VideoRef, release, artist string) map[int]int {
	noise := map[string]bool{}
	for _, w := range words(release + " " + artist) {
		noise[w] = true
	}
	sides := map[string]int{}
	for _, t := range tracks {
		sides[side(t.Position)]++
	}
	type pair struct {
		v, t  int
		score float64
	}
	var pairs []pair
	for vi, v := range vids {
		vw := words(v.Title)
		vset := map[string]bool{}
		for _, w := range vw {
			vset[w] = true
		}
		for ti, t := range tracks {
			score := 0.0
			// title: share of the track title's informative words in the video title
			var tw []string
			for _, w := range words(t.Title) {
				if !noise[w] && !stopWord[w] {
					tw = append(tw, w)
				}
			}
			if len(tw) > 0 {
				hit := 0
				for _, w := range tw {
					if vset[w] {
						hit++
					}
				}
				if f := float64(hit) / float64(len(tw)); f >= 0.5 {
					score += 2 * f
				}
			}
			// position: "A1" as a word, or the side letter when that side has one track
			if pos := strings.ToLower(strings.TrimSpace(t.Position)); pos != "" {
				if vset[pos] {
					score += 1.5
				} else if s := side(t.Position); s != "" && s != pos && sides[s] == 1 && vset[s] {
					score += 1
				}
			}
			// duration
			if d := parseDuration(t.Duration); d > 0 && v.Duration > 0 && score > 0 {
				switch diff := absInt(d - v.Duration); {
				case diff <= 8:
					score += 0.5
				case diff > 45:
					score = 0 // a different version or edit
				}
			}
			if score > 0 {
				pairs = append(pairs, pair{vi, ti, score})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
	out := map[int]int{}
	usedT := map[int]bool{}
	for _, p := range pairs {
		if _, done := out[p.v]; !done && !usedT[p.t] {
			out[p.v], usedT[p.t] = p.t, true
		}
	}
	// Pair the leftovers in order when the counts agree.
	var lv, lt []int
	for vi := range vids {
		if _, ok := out[vi]; !ok {
			lv = append(lv, vi)
		}
	}
	for ti := range tracks {
		if !usedT[ti] {
			lt = append(lt, ti)
		}
	}
	if len(lv) == len(lt) {
		for i := range lv {
			out[lv[i]] = lt[i]
		}
	}
	return out
}

var stopWord = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true, "mix": true,
	"original": true, "remix": true, "untitled": true, "track": true, "feat": true, "ft": true, "vs": true}

// words splits s into lower-case letter/digit runs.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// side is the letter part of a vinyl position: "B2" → "b", "AA" → "aa", "3" → "".
func side(pos string) string {
	pos = strings.ToLower(strings.TrimSpace(pos))
	i := strings.IndexFunc(pos, func(r rune) bool { return !unicode.IsLetter(r) })
	if i < 0 {
		return pos
	}
	return pos[:i]
}

// parseDuration reads Discogs's "m:ss" or "h:mm:ss" into seconds (0 if unknown).
func parseDuration(s string) int64 {
	var total int64
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	for _, p := range parts {
		n := int64(0)
		if p == "" {
			return 0
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return 0
			}
			n = n*10 + int64(r-'0')
		}
		total = total*60 + n
	}
	return total
}

func absInt(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
