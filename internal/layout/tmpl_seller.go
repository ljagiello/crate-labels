package layout

import (
	"image"
	"math"
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// Seller tags price a record for a stall or a shop crate. The price is the largest thing
// on the tag and the grades sit next to it as outlined badges captioned Disc and Cover;
// the record's identity and facts fill the rest. A tag without a price gets a write-in
// line instead.

func currencyOption() OptionSpec {
	return choiceOption("currency", "Currency (when the price has no symbol)", "USD",
		append([]string(nil), Currencies...), nil, "")
}

func sellerOptions(handle bool) []OptionSpec {
	o := []OptionSpec{qrOption(true), currencyOption()}
	if handle {
		o = append(o, textOption("handle", "Shop or seller name", "Printed at the foot of the tag."))
	}
	return append(o, fontOption(), fontScaleOption())
}

// Grade captions.
const (
	discCaption  = "Disc"
	coverCaption = "Cover"
)

// gradeSet is the badges for a record's condition: one plain badge for a single grade,
// or a captioned pair.
type gradeSet struct {
	texts    []string
	captions []string
}

func gradesOf(d *LabelData) gradeSet {
	c := parseCondition(d.Condition)
	switch {
	case c.disc != "" && c.cover != "":
		return gradeSet{[]string{c.disc, c.cover}, []string{discCaption, coverCaption}}
	case c.disc != "":
		return gradeSet{[]string{c.disc}, nil}
	case c.cover != "":
		return gradeSet{[]string{c.cover}, []string{coverCaption}}
	}
	return gradeSet{}
}

// gradeCaptionSize is the caption size for badges of cap height c: a third of the badge
// height, never under the text floor.
func gradeCaptionSize(c float64) float64 { return math.Max(minTextMM, badgeHeightPerCap*c/3) }

// captionHeight is the room a caption takes above its badge, with a sliver of space.
func captionHeight(f *face, c float64) float64 {
	return blockHeight(f, gradeCaptionSize(c), 1) + 0.15*c
}

// fitGrades is the largest cap height (to fitPrecision) at which the grade badges and
// their captions fit a w × h area, capped at capMax; 0 when they do not fit above the
// floor. Width and height both grow with the cap height, so a bisection finds it.
func fitGrades(fc faces, gs gradeSet, w, h, gap, capMax float64) float64 {
	if len(gs.texts) == 0 {
		return 0
	}
	fits := func(c float64) bool {
		return gradesWidth(fc, gs, c, gap) <= w && gradesHeight(fc, gs, c) <= h
	}
	lo, hi := badgeCapFloor(fc.bold), math.Min(capMax, h/badgeHeightPerCap)
	if hi < lo || !fits(lo) {
		return 0
	}
	if fits(hi) {
		return hi
	}
	for hi-lo > fitPrecision*fc.bold.capHeight {
		if mid := (lo + hi) / 2; fits(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// gradesWidth is the width drawGrades uses at cap height c.
func gradesWidth(fc faces, gs gradeSet, c, gap float64) float64 {
	w := 0.0
	for i, t := range gs.texts {
		bw := badgeWidthPerCap(fc.bold, t) * c
		if gs.captions != nil {
			bw = math.Max(bw, fc.regular.width(gs.captions[i], gradeCaptionSize(c)))
		}
		w += bw
	}
	return w + gap*float64(max(0, len(gs.texts)-1))
}

// gradesHeight is the height drawGrades uses at cap height c.
func gradesHeight(fc faces, gs gradeSet, c float64) float64 {
	h := badgeHeightPerCap * c
	if gs.captions != nil {
		h += captionHeight(fc.regular, c)
	}
	return h
}

// drawGrades draws the grade badges with their captions, top-left at (x, y).
func drawGrades(sh *sheet, fc faces, gs gradeSet, x, y, c, gap float64) rect {
	start := x
	for i, t := range gs.texts {
		bw := badgeWidthPerCap(fc.bold, t) * c
		top := y
		if gs.captions != nil {
			size := gradeCaptionSize(c)
			bw = math.Max(bw, fc.regular.width(gs.captions[i], size))
			cb := textBlock{face: fc.regular, size: size, lines: []string{gs.captions[i]}, width: bw, align: alignCenter}
			sh.text(cb, x, y, "caption"+string(rune('1'+i)))
			top += captionHeight(fc.regular, c)
		}
		inner := badgeWidthPerCap(fc.bold, t) * c
		sh.badge(fc.bold, t, badgeOutline, x+(bw-inner)/2, top, c, "grade"+string(rune('1'+i)))
		x += bw + gap
	}
	return rect{start, y, x - gap - start, gradesHeight(fc, gs, c)}
}

// drawPrice sets the price in area at up to maxSize, aligned as given, or a write-in
// line when there is none. It returns the box used.
func drawPrice(sh *sheet, f *face, d *LabelData, o opts, area rect, maxSize, minSize float64, a align) rect {
	currency := o.text("currency", "USD")
	price := FormatPrice(d.Price, currency)
	if price != "" {
		b := oneLine(price, area.w, area.h, f, maxSize, minSize, a)
		return sh.textIn(b, area, 0.5, "price")
	}
	// Write-in: the currency mark and a line to write on, sitting on the price's baseline.
	size := math.Min(maxSize, area.h/(f.capHeight+f.descent))
	mark := oneLine(currencyMark(currency), area.w/3, area.h, f, size, minSize, alignLeft)
	box := sh.textIn(mark, area, 0.5, "price")
	baseline := box.y + mark.size*f.capHeight
	sh.hline(box.right()+0.5, area.right(), baseline-minStrokeMM, minStrokeMM, "write-in")
	return rect{box.x, box.y, area.right() - box.x, box.h}
}

// --- seller-small ----------------------------------------------------------------------

type sellerSmall struct{}

func (sellerSmall) ID() string   { return "seller-small" }
func (sellerSmall) Name() string { return "Price tag, small" }
func (sellerSmall) Description() string {
	return "Price and grades under the artist, title and catalogue facts, with an optional QR code (40×30, 50×30, Avery 5160)."
}

func (sellerSmall) Supports(s *printer.Stock) bool { return djSingle{}.Supports(s) }

func (sellerSmall) Options() []OptionSpec { return sellerOptions(false) }

func (t sellerSmall) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

func (sellerSmall) draw(sh *sheet, g grid, d *LabelData, _ *printer.Stock, o opts) {
	fc := o.faces()
	c := g.content
	gap := g.gutter
	low, top := c.cutBottom(0.46*c.h, gap)

	// Price and grades share the lower band; the grades take what they need on the right.
	gs := gradesOf(d)
	if cp := fitGrades(fc, gs, 0.48*low.w, low.h, gap, g.scale.upTo(3)*fc.bold.capHeight); cp > 0 {
		gw := gradesWidth(fc, gs, cp, gap)
		col, rest := low.cutRight(gw, gap)
		drawGrades(sh, fc, gs, col.x, col.y+(col.h-gradesHeight(fc, gs, cp))/2, cp, gap)
		low = rest
	}
	drawPrice(sh, fc.bold, d, o, low, g.scale.upTo(6), g.scale.downTo(3), alignLeft)

	if url := qrLink(d, o); url != "" {
		if q := sh.codeSide(url, math.Min(top.h, 0.3*top.w)); q > 0 {
			col, rest := top.cutRight(q, gap)
			sh.code(url, col.x, col.y, q, "qr")
			top = rest
		}
	}
	facts := joinFacts(d.Catno, FormatDisplay(d.Format), d.Year)
	factsSize := g.scale.step(-1)
	if facts != "" {
		line, rest := top.cutBottom(blockHeight(fc.regular, factsSize, 1), gap/2)
		sh.textIn(lineAt(facts, line.w, line.h, fc.regular, factsSize, alignLeft), line, 1, "facts")
		top = rest
	}
	stackNames(sh, g, fc, d, top, 1, 2, 3)
}

// stackNames sets the artist (bold) over the release title in area, the artist at up to
// artistLines lines and the title at up to titleLines. Both grow to step n of the scale.
func stackNames(sh *sheet, g grid, fc faces, d *LabelData, area rect, artistLines, titleLines, n int) {
	gap := g.gutter / 2
	artist, title := artistName(d), strings.TrimSpace(d.Title)
	// The artist is set one step larger than the title, so it gets that much more room.
	split := typeRatio / (1 + typeRatio)
	switch {
	case title == "":
		split = 1
	case artist == "":
		split = 0
	}
	artistArea, titleArea := area.cutTop(split*area.h, 0)
	next := area.y
	if artist != "" {
		ab := fitText(artist, artistArea.w, artistArea.h, fitSpec{face: fc.bold, max: g.scale.upTo(n), min: g.scale.downTo(n - 1), lines: artistLines})
		next = sh.textIn(ab, artistArea, 0, "artist").bottom() + gap
	}
	if title != "" {
		titleArea = rect{area.x, next, area.w, area.bottom() - next}
		tb := fitText(title, titleArea.w, titleArea.h, fitSpec{face: fc.regular, max: g.scale.upTo(n - 1), min: g.scale.downTo(n - 1), lines: titleLines})
		sh.textIn(tb, titleArea, 0, "title")
	}
}

// --- seller-large ----------------------------------------------------------------------

type sellerLarge struct{}

func (sellerLarge) ID() string   { return "seller-large" }
func (sellerLarge) Name() string { return "Price tag, large" }
func (sellerLarge) Description() string {
	return "Big price and captioned grades with the artist, title, pressing facts, shop name and QR code (80×48, 70×80, 4×6″)."
}

func (sellerLarge) Supports(s *printer.Stock) bool {
	long, short, _ := stockShape(s)
	return !s.Round && 70 <= long && long <= 160 && 44 <= short && short <= 110 &&
		1.1 <= long/short && long/short <= 1.8
}

func (sellerLarge) Options() []OptionSpec { return sellerOptions(true) }

func (t sellerLarge) Render(d *LabelData, c printer.Canvas, s *printer.Stock, o map[string]any) (*image.Gray, error) {
	return render(t, d, c, s, o)
}

// factLines are the pressing facts of a large tag, most important first.
func factLines(d *LabelData) []string {
	var out []string
	for _, l := range []string{
		joinFacts(labelName(d), d.Catno),
		joinFacts(FormatDisplay(d.Format), d.Year, d.Country),
		styleLine(d, 3),
	} {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// drawFactLines sets the fact lines (and the shop name, bold, last) from the bottom of
// area up, each on one line, and returns the height used.
func drawFactLines(sh *sheet, fc faces, lines []string, handle string, area rect, size float64) float64 {
	y := area.bottom()
	type entry struct {
		text string
		f    *face
		name string
	}
	var entries []entry
	for i, l := range lines {
		entries = append(entries, entry{l, fc.regular, "facts" + string(rune('1'+i))})
	}
	if handle != "" {
		entries = append(entries, entry{handle, fc.bold, "handle"})
	}
	// One size for every line: the largest at which they all fit, at most one step of the
	// type scale under size; a line that needs more is cut with "…".
	shared := size
	for _, e := range entries {
		b := oneLine(e.text, area.w, area.h, e.f, size, size/typeRatio, alignLeft)
		shared = math.Min(shared, b.size)
	}
	size = shared
	pitch := size * leading
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		h := blockHeight(e.f, size, 1)
		if y-h < area.y-1e-9 {
			break
		}
		b := oneLine(e.text, area.w, h, e.f, size, size, alignLeft) // cut, never shrink
		sh.text(b, area.x, y-h, e.name)
		y -= pitch
	}
	return area.bottom() - y
}

func (sellerLarge) draw(sh *sheet, g grid, d *LabelData, _ *printer.Stock, o opts) {
	fc := o.faces()
	c := g.content
	gap := g.gutter
	handle := strings.TrimSpace(o.text("handle", ""))
	gs := gradesOf(d)
	factSize := g.scale.step(0)
	url := qrLink(d, o)

	if g.w > g.h { // landscape: identity and facts on the left, price and grades on the right
		right, left := c.cutRight(0.4*c.w, gap)
		sh.vline(left.right()+gap/2-minStrokeMM/2, c.y, c.bottom(), minStrokeMM, "divider")
		priceArea, gradeArea := right.cutTop(0.5*right.h, gap)
		drawPrice(sh, fc.bold, d, o, priceArea, g.scale.upTo(8), g.scale.downTo(4), alignCenter)
		if cp := fitGrades(fc, gs, gradeArea.w, gradeArea.h, gap, g.scale.upTo(4)*fc.bold.capHeight); cp > 0 {
			gw := gradesWidth(fc, gs, cp, gap)
			drawGrades(sh, fc, gs, gradeArea.x+(gradeArea.w-gw)/2, gradeArea.y, cp, gap)
		}
		low, top := left.cutBottom(0.4*left.h, gap)
		if url != "" {
			if q := sh.codeSide(url, math.Min(low.h, 0.3*low.w)); q > 0 {
				col, rest := low.cutRight(q, gap)
				sh.code(url, col.x, col.bottom()-q, q, "qr")
				low = rest
			}
		}
		drawFactLines(sh, fc, factLines(d), handle, low, factSize)
		stackNames(sh, g, fc, d, top, 2, 2, 5)
		return
	}

	// Portrait: names, then the price over the grades, then facts beside the QR code.
	names, rest := c.cutTop(0.28*c.h, gap)
	priceBand, low := rest.cutTop(0.46*rest.h, gap)
	stackNames(sh, g, fc, d, names, 2, 2, 5)
	sh.hline(c.x, c.right(), names.bottom()+gap/2-minStrokeMM/2, minStrokeMM, "rule")
	priceArea, gradeArea := priceBand.cutTop(0.58*priceBand.h, gap/2)
	drawPrice(sh, fc.bold, d, o, priceArea, g.scale.upTo(9), g.scale.downTo(5), alignCenter)
	if cp := fitGrades(fc, gs, gradeArea.w, gradeArea.h, gap, g.scale.upTo(5)*fc.bold.capHeight); cp > 0 {
		gw := gradesWidth(fc, gs, cp, gap)
		drawGrades(sh, fc, gs, gradeArea.x+(gradeArea.w-gw)/2, gradeArea.y+(gradeArea.h-gradesHeight(fc, gs, cp))/2, cp, gap)
	}
	sh.hline(c.x, c.right(), priceBand.bottom()+gap/2-minStrokeMM/2, minStrokeMM, "rule")
	if url != "" {
		if q := sh.codeSide(url, math.Min(low.h, 0.4*low.w)); q > 0 {
			col, rest := low.cutRight(q, gap)
			sh.code(url, col.x, col.bottom()-q, q, "qr")
			low = rest
		}
	}
	drawFactLines(sh, fc, factLines(d), handle, low, factSize)
}
