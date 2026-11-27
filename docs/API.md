# crate-labels HTTP API

`crate-labels serve` runs this API on `127.0.0.1` (port 8787 by default) together with the
browser UI it was written for: `internal/web/static/` (`index.html`, `app.js`, `style.css`),
plain JavaScript with no build step. This page lists every endpoint the UI calls. Endpoints
the UI does not call are marked *optional*.

How a Discogs release becomes label fields is in [DISCOGS-MAPPING.md](DISCOGS-MAPPING.md).

## Ground rules

- **Local only.** The server binds `127.0.0.1`. A request whose `Host` is not `127.0.0.1`,
  `localhost` or `::1` gets **400**, which stops DNS-rebinding pages from reaching it.
- **Writes come from the UI's own origin.** POST, PUT, PATCH and DELETE are refused with
  **403** `{"error":"Cross-site requests are not allowed."}` when:
  - `Sec-Fetch-Site` is present and is neither `same-origin` nor `none`; or
  - `Origin` is present and is not `http://` + `127.0.0.1`, `localhost` or `[::1]` + this
    server's port. That rules out `Origin: null` and other local ports.
- **Writes are JSON.** They must send `Content-Type: application/json`, even with an empty
  `{}` body, or get **415**. A cross-site page can only send that type after a CORS
  preflight, and the server never answers one. The one exception is the audio upload
  `POST /api/tempo`, which takes `application/octet-stream` (see "Tempo"). That type needs a
  preflight too, and the Host, Origin and `Sec-Fetch-Site` checks still apply.
- **Every response** carries `X-Frame-Options: DENY`,
  `Content-Security-Policy: frame-ancestors 'none'` and `X-Content-Type-Options: nosniff`.
- Bodies are JSON, except the preview PNG, the image proxy, the event stream and the audio
  body of `POST /api/tempo`. Times are **milliseconds since the Unix epoch**.
- **Errors** are `{"error": "<what went wrong and what to do>"}`. The UI shows the text as it is.

  | status | meaning |
  |---|---|
  | 400 | the request breaks a rule: unknown template, a stock the printer can't print on, a full crate, a malformed token, … |
  | 401 | Discogs refused the token, or the endpoint needs a token and none is set |
  | 404 | no such printer, crate, crate record, job or history entry, or no such release on Discogs |
  | 409 | the device is busy (a Bluetooth probe while a job prints on it), or the job belongs to another crate-labels process |
  | 413 | an audio upload over the size cap (400 MB) |
  | 415 | a write that is not `application/json` (`application/octet-stream` for `POST /api/tempo`) |
  | 422 | the body or query has the wrong shape: a missing field, a wrong type, a value out of range |
  | 429 | Discogs is still rate-limiting after the retries |
  | 501 | the feature is not in this build (Bluetooth, IPP discovery, CUPS) |
  | 502 | Discogs could not be reached, or answered with something unexpected |

## Data shapes

```ts
type Track = {
  position: string; title: string; artist: string; duration: string;   // "" when unknown
  bpm: number | null;                                                   // whole BPM
  key: string | null;                                                   // as typed: "8A", "Am", ...
  included: boolean;                                                    // printed on the label; default true
};

type LabelData = {            // what a template draws
  artist: string; title: string; label: string; catno: string;
  year: string; country: string;
  format: string;     // a disc size as Discogs writes it ('12"', '7"', '10"', rarer '8"' …), 'LP' or ''
  genres: string[]; styles: string[];   // Discogs' order, trimmed, de-duplicated, not cut; templates print what fits
  tracks: Track[];
  notes: string; price: string; condition: string; // free text; price and condition are for seller tags
  qr_url: string;                                  // "" = no QR code
  release_id: number | null;                       // the Discogs release, if any
};
// Posted LabelData is read leniently: unknown keys are ignored, missing keys get the
// defaults above, "styles" and "genres" may be one comma-separated string, release_id may
// be a numeric string, and a track's bpm may be a decimal (rounded to a whole BPM).

type LabelSource = {          // what to print; the first one present wins
  data?: LabelData;           // a label as edited in the UI
  release_id?: number;        // a Discogs release
  ref?: string;               // anything pasted: an id, a release or master URL, "[r123]", ...
};

type OptionSpec = {           // one template option; the UI draws a control for it
  id: string; label: string;
  type: "bool" | "int" | "float" | "choice" | "text";
  default: boolean | number | string;
  min: number | null; max: number | null;
  choices: string[]; choice_labels: string[];   // choice_labels is [] or as long as choices
  help: string;
};
// The UI shows a "text" option whose id is "track" as a menu of the record's tracks
// (value = track position; "" = the first ticked track).

type Stock = {
  id: string; name: string;
  media: "die_cut" | "continuous" | "sheet";
  design_mm: [number, number];          // the area the template draws on (width, height)
  feed_mm: [number, number | null];     // the label as it leaves the printer; null = continuous
  rotate_cw: 0 | 90 | 180 | 270;
  round: boolean;                        // a round label: the preview is clipped to a circle
  sheet: null | { rows: number; cols: number; paper_mm: [number, number] };
};

type Job = {
  id: number;
  status: "queued" | "printing" | "done" | "failed" | "cancelled" | "interrupted";
  created_at: number; started_at: number | null; finished_at: number | null;
  printer_id: string; printer_name: string;
  template: string; stock: string;
  copies: number;          // of each record
  total: number;           // records × copies
  printed: number;         // labels the driver says came out
  progress: number;        // 0 to 1
  message: string;         // the live status line, e.g. "Printed 3 of 8"
  error: string | null;
  source: string;          // "ui", "cli", "test", "crate:<crate name>", "reprint:<history id>" or "retry:<job id>"
  records: number;         // how many labels' worth of data the job holds
  title: string;           // "<artist> – <title>" of the first record (an en dash), or ""
  catno: string;           // the first record's catalogue number
};

type Printer = {
  id: string; name: string;
  model_id: string; model: string;      // model: the model's display name, or model_id if the catalog lacks it
  family: string | null;                // driver family: "phomemo", "munbyn", "clabel", "ipp", "cups", ...
  link: "ble" | "tcp" | "ipp" | "cups" | "file" | null;
  address: string;                      // Bluetooth id, ipp:// URI, CUPS queue, host:port or a file path
  settings: Record<string, unknown>;    // calibration; see "Printers"
  missing_model: boolean;               // the saved model id is not in the catalog
};

type ReleaseSummary = {
  id: number; master_id: number | null;
  artist: string; title: string; label: string; catno: string;
  year: string; country: string;
  format: string;          // readable, e.g. 'Vinyl, 12", 45 RPM'
  genres: string[]; styles: string[];   // as Discogs lists them
  thumb: string; cover: string;         // Discogs image URLs, "" when missing
  url: string;                          // the release page on discogs.com
  tracks: number;                       // tracklist length
  printed: boolean;                     // whether it has a label (see "Printed state")
  videos: VideoRef[];                   // the release's YouTube links, matched to tracks
};

type VideoRef = {
  url: string;       // https://www.youtube.com/watch?v=<id>
  id: string;        // the YouTube id
  title: string; duration: number;   // seconds; 0 = unknown
  track: string;     // the position of the track it plays, "" when it matches none.
                     // Matched on title, position (or side) and duration; when the counts
                     // agree, the rest pair up in order. Videos over 20 minutes are dropped.
};
```

## Static files

| method | path | reply |
|---|---|---|
| GET | `/` | `index.html`, `Cache-Control: no-cache` |
| GET | `/static/{file}` | a file from `internal/web/static/` |

## Health

`GET /api/health` → `{"ok": true, "version": string, "templates": int, "drivers": string[], "drivers_error": string|null, "data_dir": string}`.
The Settings page shows `drivers` and `data_dir`.

## Settings and the Discogs token

`GET /api/settings` →
```json
{"discogs": {"token_set": true, "source": "keychain", "username": "someone"},
 "values": {"ui.template": "dj-single", "...": "..."},
 "labels_printed": 12,
 "data_dir": "/Users/…/Library/Application Support/crate-labels"}
```
- `source` is `"env"` (from `DISCOGS_TOKEN`), `"keychain"` or `"none"`.
- `username` is the account the last identity check found, or null.
- `labels_printed` counts every label ever printed (see "Printed state").
- **No response ever contains the token.**

`PUT /api/settings` with `{"values": {key: any JSON}}` → `{"values": {...every setting...}}`.
Keys match `^[a-z][a-z0-9_.]{0,63}$` and may not contain `token`, `secret`, `password` or
`key_material` (400). A `null` value deletes the key. The keys the UI uses:

| key | type | what for |
|---|---|---|
| `ui.template` | string | the template last used |
| `ui.stock` | string | the stock last used |
| `ui.options` | `{templateId: {optionId: value}}` | the last option values of each template |
| `ui.bpm_input` | string | the audio input (`MediaDeviceInfo.deviceId`) last used for Listen; missing or `""` = the system default |
| `ui.bpm_from_videos` | bool | whether BPMs are filled in from the release's videos (default true) |
| `default_printer` | string | a printer id; set by itself when the first printer is saved |
| `default_template` | string | the first template to show, until `ui.template` is set |
| `theme` | `"auto"`, `"light"` or `"dark"` | also kept in `localStorage["crate-labels.theme"]` |
| `discogs.prefer_anv` | bool | print artist names as credited on the release (see the mapping doc) |

`PUT /api/settings/discogs-token` with `{"token": string}`:
1. The token must match `^[A-Za-z0-9_-]{10,128}$`, or the reply is 400 with a hint.
2. The server asks Discogs `GET /oauth/identity` with that token.
3. If Discogs refuses it (401 or 403), nothing is stored and the reply is **401**
   "Discogs rejected this token; nothing was saved. …".
4. Otherwise the token goes into the macOS Keychain as a generic password (service
   `crate-labels.discogs`, account `personal-access-token`), handed to `/usr/bin/security -i`
   on stdin so it never shows in a process list. It is never written to the database or a log.
5. The reply is the `discogs` status object, with the username recorded.

If Discogs can't be reached to check (network error, 5xx, rate limit), the token is still
stored, the recorded username is cleared, and the reply adds
`"warning": "Saved, but couldn't reach Discogs to check it. …"`, which the UI shows.
`crate-labels discogs token set` behaves the same way.

`POST /api/settings/discogs-token/test` → the status object, after a fresh identity check
(400 when no token is set). `DELETE /api/settings/discogs-token` removes the Keychain item and
the recorded username, and replies with the status object.

On every Discogs call the token comes from `DISCOGS_TOKEN` if that is set, else the Keychain.

## Discogs

These endpoints call `https://api.discogs.com` with a `crate-labels/…` User-Agent and,
when there is a token, `Authorization: Discogs token=<token>`. That header is only ever sent to
api.discogs.com.

- **Pacing.** Requests go through a token bucket sized from Discogs' published limits: 60
  requests a minute with a token, 25 without, over a moving 60 s window. The bucket holds 3
  requests and refills at (limit − 3) per minute, so no 60 s window sees more than the
  limit. If Discogs sends `X-Discogs-Ratelimit`, that value replaces the published one.
- `X-Discogs-Ratelimit-Remaining` can only lower the budget: at most Remaining − 1 more
  requests start before the bucket refills, because Discogs counts a request only from the
  next response on.
- **429.** Every request is held for `Retry-After` (seconds or an HTTP-date). Without it the
  hold is 5 s, doubling with each 429 in a row, plus up to 20 % jitter, and never more than
  60 s. When the hold ends one request goes, then the steady rate resumes. A call retries at
  most twice and waits out at most 20 s of holds in total; after that it replies 429
  ("try again in N s").
- **Cache.** Response bodies are cached in SQLite under the full URL (query sorted, token
  left out). Releases and masters keep for 30 days, searches and master versions for an hour,
  collection pages, folders and the wantlist for 5 minutes, and identity checks are never
  cached. A cached answer does not wait for the pacing. `refresh=true` skips the cache.
- **Errors** become 401 (Discogs said 401 or 403), 404, 429 or 502 (anything else), with
  messages such as "Discogs needs your personal access token for search. Add it in Settings."

`GET /api/discogs/lookup?q=<text>&refresh=<bool>` →
`{"ref": {"kind": "release"|"master", "id": int}, "release": ReleaseSummary, "data": LabelData}`.
`q` is read as the mapping doc's "Reference parsing" describes. A master opens its main
release. Text that names neither is 400 "Paste a Discogs release or master link, or a release id."

`GET /api/discogs/release/{id}?refresh=<bool>` → the same shape.

`GET /api/discogs/search` takes `q`, `barcode`, `catno`, `label`, `artist` and `title`
(strings; at least one must be non-empty, else 400), `type=release|master` (default
`release`, since only releases print), `format` (default: no filter; the UI sends `Vinyl`
while "Vinyl only" is ticked), `page` (1 and up) and `per_page` (1–100, default 50, Discogs'
own default). The server sends `title` to Discogs as `release_title` and keeps only the
digits of `barcode`. The reply is
`{"pagination": {...as Discogs sends it...}, "results": SearchHit[]}`:
```ts
type SearchHit = { id: number; type: string; master_id: number | null;
  artist: string; title: string;     // Discogs' "Artist - Title", split at the first " - "; "(n)" removed from the artist
  label: string;                     // the first label, "(n)" removed
  catno: string; year: string; country: string;
  format: string;                    // Discogs' format list joined with ", "
  thumb: string; cover: string; barcode: string[] /* up to 3 */; printed: boolean };
```

`GET /api/discogs/collection/folders` → `{"folders": [{"id": int, "name": string, "count": int}]}`,
for the account the token belongs to.

`GET /api/discogs/collection` takes `folder_id` (default 0, Discogs' "All" folder), `page`,
`per_page` (1–100, default 100, the most Discogs allows, so a collection takes fewer
requests), `sort=artist|title|catno|label|year|added|rating` (default `artist`),
`sort_order=asc|desc` (default `desc` for `added` and `rating`, else `asc`) and `refresh`.
The UI starts on artist A to Z and always sends both. The reply is
`{"pagination": {...}, "items": CollectionItem[]}`:
```ts
type CollectionItem = { id: number /* the release id */; instance_id: number; folder_id: number;
  master_id: number | null; artist: string; title: string; label: string; catno: string;
  year: string; format: string; disc_size: string /* the record size the label prints, e.g. 12" */; genres: string[]; styles: string[];
  thumb: string; cover: string; date_added: string; rating: number;
  notes: string;   // collection custom-field values, one per line, each trimmed; empty ones left out
  url: string; printed: boolean };
```
*Optional:* `GET /api/discogs/wantlist?page&per_page&refresh` (`per_page` default 100) → the same shape.

`GET /api/discogs/image?url=<url>` proxies a Discogs image so the UI can show thumbnails: it
replies with the image bytes, the upstream `Content-Type` and
`Cache-Control: private, max-age=604800`. Only `https` URLs on a `*.discogs.com` host other
than api.discogs.com are fetched (anything else is 502), never with the token, and the bytes
are cached in SQLite.

## Templates, models, stocks

`GET /api/templates` →
`{"templates": [{"id", "name", "description", "options": OptionSpec[], "stocks": string[], "needs_artwork": bool, "max_tracks": int}]}`.
- `stocks` lists the ids of the stocks the template can draw on.
- `max_tracks` is how many track rows the template prints (0 for templates that don't
  list tracks). When a record has more ticked tracks than the current template prints,
  the UI moves to a track-listing template that fits them all, unless the user chose the
  template by hand for that record.
- `test-pattern` is the calibration print. It takes every stock, and
  `POST /api/printers/{id}/test` uses it by default.
- No template draws cover art yet, so `needs_artwork` is false throughout.
- The list may be empty.

`GET /api/models` →
`{"models": [{"id", "label", "family", "link", "dpi": number, "head_dots": int|null, "stocks": string[], "options": {...the model's default settings...}}]}`.
Models come from `internal/printer/data/models/*.toml` and the user's
`~/.config/crate-labels/printers/*.toml`.

`GET /api/stocks?printer_id=<id>`, `?model_id=<id>` or no query → `{"stocks": Stock[]}`.
For a printer or a model, the stocks it lists come first, then every other stock that
physically fits it. With no query, every stock. An unknown printer is 404.

## Preview

`POST /api/preview` with `LabelSource & {"template": string, "stock": string, "printer_id"?: string|null, "model_id"?: string|null, "options": {...}}`
→ `200 image/png`: the 1-bit image the driver will print, dot for dot, in design orientation.
The UI reads these headers:

| header | example | used for |
|---|---|---|
| `X-Canvas-Width`, `X-Canvas-Height` | `384`, `240` | the size in dots; the UI scales by whole numbers with `image-rendering: pixelated` |
| `X-Canvas-Dpi` | `203.2` | the status line |
| `X-Model-Id` | `phomemo-m110` | the status line |
| `X-Design-Mm` | `48.0x30.0` | `"<w>x<h>"` in mm, for the mm grid and the size labels |
| `Cache-Control` | `no-store` | |

The preview is drawn for the first of: the printer's model; `model_id`; the default
printer's model, if it can print on the stock; the first model that lists the stock; the
first model it fits. It is 400 when that model can't print on the stock ("Phomemo M110 can't
print on 70×80 mm four-track."), when the template doesn't take the stock or is unknown, or
when nothing says what to print. The UI waits 90 ms after the last edit before asking, and
abandons previews that a newer one has replaced.

## Tempo (BPM detection)

`POST /api/tempo?styles=<comma-separated>&min=<bpm>&max=<bpm>` takes **the audio file itself
as the body**, `Content-Type: application/octet-stream`. An optional `X-Filename` header holds
the file's name, percent-encoded (`encodeURIComponent`), because header values must be Latin-1.
Only its extension is used, and only as a hint for bytes the server can't identify from their
first bytes. The reply is `{"result": TempoResult}`:

```ts
type TempoResult = {
  bpm: number;               // the best estimate, to 0.01
  confidence: number;        // 0 to 1; the UI says "sure" from 0.7, "likely" from 0.4, else "check this"
  steady: boolean;           // false when the tempo drifts or changes (a live drummer, a mix)
  alternatives: { bpm: number; score: number; relation: "half" | "double" | "two-thirds" | "three-halves" | "" }[];
  range: [number, number];   // the tempo range the estimate favoured
  seconds: number;           // how much audio was analysed (at most the first 900 s)
};
```

- `styles` are the record's Discogs styles and genres; the UI sends `data.styles`, then
  `data.genres`. They decide between half and double tempo (see [TEMPO.md](TEMPO.md)).
  `min` and `max` (give both or neither, 30–400) set the range directly and win over `styles`.
- WAV and AIFF/AIFC are decoded in Go. MP3, AAC/M4A, ALAC, FLAC, CAF and Opus go through
  macOS's `afconvert`, or `ffmpeg` if that is on `PATH`. The bytes decide the format, not the name.
- Bodies over 400 MB get **413**. Two analyses run at a time and the rest wait. Closing the
  request cancels its analysis.
- Results are cached in SQLite under the SHA-256 of the audio, the tidied styles, the range
  and the estimator version, so the same file answers at once the second time. Failures are
  not cached.
- Errors (400) include "This recording is too short to judge the tempo: give it at least 10
  seconds.", "No steady beat found in this audio. Try a section with drums.", "Can't read this
  audio file (…). WAV, AIFF, MP3, AAC/M4A, ALAC and FLAC work." and the range messages. Any
  other `Content-Type` is **415**.

**Why the octet-stream exception is safe.** The write guard depends on a cross-site page
being unable to send a non-simple request without a CORS preflight, which this server never
grants. Without a preflight a page can only send `text/plain`,
`application/x-www-form-urlencoded` or `multipart/form-data`, and those still get 415 here.
`application/octet-stream` is not CORS-safelisted, so a cross-site page can't send it at all.
The exception covers this one method and path, after the Host, `Origin` and `Sec-Fetch-Site`
checks.

For "Listen…" the UI records in the browser (`getUserMedia` with echo cancellation, noise
suppression and auto gain turned off) for 10–45 s, mixes to mono and posts a 16-bit PCM WAV
at the AudioContext's rate. The server resamples it.

### From the release's videos

`POST /api/tempo/video` with `{"url": string, "styles"?: string[], "min"?: number, "max"?: number, "refresh"?: boolean}`
→ `{"result": TempoResult}`. `url` must be a YouTube link, usually a `VideoRef.url`; anything
else is refused with a message. The server fetches the audio with yt-dlp (install it with
`brew install yt-dlp`; without it the reply says so) into a temporary folder, analyses it and
deletes it. Results are cached by video id and options, so a video is fetched once. This is an
ordinary JSON write with the usual guards. When a release opens, the UI asks for every track
that has a matched video and no BPM yet, one track at a time, unless "BPM from Discogs videos"
is off.

## Printing and jobs

`POST /api/print` with:
```json
{"items": [LabelSource, ...],            // 1 to 500
 "template": "dj-single", "stock": "40x30", "printer_id": "c3c6ed290fd0",
 "copies": 1,                            // 1 to 99, of each item
 "options": {},                          // template options
 "print_options": {}}                    // driver settings for this job only
```
→ the new `Job`, status `queued`. Items given as `release_id` or `ref` are turned into
LabelData before the job is queued, so the job prints what was on Discogs at that moment.
It is 400 for an unknown printer ("No saved printer called …"), a stock the printer can't print
on, or an unknown template.

How jobs run:
- **One queue per device.** Saved printers with the same link and address (one Bluetooth
  printer saved twice, say) share a queue and print one job at a time. Different devices
  print in parallel.
- **One driver call per job.** A job draws every record and sends all the labels at once,
  each record's copies together: `record 1 × copies, record 2 × copies, …`. That way a sheet
  printer lays a whole job out on its sheets.
- **Progress** updates `progress` and `message` at most every 2 % (or when the message
  changes) and goes out on the event stream.
- **Settings**: the model's defaults, overridden by the printer's `settings`, overridden by
  the job's `print_options`.
- **Outcomes**: `done` (everything printed); `failed` (`error` set, `printed` = what came out);
  `cancelled`.
- **Ownership.** A job records the process that queued it (pid and start time). When the
  server takes its port, queued or printing jobs whose process has gone become `interrupted`,
  with error "crate-labels stopped before this job finished". Jobs of a process that is still
  running (a CLI print) are left alone. A process that is stopped (Ctrl-C, SIGTERM) marks its
  own unfinished jobs `interrupted` and keeps their `printed`. Interrupted jobs never restart
  by themselves.
- **Records.** After a job, each record with at least one label out is added to the history,
  and logged as printed (see "Printed state").
- **File printers.** For a printer on the `file` link, the raw bytes for the printer go to
  `address`: to `<dir>/job-<id>.bin` when the address ends in `/` or is a folder, else to
  `address` itself, replacing it.

`GET /api/jobs?active=<bool>&limit=<1..200, default 30>` → `{"jobs": Job[]}`, newest first.
`active=true` keeps only queued and printing jobs.

`GET /api/jobs/{id}` → `Job`, or 404.

`POST /api/jobs/{id}/cancel` → `Job`. A queued job is `cancelled` straight away. A printing
job is told to stop and becomes `cancelled` shortly after, keeping `printed`; watch the event
stream for it. A job whose process has gone becomes `interrupted`. A job printing in another
running crate-labels process (a CLI print) can't be stopped from here: **409**.

`POST /api/jobs/{id}/retry` → a **new** `Job` with the same records and settings and
`source: "retry:<id>"`. It is 400 while the old job is still queued or printing.

**Event stream.** `GET /api/jobs/events` is `text/event-stream`. It starts with `retry: 2000`,
then sends one event for every job change:
```
event: job
data: {"id":7,"status":"printing","progress":0.5,"message":"Printed 2 of 4",...the whole Job...}

```
It sends a `: keep-alive` comment every 15 s and ends when the server shuts down, so Ctrl-C
is not held up. While any job it knows is queued or printing, the UI also polls
`GET /api/jobs?limit=12` every 4 s in case the stream dropped. When a job ends `done` or
`failed`, the UI reloads the history and the printed dots.

## Printers

`GET /api/printers` → `{"printers": Printer[], "default": string|null, "calibration_keys": {"extra_rotate_cw":"int","offset_dots":"int","darkness":"int","paper_align":"str","ble_gap_ms":"float","speed":"int","density":"int"}}`.
Printers used most recently come first, then the rest by name.

`POST /api/printers` with `{"name": string (1 to 80), "model_id": string, "address": string = "", "settings": {}}`
→ `Printer`, with a new 12-hex-digit id. An unknown `model_id` is 400. The first printer
saved becomes `default_printer`.

`PATCH /api/printers/{id}` with any of `{"name", "model_id", "address", "settings"}` →
`Printer`. `settings` is **merged** into what is saved; a `null` value removes that key.

`DELETE /api/printers/{id}` → `{"deleted": id}`. Deleting the default printer clears
`default_printer`.

Calibration settings are checked on POST and PATCH:
- empty and `null` values are dropped;
- `extra_rotate_cw` must be 0, 90, 180 or 270;
- `offset_dots`, `darkness`, `density` and `speed` must be whole numbers;
- `ble_gap_ms` must be a number from 0 to 1000;
- `paper_align` (Phomemo's paper position) must be `right` (narrow paper against the right
  edge of the head; the M110 and M220 default) or `center` (paper between centring guides;
  the D30 default);
- other keys are kept as they are.

Who reads what: Phomemo and Munbyn read `darkness`; Clabel reads `density` (0–15) and
`speed`; Phomemo reads `paper_align`; every Bluetooth driver reads `ble_gap_ms`; every thermal
driver reads `extra_rotate_cw` and `offset_dots`. A negative `offset_dots` counts as 0,
because a label can't start before the head's first dot; Phomemo's paper position is the way
to centre a narrow label. So the UI only offers 0 and up.

`POST /api/printers/{id}/test` with `{"stock": string, "template": "test-pattern", "options": {}}`
→ `Job`: the template printed with `{"artist":"crate-labels","title":"Test pattern"}`.

Finding printers. Each of these is 501 `{"error":"… is not available in this build yet (…)"}`
when the build lacks it:
- `GET /api/printers/ble/scan?timeout=<0..30, default 6>&show_all=<bool>` →
  `{"devices": [{"address": string, "name": string, "rssi": int, "service_uuids": string[], "model_ids": string[], "maybe_family": string|null}]}`.
  `model_ids` are the catalog models the device can be. A device that advertises a
  model's Bluetooth service can only be one of the models on that service, and its name
  (`ble.name_patterns`) picks among them; only a device that advertises no known service
  is matched by name alone. `maybe_family` is set when the device advertises a printer
  family's service but no model name matches; a name alone never sets it.
- `POST /api/printers/ble/probe` with `{"address": string, "timeout": number = 15}` →
  `{"probe": {"services": string[], "handshake": "<hex>"|null, "family": string|null, "model_id": string|null}}`.
  While a job prints on that address, in any crate-labels process, the probe is **409**:
  connecting would cut the job's link.
- `GET /api/printers/ipp/discover?timeout=<0..30, default 5>` →
  `{"printers": [{"name", "host", "port", "resource", "uuid", "model", "formats": [], "urf": [], "kind": [], "server", "txt": {}, "uri": "ipp://host:631/ipp/print", "hostname_uri": string|null, "printable": bool, "suggested_model_id": "ipp-label"|"ipp-sheet"}]}`.
  The server alone picks `suggested_model_id`: `ipp-label` when `kind` names `label` or
  `roll` but not `document` (a dedicated label printer), else `ipp-sheet`. An office printer
  such as the Brother HL-L3290CDW sends `document,envelope,label,postcard`, so it is a sheet
  printer. The UI saves `uri` as the address and preselects the suggestion, which the user
  can change.
- `GET /api/printers/cups/queues` →
  `{"queues": [{"name", "description", "state", "enabled", "message", "is_default"}]}`.
  The UI saves `name` as the address, with model `cups`.

## Crates

A crate is a named, ordered list of Discogs releases whose labels print together.

```ts
type Crate = { id: string; name: string; created_at: number; record_count: number };
type CrateRecord = { id: number; position: number /* 0 = first */; release_id: number;
  artist: string; title: string; catno: string; year: number | null; thumb: string;   // for the crate list
  data: LabelData | null;     // the edited label; null = print from Discogs
  edited_at: number | null;   // when data was last saved
  added_at: number;
  printed: boolean };         // see "Printed state"
type CrateDetail = Crate & { records: CrateRecord[] };   // in crate order
```

Rules:
- Names are 1 to 40 characters and unique, ignoring case; runs of spaces become one. A crate
  made without a name is called "Untitled crate", or "Untitled crate 2", "Untitled crate 3", …
  when that is taken. Renaming to a blank or a taken name is 400.
- A crate holds a release once, and at most 500 records: the most one print job takes, so
  a whole crate always prints as one job. Adding past either rule is 400.
- Only Discogs releases go in a crate: a label with no `release_id` is 400.

| method | path | body | reply |
|---|---|---|---|
| GET | `/api/crates` | | `{"crates": Crate[]}`, oldest first |
| POST | `/api/crates` | `{"name"?: string}` | `Crate` |
| GET | `/api/crates/{id}` | | `CrateDetail` |
| PATCH | `/api/crates/{id}` | `{"name": string}` | `Crate` |
| DELETE | `/api/crates/{id}` | | `{"deleted": id}`, records included |
| POST | `/api/crates/{id}/records` | `LabelSource` | `CrateDetail` |
| PUT | `/api/crates/{id}/records/{recordId}/data` | `{"data": LabelData \| null}` | `CrateDetail` |
| DELETE | `/api/crates/{id}/records/{recordId}` | | `CrateDetail` |
| PUT | `/api/crates/{id}/order` | `{"record_ids": int[]}`: every record, each once (else 400) | `CrateDetail` |
| POST | `/api/crates/{id}/print` | `{"template", "stock", "printer_id", "record_ids": int[] \| null, "order": "crate" \| "artist" \| "catno" \| "newest" = "crate", "copies": 1..99, "options": {}, "print_options": {}}` | `Job` (`source: "crate:<name>"`) |

- **Adding.** A record posted as `data` keeps that label as its edited label. One posted as
  `release_id` or `ref` has no edited label. Either way the server stores the artist, title,
  catalogue number, year (when it is a number) and the release's Discogs thumbnail, so the
  crate list can show them without asking Discogs.
- **Editing.** `PUT …/data` saves an edited label; `{"data": null}` drops it, and the record
  prints from Discogs again.
- Record routes are 404 when the record is not in that crate.
- **Printing.** `record_ids` picks records; null or missing means all of them. Each record
  prints its edited label if it has one, else its release's label data. `order` is the order
  the labels come out in:
  - `crate`: the crate's own order;
  - `artist`: by artist, A to Z, case ignored and a leading "The " skipped, then by catalogue number;
  - `catno`: by catalogue number, with case, spaces, dots and dashes ignored and the numbers
    in it compared as numbers (KF9 before KF10), then by artist;
  - `newest`: by year, newest first, then by artist.

  Records missing the key (no artist, no catalogue number, no year) come after the rest,
  and ties keep the crate's order.

## History

```ts
type HistoryEntry = { id: number; printed_at: number; template: string; stock: string;
  printer: string /* the printer's name */; artist: string; title: string; catno: string;
  year: string; label: string; format: string; release_id: number | null;
  copies: number; options: {...template options...}; data: LabelData };
```
`GET /api/history?limit=<1..500, default 100>` → `{"history": HistoryEntry[], "labels_printed": int}`, newest first.

The history is for reprinting. After a job, each record that got at least one label goes to
the top with `copies` = how many it got. An older entry with the same data, template, stock
and options is replaced rather than repeated. The newest 500 entries are kept.

`POST /api/history/{id}/reprint` with `{"printer_id"?: string, "copies"?: 1..99}` → `Job`,
with the entry's data, template, stock and options. The printer defaults to
`default_printer` (400 when there is none), and copies to the entry's copies.

*Optional:* `DELETE /api/history/{id}` → `{"deleted": id}`. This only shortens the list; it
doesn't change whether the record counts as printed.

## Printed state

The UI puts a dot on every record that already has a label. Two things decide it:
- **The print log.** Every record a job prints is logged with its release id (if it has
  one), the job, the template and how many labels came out. The log is never trimmed. A
  release with at least one logged label is printed. The log's total is `labels_printed`.
- **An override**, which the user can set when the log is wrong: `printed` (labelled by hand,
  or before crate-labels) or `not_printed` (the label came off). `auto` means no override.
  Printing a release again lifts a `not_printed` override, because it has a label now.

```ts
type PrintedState = {
  printed: boolean;
  override: "auto" | "printed" | "not_printed";
  labels: number;                 // labels logged for this release
  last_printed_at: number | null; // null when none were
};
```

`GET /api/printed?release_ids=1,2,3` (at most 500) → `{"releases": {"1": PrintedState, ...}}`,
one entry for every distinct id. An id that is not a positive whole number is 422. The UI
uses this to refresh the dots in search results.

*Optional:* `PUT /api/printed/{release_id}` with `{"override": "auto" | "printed" | "not_printed"}` →
`PrintedState & {"release_id": number}`.

## Keyboard and paste (UI only)

- `⌘P` prints the label on screen.
- `/` jumps to the search box.
- Pasting outside a text field puts the text in the search box and runs it: a Discogs link
  or a bare id calls `/api/discogs/lookup`, anything else `/api/discogs/search`.
- `↑` and `↓` move through the lists.
