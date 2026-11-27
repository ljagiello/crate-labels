// Package audio decodes audio files to mono float32 samples at a chosen rate, for tempo
// analysis.
//
// WAV (PCM 8/16/24/32-bit, 32/64-bit float, WAVE_FORMAT_EXTENSIBLE, RF64) and AIFF/AIFC
// (big-endian PCM, sowt, fl32, fl64) are read in pure Go and resampled with a windowed-sinc
// low-pass. Everything else (MP3, AAC/M4A, ALAC, FLAC, Opus…) goes through macOS's
// /usr/bin/afconvert, or ffmpeg when afconvert is missing or cannot read the file. The
// format is told by the file's first bytes, never by its name.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxBytes is the largest file DecodeBytes accepts (about 40 minutes of CD-quality WAV).
const MaxBytes = 400 << 20

// ErrTooLarge is returned by DecodeBytes for data over MaxBytes.
var ErrTooLarge = fmt.Errorf("audio: the file is over %d MB", MaxBytes>>20)

// FormatError means the data is not audio this package can read. Reason is a short
// phrase fit for the user, e.g. "macOS can't decode it"; it never contains a path.
type FormatError struct{ Reason string }

func (e *FormatError) Error() string { return "audio: " + e.Reason }

func formatErr(format string, args ...any) error {
	return &FormatError{Reason: fmt.Sprintf(format, args...)}
}

// Decode reads the audio file at path as mono samples at rate Hz, at most maxSeconds
// long (0 = the whole file). It honours ctx, killing a running converter.
func Decode(ctx context.Context, path string, rate int, maxSeconds float64) ([]float32, error) {
	if err := checkRate(rate); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, formatErr("it is a folder")
	}
	if info.Size() == 0 {
		return nil, formatErr("it is empty")
	}
	head := make([]byte, 64)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	kind, ext := sniff(head[:n])
	var nativeErr error
	if kind != kindOther {
		samples, err := decodeNative(ctx, kind, f, info.Size(), rate, maxSeconds)
		if err == nil || ctx.Err() != nil {
			return samples, err
		}
		nativeErr = err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "crate-labels-audio-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := abs
	// afconvert trusts the extension: a FLAC file named .mp3 fails. Point it at a link
	// with the right one.
	if ext != "" && !strings.EqualFold(filepath.Ext(abs), ext) {
		link := filepath.Join(dir, "input"+ext)
		if os.Symlink(abs, link) == nil {
			in = link
		}
	}
	samples, err := convert(ctx, in, dir, rate, maxSeconds)
	if err != nil && nativeErr != nil && ctx.Err() == nil {
		return nil, nativeErr
	}
	return samples, err
}

// DecodeBytes is Decode for a file held in memory, such as an upload. nameHint is the
// file's name; only its extension is used, and only when the bytes do not say what the
// format is.
func DecodeBytes(ctx context.Context, data []byte, nameHint string, rate int, maxSeconds float64) ([]float32, error) {
	if err := checkRate(rate); err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, ErrTooLarge
	}
	if len(data) == 0 {
		return nil, formatErr("it is empty")
	}
	kind, ext := sniff(data)
	var nativeErr error
	if kind != kindOther {
		samples, err := decodeNative(ctx, kind, bytes.NewReader(data), int64(len(data)), rate, maxSeconds)
		if err == nil || ctx.Err() != nil {
			return samples, err
		}
		nativeErr = err
	}
	if ext == "" {
		ext = hintExt(nameHint)
	}
	dir, err := os.MkdirTemp("", "crate-labels-audio-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "input"+ext)
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return nil, err
	}
	samples, err := convert(ctx, in, dir, rate, maxSeconds)
	if err != nil && nativeErr != nil && ctx.Err() == nil {
		return nil, nativeErr
	}
	return samples, err
}

func checkRate(rate int) error {
	if rate < 1000 || rate > 384000 {
		return fmt.Errorf("audio: sample rate %d is out of range", rate)
	}
	return nil
}

type kind int

const (
	kindOther kind = iota
	kindWAV
	kindAIFF
)

// sniff tells the format from a file's first bytes. ext is the extension the converters
// should see ("" when unknown).
func sniff(b []byte) (kind, string) {
	at := func(off int, s string) bool { return len(b) >= off+len(s) && string(b[off:off+len(s)]) == s }
	switch {
	case (at(0, "RIFF") || at(0, "RF64") || at(0, "BW64")) && at(8, "WAVE"):
		return kindWAV, ".wav"
	case at(0, "FORM") && (at(8, "AIFF") || at(8, "AIFC")):
		return kindAIFF, ".aiff"
	case at(0, "fLaC"):
		return kindOther, ".flac"
	case at(4, "ftyp"):
		return kindOther, ".m4a"
	case at(0, "caff"):
		return kindOther, ".caf"
	case at(0, "OggS"):
		if bytes.Contains(b[:min(len(b), 64)], []byte("OpusHead")) {
			return kindOther, ".opus"
		}
		return kindOther, ".ogg"
	case at(0, "#!AMR"):
		return kindOther, ".amr"
	case at(0, "ID3"):
		return kindOther, ".mp3"
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xF6 == 0xF0:
		return kindOther, ".aac" // ADTS: sync word, layer 0
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0 && (b[1]>>1)&3 != 0:
		return kindOther, ".mp3" // an MPEG audio frame header
	}
	return kindOther, ""
}

// hintExt is the extension of an uploaded file's name, if it is one the converters know.
func hintExt(name string) string {
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(name, `\`, "/")))
	switch ext {
	case ".mp3", ".mp2", ".mp1", ".m4a", ".m4b", ".mp4", ".aac", ".adts", ".flac", ".ogg", ".oga", ".opus",
		".caf", ".aif", ".aiff", ".aifc", ".wav", ".w64", ".amr", ".ac3", ".ec3", ".3gp", ".3g2":
		return ext
	}
	return ""
}
