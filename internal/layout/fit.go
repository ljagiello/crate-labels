package layout

import (
	"math"
	"strings"
)

// Text setting. Sizes, widths and heights are in mm (or any linear unit), because face
// metrics scale linearly. A block is placed by the top of its capitals, so blocks line
// up with rules and badges on the grid rather than with the font's ascender.

// ellipsis is U+2026, appended to text that had to be cut.
const ellipsis = "…"

// leading is the baseline-to-baseline distance in em. Labels carry short runs of large
// text, so lines sit closer than in body copy.
const leading = 1.18

type align uint8

const (
	alignLeft align = iota
	alignCenter
	alignRight
)

// textBlock is text broken into lines at one size, aligned in a lane of the given width.
type textBlock struct {
	face  *face
	size  float64
	lines []string
	width float64
	align align
}

func (b textBlock) empty() bool { return len(b.lines) == 0 }

// height is the block's extent from the cap line of the first line to the descender
// of the last.
func (b textBlock) height() float64 { return blockHeight(b.face, b.size, len(b.lines)) }

func blockHeight(f *face, size float64, lines int) float64 {
	if lines == 0 {
		return 0
	}
	return size * (f.capHeight + leading*float64(lines-1) + f.descent)
}

// inkWidth is the widest line's advance.
func (b textBlock) inkWidth() float64 {
	w := 0.0
	for _, l := range b.lines {
		w = math.Max(w, b.face.width(l, b.size))
	}
	return w
}

// fitSpec is how one text element may be set: a face, a size range and a line limit.
type fitSpec struct {
	face     *face
	max, min float64 // em size in mm; the fit never leaves [min, max]
	lines    int     // at most this many lines (at least 1)
	align    align
}

// thinStemMM is the thinnest stem small text may have: 1.5 dots of a 203.2 dpi head.
// Regular text whose stems would be thinner is set in the family's bold cut instead,
// because a one-dot stem breaks up on a thermal head.
const thinStemMM = 1.5 / 8

// fitPrecision is where the size search stops: 0.05 mm is under half a dot at 203 dpi
// and under a dot at 300 dpi, so a finer search would not change the print.
const fitPrecision = 0.05

// fitText sets text in a w × h area: the largest size in [min, max] at which it wraps
// into at most spec.lines lines, every word whole, within the height. When even min is
// too big, it sets the text at min, breaks overlong words, keeps the lines that fit and
// ends the last one with "…". Text that ends up with stems under thinStemMM is set
// again in the bold cut.
func fitText(text string, w, h float64, spec fitSpec) textBlock {
	out := setText(text, w, h, spec)
	if !out.empty() && spec.face.heavier != nil && out.size*spec.face.stem < thinStemMM {
		spec.face = spec.face.heavier
		return setText(text, w, h, spec)
	}
	return out
}

func setText(text string, w, h float64, spec fitSpec) textBlock {
	text = strings.Join(strings.Fields(text), " ")
	lines := max(1, spec.lines)
	hi := math.Max(spec.min, spec.max)
	lo := math.Min(spec.min, hi)
	out := textBlock{face: spec.face, size: hi, width: w, align: spec.align}
	if text == "" || w <= 0 || h <= 0 {
		return out
	}
	try := func(size float64) []string {
		ls, whole := wrapWords(spec.face, text, size, w)
		if !whole || len(ls) > lines || blockHeight(spec.face, size, len(ls)) > h {
			return nil
		}
		return ls
	}
	if ls := try(hi); ls != nil {
		out.lines = ls
		return out
	}
	best := try(lo)
	if best == nil {
		out.size = lo
		out.lines = overflowLines(spec.face, text, lo, w, h, lines)
		return out
	}
	for hi-lo > fitPrecision {
		mid := (lo + hi) / 2
		if ls := try(mid); ls != nil {
			lo, best = mid, ls
		} else {
			hi = mid
		}
	}
	out.size, out.lines = lo, best
	return out
}

// wrapWords breaks text greedily at spaces. whole is false when a word is wider than the
// lane by itself.
func wrapWords(f *face, text string, size, w float64) (lines []string, whole bool) {
	whole = true
	line := ""
	for _, word := range strings.Fields(text) {
		if f.width(word, size) > w {
			whole = false
		}
		if line == "" {
			line = word
			continue
		}
		if next := line + " " + word; f.width(next, size) <= w {
			line = next
			continue
		}
		lines = append(lines, line)
		line = word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines, whole
}

// overflowLines sets text that does not fit at size: words are broken where they must
// be, the lines that fit the height (and the limit) are kept, and the last kept line ends
// with "…" when anything was left out.
func overflowLines(f *face, text string, size, w, h float64, limit int) []string {
	var all []string
	line := ""
	flush := func() {
		if line != "" {
			all = append(all, line)
			line = ""
		}
	}
	for _, word := range strings.Fields(text) {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if f.width(candidate, size) <= w {
			line = candidate
			continue
		}
		flush()
		for f.width(word, size) > w {
			head := longestPrefix(f, word, size, w)
			if head == "" {
				break
			}
			all = append(all, head)
			word = word[len(head):]
		}
		line = word
	}
	flush()
	room := limit
	for room > 1 && blockHeight(f, size, room) > h {
		room--
	}
	if len(all) <= room {
		return all
	}
	kept := append([]string(nil), all[:room]...)
	rest := strings.Join(all[room-1:], " ")
	kept[room-1] = withEllipsis(f, rest, size, w)
	return kept
}

// longestPrefix is the longest leading run of whole runes of s that fits w.
func longestPrefix(f *face, s string, size, w float64) string {
	end := 0
	for i := range s {
		if i > 0 && f.width(s[:i], size) > w {
			break
		}
		end = i
	}
	if f.width(s, size) <= w {
		return s
	}
	if end == 0 {
		// Even one rune is too wide: take it anyway so the loop advances.
		for i := range s {
			if i > 0 {
				return s[:i]
			}
		}
		return s
	}
	return s[:end]
}

// withEllipsis cuts s to the longest prefix that fits w with "…" appended, preferring to
// cut at a space. It returns "…" alone when no letter fits.
func withEllipsis(f *face, s string, size, w float64) string {
	s = strings.TrimSpace(s)
	if f.width(s, size) <= w && !strings.HasSuffix(s, ellipsis) {
		return s
	}
	runes := []rune(s)
	n := len(runes)
	for n > 0 && f.width(strings.TrimRight(string(runes[:n]), " ")+ellipsis, size) > w {
		n--
	}
	if n == 0 {
		return ellipsis
	}
	cut := string(runes[:n])
	if sp := strings.LastIndex(cut, " "); sp > len(cut)/2 {
		cut = cut[:sp] // a word boundary in the second half reads better than a split word
	}
	return strings.TrimRight(cut, " ,·") + ellipsis
}

// lineAt sets one line at size, shrinking it at most one step of the type scale (never
// under minTextMM) before cutting it with "…". It is how secondary lines (facts, catalogue
// numbers, spine text) stay readable instead of collapsing to the floor.
func lineAt(text string, w, h float64, f *face, size float64, a align) textBlock {
	return oneLine(text, w, h, f, size, math.Max(minTextMM, size/typeRatio), a)
}

// oneLine is fitText for exactly one line.
func oneLine(text string, w, h float64, f *face, maxSize, minSize float64, a align) textBlock {
	return fitText(text, w, h, fitSpec{face: f, max: maxSize, min: minSize, lines: 1, align: a})
}
