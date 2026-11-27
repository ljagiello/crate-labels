# crate-labels

[![CI](https://github.com/ljagiello/crate-labels/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ljagiello/crate-labels/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ljagiello/crate-labels?sort=semver)](https://github.com/ljagiello/crate-labels/releases/latest)
[![License: Apache-2.0](https://img.shields.io/github/license/ljagiello/crate-labels)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/ljagiello/crate-labels)](go.mod)

Print labels for your records from your Mac. Paste a Discogs link, and you get a live,
dot-exact preview of the label. Press ⌘P to print it on a Bluetooth thermal label
printer, a Wi-Fi printer, or anything macOS can print to.

One Go binary with no cloud backend. It talks to Discogs directly using your own token.

## Install

crate-labels runs on macOS 13 or later, on Apple silicon and Intel Macs.

1. Download `crate-labels_<version>_macos_universal.tar.gz` and `checksums.txt` from the
   [latest release](https://github.com/ljagiello/crate-labels/releases/latest), then check
   and unpack it:

   ```sh
   shasum -a 256 -c checksums.txt --ignore-missing
   tar -xzf crate-labels_*_macos_universal.tar.gz
   ```

2. The binary is not signed or notarized yet, so Gatekeeper refuses to open a downloaded
   copy ("Apple could not verify…"). Remove the quarantine flag once:

   ```sh
   xattr -d com.apple.quarantine crate-labels   # "No such xattr" means there was none
   ```

3. Move `crate-labels` to a folder on your `PATH` (for example `/usr/local/bin`) and run it:

   ```sh
   crate-labels version
   crate-labels serve --open          # http://127.0.0.1:8787
   ```

Signing and notarization need an Apple Developer ID and repository secrets that aren't
set up yet; see [Releasing](#releasing).

To build from source instead, you need Go (the version in `go.mod`) and the Xcode Command
Line Tools. See [CONTRIBUTING.md](CONTRIBUTING.md).

```sh
go build -o crate-labels ./cmd/crate-labels
```

## Run it

```sh
crate-labels serve --open          # http://127.0.0.1:8787
```

The first time it runs, macOS may ask whether your terminal app may use Bluetooth: allow it.
If a Bluetooth scan finds nothing, or every command exits at once, open System Settings →
Privacy & Security → Bluetooth and turn your terminal app on. The Bluetooth stack starts
with the binary, even for commands that never use it.

### Discogs token

crate-labels needs a Discogs personal access token: Discogs is ending unauthenticated API
access on 1 December 2026. Create one at <https://www.discogs.com/settings/developers>,
then paste it on the Settings page or run:

```sh
crate-labels discogs token set     # kept in the macOS Keychain, never in a file
```

A `DISCOGS_TOKEN` environment variable works too, and wins over the Keychain.

## What it does

- **Find the record.** Paste a Discogs release or master link (or just the release id)
  anywhere on the page, search by catalogue number, barcode, label or artist, or browse the
  folders of your Discogs collection.
- **Make the label.** The release fills in the label: artist, title, label and catalogue
  number, year, styles, and the tracks you tick, with BPM and key. Change any of it before
  printing, or start from a blank label for a record that isn't on Discogs. The preview shows
  the label dot for dot as the chosen printer will print it, on the chosen label stock.
- **Pick a template.** `crate-labels templates` lists them, with the stocks each one takes
  and its options. When a record has more ticked tracks than the template prints, the page
  switches to one that fits them all.
- **Print a crate.** Put records in a crate, drag them into order, and print the whole
  crate, or the ones you tick, as one job: in your order, by artist, by catalogue number, or
  newest first.
- **Keep track.** Every job shows live in the queue, and can be cancelled or retried. Recent
  labels stay in the history for a one-click reprint. Records that already have a label get
  a dot in search results, your collection and your crates. (The API can also mark a record
  as labelled or not by hand; see `PUT /api/printed` in [docs/API.md](docs/API.md).)

## Command line

```sh
crate-labels templates                                  # templates, the stocks each takes, their options
crate-labels discogs lookup https://www.discogs.com/release/118638
crate-labels printers --scan --ipp --cups               # look for printers
crate-labels print --release 118638 --template dj-single --stock 40x30 --printer "Desk M110"
crate-labels print ... --printer phomemo-m110 --dry-run label.png   # the exact 1-bit output; nothing prints
crate-labels bpm --styles "Hardcore,Breakbeat" track.flac             # tempo of an audio file (below)
```

`print` takes `--copies N` and any number of `--option key=value` for the template's options.

## BPM detection

Open a release and its tracks' BPMs fill in by themselves from the **YouTube videos linked
on the release's Discogs page**. Most releases have one per track. crate-labels matches each
video to its track, fetches the audio, works out the tempo on your Mac and deletes the audio
again. That takes a few seconds per track the first time and is instant after that. It needs
[yt-dlp](https://github.com/yt-dlp/yt-dlp) (`brew install yt-dlp`). Only empty BPM fields are
filled, an uncertain result is outlined in yellow, and the "BPM from Discogs videos" box above
the track list turns it off.

Each BPM field also has a **♩** button with three ways in:

- **Discogs video**: the release's videos, this track's first, with the reading and its half
  and double.
- **From a file…**: choose the track's audio file, or drop it on the track's row. WAV, AIFF,
  MP3, AAC/M4A, ALAC and FLAC all work. WAV and AIFF are read directly; the rest go through
  macOS's own `afconvert`.
- **Listen…**: for records that only exist on vinyl. Choose the line-in (or a microphone),
  play the record at the speed shown (from the Discogs format, e.g. "Play at 45 RPM, pitch at
  0"), and it records 10 to 45 seconds while showing the input level. A busy passage with
  drums works best. The browser asks for the input the first time, and the page remembers
  which one you chose.

Every answer says how sure it is ("sure", "likely" or "check this") and whether the tempo
holds steady, with buttons for the half, double and other readings. Click one to put the
whole number on the label.

The release's Discogs styles and genres settle the choice between half and double tempo:
drum & bass comes out at 174, not 87. How the estimate works, how it was tested and where it
goes wrong are in [docs/TEMPO.md](docs/TEMPO.md).

From the command line:

```sh
crate-labels bpm A1.wav B1.mp3                       # one line per file
crate-labels bpm --styles "Drum n Bass" --json track.flac
crate-labels bpm --range 140-190 track.m4a           # give the range yourself
crate-labels bpm --release https://www.discogs.com/release/964163   # from the release's videos
```

A line reads, for example, `174.02 BPM  (confidence 0.83, steady)  alternatives: 87.01 (half)`.
If any file fails, the others still print and the exit status is non-zero. Results are
cached (by file on the command line, by audio content in the UI); `--no-cache` works the
file out again. Only the first 15 minutes of a long file are analysed.

All analysis happens on your Mac and nothing is uploaded. The only audio fetched from the
network is the YouTube videos a release links to, and only while one is being analysed.

## Printers

| Printer | Connection | Status |
|---|---|---|
| Phomemo M110, M220, D30 | Bluetooth LE | the byte stream is tested; **not yet tried on a real printer** |
| Munbyn FM226 | Bluetooth LE | the same |
| Clabel CT-230B, CT-320B | Bluetooth LE | the same |
| Network and AirPrint printers (IPP Everywhere) | IPP over the LAN | checked against a Brother HL-L3290CDW (Validate-Job only) |
| Anything with a macOS driver (Brother QL, Dymo, Zebra, laser printers…) | CUPS (`lp`) | the commands are tested; printing uses your installed driver |

Sheets of labels (Avery 5160 and 5163, and grids you cut yourself on Letter paper) print on
any page printer over IPP or CUPS. Before you use real sheets, print once on plain paper and
hold it up against a sheet.

### Calibrate a thermal printer first

Orientation and the offset across the head have not been checked on real hardware yet. On
the **Printers** page, print the `test-pattern` label. It has a "FEED →" arrow, a TOP-LEFT
mark, a 1 mm border and a 5 mm ruler. Then change that printer's settings until the pattern
comes out right:

| Setting | What it changes |
|---|---|
| `extra_rotate_cw` | 0, 90, 180 or 270: turns the label if it comes out sideways or upside down |
| `offset_dots` | moves the label across the head (8 dots = 1 mm) |
| `paper_align` | Phomemo's paper position: `right` when narrow paper runs against the right edge of the head (the usual default), `center` when it runs down the middle |
| `darkness` (`density` on Clabel) | how much heat; higher is darker |
| `ble_gap_ms` | a pause between Bluetooth packets; raise it if long labels stall |

## Adding printers

1. **Another model of a family crate-labels already speaks** (a new Phomemo, say): put a
   TOML file in `~/.config/crate-labels/printers/`. No code and no rebuild. A model that
   lists no stocks is offered every stock that physically fits its head.

   ```toml
   [[model]]
   id = "phomemo-m120"
   label = "Phomemo M120"
   family = "phomemo"
   link = "ble"
   dpi = 203.2
   head_dots = 384
   paper_width_mm = [20, 50]
   options = { speed = 3, darkness = 8, paper_align = "right" }

   [model.ble]
   name_patterns = ["m120"]
   service = "0000ff00-0000-1000-8000-00805f9b34fb"
   write = { data = "0000ff02-0000-1000-8000-00805f9b34fb" }
   notify = "0000ff03-0000-1000-8000-00805f9b34fb"
   ```

   The built-in models are in `internal/printer/data/models/*.toml`; a user file with the
   same `id` replaces one.

2. **A new label size:** add a `[[stock]]` to `~/.config/crate-labels/stocks.toml`, in the
   format of `internal/printer/data/stocks.toml`. Every printer it fits offers it.

3. **A printer macOS can already print to:** choose it under "This Mac" on the Printers
   page. There is nothing to write.

4. **A new protocol family** (Niimbot, for example): add a package under
   `internal/driver/<name>` that implements `printer.Driver` and calls `printer.Register` in
   `init()`, blank-import it in `cmd/crate-labels/main.go`, and rebuild. Bluetooth, TCP,
   rasterising, dithering and the preview come for free. `internal/driver/phomemo` is the
   smallest example, and `internal/driver/clabel/tspl` builds pages for TSPL printers. More
   in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Where things live

| What | Where |
|---|---|
| Crates, saved printers, history, the print log, caches | `~/Library/Application Support/crate-labels/crate-labels.sqlite` (`CRATE_LABELS_DATA` moves the folder) |
| Your own printer models and stocks | `~/.config/crate-labels/` (`CRATE_LABELS_CONFIG` moves it) |
| Discogs token | the macOS Keychain |

Pre-release builds kept their data in `crate-labels.db` in the same folder. This version
never opens that file and starts a fresh database beside it, so you can delete the old one.

## Development

Setup, the test policy and the commit style are in [CONTRIBUTING.md](CONTRIBUTING.md).
CI runs the same checks on every pull request:

```sh
gofmt -l .                     # must print nothing
go vet ./...
go test -race -count=1 ./...
govulncheck ./...
```

Tests never touch Bluetooth, the network or a printer. Fixed command frames are checked byte
for byte, compressed payloads are checked by decoding them back, and encoder output is
snapshotted in `testdata/golden/` (regenerate with `go test ... -update`; see its README).

The fonts in `internal/layout/fonts/` are under the SIL Open Font License; each family's
licence file sits next to it. `internal/heatshrink` contains a decoder ported from
[heatshrink](https://github.com/atomicobject/heatshrink) (ISC licence; the notice is in
the source); the encoder is crate-labels' own. [NOTICE](NOTICE) lists all third-party material.

## Releasing

1. Check that CI is green on `main`, then tag it and push the tag:

   ```sh
   git tag -a v0.2.0 -m "v0.2.0"
   git push origin v0.2.0
   ```

2. The Release workflow runs the tests, then [GoReleaser](https://goreleaser.com)
   (`.goreleaser.yaml`) builds darwin/arm64 and darwin/amd64 with cgo and merges them into
   one universal binary. It publishes a GitHub release with:
   - `crate-labels_<version>_macos_universal.tar.gz`: the binary, README, LICENSE, NOTICE
     and the font licences;
   - `checksums.txt` (SHA-256);
   - a changelog grouped by Conventional Commit type.

   A tag with a suffix, such as `v0.2.0-rc.1`, becomes a pre-release.

3. To try the release build locally, run `goreleaser release --snapshot --clean`. The
   output goes to `dist/`, and the universal binary is
   `dist/crate-labels_darwin_all/crate-labels`.

The version comes from the tag. GoReleaser sets `internal/service.Version` and the commit
with `-ldflags -X`, and both `crate-labels version` and `/api/health` report them. A plain
`go build` reports the default version and the commit Go stamps into the binary.

Release binaries are not signed or notarized. That needs a Developer ID Application
certificate, an App Store Connect API key for notarization, both stored as repository
secrets, and a `notarize` section in `.goreleaser.yaml`. None of it is set up yet.
