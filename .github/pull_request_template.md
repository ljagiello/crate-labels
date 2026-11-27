## What and why

<!-- What does this change, and why? Link the issue it fixes, e.g. "Fixes #12". -->

## How it was tested

<!-- Tests added or changed. If you printed on real hardware, say which printer and stock. -->

## Checklist

- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `go test -race -count=1 ./...` pass
- [ ] Tests need no Bluetooth, network or printer (see CONTRIBUTING.md)
- [ ] Wire-format changes come with updated golden fixtures, and the PR explains why they changed
- [ ] The PR title is a conventional commit, e.g. `feat(phomemo): support the M120`
- [ ] Docs updated (README, `docs/`) if behaviour or the CLI changed
