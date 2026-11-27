package layout

import (
	"embed"
	"fmt"
	"io/fs"
	"math"
	"sync"
	"unicode"

	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed fonts/*.ttf fonts/*.txt
var fontFiles embed.FS

// FontFS holds the bundled OFL fonts and their licences at its root, e.g.
// "plexcond_400.ttf", so the web UI can serve them as /fonts/<file>.ttf.
// fonts/SOURCES.md records where each file came from.
func FontFS() fs.FS {
	sub, err := fs.Sub(fontFiles, "fonts")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	return sub
}

// FontFamily is one print font with its regular and bold files.
type FontFamily struct {
	ID, Name      string
	Regular, Bold string // file names in FontFS
}

// DefaultFontFamily is the family used when an option names none or an unknown one.
// A condensed grotesque gets the most characters across a narrow label while staying
// open enough to survive a thermal head's dot gain.
const DefaultFontFamily = "plex-condensed"

// fontFamilies are the print fonts in menu order: a condensed sans (the default), a
// legibility-first sans, a monospace and a slab serif.
var fontFamilies = []FontFamily{
	{"plex-condensed", "IBM Plex Sans Condensed", "plexcond_400.ttf", "plexcond_700.ttf"},
	{"atkinson", "Atkinson Hyperlegible", "atkinson_400.ttf", "atkinson_700.ttf"},
	{"plex-mono", "IBM Plex Mono", "plexmono_400.ttf", "plexmono_700.ttf"},
	{"enriqueta", "Enriqueta (slab serif)", "enriqueta_400.ttf", "enriqueta_700.ttf"},
}

// coverFamily supplies the glyphs another family lacks (⅓, arrows, ″ and the like), at
// the same weight. It covers every symbol the templates print except ♯ and ♭, which
// accidental draws.
const coverFamily = "plex-condensed"

// FontFamilies lists the print fonts in menu order.
func FontFamilies() []FontFamily { return append([]FontFamily(nil), fontFamilies...) }

func familyByID(id string) FontFamily {
	for _, f := range fontFamilies {
		if f.ID == id {
			return f
		}
	}
	return familyByID(DefaultFontFamily)
}

// face is one font file with linear, em-relative metrics. Sizes passed to its methods are
// in any linear unit (mm or px), because the metrics scale linearly.
type face struct {
	name      string
	font      *sfnt.Font
	upem      float64
	fallback  *face
	ascent    float64 // em, above the baseline
	descent   float64 // em, below the baseline
	capHeight float64 // em, top of "H"
	stem      float64 // em, a typical vertical stroke, for the drawn ♯ and ♭
	heavier   *face   // the family's bold cut, for small regular text (set once, under faceMu)

	mu       sync.Mutex // guards buf and the caches
	buf      sfnt.Buffer
	advances map[rune]float64
	outlines map[sfnt.GlyphIndex]path
}

var (
	faceMu    sync.Mutex
	faceCache = map[string]*face{}
)

// loadFace returns the cached face for a bundled file, with the fallback face attached.
func loadFace(file, fallbackFile string) *face {
	faceMu.Lock()
	defer faceMu.Unlock()
	return loadFaceLocked(file, fallbackFile)
}

func loadFaceLocked(file, fallbackFile string) *face {
	key := file + "|" + fallbackFile
	if f, ok := faceCache[key]; ok {
		return f
	}
	f, err := parseFace(file)
	if err != nil {
		panic(fmt.Sprintf("layout: bundled font %s: %v", file, err)) // embedded, so a build defect
	}
	if fallbackFile != "" && fallbackFile != file {
		f.fallback = loadFaceLocked(fallbackFile, "")
	}
	faceCache[key] = f
	return f
}

// parseFace reads a bundled font and its metrics in font units (ppem = units per em, so
// sfnt's 26.6 values are font units and nothing is rounded to pixels).
func parseFace(file string) (*face, error) {
	data, err := fontFiles.ReadFile("fonts/" + file)
	if err != nil {
		return nil, err
	}
	font, err := sfnt.Parse(data)
	if err != nil {
		return nil, err
	}
	f := &face{
		name:     file,
		font:     font,
		upem:     float64(font.UnitsPerEm()),
		advances: map[rune]float64{},
		outlines: map[sfnt.GlyphIndex]path{},
	}
	m, err := font.Metrics(&f.buf, f.ppem(), xfont.HintingNone)
	if err != nil {
		return nil, err
	}
	f.ascent, f.descent = f.em(m.Ascent), f.em(m.Descent)
	f.capHeight = 0.7
	if b, ok := f.bounds('H'); ok {
		f.capHeight = f.em(-b.Min.Y)
	}
	// The vertical bar is a single stem in every bundled family, serif or not.
	f.stem = 0.09
	if b, ok := f.bounds('|'); ok {
		f.stem = math.Max(0.05, math.Min(0.16, f.em(b.Max.X-b.Min.X)))
	}
	return f, nil
}

func (f *face) bounds(r rune) (fixed.Rectangle26_6, bool) {
	g, err := f.font.GlyphIndex(&f.buf, r)
	if err != nil || g == 0 {
		return fixed.Rectangle26_6{}, false
	}
	b, _, err := f.font.GlyphBounds(&f.buf, g, f.ppem(), xfont.HintingNone)
	return b, err == nil
}

// ppem makes sfnt report font units: it scales by ppem / unitsPerEm.
func (f *face) ppem() fixed.Int26_6 { return fixed.Int26_6(f.font.UnitsPerEm()) }

// em converts font units to em.
func (f *face) em(units fixed.Int26_6) float64 { return float64(units) / f.upem }

// glyphIndexLocked is r's glyph, or 0 (.notdef) when the face lacks it. Callers hold f.mu.
func (f *face) glyphIndexLocked(r rune) sfnt.GlyphIndex {
	g, err := f.font.GlyphIndex(&f.buf, r)
	if err != nil {
		return 0
	}
	return g
}

// faces is a family's regular and bold face.
type faces struct{ regular, bold *face }

func (fc faces) pick(bold bool) *face {
	if bold {
		return fc.bold
	}
	return fc.regular
}

// familyFaces loads a family by id (unknown ids give the default family).
func familyFaces(id string) faces {
	fam := familyByID(id)
	cover := familyByID(coverFamily)
	faceMu.Lock()
	defer faceMu.Unlock()
	fc := faces{regular: loadFaceLocked(fam.Regular, cover.Regular), bold: loadFaceLocked(fam.Bold, cover.Bold)}
	if fc.regular.heavier == nil {
		fc.regular.heavier = fc.bold
	}
	return fc
}

func (f *face) String() string { return "face(" + f.name + ")" }

// native reports whether the font file itself has r. ASCII and spaces always count.
func (f *face) native(r rune) bool {
	if r < 0x7F || unicode.Is(unicode.Z, r) {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.glyphIndexLocked(r) != 0
}

// has reports whether text with r draws a real glyph: from this face, its fallback, or
// the drawn accidentals.
func (f *face) has(r rune) bool {
	return f.native(r) || (f.fallback != nil && f.fallback.native(r)) || isAccidental(r)
}

// glyphFace is the face that draws r: this one, or the fallback when only it has r.
func (f *face) glyphFace(r rune) *face {
	if f.fallback != nil && !f.native(r) && f.fallback.native(r) {
		return f.fallback
	}
	return f
}

// advance is r's advance in em, from this face only.
func (f *face) advance(r rune) float64 {
	if isAccidental(r) && !f.native(r) {
		return accidentalAdvance(f, r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.advances[r]; ok {
		return v
	}
	v := 0.0
	if adv, err := f.font.GlyphAdvance(&f.buf, f.glyphIndexLocked(r), f.ppem(), xfont.HintingNone); err == nil {
		v = f.em(adv)
	}
	f.advances[r] = v
	return v
}

// emWidth is the sum of per-glyph advances in em (no kerning), as glyphs are drawn.
func (f *face) emWidth(text string) float64 {
	total := 0.0
	for _, r := range text {
		total += f.glyphFace(r).advance(r)
	}
	return total
}

// width is the advance width of text at size.
func (f *face) width(text string, size float64) float64 { return f.emWidth(text) * size }

// outline is r's glyph outline in em, y down, origin on the baseline at the pen.
func (f *face) outline(r rune) path {
	if isAccidental(r) && !f.native(r) {
		return accidental(f, r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.glyphIndexLocked(r)
	if p, ok := f.outlines[g]; ok {
		return p
	}
	var p path
	segs, err := f.font.LoadGlyph(&f.buf, g, f.ppem(), nil)
	if err == nil {
		em := func(v fixed.Point26_6) point { return point{float64(v.X) / f.upem, float64(v.Y) / f.upem} }
		for _, s := range segs {
			a, b, c := em(s.Args[0]), em(s.Args[1]), em(s.Args[2])
			switch s.Op {
			case sfnt.SegmentOpMoveTo:
				p.moveTo(a.X, a.Y)
			case sfnt.SegmentOpLineTo:
				p.lineTo(a.X, a.Y)
			case sfnt.SegmentOpQuadTo:
				p.quadTo(a.X, a.Y, b.X, b.Y)
			case sfnt.SegmentOpCubeTo:
				p.cubeTo(a.X, a.Y, b.X, b.Y, c.X, c.Y)
			}
		}
	}
	f.outlines[g] = p
	return p
}

// Sharp and flat signs for musical key names. No bundled family has them, so they are
// drawn from the face's own cap height and stem weight to sit with its letters.
const (
	sharpSign = '♯'
	flatSign  = '♭'
)

func isAccidental(r rune) bool { return r == sharpSign || r == flatSign }

// accidentalAdvance is the drawn sign's advance in em: the sign plus a stem of space on
// each side.
func accidentalAdvance(f *face, r rune) float64 {
	if r == sharpSign {
		return 0.62*f.capHeight + 2*f.stem
	}
	return 0.5*f.capHeight + 2*f.stem
}

// accidental is the drawn ♯ or ♭ in em, y down, on the baseline.
func accidental(f *face, r rune) path {
	c, w := f.capHeight, f.stem
	left := w // side bearing
	var p path
	if r == sharpSign {
		// Two upright strokes crossed by two heavier bars that rise to the right.
		width := 0.62 * c
		v := 0.75 * w // the uprights are lighter than the bars
		top, bottom := -1.02*c, 0.12*c
		for _, x := range []float64{left + 0.3*width, left + 0.7*width} {
			p.polygon([]point{{x - v/2, top}, {x + v/2, top}, {x + v/2, bottom}, {x - v/2, bottom}})
		}
		rise := 0.14 * width
		bar := 1.1 * w
		for _, y := range []float64{-0.66 * c, -0.3 * c} {
			p.polygon([]point{
				{left, y + rise/2 - bar/2}, {left + width, y - rise/2 - bar/2},
				{left + width, y - rise/2 + bar/2}, {left, y + rise/2 + bar/2},
			})
		}
		return p
	}
	// Flat: a stem with a bowl that swells to the right and closes at the baseline.
	top := -1.08 * c
	p.polygon([]point{{left, top}, {left + w, top}, {left + w, 0}, {left, 0}})
	reach := 0.5 * c
	// Outer edge of the bowl, clockwise on screen (y down).
	p.moveTo(left+w*0.5, -0.46*c)
	p.cubeTo(left+0.3*c, -0.66*c, left+reach+0.12*c, -0.52*c, left+reach, -0.3*c)
	p.cubeTo(left+reach-0.06*c, -0.12*c, left+0.25*c, -0.02*c, left+w*0.5, 0)
	// Inner edge, wound the other way so it cuts the counter out of the bowl.
	in := 0.85 * w
	p.moveTo(left+w*0.5, -in*0.6)
	p.cubeTo(left+0.2*c, -0.1*c, left+reach-in-0.04*c, -0.18*c, left+reach-in, -0.3*c)
	p.cubeTo(left+reach-in+0.06*c, -0.46*c, left+0.24*c, -0.5*c, left+w*0.5, -0.46*c+in*0.9)
	return p
}

// drawText draws text at pxSize glyph by glyph at linear advances, without kerning or
// hinting, so the ink matches the measured width exactly. t maps text coordinates in
// pixels (x along the line from the pen, y down from the baseline) to the canvas, which
// lets the same code draw upright and turned text. It returns the advance in pixels.
func (cv *canvas) drawText(f *face, text string, pxSize float64, t affine, ink uint8) float64 {
	x := 0.0
	for _, r := range text {
		gf := f.glyphFace(r)
		if !unicode.IsSpace(r) {
			g := affine{
				a: t.a * pxSize, b: t.b * pxSize, c: t.a*x + t.c,
				d: t.d * pxSize, e: t.e * pxSize, f: t.d*x + t.f,
			}
			cv.fillTransformed(gf.outline(r), g, ink, false)
		}
		x += gf.advance(r) * pxSize
	}
	return x
}
