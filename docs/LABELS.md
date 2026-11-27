# Label templates

This note describes how crate-labels lays out its labels: the design system every
template shares, then each template. The code is in `internal/layout`, and reference
renders of every template on every stock are in `testdata/layout-reference`.

## Goals

- **Readable from the shelf.** Tempo and key are the first things a DJ reads, so they get
  boxed, high-contrast badges. Prices on seller tags are the largest text on the tag.
- **Thermal-friendly.** Labels print on 203.2 dpi direct-thermal heads, which spread every
  dot a little. Nothing is thinner than 0.25 mm (2 dots), text is never set below 1.6 mm,
  QR modules are whole pixels and at least 0.25 mm, and solid black is kept to small blocks.
- **One layout at any resolution.** Templates work in millimetres and take their size from
  the canvas, so the same template prints on a 203.2 dpi head and on a 300 dpi page printer.
- **Graceful with bad data.** Long names shrink, then wrap, then get an ellipsis. Missing
  fields leave no gaps or stray separators.

## Design system

### Grid

Every label gets one margin and one gutter, from its short side:

| Rule | Value | Why |
|---|---|---|
| Margin | 7 % of the short side, snapped to 0.5 mm, between 1.5 mm and 6 mm | The white frame keeps the same proportion on every stock. 1.5 mm covers a die-cut label's registration error. Above 6 mm is wasted paper. |
| Gutter | Half the margin, at least 1 mm | Space between columns, bands and badges. |

Examples: 40×30 mm gets a 2 mm margin and a 1 mm gutter; 80×48 mm gets 3.5 mm and
1.75 mm; 4×6″ gets 6 mm and 3 mm.

Layouts never place things at absolute coordinates. They take the content area (the label
minus its margin) and cut it into bands and columns (`cutTop`, `cutBottom`, `cutLeft`,
`cutRight`, `rows`). Each piece of text is then fitted into its own rectangle.

### Type scale

Sizes are em heights in millimetres on a modular scale with a ratio of 1.25 (a major
third). Step 0 is 2 mm on labels up to 48 mm on the short side. Larger labels raise it to
1/24 of the short side, so a 4×6″ tag is not set like a small sticker.

| Step | −1 | 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 |
|---|---|---|---|---|---|---|---|---|---|
| mm (small labels) | 1.6 | 2.0 | 2.5 | 3.1 | 3.9 | 4.9 | 6.1 | 7.6 | 9.5 |

Each element has a step it may grow to (`upTo`) and a step it may shrink to (`downTo`, two
steps lower). No text goes under the 1.6 mm floor: that leaves about 9 dots in a capital at
203 dpi, the smallest size that stays legible after dot spread. The **Text size** option
(`font_scale`, 0.8–1.6) only moves the upper bound. Text always shrinks to fit, so a larger
scale never makes anything overflow.

### Text fitting

One primitive, `fitText`, sets every piece of text:

1. Collapse whitespace.
2. If the text fits at its largest size, wrapping at spaces within its line limit and
   height, use that size.
3. Otherwise, bisect between the largest and smallest sizes. The search stops at 0.05 mm,
   less than half a dot at 203 dpi.
4. If it does not fit even at the smallest size, set it there anyway. Break words that are
   wider than the line, keep the lines that fit, and end the last one with "…" (preferring a
   cut at a space).
5. If regular text ends up so small that its stems are under 1.5 dots of a 203.2 dpi head,
   set it again in the family's bold cut. Small facts lines stay solid on a thermal head
   instead of breaking up.

Secondary lines (facts, catalogue numbers, spine text) shrink at most one step before
they are cut with "…", so they never collapse to the floor on a large label.

Blocks are positioned by the top of their capitals, not by the font's ascender, so text
lines up exactly with badges and rules. Lines are 1.18 em apart.

When several bands share a role, such as the track titles of one label, they share one
size: the smallest any title needs, but at most one step below the largest. A title that
would need more than that is cut with "…" rather than shrinking every band. The fact lines
on large seller tags follow the same rule.

### Badges

One primitive draws every boxed value. A badge is sized from the cap height *c* of its text:

- height 1.9*c* (0.45*c* above and below the capitals);
- 0.5*c* of padding each side;
- never narrower than it is tall;
- square corners, which stay crisp on a thermal head;
- an outline of 0.12*c*, at least 0.25 mm.

Tempo is a **solid** badge (white figures on black). Key is an **outlined** badge, and so
are the grades on seller tags. A row of badges is fitted as a unit (`fitBadges`): the
largest cap height at which the row fits its width and height. When a label shows several
tracks, every band uses the same badge size.

### Lines

Rules, outlines, guides and cut lines all go through one stroke helper. It snaps them to
whole pixels and never draws them thinner than 0.25 mm. Cut lines (centre label, sleeve
sticker) are dashed 2 mm on, 1.2 mm off. Fold guides on OBI strips are short dashes at the
ends of each fold, outside the text.

### QR codes

QR codes link to the record's Discogs page: `https://www.discogs.com/release/{id}`, or
`/master/{id}`. Encoding uses level M and the smallest version that holds the URL.

Each module is a whole number of pixels, at least 2 pixels and 0.25 mm: 2 dots at
203.2 dpi, 3 dots at 300 dpi. If a code cannot be that large in the space a template gives
it, it is left out. The bitmap has no quiet zone; every template keeps at least a gutter of
white paper around the code instead.

### Fonts

Four OFL families are bundled, unmodified from upstream (see
`internal/layout/fonts/SOURCES.md`):

| Option id | Family | Character |
|---|---|---|
| `plex-condensed` (default) | IBM Plex Sans Condensed | Fits the most text across a narrow label and stays open under dot spread. |
| `atkinson` | Atkinson Hyperlegible | Designed for low vision; distinct letterforms. |
| `plex-mono` | IBM Plex Mono | Monospaced; digits line up. |
| `enriqueta` | Enriqueta | Slab serif. |

None of the families has ♯ or ♭, so the renderer draws those two signs from the face's own
cap height and stem weight. Other symbols a family lacks, such as ⅓, arrows and ″, come
from IBM Plex Sans Condensed at the same weight.

### Field rules

- **Names.** Discogs' disambiguation suffix is removed ("Moth Signal (2)" → "Moth Signal").
  Case is kept as entered.
- **Facts.** These are joined with " · ". A middle dot never occurs in Discogs genre or
  style names, so it cannot be confused with the commas and slashes some of them contain.
  Styles are preferred over genres.
- **Keys.** Camelot ("8A"), Open Key ("1m") and note names ("Am", "F#m", "B♭", "Ebm") are
  all read. `key_notation` prints them as `camelot`, `musical`, `both` ("8A Am") or `off`.
  Note names follow the usual DJ-software spelling: flats for the black keys, except F♯.
  They are worked out from pitch classes (the Camelot wheel steps by fifths), not looked up
  in a table.
- **Tempo.** `bpm_range` folds each BPM by whole octaves into a one-octave window:
  - `off`: as detected;
  - `60-119`: downtempo and hip-hop stay as detected;
  - `75-149`: house, techno and trance;
  - `85-169`: hip-hop through techno;
  - `95-189`: house through drum and bass and hardcore.

  A value that would round onto the top of the window prints as the bottom.
- **Formats.** Any size written with an inch mark gets a proper inch sign: `12"`,
  "12-inch" and "12in" print as "12″", and `6½"` as "6½″". A bare 7, 10 or 12 gets one
  too. Other text, such as "25cm" or "LP", prints as written.
- **Prices.** Prices show cents only when there are some ("$18.50", "$7"), with thousands
  grouped. The symbol goes before the amount for $, €, £, ¥, CA$ and A$, "zł" goes after,
  and any other currency is shown by its code. A tag without a price gets a write-in line
  after the currency symbol.
- **Grades.** Goldmine and Discogs grades are read ("Near Mint (NM or M-)" → NM). The first
  is the disc's grade and the second the cover's; text that is not a grade, such as
  "Generic", is ignored.

## Templates

Template ids are stable. Stored settings, print history and the API refer to them.

| Id | Stocks | Tracks |
|---|---|---|
| `dj-single` | 40×30, 50×30 (also as Letter cells), Avery 5160 | 1 |
| `dj-two-track` | 80×48 (50×80 die-cut and Letter cells), Avery 5163 | 2 |
| `dj-four-track` | 70×80, 4×6″ | 4 |
| `dj-tape` | 12 mm tape (D30 roll and 15×50 die-cut) | 1 |
| `seller-small` | the same stocks as `dj-single` | – |
| `seller-large` | 80×48, 70×80, 4×6″ | – |
| `center-label` | round 4×6″ stock | the chosen side |
| `obi-strip` | full and mini OBI sheets | the tracklist |
| `test-pattern` | every stock | – |

Templates choose stocks by shape (orientation, size range and aspect ratio), not by stock
id. A stock added in `~/.config/crate-labels/stocks.toml` gets every template it fits.

### DJ labels: `dj-single`, `dj-two-track`, `dj-four-track`

All three share one grammar: a **heading** and **track bands**.

- **Heading.** Artist (bold), release title and a facts line (label · catalogue number ·
  year · styles), with the QR code at its right.
- **Track bands.** Each band, separated by a rule, has the track's position, its title (up
  to two lines) and its tempo and key badges at the right. Bands keep the height they would
  have with every track present, so labels from one crate line up whatever their track
  count. All bands on a label share one badge size, one position column and one title size.

`dj-single` folds the two together for a 30 mm sticker. The track title (regular, up to two
lines) sits over the artist (bold). The lower band holds the badges over a facts line
(position · style · catalogue number · year), with the QR code beside them. On a wide label
(Avery 5160) the QR code takes a full-height column at the right instead.

Options: `show_qr`, `key_notation`, `bpm_range`, `track` (single only: a position such as
B1, or a number), `font`, `font_scale`.

### `dj-tape`

One line along 12 mm tape: tempo and key badges at the full height of the tape, then the
artist (bold) over the title, and optionally a QR code. On tape, names are cut short rather
than set below 2 mm. The QR code is off by default here, because at this height it can only
use the smallest module. Same options as `dj-single`.

### Seller tags: `seller-small`, `seller-large`

- **`seller-small`.** The lower band holds the price (left, as large as it fits) and the
  grades (right). A single grade is one outlined badge; a disc and cover pair are two badges
  captioned **Disc** and **Cover**. Above that: artist, release title and a facts line
  (catalogue number · format · year), with an optional QR code at the top right.
- **`seller-large`.** The layout follows the canvas:
  - On a landscape canvas (80×48), the identity and facts are on the left, and the price
    over the grades is on the right, behind a vertical rule.
  - On a portrait canvas (70×80, 4×6″), it is three bands: names; then the price over the
    grades; then the fact lines and shop name beside the QR code.

  Fact lines are label · catalogue number, format · year · country, and styles.

Options: `show_qr`, `currency` (used when the price has no symbol), `handle` (large only:
the shop or seller name), `font`, `font_scale`.

### `center-label`

This is a paper disc for the middle of a record, printed on the round 4×6″ stock. A matching
sleeve sticker for the same side goes underneath.

- **Disc size.** A 7″ label is 90 mm across and a 12″ label 100 mm. The hole is the 38 mm
  jukebox hole or the 7.2 mm spindle hole.
  - The 7″ size and hole default to the stock's `disc_mm` and `hole_mm`.
  - The disc is cut from plain 4×6″ stock by hand, so it is centred on the label both
    ways. A stock can set `disc_top_mm` in its extra to put the disc's top edge at a
    fixed distance from the label's top instead.
  - The cut line and a jukebox hole are dashed rings. A spindle hole gets a solid ring and a
    crosshair for punching.
- **Text on the disc.** All text is horizontal, set in bands. Each band is as wide as the
  circle allows at its far edge, inside a rim margin:
  - above the hole: the label name, the artist (bold), then the release title;
  - beside the hole: the side letter (left) and the speed (right);
  - below the hole: up to three tracks of the side, each with small badges, then the
    catalogue number and year at the rim.
- **Sleeve sticker.** Under the disc, inside a dashed cut frame, is a DJ label for the
  side. It always has the heading (artist, title, facts and QR code). It adds up to four
  track bands when it is at least 30 mm tall, which happens only when `disc_top_mm` moves
  the disc up. Under 15 mm there is no sticker.

Options:
- `center_format`: `auto`, `7-38`, `7-7`, `12`. With `auto`, a 12″ or LP gets the 12″
  label, a 10″ gets the 90 mm label with a spindle hole, and anything else uses the stock's
  disc.
- `speed`: `auto`, `33`, `45`, `78`, `off`. With `auto`, a speed written in the format
  wins; otherwise 45 for a 7″ and 33⅓ for the rest.
- `side`: which side to print. Blank means the first side. Records without lettered
  positions print every track.
- `show_strip`: the sleeve sticker.
- Also `show_qr` (on the sticker), `key_notation`, `bpm_range`, `font` and `font_scale`.

### `obi-strip`

An OBI wraps round the spine edge of an LP sleeve. Flat, seen from the printed side, it has
three panels: **back | spine | front**.

- **Front panel.** The artist and title are as large as they fit, from the top. Styles;
  format · year · country; the label; and the catalogue number sit at the foot, under a
  rule.
- **Spine.** "Artist · Title" reads top to bottom, with the catalogue number at the foot.
  Spine width follows the disc count:
  - 4 mm for one disc;
  - 7 mm for two;
  - 10 mm for three.

  The disc count comes from the side letters (two sides per disc) or from "2-5" style
  positions.
- **Back panel.** The tracklist is drawn as track bands. On this narrow panel the badges
  stack under each title. The QR code and catalogue number are at the foot.

The type grows with the strip's length: a full LP strip reaches twice the usual base,
because it is read from across the room. Each panel takes its margin from its own width.

Options: `spine` (`auto`, `1`, `2`, `3`), `tracklist`, `tempo` (tempo and key badges in the
tracklist), `show_qr`, `fold_lines`, `key_notation`, `bpm_range`, `font`, `font_scale`.

### `test-pattern`

This is a calibration print for any stock:
- a 1 mm border flush with the edge (uneven widths show an offset);
- a TOP-LEFT corner mark;
- millimetre rulers;
- a FEED arrow pointing at the edge the driver sends first, which follows from the stock's
  `rotate_cw`;
- the design size, stock id, feed size and canvas size.

On round stocks it also outlines the disc and hole where the centre label puts them.

## Tests

- **`TestLayoutProperties`.** Renders every template on every stock it supports, in every
  font, at both ends of the text-size range, for full, stressed, blank and tempo-less
  records. It checks that:
  - content stays inside the safe area;
  - no two content elements overlap;
  - no text is under 1.6 mm;
  - no line is under 0.25 mm;
  - no QR module is under the floor.
- **`TestLayoutOptionsKeepProperties`.** Repeats those checks for each value of every
  option.
- **`TestSameLinesAtBothResolutions`.** Checks that 203.2 and 300 dpi set the same words on
  the same lines.
- **`TestReferenceRenders`.** Compares every render with its 1-bit reference in
  `testdata/layout-reference`.
  - After an intended change, regenerate the references with
    `go test ./internal/layout -run TestReferenceRenders -update`.
  - Set `CRATE_LABELS_CONTACT_DIR` to also write one contact sheet per template.
- **`TestQREncodesTheDiscogsURL`.** Reads every template's QR code back, module by module.
