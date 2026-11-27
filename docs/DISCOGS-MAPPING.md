# Discogs release JSON → LabelData

This is how crate-labels turns one Discogs release into the `LabelData` a template prints
(the JSON form is in [API.md](API.md)). The input is a `GET https://api.discogs.com/releases/{id}`
response. The code is `discogs.Map` in `internal/discogs/mapping.go` and `summary.go`.

The recorded inputs in `testdata/discogs/` are real Discogs responses cut down to their CC0
release data: community and marketplace fields are removed and image URLs are placeholders
(see NOTICE):
`release-118638.json`, `release-89569.json`, `release-10550772.json`, `release-10953799.json`
and `master-4589.json`.

What a label needs from a release is small: who, what, which pressing (label, catalogue
number, year, size), what kind of music, and the tracks. The rules below keep Discogs' own
wording and order wherever Discogs already has an answer, and leave choosing and trimming
to the template, which knows how much room it has.

## Fields

| LabelData | from | rule |
|---|---|---|
| `artist` | `artists[]` | the credit line (below) |
| `title` | `title` | trimmed |
| `label` | `labels[0].name` | the first label, stripped; `""` when there is none |
| `catno` | `labels[0].catno` | the first label's, trimmed; Discogs' placeholder `none` (any case) becomes `""` |
| `year` | `year` | the year as text when it is above 0, else `""`; `released` is not used |
| `country` | `country` | trimmed, `""` when missing |
| `format` | `formats[]` | the disc size (below) |
| `styles` | `styles[]` | the list as Discogs orders it, cleaned (below) |
| `genres` | `genres[]` | the same |
| `tracks[]` | `tracklist[]` | the track rules (below) |
| `qr_url` | `id` | `https://www.discogs.com/release/{id}` |
| `release_id` | `id` | the integer |
| `notes`, `price`, `condition` | | `""`; the user fills them in |

## Names and credit lines

**strip(name)** removes the ` (n)` Discogs appends to names several artists or labels share,
then trims: `Paul Bradley (10)` becomes `Paul Bradley`.

**The credit line** is built from `artists[]` (the release's, or one track's) in their order.
Each entry's name is `name`, or with the `discogs.prefer_anv` setting its `anv` when that is
not empty (the name as printed on this record), and then stripped. Entries left empty are
skipped. Between two printed names goes the first one's `join`, as Discogs gives it:

- `,` and `;` follow the name directly and are followed by a space: `A, B`;
- any other join stands between spaces, with its own spelling and case: `A & B`, `A / B`,
  `A Vs. B`, `A Feat. B`;
- a blank join between two names reads `, `.

The last artist's join is never printed. `prefer_anv` applies to the release artists and to
the track artists alike.

## Disc size

`format` is the size of the record, read from the `descriptions` of the release's disc
formats: the `formats[]` entries named `Vinyl`, `Acetate`, `Lathe Cut`, `Flexi-disc`,
`Shellac`, `Pathé Disc` or `Edison Disc` (in any case), the records on Discogs'
[formats list](https://www.discogs.com/help/formatslist). Other media (CD, Cassette, File,
Laserdisc) have no size to print.

1. The first description, in the order the release lists its formats and their descriptions,
   that is a size as the formats list writes one: whole inches, optionally with `½`, and a
   `"` (`12"`, `10"`, `7"`, and rarer sizes such as `8"` or `6½"`), or whole centimetres
   (`25cm`). It is kept exactly as written. The first listed medium is the main one, so a 12"
   with a bonus 7" prints `12"`.
2. Otherwise `LP`, if a disc format lists `LP`. Discogs' format guideline (6.14) makes `LP` on
   its own mean a 12" 33 rpm record and adds a size only when it differs (`LP, 10"`), so an LP
   usually lists no size.
3. Otherwise `""`. Descriptions that describe the release rather than the disc (`Album`, `EP`,
   `Single`, `Compilation`) and free text are not read.

Quantity is not printed: `3×Vinyl, LP` prints `LP`.

## Genres and styles

`styles` and `genres` each keep Discogs' order. Values are trimmed, empty ones dropped, and a
value repeated in another case is kept once. Nothing is cut: the label editor shows them all,
and a template prints as many as it has room for (the current templates print up to three),
preferring styles (`Happy Hardcore`) to genres (`Electronic`) because styles say more about
how a record plays. The release and master ids
play no part.

## Tracks

`tracks[]` follows `tracklist[]` in order:

- `heading` rows (a side, disc or part name) are not tracks and are left out;
- a row with `sub_tracks` (an `index` row: one groove holding several pieces) is replaced by
  its sub-tracks; a sub-track with no position, or no artists, of its own takes the index
  row's;
- an `index` row without sub-tracks stays one row;
- rows with no title are left out;
- `position` and `duration` are Discogs' text, trimmed (`""` if missing);
- `artist` is the track's own credit line, `""` when the track credits nobody;
- `bpm` and `key` start empty (null) and `included` is true.

## Other release facts

**Artwork** (for templates with `needs_artwork`): the first `images[]` entry with
`type == "primary"` and a `uri`, else the first image with a `uri`, else `cover_image`, else
`thumb`. It is fetched from i.discogs.com **without** the token.

**ReleaseSummary.format** (shown in the UI, not printed): each `formats[]` entry as
`<qty>×<name>` (just `<name>` when qty is 1) followed by its descriptions, joined `, `; entries
joined `; `.

**CollectionItem** rows use the same rules on `basic_information`: `disc_size` is the disc
size above, and `notes` holds the row's custom-field values, one per line in Discogs' field
order, each trimmed, empty ones left out.

## Reference parsing (`/api/discogs/lookup?q=`)

1. A bare id, optionally prefixed `r` or `m` and optionally in brackets, matching
   `^\[?\s*([rm])?\s*(\d{1,10})\s*\]?$` (any case), is a release (a master with `m`).
   0 is nothing.
2. Otherwise the text must mention `discogs`. The scheme, query and fragment are dropped,
   and the first `(?:^|/)(master|release)s?/(\d{1,10})` (any case) in the text is the
   reference, so with two links pasted the first one counts. This reads
   `www.discogs.com/release/118638-Slug`, `/de/release/…`, the older slug-first
   `discogs.com/Artist-Title/release/118638`, `/sell/release/…` and
   `api.discogs.com/releases/118638`.
3. A master resolves through `GET /masters/{id}` to its `main_release`: master 4589 gives
   release 118638.
4. Anything else is not a Discogs reference, and the UI searches for the text instead.

## Worked examples (from the fixtures)

### 1. `release-118638.json`: DJ Force & The Evolution, KF039 (12")

Input (abridged): `artists: [{name:"DJ Force & The Evolution", anv:"", join:""}]`,
`title: "Simply Electric / Out Of Control"`, `labels: [{name:"Kniteforce Records", catno:"KF039"}]`,
`year: 1995`, `country: "UK"`, `formats: [{name:"Vinyl", qty:"1", descriptions:["12\"","45 RPM"]}]`,
`genres: ["Electronic"]`, `styles: ["Breakbeat","Happy Hardcore"]`,
`tracklist: [{position:"A", title:"Simply Electric", duration:"4:39", type_:"track"}, {position:"B", title:"Out Of Control", duration:"5:05", type_:"track"}]`.

```json
{"artist": "DJ Force & The Evolution", "title": "Simply Electric / Out Of Control",
 "label": "Kniteforce Records", "catno": "KF039", "year": "1995", "country": "UK",
 "format": "12\"", "genres": ["Electronic"], "styles": ["Breakbeat", "Happy Hardcore"],
 "tracks": [
   {"position": "A", "title": "Simply Electric", "artist": "", "duration": "4:39", "bpm": null, "key": null, "included": true},
   {"position": "B", "title": "Out Of Control", "artist": "", "duration": "5:05", "bpm": null, "key": null, "included": true}],
 "notes": "", "price": "", "condition": "",
 "qr_url": "https://www.discogs.com/release/118638", "release_id": 118638}
```
ReleaseSummary.format: `Vinyl, 12", 45 RPM`.

### 2. `release-10550772.json`: four artists joined with "/" and a name variation (KF70)

Input: `artists: [{name:"Sanxion", join:"/"}, {name:"Dj Nicky Allen", anv:"Nicky Allen", join:"/"}, {name:"TNO Project", join:"/"}, {name:"Mannik", join:""}]`,
`formats: [{name:"Vinyl", descriptions:["12\"","33 ⅓ RPM","EP","Limited Edition"]}]`,
`labels[0]: {name:"Kniteforce Records", catno:"KF70"}`, `year: 2017`, `styles: ["Breakbeat","Hardcore"]`,
`tracklist[1]: {position:"A2", title:"All The Time", artists:[{name:"Dj Nicky Allen", anv:"Nicky Allen"}]}`.

- `artist` is `"Sanxion / Dj Nicky Allen / TNO Project / Mannik"`; with `discogs.prefer_anv`
  it is `"Sanxion / Nicky Allen / TNO Project / Mannik"`.
- `format` is `'12"'`, `catno` `"KF70"`, `year` `"2017"`, `styles` `["Breakbeat","Hardcore"]`.
- `tracks[0]` is `{"position":"A1","title":"Waiting On My Feelings","artist":"Sanxion","duration":"",...}`.
  `tracks[1].artist` is `"Dj Nicky Allen"`, or `"Nicky Allen"` with `discogs.prefer_anv`.

### 3. `release-10953799.json`: a CD by "Paul Bradley (10)" (no disc size, no country)

Input: `artists: [{name:"Paul Bradley (10)"}]`, `labels[0]: {name:"Knitebreed", catno:"BREEDCD01"}`,
`formats: [{name:"CD", descriptions:["Mini-Album","Limited Edition"]}]`, no `country`, `year: 2017`,
and 8 tracks with positions `"1"`…`"8"`.

- `artist` is `"Paul Bradley"` (stripped), `label` `"Knitebreed"`, `catno` `"BREEDCD01"`.
- `format` is `""` (a CD is not a disc format here) and `country` is `""`.
- `tracks` has 8 rows, positions `"1"`…`"8"`, durations `""`.

### 4. `release-89569.json`: a 3×LP compilation (track artists, second label ignored)

`labels: [{name:"Kniteforce Records", catno:"KF LP 002"}, {name:"Remix Records", …}]` gives
`label` `"Kniteforce Records"` and `catno` `"KF LP 002"`.
`formats: [{name:"Vinyl", qty:"3", descriptions:["LP","Compilation"]}]` gives `format` `"LP"`
(no size is listed), and the summary format is `3×Vinyl, LP, Compilation`. `artist` is `"Various"`.
`tracklist[0]: {position:"F1", title:"Sumaki (Jimmy J & Cru-L-T Mix)", artists:[{name:"DJ Desire (2)"}]}`
gives the track artist `"DJ Desire"`.
