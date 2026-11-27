# Golden files: IPP, URF and sheet imposition

Covers `ipp/` and `sheets/` here. Paths below are relative to `testdata/golden/`.

The tests write these files from the current code. Check a change, then regenerate:

```sh
go test ./internal/ipp ./internal/driver/ipp ./internal/sheets -update
```

The exception is `ipp/brother-gpa-decoded.json`. It is the recorded Get-Printer-Attributes
reply in `testdata/ipp/brother-hl-l3290cdw-get-printer-attributes.bin`, decoded value by
value, and it changes only if that capture does. In requests, the printer is at the
documentation address `198.51.100.9` and the user name is `crate-labels`.

## What the files pin down

- **IPP wire bytes** (RFC 8010): every request the client sends and the operation-group
  order of RFC 8011 section 4.1.4.
- **Job attributes for the Brother**: `media-col`, `print-color-mode` and
  `print-scaling`.
  - `media-col` repeats the printer's loaded `media-col-ready` entry, in the printer's
    order. It keeps only the members the IANA registry defines for the media-col Job
    Template attribute, so `media-auto-dimension` and `media-source-properties` are
    left out.
  - `print-color-mode` is `monochrome` and `print-scaling` is `none`.
  - `print-quality` is left to the printer's default.
  - `TestLiveValidate` in `internal/driver/ipp` sent these attributes to the real printer
    with Validate-Job, and it accepted them.
- **URF**: the page header and the line coding of PWG 5102.4 section 4.4. A run of
  three or more equal pixels becomes a repeat code. Shorter runs are gathered into
  literals. A stretch of one or two equal pixels on its own is written as a repeat. Code
  128 is never written.
- **Placement on the Brother**: every page is placed at actual size on the media chosen
  for it.
  - The medium is the loaded Letter sheet.
  - A label smaller than the sheet sits in its middle, unturned.
  - Pages at 300 dpi are doubled pixel for pixel to the printer's 600 dpi.
- **Sheet imposition**: cells, centring, shrink-only fitting and trim marks.
  - Each mark is 0.25 mm wide, on the seam, and starts 1 mm outside the grid.
  - It runs up to 3 mm outward and stops 4.5 mm from the paper edge.
  - A mark with less than 0.5 mm of room is not drawn.

Pixel boxes in JSON are `[x0, y0, x1, y1)`, end-exclusive, found on the black test
labels. PNG goldens are compared by pixels, not bytes.

## Files

| File | What |
|---|---|
| `ipp/brother-gpa-decoded.json` | the recorded Brother reply, decoded attribute by attribute |
| `ipp/brother-summary.json` | what `internal/ipp` reads from that reply, and the media it would choose for some page sizes |
| `ipp/driver-plans.json` | the driver's plan for one black label of each stock on the Brother: format, dpi, media, page size and where the ink lands |
| `ipp/requests/gpa.bin`, `.json` | Get-Printer-Attributes for `all` and `media-col-database` |
| `ipp/requests/gpa-printer-status.bin`, `.json` | Get-Printer-Attributes for printer-state, printer-state-reasons and printer-state-message |
| `ipp/requests/get-job-attributes-42.bin`, `.json` | Get-Job-Attributes for job 42 |
| `ipp/requests/cancel-job-42.bin`, `.json` | Cancel-Job for job 42 |
| `ipp/requests/print-job-header-50x80.bin`, `.json` | Print-Job for a 50×80 mm label without its document (append the URF bytes) |
| `ipp/requests/validate-job-50x80.bin`, `.json` | the same job as Validate-Job: operation 0x0004 and no document |
| `ipp/requests/validate-job-<stock>.bin` | the driver's Validate-Job for one label of each stock in `driver-plans.json` |
| `ipp/urf/cases.json` | URF cases: inputs, expected file and length, and what each one exercises |
| `ipp/urf/<case>.p1.png`, `.p2.png` | input pages (8-bit grey) |
| `ipp/urf/<case>.urf` | the expected URF document |
| `sheets/<stock>.json` | imposition at 300 dpi: canvas, page size, ink boxes; for sheets also the grid, pages per label count and trim-mark boxes |
| `sheets/<stock>.page1.png` | one label stock's page |
| `sheets/<sheet>.full-page.png` | a full sheet of black labels, trim marks as the stock defaults them |
| `sheets/<sheet>.marks-only.png` | trim marks alone, forced on |

The `sheets/test-uncut-*` stocks are defined in `internal/sheets/sheets_test.go`, not in
the catalog. Their margins make the trim marks run full length (`roomy`), get shortened
(`tight`) and be left out (`edge`).
