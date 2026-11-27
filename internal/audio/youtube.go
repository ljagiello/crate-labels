package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Fetching a YouTube video's audio track, for releases whose Discogs page links one per
// track. yt-dlp does the fetching; it is not bundled (brew install yt-dlp).

// ErrNoYtDlp means yt-dlp is not installed.
var ErrNoYtDlp = errors.New("audio: yt-dlp is not installed")

// YouTubeError is a failure yt-dlp reported, in its own words.
type YouTubeError struct{ Reason string }

func (e *YouTubeError) Error() string { return "audio: YouTube: " + e.Reason }

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ytDlpPaths are where yt-dlp usually lives when the server was not started from a shell
// with Homebrew on its PATH.
var ytDlpPaths = []string{"/opt/homebrew/bin/yt-dlp", "/usr/local/bin/yt-dlp"}

func ytDlp() string {
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return p
	}
	for _, p := range ytDlpPaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// HaveYtDlp reports whether YouTube audio can be fetched.
func HaveYtDlp() bool { return ytDlp() != "" }

// DecodeYouTube fetches the audio of YouTube video id into a temporary folder, decodes it
// like Decode and removes the download. Videos longer than maxSeconds are refused before
// downloading.
func DecodeYouTube(ctx context.Context, id string, rate int, maxSeconds float64) ([]float32, error) {
	if !videoID.MatchString(id) {
		return nil, fmt.Errorf("audio: %q is not a YouTube video id", id)
	}
	bin := ytDlp()
	if bin == "" {
		return nil, ErrNoYtDlp
	}
	dir, err := os.MkdirTemp("", "crate-labels-yt-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	args := []string{
		"--no-playlist", "--no-progress", "--no-warnings", "--quiet", "--no-cache-dir",
		"--no-config", "--no-mtime", "--no-part",
		// AAC in MP4 decodes with afconvert; anything else falls back to ffmpeg.
		"--format", "bestaudio[ext=m4a]/bestaudio",
		"--max-filesize", "200M",
		"--output", filepath.Join(dir, "audio.%(ext)s"),
	}
	if maxSeconds > 0 {
		args = append(args, "--match-filter", fmt.Sprintf("duration < %d", int(maxSeconds)+60))
	}
	args = append(args, "--", "https://www.youtube.com/watch?v="+id)
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctxErr(ctx, ctx.Err())
		}
		return nil, &YouTubeError{Reason: ytReason(stderr.String())}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "audio.*"))
	if len(files) == 0 {
		// --match-filter skips silently
		return nil, &YouTubeError{Reason: "the video is too long to be one track"}
	}
	return Decode(ctx, files[0], rate, maxSeconds)
}

// ytReason is yt-dlp's last error line without its prefix.
func ytReason(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		l = strings.TrimPrefix(l, "ERROR: ")
		if _, rest, ok := strings.Cut(l, "] "); ok && strings.HasPrefix(l, "[") {
			l = rest
		}
		if _, rest, ok := strings.Cut(l, ": "); ok && videoID.MatchString(strings.TrimSpace(l[:len(l)-len(rest)-2])) {
			l = rest
		}
		if len(l) > 200 {
			l = l[:200] + "…"
		}
		return l
	}
	return "yt-dlp failed"
}
