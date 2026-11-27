package layout

import (
	"image"
	"math"
	"strings"
	"unicode"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// The centre label is a paper disc for the middle of a record, printed on a 4×6″ label
// with a sleeve sticker for the same side underneath. All text on the disc is set
// horizontally in bands, each as wide as the circle allows at its far edge: names above
// the hole, the side letter and speed beside it, the side's tracks below.

// Physical sizes. A 7″ single's centre label is about 3½″ across and a 12″'s about 4″;
// the jukebox hole of a 7″ is 1½″ and a standard spindle hole about 7.2 mm.
const (
	disc7MM     = 90.0
	disc12MM    = 100.0
	jukeboxHole = 38.0
	spindleHole = 7.2
)

// disc is a centre label's circle and hole, in mm on the label.
type disc struct{ cx, cy, d, hole float64 }

func (dc disc) r() float64 { return dc.d / 2 }

// placeDisc places a disc of diameter d with hole h on a w × ht label: centred both
// ways, because the disc is cut from plain stock by hand, unless the stock's extra gives
// disc_top_mm (the distance from the label's top edge to the disc).
func placeDisc(s *printer.Stock, w, ht, d, h float64) disc {
	cy := ht / 2
	if top, ok := extraFloat(s.Extra, "disc_top_mm"); ok {
		cy = top + d/2
	}
	return disc{cx: w / 2, cy: cy, d: d, hole: h}
}

// discFromStock is the disc a round stock is made for: its disc_mm and hole_mm, else a
// 7″ label with the jukebox hole.
func discFromStock(s *printer.Stock, w, ht float64) disc {
	d, ok := extraFloat(s.Extra, "disc_mm")
	if !ok {
		d = disc7MM
	}
	h, ok := extraFloat(s.Extra, "hole_mm")
	if !ok {
		h = jukeboxHole
	}
	return placeDisc(s, w, ht, d, h)
}

func extraFloat(extra map[string]any, key string) (float64, bool) {
	switch v := extra[key].(type) {
	case float64:
		return v, v > 0
	case int64:
		return float64(v), v > 0
	case int:
		return float64(v), v > 0
	}
	return 0, false
}

// Disc choices of the center_format option.
const (
	discAuto    = "auto"
	disc7Large  = "7-38"
	disc7Small  = "7-7"
	disc12Small = "12"
)

// chooseDisc resolves the center_format option: an explicit choice, else the record's
// size (12″ and LPs get the 12″ label, 10″ the 7″ size with a spindle hole), else the
// stock's own disc.
func chooseDisc(choice string, d *LabelData, s *printer.Stock, w, ht float64) disc {
	stock := discFromStock(s, w, ht)
	switch choice {
	case disc7Large:
		return placeDisc(s, w, ht, stock.d, jukeboxHole)
	case disc7Small:
		return placeDisc(s, w, ht, stock.d, spindleHole)
	case disc12Small:
		return placeDisc(s, w, ht, disc12MM, spindleHole)
	}
	format := strings.ToUpper(FormatDisplay(d.Format))
	switch {
	case strings.HasPrefix(format, "12") || strings.Contains(format, "LP"):
		return placeDisc(s, w, ht, disc12MM, spindleHole)
	case strings.HasPrefix(format, "10"):
		return placeDisc(s, w, ht, disc7MM, spindleHole)
	}
	return stock
}

// Speed choices of the speed option.
var speedLabels = map[string]string{"33": "33⅓", "45": "45", "78": "78"}

// speedOf resolves the speed option: an explicit choice, a speed written in the format,
// else 45 for a 7″ and 33⅓ for everything else.
func speedOf(choice string, d *LabelData) string {
	if choice == "off" {
		return ""
	}
	if s, ok := speedLabels[choice]; ok {
		return s
	}
	format := strings.ToUpper(d.Format)
	for _, sp := range []string{"78", "45", "33"} {
		if strings.Contains(format, sp) {
			return speedLabels[sp]
		}
	}
	if strings.HasPrefix(FormatDisplay(d.Format), "7″") {
		return speedLabels["45"]
	}
	return speedLabels["33"]
}

// sideLetters is a track position's leading letters, upper-cased: "b2" → "B", "AA" →
// "AA", "3" → "".
func sideLetters(position string) string {
	p := strings.TrimSpace(position)
	end := 0
	for end < len(p) && p[end] < 0x80 && unicode.IsLetter(rune(p[end])) {
		end++
	}
	return strings.ToUpper(p[:end])
}

// sideTracks picks the side to print and its tracks. A typed side that some track is on
// wins; otherwise the first side on the record. Records without lettered positions print
// every included track under the typed side letter, if any.
func sideTracks(d *LabelData, typed string) (string, []Track) {
	tracks := includedTracks(d)
	if len(tracks) == 0 {
		tracks = d.Tracks
	}
	want := strings.ToUpper(strings.TrimSpace(typed))
	first := ""
	for _, t := range tracks {
		if s := sideLetters(t.Position); s != "" && first == "" {
			first = s
		}
	}
	if first == "" {
		return want, tracks
	}
	pick := first
	for _, t := range tracks {
		if want != "" && sideLetters(t.Position) == want {
			pick = want
			break
		}
	}
	var out []Track
	for _, t := range tracks {
		if sideLetters(t.Position) == pick {
			out = append(out, t)
		}
	}
	return pick, out
}

// chordRect is the widest rectangle spanning [y0, y1] inside a circle of radius r
// around (cx, cy): its width is the chord at the band's edge farther from the centre.
func chordRect(cx, cy, r, y0, y1 float64) rect {
	far := math.Max(math.Abs(y0-cy), math.Abs(y1-cy))
	if y0 < cy && y1 > cy {
		far = math.Max(cy-y0, y1-cy)
	}
	if far >= r || y1 <= y0 {
		return rect{cx, y0, 0, math.Max(0, y1-y0)}
	}
	half := math.Sqrt(r*r - far*far)
	return rect{cx - half, y0, 2 * half, y1 - y0}
}

type centerLabel struct{}

func (centerLabel) ID() string   { return "center-label" }
func (centerLabel) Name() string { return "7″ centre label" }
func (centerLabel) Description() string {
	return "A disc for the middle of a 7″ or 12″ with names, side, speed and the side's tracks, and a matching sleeve sticker below (round 4×6″ stock)."
}

func (centerLabel) Supports(s *printer.Stock) bool {
	return s.Round && s.DesignMM[0] >= disc12MM && s.DesignMM[1] >= disc12MM+2*marginMin
}

func (centerLabel) Options() []OptionSpec {
	return []OptionSpec{
		choiceOption("center_format", "Disc", discAuto,
			[]string{discAuto, disc7Large, disc7Small, disc12Small},
			[]string{"From the record and stock", "7″, 38 mm jukebox hole", "7″, 7 mm spindle hole", "12″, 7 mm spindle hole"}, ""),
		choiceOption("speed", "Speed", "auto", []string{"auto", "33", "45", "78", "off"},
			[]string{"From the format", "33⅓ RPM", "45 RPM", "78 RPM", "Hidden"}, ""),
		textOption("side", "Side", "The side to print (A, B…). Blank picks the first side."),
		boolOption("show_strip", "Sleeve sticker under the disc", true),
		qrOption(true),
		keyNotationOption(),
		bpmRangeOption(),
		fontOption(),
		fontScaleOption(),
	}
}

func (t centerLabel) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

func (centerLabel) draw(sh *sheet, g grid, d *LabelData, s *printer.Stock, o opts) {
	fc := o.faces()
	dc := chooseDisc(o.text("center_format", discAuto), d, s, g.w, g.h)
	side, tracks := sideTracks(d, o.text("side", ""))
	drawDisc(sh, fc, dc, d, o, side, tracks)

	if !o.flag("show_strip", true) {
		return
	}
	// The sticker starts a gutter and a half below the disc: room for two separate cuts.
	top := dc.cy + dc.r() + 1.5*g.gutter
	strip := rect{g.margin, top, g.w - 2*g.margin, g.h - g.margin - top}
	if strip.h < stickerMinMM {
		return
	}
	// The sticker is cut out along a dashed line and laid out like a DJ label of its size:
	// a heading, and the side's track bands when there is room for them.
	sg := newGrid(strip.w, strip.h, o.userScale())
	drawCutFrame(sh, strip)
	inner := strip.inset(sg.margin, sg.margin)
	sg.content = inner
	shown := tracks[:min(len(tracks), 4)]
	if inner.h < stickerBandsMM {
		shown = nil
	}
	head, body := inner.cutTop(0.34*inner.h, sg.gutter)
	if len(shown) == 0 {
		head = inner
	}
	drawHeading(sh, sg, fc, d, o, head)
	trackBands(sh, sg, fc, shown, o, bandAreas(sh, sg, body, len(shown), len(shown)))
}

// Sleeve sticker thresholds: under 15 mm there is no room for a readable heading, and
// track bands need 30 mm (a heading plus two bands of a two-line title).
const (
	stickerMinMM   = 15.0
	stickerBandsMM = 30.0
)

// Cut lines are dashed 2 mm on, 1.2 mm off: long enough to follow with scissors, with
// gaps that tell them apart from printed rules.
const (
	cutDash = 2.0
	cutGap  = 1.2
)

// drawCutFrame dashes the outline of a piece to cut out.
func drawCutFrame(sh *sheet, r rect) {
	w := minStrokeMM
	sh.dashH(r.x, r.right(), r.y, w, cutDash, cutGap, "cut")
	sh.dashH(r.x, r.right(), r.bottom()-w, w, cutDash, cutGap, "cut")
	sh.dashV(r.x, r.y, r.bottom(), w, cutDash, cutGap, "cut")
	sh.dashV(r.right()-w, r.y, r.bottom(), w, cutDash, cutGap, "cut")
}

// drawDisc draws the cut and hole guides and the disc's text.
func drawDisc(sh *sheet, fc faces, dc disc, d *LabelData, o opts, side string, tracks []Track) {
	sh.ring(dc.cx, dc.cy, dc.d, minStrokeMM, cutDash, cutGap, "disc")
	if dc.hole >= 20 {
		sh.ring(dc.cx, dc.cy, dc.hole, minStrokeMM, cutDash, cutGap, "hole")
	} else {
		sh.ring(dc.cx, dc.cy, dc.hole, minStrokeMM, 0, 0, "hole")
		arm := dc.hole/2 + 1.5
		sh.hline(dc.cx-arm, dc.cx+arm, dc.cy-minStrokeMM/2, minStrokeMM, "crosshair")
		sh.vline(dc.cx-minStrokeMM/2, dc.cy-arm, dc.cy+arm, minStrokeMM, "crosshair")
	}

	// The disc gets its own grid: margin from the rim, type scale from its radius.
	dg := newGrid(dc.r(), dc.r(), o.userScale())
	dg.scale = newTypeScale(dc.d*0.5, o.userScale())
	outer := dc.r() - dg.margin
	inner := dc.hole/2 + dg.gutter*1.5
	gap := dg.gutter / 2
	band := func(y0, y1 float64) rect {
		r := chordRect(dc.cx, dc.cy, outer, y0, y1)
		return r.inset(gap, 0)
	}

	// Above the hole: label name at the rim, then the artist, then the release title.
	upTop, upBottom := dc.cy-outer, dc.cy-inner
	if dc.hole < 20 {
		upBottom = dc.cy - math.Max(inner, 0.2*outer)
	}
	upH := upBottom - upTop
	label := band(upTop+0.06*upH, upTop+0.3*upH)
	artist := band(upTop+0.3*upH, upTop+0.66*upH)
	title := band(upTop+0.66*upH, upBottom-gap)
	sh.textIn(lineAt(labelName(d), label.w, label.h, fc.bold, dg.scale.upTo(1), alignCenter), label, 0.5, "label")
	sh.textIn(fitText(artistName(d), artist.w, artist.h, fitSpec{face: fc.bold, max: dg.scale.upTo(4), min: dg.scale.downTo(2), lines: 2, align: alignCenter}), artist, 0.5, "artist")
	sh.textIn(fitText(d.Title, title.w, title.h, fitSpec{face: fc.regular, max: dg.scale.upTo(2), min: dg.scale.downTo(1), lines: 2, align: alignCenter}), title, 0.5, "release")

	// Beside the hole: the side letter on the left, the speed on the right.
	midTop, midBottom := upBottom+gap, dc.cy+(dc.cy-upBottom)-gap
	mid := chordRect(dc.cx, dc.cy, outer, midTop, midBottom)
	holeEdge := dc.hole/2 + dg.gutter
	leftArea := rect{mid.x, midTop, dc.cx - holeEdge - mid.x, midBottom - midTop}
	rightArea := rect{dc.cx + holeEdge, midTop, mid.right() - dc.cx - holeEdge, midBottom - midTop}
	if side != "" && leftArea.w > 0 {
		sb := lineAt(side, leftArea.w, leftArea.h, fc.bold, dg.scale.upTo(7), alignCenter)
		sh.textIn(sb, leftArea, 0.5, "side")
	}
	if speed := speedOf(o.text("speed", "auto"), d); speed != "" && rightArea.w > 0 {
		number, unit := rightArea.cutTop(0.62*rightArea.h, 0)
		nb := lineAt(speed, number.w, number.h, fc.bold, dg.scale.upTo(5), alignCenter)
		box := sh.textIn(nb, number, 1, "speed")
		ub := lineAt("RPM", unit.w, unit.h, fc.regular, dg.scale.upTo(0), alignCenter)
		sh.textIn(ub, rect{unit.x, box.bottom() + gap/2, unit.w, unit.bottom() - box.bottom() - gap/2}, 0, "rpm")
	}

	// Below the hole: the side's tracks, then the catalogue number and year at the rim.
	lowTop, lowBottom := midBottom+gap, dc.cy+outer
	lowH := lowBottom - lowTop
	factsBand := band(lowTop+0.7*lowH, lowTop+0.94*lowH)
	sh.textIn(lineAt(joinFacts(d.Catno, d.Year), factsBand.w, factsBand.h, fc.regular, dg.scale.upTo(0), alignCenter), factsBand, 0.5, "facts")
	// As many tracks as fit at step 1 of the disc's scale.
	room := 0.7*lowH - gap
	fit := int((room + gap/2) / (blockHeight(fc.regular, dg.scale.step(1), 1) + gap/2))
	shown := tracks[:min(len(tracks), max(0, fit))]
	if len(shown) == 0 {
		return
	}
	rows := rect{0, lowTop, 0, room}.rows(len(shown), gap/2)
	for i, tr := range shown {
		row := band(rows[i].y, rows[i].bottom())
		drawDiscTrack(sh, dg, fc, &tr, o, row, i)
	}
}

// drawDiscTrack sets one track line on the disc: position and title, centred, with small
// tempo and key badges after them.
func drawDiscTrack(sh *sheet, g grid, fc faces, tr *Track, o opts, row rect, i int) {
	name := "disc" + string(rune('1'+i)) + "."
	badges := tempoBadges(tr, o, name)
	gap := g.gutter / 2
	text := strings.TrimSpace(tr.Title)
	if p := strings.TrimSpace(tr.Position); p != "" {
		text = p + "  " + text
	}
	capB := 0.0
	bw := 0.0
	if hasText(badges) {
		capB = fitBadges(fc.bold, badgeTexts(badges), 0.4*row.w, row.h, gap, math.Min(row.h/badgeHeightPerCap, g.scale.upTo(1)*fc.bold.capHeight))
		if capB > 0 {
			bw = badgeRowWidth(fc.bold, badgeTexts(badges), capB, gap) + gap*2
		}
	}
	tb := lineAt(text, row.w-bw, row.h, fc.regular, g.scale.upTo(2), alignLeft)
	tw := tb.inkWidth()
	x := row.x + (row.w-tw-bw)/2
	box := sh.textIn(tb, rect{x, row.y, tw, row.h}, 0.5, name+"title")
	if capB > 0 {
		sh.badgeRow(fc.bold, badges, box.right()+gap*2, row.y+(row.h-badgeHeightPerCap*capB)/2, capB, gap, false)
	}
}
