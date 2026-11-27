// Package discogs talks to the Discogs API with the user's personal access token and
// turns release JSON into label data.
//
// The client (client.go) sends `Authorization: Discogs token=…` to api.discogs.com only,
// paces requests with a token bucket sized from Discogs' published rate limits and its
// X-Discogs-Ratelimit headers (limiter.go), holds off after HTTP 429 and caches response
// bodies in SQLite. Cover images come from i.discogs.com without the token. Map
// (mapping.go) applies docs/DISCOGS-MAPPING.md.
package discogs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Ref kinds.
const (
	KindRelease = "release"
	KindMaster  = "master"
)

// Ref is a release or master a pasted text points at.
type Ref struct {
	Kind string `json:"kind"` // "release" or "master"
	ID   int64  `json:"id"`
}

// URL is the Discogs web page of the reference.
func (r Ref) URL() string { return fmt.Sprintf("https://www.discogs.com/%s/%d", r.Kind, r.ID) }

var (
	bareRef = regexp.MustCompile(`(?i)^\[?\s*([rm])?\s*(\d{1,10})\s*\]?$`)
	urlRef  = regexp.MustCompile(`(?i)(?:^|/)(master|release)s?/(\d{1,10})(?:\b|$)`)
	scheme  = regexp.MustCompile(`(?i)^[a-z]+://`)
)

// ParseRef reads what people paste (docs/DISCOGS-MAPPING.md "Reference parsing"): a bare
// id, optionally prefixed r/m and in brackets ("118638", "r118638", "[m4589]"), or a
// discogs.com URL in any of its forms, including api.discogs.com/releases/<id>. When the
// text holds several links, the first one counts. ok is false when text names neither.
func ParseRef(text string) (Ref, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Ref{}, false
	}
	if m := bareRef.FindStringSubmatch(text); m != nil {
		n, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || n <= 0 {
			return Ref{}, false
		}
		kind := KindRelease
		if strings.EqualFold(m[1], "m") {
			kind = KindMaster
		}
		return Ref{Kind: kind, ID: n}, true
	}
	if !strings.Contains(strings.ToLower(text), "discogs") {
		return Ref{}, false
	}
	path := scheme.ReplaceAllString(text, "")
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if i := strings.IndexByte(path, '#'); i >= 0 {
		path = path[:i]
	}
	pick := urlRef.FindStringSubmatch(path)
	if pick == nil {
		return Ref{}, false
	}
	n, err := strconv.ParseInt(pick[2], 10, 64)
	if err != nil || n <= 0 {
		return Ref{}, false
	}
	kind := KindRelease
	if strings.EqualFold(pick[1], KindMaster) {
		kind = KindMaster
	}
	return Ref{Kind: kind, ID: n}, true
}

// ReleaseURL is the Discogs web page of a release.
func ReleaseURL(id int64) string { return fmt.Sprintf("https://www.discogs.com/release/%d", id) }
