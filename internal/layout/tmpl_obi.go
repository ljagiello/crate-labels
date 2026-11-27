package layout

import (
	"image"
	"math"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// An OBI strip wraps around the spine edge of an LP sleeve. Laid flat and seen from the
// printed side it has three panels, left to right: the back panel (it ends up on the back
// of the sleeve), the spine, and the front panel. The front names the record; the back
// carries the tracklist with tempos, the QR code and the catalogue number, so the record
// can be judged from the shelf by its spine and from the back without pulling the sleeve.

// Spine widths in mm by disc count, from measured sleeves: a single LP sleeve is about
// 3–4 mm thick, a gatefold double 6–8 mm, a triple box about 10 mm.
var spineMM = map[int]float64{1: 4, 2: 7, 3: 10}

// discsOf counts the discs a tracklist spans: two sides per disc, sides lettered A, B,
// C…, or disc numbers written as "2-5".
func discsOf(d *LabelData) int {
	discs := 1
	for _, t := range d.Tracks {
		p := strings.TrimSpace(t.Position)
		if s := sideLetters(p); s != "" {
			if i := strings.IndexByte("ABCDEFGHIJKL", s[0]); i >= 0 {
				discs = max(discs, i/2+1)
			}
			continue
		}
		if head, _, ok := strings.Cut(p, "-"); ok {
			if n, err := strconv.Atoi(head); err == nil && n > 0 && n <= 12 {
				discs = max(discs, n)
			}
		}
	}
	return discs
}

type obiStrip struct{}

func (obiStrip) ID() string   { return "obi-strip" }
func (obiStrip) Name() string { return "OBI / spine strip" }
func (obiStrip) Description() string {
	return "A strip to wrap round the sleeve's spine edge: names on the front panel, title on the spine, tracklist with tempos and QR on the back (full or mini, on plain paper)."
}

// Supports takes plain-paper strips about 3¾–4″ wide and at least as long: an OBI is
// folded round the sleeve, so it is never a sticker.
func (obiStrip) Supports(s *printer.Stock) bool {
	w, h := s.DesignMM[0], s.DesignMM[1]
	return s.Media == printer.MediaSheet && !s.Round && 90 <= w && w <= 105 && h >= 100 && h >= w
}

func (obiStrip) Options() []OptionSpec {
	return []OptionSpec{
		choiceOption("spine", "Spine width", "auto", []string{"auto", "1", "2", "3"},
			[]string{"From the tracklist's sides", "One disc (4 mm)", "Two discs (7 mm)", "Three discs (10 mm)"}, ""),
		boolOption("tracklist", "Tracklist on the back panel", true),
		boolOption("tempo", "Tempo and key in the tracklist", true),
		qrOption(true),
		boolOption("fold_lines", "Fold guides", true),
		keyNotationOption(),
		bpmRangeOption(),
		fontOption(),
		fontScaleOption(),
	}
}

func (t obiStrip) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

func (obiStrip) draw(sh *sheet, g grid, d *LabelData, _ *printer.Stock, o opts) {
	fc := o.faces()
	discs := discsOf(d)
	if n, err := strconv.Atoi(o.text("spine", "auto")); err == nil {
		discs = n
	}
	sw := spineMM[min(3, max(1, discs))]
	pw := (g.w - sw) / 2
	back := rect{0, 0, pw, g.h}
	spine := rect{pw, 0, sw, g.h}
	front := rect{pw + sw, 0, pw, g.h}

	// Each panel is a narrow column; its margin follows the column's width, but the type
	// grows with the strip's length, because a full-height strip is read from across the
	// room and a mini one from arm's length.
	pg := newGrid(pw, g.h, o.userScale())
	pg.scale.base = math.Max(typeBase, math.Min(2*typeBase, g.h/obiTypeDivisor))
	if o.flag("fold_lines", true) {
		// Short dashes at both ends of each fold, outside the text area.
		for _, x := range []float64{spine.x, spine.right()} {
			sh.dashV(x-minStrokeMM/2, 0, pg.margin, minStrokeMM, 1.5, 1, "fold")
			sh.dashV(x-minStrokeMM/2, g.h-pg.margin, g.h, minStrokeMM, 1.5, 1, "fold")
		}
	}
	drawObiFront(sh, pg, fc, d, front.inset(pg.margin, pg.margin))
	drawObiSpine(sh, pg, fc, d, spine.inset(0, pg.margin))
	drawObiBack(sh, pg, fc, d, o, back.inset(pg.margin, pg.margin))
}

// obiTypeDivisor sets an OBI's type base from its length: a full LP strip (about 311 mm)
// reaches twice the usual base, a mini strip keeps it.
const obiTypeDivisor = 75

// drawObiFront sets the names large from the top and the facts from the foot.
func drawObiFront(sh *sheet, g grid, fc faces, d *LabelData, area rect) {
	gap := g.gutter
	tall := area.h > 2.5*area.w

	// Facts first, from the bottom up, so the names get every millimetre left.
	type line struct {
		text string
		f    *face
		name string
		n    int
	}
	lines := []line{
		{styleLine(d, 4), fc.regular, "styles", 2},
		{joinFacts(FormatDisplay(d.Format), d.Year, d.Country), fc.regular, "pressing", 2},
		{labelName(d), fc.bold, "label", 2},
		{strings.TrimSpace(d.Catno), fc.regular, "catno", 1},
	}
	size := g.scale.upTo(1)
	var blocks []textBlock
	var names []string
	total := 0.0
	for _, l := range lines {
		if l.text == "" {
			continue
		}
		b := fitText(l.text, area.w, 0.12*area.h, fitSpec{face: l.f, max: size, min: g.scale.downTo(1), lines: l.n})
		blocks = append(blocks, b)
		names = append(names, l.name)
		total += b.height() + gap/2
	}
	y := area.bottom() - total + gap/2
	for i, b := range blocks {
		y = sh.text(b, area.x, y, names[i]).bottom() + gap/2
	}
	top := area
	if len(blocks) > 0 {
		ruleY := area.bottom() - total - gap/2
		sh.hline(area.x, area.right(), ruleY-minStrokeMM, minStrokeMM, "rule")
		top.h = ruleY - minStrokeMM - gap - area.y
	}

	artistLines, titleLines, step := 4, 5, 5
	if !tall {
		artistLines, titleLines, step = 2, 3, 4
	}
	artist, title := artistName(d), strings.TrimSpace(d.Title)
	next := top.y
	if artist != "" {
		share := 0.5
		if title == "" {
			share = 1
		}
		ab := fitText(artist, top.w, share*top.h, fitSpec{face: fc.bold, max: g.scale.upTo(step), min: g.scale.downTo(2), lines: artistLines})
		next = sh.text(ab, top.x, top.y, "artist").bottom() + gap
	}
	if title != "" {
		rest := rect{top.x, next, top.w, top.bottom() - next}
		tb := fitText(title, rest.w, rest.h, fitSpec{face: fc.regular, max: g.scale.upTo(step - 1), min: g.scale.downTo(1), lines: titleLines})
		sh.text(tb, rest.x, rest.y, "release")
	}
}

// drawObiSpine sets "artist · title" reading top to bottom, and the catalogue number
// at the foot.
func drawObiSpine(sh *sheet, g grid, fc faces, d *LabelData, area rect) {
	inset := math.Max(0.6, 0.12*area.w)
	across := area.w - 2*inset
	if across <= 0 {
		return
	}
	catno := strings.TrimSpace(d.Catno)
	mainLen := area.h
	if catno != "" {
		mainLen = 0.72 * area.h
	}
	cx := area.x + area.w/2
	main := joinFacts(artistName(d), strings.TrimSpace(d.Title))
	size := across / (fc.bold.capHeight + fc.bold.descent)
	if main != "" {
		b := lineAt(main, mainLen, across, fc.bold, math.Min(size, g.scale.upTo(3)), alignLeft)
		w := b.inkWidth()
		sh.turnedText(b, cx, area.y+w/2, true, "spine")
	}
	if catno != "" {
		b := lineAt(catno, area.h-mainLen-g.gutter, across, fc.regular, math.Min(size, g.scale.upTo(2)), alignLeft)
		w := b.inkWidth()
		sh.turnedText(b, cx, area.bottom()-w/2, true, "spine-catno")
	}
}

// drawObiBack sets the tracklist as bands, with the QR code and catalogue number at the
// foot.
func drawObiBack(sh *sheet, g grid, fc faces, d *LabelData, o opts, area rect) {
	gap := g.gutter
	if url := qrLink(d, o); url != "" {
		q := sh.codeSide(url, math.Min(0.6*area.w, 0.3*area.h))
		if q > 0 {
			catSize := g.scale.upTo(1)
			catH := blockHeight(fc.regular, catSize, 1)
			var foot rect
			foot, area = area.cutBottom(q+gap/2+catH, 2*gap)
			sh.code(url, foot.x, foot.y, q, "qr")
			catArea := rect{foot.x, foot.y + q + gap/2, foot.w, catH}
			sh.textIn(lineAt(strings.TrimSpace(d.Catno), catArea.w, catArea.h, fc.regular, catSize, alignLeft), catArea, 0, "back-catno")
		}
	}
	if !o.flag("tracklist", true) {
		return
	}
	tracks := append([]Track(nil), includedTracks(d)...)
	if len(tracks) == 0 {
		tracks = append(tracks, d.Tracks...)
	}
	if len(tracks) == 0 || area.empty() {
		return
	}
	if !o.flag("tempo", true) {
		for i := range tracks {
			tracks[i].BPM, tracks[i].Key = 0, ""
		}
	}
	// Bands share the panel's height but are never taller than 80 % of its width (a
	// two-line title over its badges), nor shorter than a line of floor-sized text plus
	// its badges; tracks beyond what fits are left out.
	minBand := blockHeight(fc.regular, g.scale.downTo(1), 1) + 2*gap
	bandH := math.Max(minBand, math.Min(0.8*area.w, area.h/float64(len(tracks))))
	n := min(len(tracks), int(area.h/bandH))
	if n == 0 {
		return
	}
	trackBands(sh, g, fc, tracks[:n], o, bandAreas(sh, g, rect{area.x, area.y, area.w, bandH * float64(n)}, n, n))
}
