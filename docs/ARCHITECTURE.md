# How crate-labels is built

crate-labels is one Go binary for macOS. It looks records up on Discogs with the user's own
token, draws labels for them, and prints them on Bluetooth thermal printers, network (IPP)
printers or any CUPS queue. `crate-labels serve` starts an HTTP server on 127.0.0.1 that
also serves the browser UI, which is embedded in the binary as plain HTML, CSS and
JavaScript with no build step.

Module: `github.com/ljagiello/crate-labels`. The other docs:
- [API.md](API.md): the HTTP API the UI talks to;
- [DISCOGS-MAPPING.md](DISCOGS-MAPPING.md): how a release becomes label fields;
- [TEMPO.md](TEMPO.md): the BPM estimator, how it was measured and where it falls short.

## From a release to a printed label

```
Discogs release JSON ──discogs.Map──▶ layout.LabelData
LabelData + template + stock + options ──layout.Render──▶ *image.Gray, exactly Catalog.CanvasFor(model, stock) pixels
image ──Catalog.Preview──▶ the dots the printer will print (what the UI shows)
images ──Catalog.Print──▶ Driver.Send over a printer.Link (Bluetooth, TCP, file), or over IPP or CUPS
```

## Packages

| Package | What it does |
|---|---|
| `internal/printer` | The shared types (`Stock`, `Model`, `BLEProfile`, `Canvas`, `Target`, `Link`, `Driver`, `Settings`, `Error`), the built-in catalog (`data/stocks.toml`, `data/models/*.toml`) merged with the user's TOML, the fit rule, the driver and link registries, and `Catalog.Print`, `Preview` and `CanvasFor`. |
| `internal/transport/ble` | Bluetooth LE in pure Go over a `Backend` interface: `Link`, `Scan`, `Probe`, chunked writes, one connection per address, address normalising. |
| `internal/transport/ble/corebluetooth` | The CoreBluetooth backend (darwin). Only `cmd/crate-labels` imports it: it starts the system Bluetooth manager when the package loads, which test binaries must not do. |
| `internal/transport/tcp`, `internal/transport/fake` | A raw TCP link (port 9100), and an in-memory link for tests and dry runs. |
| `internal/raster` | Grey conversion, resampling, dithering and bit packing, and `Rasterize`: the one path from a label image to head rows for every thermal driver. |
| `internal/heatshrink` | The compression the Munbyn protocol uses. |
| `internal/driver/phomemo`, `munbyn`, `clabel` | The thermal printer families. `clabel/tspl` builds TSPL pages and can serve other TSPL printers. |
| `internal/driver/internal/thermal` | Settings every thermal driver shares: rotation, offsets, chunk size and pacing. |
| `internal/ipp`, `internal/sheets` | The IPP client and codec, the URF encoder and DNS-SD discovery; laying labels out on sheets, and a small PDF writer. |
| `internal/driver/ipp`, `cups`, `internal/driver/internal/page` | The page-printer families and what they share. |
| `internal/layout` | `LabelData`, the templates, the bundled OFL fonts (`FontFS`), QR codes and `Render`. |
| `internal/discogs` | The Discogs client (personal token, token-bucket pacing from Discogs' published rate limits and its X-Discogs-Ratelimit headers, 429 hold-off, SQLite cache, Keychain token store) and `Map`. `ReleaseVideos` matches a release's YouTube links to its tracks. |
| `internal/store` | Everything crate-labels remembers, in one SQLite file. See "What is stored". |
| `internal/audio` | Turns an audio file into mono float32 samples at a chosen rate, capped in length. WAV and AIFF are read in Go and resampled with a windowed-sinc filter; other formats go through `/usr/bin/afconvert`, or `ffmpeg` when afconvert can't read them. The format is judged from the file's first bytes. `DecodeYouTube` fetches a video's audio with yt-dlp into a temporary folder and removes it afterwards. |
| `internal/tempo` | The tempo estimator: samples in, BPM, confidence, steadiness and half/double alternatives out, with Discogs styles choosing between half and double. Pure Go, no I/O. |
| `internal/tempo/tempoeval` | Development only: scores the estimator against a list of known tempi. Not in the binary. |
| `internal/service` | The layer the web server and the CLI share: lookups, previews, the print queue, crates, history, the printed state and BPM detection. |
| `internal/web` | The `net/http` API, its guards, the event stream, and the embedded UI. |
| `cmd/crate-labels` | The CLI: `serve`, `printers`, `print`, `templates`, `discogs`, `bpm`, `version`. It imports every driver and transport so they register. It has no tests because it links CoreBluetooth; the logic it needs lives in `internal/service`. |

## The print queue

- **One worker per device.** A device is the printer's link plus its normalised address,
  so two saved printers that reach the same Bluetooth printer share one queue. Jobs on one
  device run one at a time and in order; different devices print at the same time.
- **Jobs live in SQLite.** A job holds its records' label data, frozen when it was queued,
  and every change to it is pushed to subscribers (the UI's event stream).
- **Ownership.** Each job records the process that queued it as `<pid>:<start time>`. The
  start time comes from the kernel's process table, so a reused pid is not mistaken for the
  old process. When the server takes its port it marks jobs whose process has gone as
  interrupted; a CLI print running at the same time is left alone. A process that stops
  marks its own unfinished jobs interrupted. Nothing restarts by itself.
- **Cancelling.** A queued job is cancelled at once. A printing job's context is cancelled
  and the driver stops between writes; the job keeps the count of labels that got out.

## What is stored

`internal/store` keeps one SQLite file, `crate-labels.sqlite`, in the data folder. Its schema
is version 1 and is created in one transaction. The file is marked with its own SQLite
application id. A file without that mark, or from a newer build, is refused and left
untouched, so a pre-release database (`crate-labels.db`, never opened by this build) or
another program's file is never altered by mistake. Later schema changes will be numbered
steps, applied in order inside one transaction.

| Table | Holds |
|---|---|
| `settings` | UI and user preferences, as JSON values |
| `discogs_identity` | the Discogs username the token belongs to and where the token is kept; never the token |
| `response_cache`, `image_cache` | Discogs replies and thumbnails, pruned by age and by total size |
| `tempo_cache` | BPM results, keyed by a hash of the audio (or of path, size and modification time), the options and the estimator version |
| `printers` | saved printers and their calibration |
| `crates`, `crate_records` | crates and the releases in them, in order, each with what the crate list shows and an optional edited label |
| `print_jobs` | the queue and its history, with owners |
| `print_history` | the last 500 labels printed, for reprinting; a repeat replaces the older entry |
| `label_prints` | one row for every record a job printed, kept for good: release id (if any), job, template, copies |
| `printed_overrides` | the user's "printed" or "not printed" for a release, over what `label_prints` says |

Whether a release "has a label" is worked out, not stored: an override if there is one, else
whether `label_prints` has a row for it. Deleting history never changes it, and printing a
release again lifts a "not printed" override.

## Driver interfaces

```go
type Link interface {
    MaxWrite() int                                                               // the largest single write
    Write(ctx context.Context, data []byte, role string, withResponse bool) error // no chunking
    Recv(ctx context.Context, timeout time.Duration) ([]byte, error)             // printer.ErrTimeout on timeout
    Close() error
}

type Driver interface {
    Family() string
    Links() []LinkKind                 // byte protocols: LinkBLE, LinkTCP, LinkFile
    Canvas(m *Model, s *Stock) Canvas  // usually printer.DefaultCanvas
    Preview(m *Model, s *Stock, label *image.Gray, settings Settings) (*image.Gray, error)
    Send(ctx context.Context, pc *PrintContext, labels []*image.Gray) (Result, error)
}
```

- A label reaches a driver as an `*image.Gray` (white = 255) of exactly
  `Canvas(model, stock)` pixels, in design orientation. Rotating, placing on the head,
  dithering and packing are the driver's job; thermal drivers use `raster.Rasterize`.
- `pc.Settings` is the model's options, then the printer's saved calibration, then the
  job's own options, each overriding the one before. Every thermal driver reads
  `extra_rotate_cw`, `offset_dots`, `darkness` (`density` on Clabel) and `ble_gap_ms`;
  Phomemo also reads `paper_align`, where narrow paper sits under the head (`right` or
  `center`).
- `pc.Catalog` is the catalog that sent the job, for looking up related models.
- When a job fails part-way, `Send` returns the partial `Result` (with `Printed` set) **and**
  the error. When it is cancelled it returns `ctx.Err()`, so
  `errors.Is(err, context.Canceled)` holds; the service records that as cancelled, or as
  interrupted when the process is shutting down.
- Failures meant for the user are `printer.Errorf(...)` and say what to do about them.

## Bluetooth

- Devices are known by their CoreBluetooth UUID, trimmed and lower-cased; that string is
  the key everywhere.
- One connection per address, held from connect until the system confirms the disconnect.
  A probe during a print is told the device is busy instead of breaking the job, and an
  abandoned connect can't cancel a later one. The service also refuses to probe a device
  that is printing.
- `Scan` goes by the advertised service UUIDs first: a device offering a model's service
  can only be one of the models on that service, and its name (`ble.name_patterns`) picks
  among them. Only a device offering no known service is matched by name alone, and a
  generic serial service counts only together with a matching name. `Probe`
  connects once and tells the family from the services the device offers; for Phomemo it
  then picks the model from the device's first notification, matched against the hello
  frames each model's TOML lists.
- Writes without response get back-pressure from CoreBluetooth. A model's `chunk_gap_ms`
  and the per-printer `ble_gap_ms` add pauses on top.

## How the local server protects itself

- It listens on 127.0.0.1 only, and the `Host` header must be 127.0.0.1, localhost or ::1.
- Writes (POST, PUT, PATCH, DELETE) must come from the server's own origin: `Origin: null`,
  other ports and `Sec-Fetch-Site: cross-site` are refused. They must also be
  `application/json`, which a cross-site page can't send without a preflight the server
  never grants.
- The one exception is `POST /api/tempo`, which takes the audio as
  `application/octet-stream`. That type also needs a preflight. `text/plain`, form and
  multipart bodies are still refused there, the Origin, `Sec-Fetch-Site` and Host checks
  still run, and uploads are capped at 400 MB.
- Every response sends `X-Frame-Options: DENY`, `frame-ancestors 'none'` and `nosniff`.
- The Discogs token lives in the macOS Keychain. It is held in memory and read again after
  5 minutes or after a 401, sent only to `https://api.discogs.com`, and never logged or
  written to SQLite.
- `lp`, `lpstat`, `lpoptions`, `security`, `afconvert`, `ffmpeg` and `yt-dlp` run with
  argument lists, never through a shell. The token goes to `security` on stdin. Paths given
  to the audio converters are absolute, so none can be read as an option; ffmpeg gets
  `file:` URLs, and a YouTube id is checked against `^[A-Za-z0-9_-]{11}$` and passed after `--`.

## Adding printer support

1. **A model of a family crate-labels already speaks:** add a `[[model]]` to a TOML file in
   `~/.config/crate-labels/printers/`, or to `internal/printer/data/models/` to ship it. No code.
2. **A label size:** add a `[[stock]]`. Every printer it physically fits offers it.
3. **A new protocol family:**
   - make `internal/driver/<family>/` with a type that implements `printer.Driver`, and call
     `printer.Register(New())` in `init()`;
   - byte protocols list `LinkBLE, LinkTCP, LinkFile` in `Links()` and write through
     `ble.WriteChunked`;
   - draw with `raster.Rasterize`, and read settings with `internal/driver/internal/thermal`;
   - keep the encoder a pure function and test its bytes against a `fake.Link`;
   - add the model TOML, and blank-import the package in `cmd/crate-labels/main.go`.
   TSPL printers can reuse `clabel/tspl`.
4. **Anything with a macOS driver:** use the `cups` model. No code.

## Testing

- `go test -race ./...` needs no printer, no Bluetooth and no network.
- Printer byte streams are compared with fixtures in `testdata/golden/`; compressed output
  is compared after decompressing.
- `testdata/discogs/` holds recorded Discogs releases, and `testdata/ipp/` a real
  Get-Printer-Attributes reply.
- The service and web tests run against a stand-in printer driver on the `file` link, a
  test model, fake templates and a Discogs stub that serves the recorded releases
  (`internal/service/servicetest`).
- The raster code is exact to the bit. Keep its explicit `float32(...)` conversions (Go may
  fuse multiply-adds on arm64), and round half up with `math.Floor(x + 0.5)`.
