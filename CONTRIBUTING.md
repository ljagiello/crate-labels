# Contributing to crate-labels

Thanks for helping. Bug reports, printer reports ("my M220 prints this label sideways")
and pull requests are all welcome. By taking part you agree to the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Set up

crate-labels is macOS-only. You need:

- macOS 13 or later, Apple silicon or Intel;
- Go, at the version in `go.mod`. Any Go 1.21+ downloads the right toolchain itself;
- the Xcode Command Line Tools (`xcode-select --install`). The Bluetooth backend is cgo
  over CoreBluetooth, so builds need `CGO_ENABLED=1`, which is the default on a Mac.

```sh
git clone https://github.com/ljagiello/crate-labels.git
cd crate-labels
go build -o crate-labels ./cmd/crate-labels
./crate-labels serve --open
```

Optional: [yt-dlp](https://github.com/yt-dlp/yt-dlp) for BPM detection from Discogs videos,
[GoReleaser](https://goreleaser.com) to try the release build, and
[govulncheck](https://go.dev/doc/security/vuln/) (`go install golang.org/x/vuln/cmd/govulncheck@latest`).

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) before a larger change. It explains the
pipeline, the packages and the interfaces a driver implements.

## Checks

CI runs these on every pull request, so run them first:

```sh
gofmt -l .                  # must print nothing
go vet ./...
go test -race -count=1 ./...
go mod tidy -diff           # must print nothing
govulncheck ./...
goreleaser release --snapshot --clean   # optional: the release build, into dist/
```

If you change a template, refresh the layout references and look at the comparisons
before you commit them:

```sh
go test ./internal/layout/ -run TestRenderSamples -update
```

## Tests never touch hardware or the network

The whole suite must pass on a Mac with Bluetooth off, no printer and no internet.
Contributors and CI have none of those.

- **Bluetooth:** only `cmd/crate-labels` imports `internal/transport/ble/corebluetooth`.
  Linking it creates a CoreBluetooth manager at startup, so test binaries must never
  import it. Drive drivers through `internal/transport/fake` instead.
- **Wire formats:** expose a pure encoder and compare its output byte for byte against
  a fixture in `testdata/golden/`. Fixtures are snapshots: regenerate them with
  `go test <pkg> -update` only for an intended change, review the diff, and say why in the
  pull request. Never regenerate one just to make a failing test pass.
- **Network:** use `net/http/httptest` and the recorded responses in `testdata/discogs/` and
  `testdata/ipp/`. Never call Discogs, YouTube or a real printer from a test.
- **Audio:** synthesise input with `internal/audio/audiotest`. Tests may run
  `/usr/bin/afconvert`, which every Mac has.
- **Floats:** the raster code is bit-exact against its fixtures. Keep the explicit
  `float32(...)` conversions and `math.Floor(x + 0.5)` rounding (see ARCHITECTURE).

## Adding a printer

Most printers need no Go code. See [Adding printers](README.md#adding-printers) in the
README: a new model in a supported family is a TOML file, a new label size is a `[[stock]]`,
and anything macOS can already print to works through CUPS. A new protocol family is a
package under `internal/driver/`; ARCHITECTURE's "Adding printer support" lists the steps.

When you add a model, say in the pull request which unit you tested and attach a photo of
the `test-pattern` label it printed. Models nobody has printed on are marked as untested in
the README's printer table.

## Commits and pull requests

Write commit messages and pull request titles as
[Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/). The release
changelog is grouped by type.

```
feat(phomemo): support the M120
fix(web): keep the preview when the token expires
docs: explain the darkness setting
```

- **Types:** `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`.
- **Scope:** the package or driver you touched, e.g. `ble`, `layout`, `clabel`.
- **Breaking changes:** add `!`, as in `feat!: …`, and explain the change in the body.

Keep a pull request to one change, and add or update tests with it. Pull requests are
squash-merged, so the title becomes the commit message.

## Releases

Maintainers cut releases by pushing a `v*` tag; see "Development / Releasing" in the README.
