package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/ljagiello/crate-labels/internal/service"
)

// Release builds set commit and date with -ldflags -X (see .goreleaser.yaml). A plain
// `go build` leaves them empty, and versionLine falls back to the VCS stamp Go embeds.
var (
	commit string
	date   string
)

func cmdVersion(stdout io.Writer) error {
	_, err := fmt.Fprintln(stdout, versionLine())
	return err
}

// versionLine is what `crate-labels version` prints, for example
// "crate-labels 0.2.0 (commit 1a2b3c4d5e6f, 2026-11-26T10:00:00Z, go1.27.4 darwin/arm64)".
func versionLine() string {
	rev, when, modified := commit, date, false
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if rev == "" {
					rev = s.Value
				}
			case "vcs.time":
				if when == "" {
					when = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	var parts []string
	if rev != "" {
		rev = rev[:min(len(rev), 12)]
		if modified {
			rev += "-dirty"
		}
		parts = append(parts, "commit "+rev)
	}
	if when != "" {
		parts = append(parts, when)
	}
	parts = append(parts, runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH)
	return fmt.Sprintf("crate-labels %s (%s)", service.Version, strings.Join(parts, ", "))
}
