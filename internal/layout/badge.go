package layout

import "math"

// Badges are the boxed values a DJ reads first: tempo as a solid block with white
// figures, key (and on seller tags, grades) as an outlined box. Both are square-cornered,
// which keeps their edges crisp on a thermal head, and both are sized from the cap height
// of their text, so badges in one row always match.

type badgeStyle uint8

const (
	badgeSolid badgeStyle = iota
	badgeOutline
)

// Badge proportions, as multiples of the text's cap height c: 0.45c of space above and
// below the capitals (height 1.9c), 0.5c at each side, never narrower than tall, and an
// outline of 0.12c (at least the stroke floor).
const (
	badgeHeightPerCap = 1.9
	badgePadPerCap    = 0.5
	badgeStrokePerCap = 0.12
)

// badgeWidthPerCap is a badge's width divided by its cap height.
func badgeWidthPerCap(f *face, text string) float64 {
	return math.Max(badgeHeightPerCap, f.emWidth(text)/f.capHeight+2*badgePadPerCap)
}

// badgeCapFloor is the smallest cap height a badge may use: its text at minTextMM.
func badgeCapFloor(f *face) float64 { return minTextMM * f.capHeight }

// fitBadges is the cap height at which the badges fit side by side, gap apart, in a
// w × h area, capped at capMax; 0 when they fit only below the floor. Empty texts take
// no room.
func fitBadges(f *face, texts []string, w, h, gap, capMax float64) float64 {
	units, n := 0.0, 0
	for _, t := range texts {
		if t != "" {
			units += badgeWidthPerCap(f, t)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	c := math.Min(capMax, h/badgeHeightPerCap)
	c = math.Min(c, (w-gap*float64(n-1))/units)
	if c < badgeCapFloor(f) {
		return 0
	}
	return c
}

// badgeRowWidth is the width of the non-empty badges at cap height c, gap apart.
func badgeRowWidth(f *face, texts []string, c, gap float64) float64 {
	w, n := 0.0, 0
	for _, t := range texts {
		if t != "" {
			w += badgeWidthPerCap(f, t) * c
			n++
		}
	}
	if n > 1 {
		w += gap * float64(n-1)
	}
	return w
}

// badge draws one badge with its top-left at (x, y) and cap height c, and returns its
// box.
func (s *sheet) badge(f *face, text string, style badgeStyle, x, y, c float64, name string) rect {
	r := rect{x, y, badgeWidthPerCap(f, text) * c, badgeHeightPerCap * c}
	if text == "" || c <= 0 {
		return rect{x, y, 0, 0}
	}
	size := c / f.capHeight
	ink := white
	if style == badgeSolid {
		s.fill(r, "")
	} else {
		s.frame(r, math.Max(minStrokeMM, badgeStrokePerCap*c), "")
		ink = black
	}
	tw := f.width(text, size)
	baseline := y + (r.h+c)/2
	s.cv.drawText(f, text, size*s.dpmm, affine{a: 1, c: (x + (r.w-tw)/2) * s.dpmm, e: 1, f: baseline * s.dpmm}, ink)
	s.note(mark{kind: markBadge, name: name, box: r, size: size, text: text, lines: 1})
	return r
}

// badgeSpec is one badge of a row.
type badgeSpec struct {
	text  string
	style badgeStyle
	name  string
}

// badgeRow draws badges side by side at cap height c from x (left to right) or ending
// at x (right-aligned), top at y. It returns the row's box.
func (s *sheet) badgeRow(f *face, specs []badgeSpec, x, y, c, gap float64, rightAligned bool) rect {
	texts := make([]string, len(specs))
	for i, b := range specs {
		texts[i] = b.text
	}
	w := badgeRowWidth(f, texts, c, gap)
	if rightAligned {
		x -= w
	}
	start := x
	for _, b := range specs {
		if b.text == "" {
			continue
		}
		r := s.badge(f, b.text, b.style, x, y, c, b.name)
		x = r.right() + gap
	}
	return rect{start, y, w, badgeHeightPerCap * c}
}
