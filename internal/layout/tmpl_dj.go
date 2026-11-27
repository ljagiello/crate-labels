package layout

import (
	"image"
	"math"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// DJ labels go on the sleeve so a record can be picked from the crate by tempo and key.
// They share one grammar: a heading (artist, release title, facts and the QR code), then
// one band per track with its position, title and badges. The single-track label folds
// the heading and the band together, because a 30 mm label has room for one of each.

// trackArtist is the artist printed for a track: its own (on compilations), else the
// release's.
func trackArtist(d *LabelData, t *Track) string {
	if t != nil {
		if a := StripDiscogsSuffix(t.Artist); a != "" {
			return a
		}
	}
	return artistName(d)
}

// tempoBadges are a track's tempo and key badges.
func tempoBadges(t *Track, o opts, name string) []badgeSpec {
	if t == nil {
		return []badgeSpec{{style: badgeSolid}, {style: badgeOutline}}
	}
	return []badgeSpec{
		{text: tempoText(t, o.bpmLow()), style: badgeSolid, name: name + "bpm"},
		{text: KeyDisplay(t.Key, o.notation()), style: badgeOutline, name: name + "key"},
	}
}

func badgeTexts(specs []badgeSpec) []string {
	out := make([]string, len(specs))
	for i, b := range specs {
		out[i] = b.text
	}
	return out
}

func hasText(specs []badgeSpec) bool {
	for _, b := range specs {
		if b.text != "" {
			return true
		}
	}
	return false
}

func render(t Template, d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return sketch(t, d, c, s, o).image(), nil
}

// --- dj-single -------------------------------------------------------------------------

// djSingle is one track on a small sticker (40×30, 50×30, Avery 5160).
type djSingle struct{}

func (djSingle) ID() string   { return "dj-single" }
func (djSingle) Name() string { return "DJ label, one track" }
func (djSingle) Description() string {
	return "Track title and artist over tempo and key badges, with the side, style, catalogue number and an optional QR code."
}

func (djSingle) Supports(s *printer.Stock) bool {
	long, short, wide := stockShape(s)
	return !s.Round && wide && 36 <= long && long <= 72 && 20 <= short && short <= 34
}

func (djSingle) Options() []OptionSpec { return djOptions(true, true) }
func (djSingle) MaxTracks() int        { return 1 }

func (t djSingle) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

func (djSingle) draw(sh *sheet, g grid, d *LabelData, _ *printer.Stock, o opts) {
	fc := o.faces()
	tr := pickTrack(d, o.text("track", ""))
	c := g.content
	gap := g.gutter
	title := strings.TrimSpace(d.Title)
	position := ""
	if tr != nil {
		if tt := strings.TrimSpace(tr.Title); tt != "" {
			title = tt
		}
		position = strings.TrimSpace(tr.Position)
	}
	badges := tempoBadges(tr, o, "")
	facts := joinFacts(position, styleLine(d, 1), d.Catno, d.Year)
	url := qrLink(d, o)

	// On a wide label (Avery 5160) the code takes a full-height column at the right; on a
	// compact one it sits in the lower band.
	if wide := c.w >= 2.2*c.h; wide && url != "" {
		if q := sh.codeSide(url, c.h); q > 0 {
			col, rest := c.cutRight(q, gap)
			sh.code(url, col.x, col.y+(col.h-q)/2, q, "qr")
			c, url = rest, ""
		}
	}

	// The lower band holds the badges over a facts line; the upper area takes whatever
	// height is left for the title and artist.
	factsSize := g.scale.step(-1)
	factsH := blockHeight(fc.regular, factsSize, 1)
	lowH := 0.0
	if hasText(badges) {
		lowH = 0.4 * c.h
	} else if facts != "" {
		lowH = factsH
	}
	q := 0.0
	if url != "" {
		q = sh.codeSide(url, math.Min(0.46*c.h, 0.34*c.w))
		lowH = math.Max(lowH, q)
	}
	top := c
	var low rect
	if lowH > 0 {
		low, top = c.cutBottom(lowH, gap)
	}
	if q > 0 {
		col, rest := low.cutRight(q, gap)
		sh.code(url, col.x, col.bottom()-q, q, "qr")
		low = rest
	}
	if facts != "" {
		line, rest := low.cutBottom(factsH, gap/2)
		sh.textIn(lineAt(facts, line.w, line.h, fc.regular, factsSize, alignLeft), line, 0, "facts")
		low = rest
	}
	if hasText(badges) && !low.empty() {
		if cap := fitBadges(fc.bold, badgeTexts(badges), low.w, low.h, gap, g.scale.upTo(5)*fc.bold.capHeight); cap > 0 {
			sh.badgeRow(fc.bold, badges, low.x, low.bottom()-badgeHeightPerCap*cap, cap, gap, false)
		}
	}

	// Title over artist, both as large as the area allows.
	artist := trackArtist(d, tr)
	titleArea := top
	if artist != "" {
		titleArea.h = 0.64 * top.h
	}
	tb := fitText(title, titleArea.w, titleArea.h, fitSpec{face: fc.regular, max: g.scale.upTo(5), min: g.scale.downTo(1), lines: 2})
	box := sh.textIn(tb, titleArea, 0, "title")
	if artist != "" {
		y := top.y
		if !tb.empty() {
			y = box.bottom() + gap/2
		}
		artistArea := rect{top.x, y, top.w, top.bottom() - y}
		ab := fitText(artist, artistArea.w, artistArea.h, fitSpec{face: fc.bold, max: g.scale.upTo(3), min: g.scale.downTo(1), lines: 1})
		sh.textIn(ab, artistArea, 0, "artist")
	}
}

// --- dj-two-track and dj-four-track ------------------------------------------------------

// djBands is a heading over n track bands.
type djBands struct{ n int }

type (
	djTwoTrack  struct{ djBands }
	djFourTrack struct{ djBands }
)

func newTwoTrack() djTwoTrack   { return djTwoTrack{djBands{2}} }
func newFourTrack() djFourTrack { return djFourTrack{djBands{4}} }

func (djTwoTrack) ID() string   { return "dj-two-track" }
func (djTwoTrack) Name() string { return "DJ label, two tracks" }
func (djTwoTrack) Description() string {
	return "Artist, release and facts over two track bands with tempo and key badges (80×48, 2″×4″)."
}

func (djTwoTrack) Supports(s *printer.Stock) bool {
	long, short, wide := stockShape(s)
	return !s.Round && wide && 72 <= long && long <= 110 && 40 <= short && short <= 60 &&
		1.5 <= long/short && long/short <= 2.2
}

func (djFourTrack) ID() string   { return "dj-four-track" }
func (djFourTrack) Name() string { return "DJ label, four tracks" }
func (djFourTrack) Description() string {
	return "Artist, release and facts over four track bands with tempo and key badges (70×80, 4×6″)."
}

func (djFourTrack) Supports(s *printer.Stock) bool {
	long, short, wide := stockShape(s)
	return !s.Round && !wide && 55 <= short && short <= 110 && long <= 160 &&
		1.1 <= long/short && long/short <= 1.6
}

func (djBands) Options() []OptionSpec { return djOptions(false, true) }
func (b djBands) MaxTracks() int      { return b.n }

func (t djTwoTrack) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

func (t djFourTrack) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

// headingShare is the part of the content height the heading takes on a banded label.
// A four-band label gives the heading less, because each band still needs room for a
// two-line title.
func (b djBands) headingShare() float64 {
	if b.n > 2 {
		return 0.24
	}
	return 0.34
}

func (b djBands) draw(sh *sheet, g grid, d *LabelData, _ *printer.Stock, o opts) {
	fc := o.faces()
	c := g.content
	gap := g.gutter
	tracks := includedTracks(d)
	if len(tracks) == 0 {
		tracks = d.Tracks
	}
	tracks = tracks[:min(len(tracks), b.n)]

	headH := c.h
	if len(tracks) > 0 {
		headH = b.headingShare() * c.h
	}
	head, body := c.cutTop(headH, gap)
	drawHeading(sh, g, fc, d, o, head)
	if len(tracks) == 0 {
		return
	}
	// Bands keep the height they would have with every track present, so labels from one
	// crate line up whatever their track count.
	trackBands(sh, g, fc, tracks, o, bandAreas(sh, g, body, b.n, len(tracks)))
}

// drawHeading sets the artist, the release title and a facts line, with the QR code at
// the right of the area.
func drawHeading(sh *sheet, g grid, fc faces, d *LabelData, o opts, area rect) {
	gap := g.gutter
	if url := qrLink(d, o); url != "" {
		if q := sh.codeSide(url, math.Min(area.h, 0.3*area.w)); q > 0 {
			col, rest := area.cutRight(q, gap)
			sh.code(url, col.x, col.y, q, "qr")
			area = rest
		}
	}
	facts := joinFacts(labelName(d), d.Catno, d.Year, styleLine(d, 2))
	factsSize := g.scale.step(0)
	if facts != "" {
		line, rest := area.cutBottom(blockHeight(fc.regular, factsSize, 1), gap/2)
		sh.textIn(lineAt(facts, line.w, line.h, fc.regular, factsSize, alignLeft), line, 1, "facts")
		area = rest
	}
	artist := artistName(d)
	title := strings.TrimSpace(d.Title)
	split := 0.5
	if title == "" {
		split = 1
	}
	artistArea, titleArea := area.cutTop(split*area.h, 0)
	ab := fitText(artist, artistArea.w, artistArea.h, fitSpec{face: fc.bold, max: g.scale.upTo(4), min: g.scale.downTo(1), lines: 2})
	box := sh.textIn(ab, artistArea, 0, "artist")
	if title != "" {
		if !ab.empty() {
			titleArea = rect{area.x, box.bottom() + gap/2, area.w, area.bottom() - box.bottom() - gap/2}
		}
		tb := fitText(title, titleArea.w, titleArea.h, fitSpec{face: fc.regular, max: g.scale.upTo(2), min: g.scale.downTo(1), lines: 2})
		sh.textIn(tb, titleArea, 0, "release")
	}
}

// trackBands lays out tracks in the given areas (one per track, areas already inside
// their rules) as a set: every band uses the same badge size, position column and title
// size, so the bands read as one table. A band
// at least stackRatio times as wide as it is tall puts its badges at the right of the
// title; a narrower one stacks them under it.
func trackBands(sh *sheet, g grid, fc faces, tracks []Track, o opts, areas []rect) {
	n := min(len(tracks), len(areas))
	if n == 0 {
		return
	}
	gap := g.gutter
	bgap := gap / 2
	stacked := areas[0].w < stackRatio*areas[0].h
	titleMax := g.scale.upTo(3)
	titleMin := g.scale.downTo(1)
	badges := make([][]badgeSpec, n)

	// One position column, wide enough for the widest position.
	posSize := math.Min(titleMax, areas[0].h/(fc.bold.capHeight+fc.bold.descent))
	if stacked {
		posSize = math.Min(titleMax, 0.5*areas[0].h/(fc.bold.capHeight+fc.bold.descent))
	}
	posW := 0.0
	for _, t := range tracks[:n] {
		posW = math.Max(posW, fc.bold.width(strings.TrimSpace(t.Position), posSize))
	}
	if limit := 0.25 * areas[0].w; posW > limit {
		posSize *= limit / posW
		posW = limit
	}
	posGap := 0.0
	if posW > 0 {
		posGap = posW + gap
	}

	// One badge size: the smallest that every band's badges need.
	capB := math.Inf(1)
	for i := range n {
		a := areas[i]
		badges[i] = tempoBadges(&tracks[i], o, "track"+string(rune('1'+i))+".")
		if !hasText(badges[i]) {
			continue
		}
		capMax := g.scale.upTo(3) * fc.bold.capHeight
		w, h := badgeShare*a.w, a.h
		if stacked {
			w, h = a.w-posGap, stackBadgeShare*a.h
		}
		capB = math.Min(capB, fitBadges(fc.bold, badgeTexts(badges[i]), w, h, bgap, math.Min(capMax, h/badgeHeightPerCap)))
	}
	if math.IsInf(capB, 1) {
		capB = 0
	}

	// Each title's lane, then one size for all of them.
	lanes := make([]rect, n)
	for i := range n {
		lane := areas[i]
		if posW > 0 {
			_, lane = lane.cutLeft(posW, gap)
		}
		if capB > 0 && hasText(badges[i]) {
			if stacked {
				lane.h = math.Max(0, lane.h-badgeHeightPerCap*capB-bgap)
			} else {
				_, lane = lane.cutRight(badgeRowWidth(fc.bold, badgeTexts(badges[i]), capB, bgap), gap)
			}
		}
		lanes[i] = lane
	}
	// The shared size is the smallest any title needs, but at most one step of the type
	// scale below the largest: a title that needs more than that is cut with "…" rather
	// than shrinking every band to its size.
	smallest, largest := titleMax, 0.0
	for i := range n {
		tb := fitText(tracks[i].Title, lanes[i].w, lanes[i].h, fitSpec{face: fc.regular, max: titleMax, min: titleMin, lines: 2})
		if !tb.empty() {
			smallest, largest = math.Min(smallest, tb.size), math.Max(largest, tb.size)
		}
	}
	size := math.Max(smallest, math.Max(titleMin, largest/typeRatio))

	for i := range n {
		a, name := areas[i], "track"+string(rune('1'+i))+"."
		tb := fitText(tracks[i].Title, lanes[i].w, lanes[i].h, fitSpec{face: fc.regular, max: size, min: size, lines: 2})
		var titleBox rect
		if stacked {
			titleBox = sh.textIn(tb, lanes[i], 0, name+"title")
		} else {
			titleBox = sh.textIn(tb, lanes[i], 0.5, name+"title")
		}
		if pos := strings.TrimSpace(tracks[i].Position); pos != "" && posW > 0 {
			pb := textBlock{face: fc.bold, size: posSize, lines: []string{pos}}
			top := a.y + (a.h-pb.height())/2
			if stacked || tb.empty() {
				top = a.y
			} else {
				// Share the first line's cap line with the title.
				top = titleBox.y + tb.size*fc.regular.capHeight - posSize*fc.bold.capHeight
			}
			sh.text(pb, a.x, top, name+"position")
		}
		if capB > 0 && hasText(badges[i]) {
			if stacked {
				y := a.bottom() - badgeHeightPerCap*capB
				if !tb.empty() {
					y = math.Min(y, titleBox.bottom()+bgap)
				}
				sh.badgeRow(fc.bold, badges[i], lanes[i].x, y, capB, bgap, false)
			} else {
				sh.badgeRow(fc.bold, badges[i], a.right(), a.y+(a.h-badgeHeightPerCap*capB)/2, capB, bgap, true)
			}
		}
	}
}

// Band proportions: badges take at most 40 % of a side-by-side band's width or of a
// stacked band's height; bands narrower than 2.6 times their height stack.
const (
	badgeShare      = 0.4
	stackBadgeShare = 0.4
	stackRatio      = 2.6
)

// bandAreas divides area into n bands of equal height with a rule above each, and
// returns the space inside each band.
func bandAreas(sh *sheet, g grid, area rect, n, count int) []rect {
	bands := area.rows(n, 0)
	out := make([]rect, 0, count)
	for i := range min(n, count) {
		b := bands[i]
		sh.hline(b.x, b.right(), b.y, minStrokeMM, "rule")
		out = append(out, rect{b.x, b.y + g.gutter/2 + minStrokeMM, b.w, b.h - g.gutter - minStrokeMM})
	}
	return out
}

// --- dj-tape ---------------------------------------------------------------------------

// djTape is one track on 12 mm tape (Phomemo D30 rolls and 15×50 die-cuts).
type djTape struct{}

func (djTape) ID() string   { return "dj-tape" }
func (djTape) Name() string { return "DJ tape label" }
func (djTape) Description() string {
	return "One line for a 12 mm tape: tempo and key badges, then artist over title, and an optional QR code."
}

func (djTape) Supports(s *printer.Stock) bool {
	long, short, wide := stockShape(s)
	return !s.Round && wide && 8 <= short && short <= 16 && long >= 2.5*short
}

// Options: the code defaults to off here, because on 12 mm tape it can only use the
// smallest module.
func (djTape) Options() []OptionSpec { return djOptions(true, false) }
func (djTape) MaxTracks() int        { return 1 }

func (t djTape) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

func (djTape) draw(sh *sheet, g grid, d *LabelData, _ *printer.Stock, o opts) {
	fc := o.faces()
	tr := pickTrack(d, o.text("track", ""))
	c := g.content
	gap := g.gutter
	if url := qrLink(d, o); url != "" {
		if q := sh.codeSide(url, c.h); q > 0 {
			col, rest := c.cutRight(q, gap)
			sh.code(url, col.x, col.y+(col.h-q)/2, q, "qr")
			c = rest
		}
	}
	badges := tempoBadges(tr, o, "")
	if hasText(badges) {
		if cap := fitBadges(fc.bold, badgeTexts(badges), 0.5*c.w, c.h, gap/2, c.h/badgeHeightPerCap); cap > 0 {
			row := sh.badgeRow(fc.bold, badges, c.x, c.y+(c.h-badgeHeightPerCap*cap)/2, cap, gap/2, false)
			_, c = c.cutLeft(row.w, gap)
		}
	}
	title := strings.TrimSpace(d.Title)
	if tr != nil && strings.TrimSpace(tr.Title) != "" {
		title = tr.Title
	}
	artist := trackArtist(d, tr)
	top, bottom := c.cutTop(c.h/2, 0)
	if artist == "" || title == "" {
		top, bottom = c, c
	}
	// On tape the names never shrink below step 0: a longer name is cut short rather than
	// set too small to read on a 12 mm strip.
	if artist != "" {
		sh.textIn(oneLine(artist, top.w, top.h, fc.bold, top.h, g.scale.step(0), alignLeft), top, 0.5, "artist")
	}
	if title != "" {
		sh.textIn(oneLine(title, bottom.w, bottom.h, fc.regular, bottom.h, g.scale.step(0), alignLeft), bottom, 0.5, "title")
	}
}
