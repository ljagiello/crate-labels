package audio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ljagiello/crate-labels/internal/audio/audiotest"
)

var ctx = context.Background()

// noConverters makes the converters unavailable for one test.
func noConverters(t *testing.T) {
	t.Helper()
	af, ff := afconvertPath, findFFmpeg
	afconvertPath = filepath.Join(t.TempDir(), "no-afconvert")
	findFFmpeg = func() (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { afconvertPath, findFFmpeg = af, ff })
}

// privateTemp points os.TempDir at an empty folder and checks it is empty at the end:
// every temporary file was removed.
func privateTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Cleanup(func() {
		if left, _ := os.ReadDir(dir); len(left) > 0 {
			t.Errorf("temporary files left behind: %v", left)
		}
	})
}

// testFrames is 0.25 s of stereo with different channels, so the downmix shows.
func testFrames(rate int) ([][]float64, []float64) {
	l := audiotest.Tone(rate, 0.25, 440, 0.6)
	r := audiotest.Tone(rate, 0.25, 660, -0.3)
	mono := make([]float64, len(l))
	for i := range l {
		mono[i] = (l[i] + r[i]) / 2
	}
	return audiotest.Stereo(l, r), mono
}

func checkSamples(t *testing.T, name string, got []float32, want []float64, tol float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d samples, want %d", name, len(got), len(want))
	}
	worst := 0.0
	for i := range want {
		worst = max(worst, math.Abs(float64(got[i])-want[i]))
	}
	if worst > tol {
		t.Errorf("%s: off by up to %g (tolerance %g)", name, worst, tol)
	}
}

func intTol(bits int) float64 { return 2 / math.Pow(2, float64(bits-1)) }

func TestWAVSampleFormats(t *testing.T) {
	noConverters(t) // the pure-Go reader must handle every one of these
	const rate = 8000
	frames, mono := testFrames(rate)
	for _, c := range []struct {
		name string
		f    audiotest.Format
		tol  float64
	}{
		{"pcm8", audiotest.Format{Bits: 8}, intTol(8)},
		{"pcm16", audiotest.Format{Bits: 16}, intTol(16)},
		{"pcm24", audiotest.Format{Bits: 24}, intTol(24)},
		{"pcm32", audiotest.Format{Bits: 32}, intTol(24)},
		{"float32", audiotest.Format{Bits: 32, Float: true}, 1e-6},
		{"float64", audiotest.Format{Bits: 64, Float: true}, 1e-6},
		{"extensible16", audiotest.Format{Bits: 16, Extensible: true}, intTol(16)},
		{"extensible24in32", audiotest.Format{Bits: 32, Extensible: true, ValidBits: 24}, intTol(24)},
		{"extensible-float", audiotest.Format{Bits: 32, Float: true, Extensible: true}, 1e-6},
	} {
		got, err := DecodeBytes(ctx, audiotest.WAV(c.f, rate, frames), "", rate, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		checkSamples(t, c.name, got, mono, c.tol)
	}
}

func TestAIFFSampleFormats(t *testing.T) {
	noConverters(t)
	const rate = 11025
	frames, mono := testFrames(rate)
	for _, c := range []struct {
		name string
		f    audiotest.Format
		tol  float64
	}{
		{"aiff8", audiotest.Format{Bits: 8}, intTol(8)},
		{"aiff16", audiotest.Format{Bits: 16}, intTol(16)},
		{"aiff24", audiotest.Format{Bits: 24}, intTol(24)},
		{"aiff32", audiotest.Format{Bits: 32}, intTol(24)},
		{"aifc-none", audiotest.Format{Bits: 16, Compression: "NONE"}, intTol(16)},
		{"aifc-sowt", audiotest.Format{Bits: 16, Compression: "sowt"}, intTol(16)},
		{"aifc-in24", audiotest.Format{Bits: 24, Compression: "in24"}, intTol(24)},
		{"aifc-fl32", audiotest.Format{Bits: 32, Float: true, Compression: "fl32"}, 1e-6},
		{"aifc-fl64", audiotest.Format{Bits: 64, Float: true, Compression: "fl64"}, 1e-6},
	} {
		got, err := DecodeBytes(ctx, audiotest.AIFF(c.f, rate, frames), "x.aif", rate, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		checkSamples(t, c.name, got, mono, c.tol)
	}
}

func TestDecodeFileCapAndResample(t *testing.T) {
	noConverters(t)
	privateTemp(t)
	wav := audiotest.WAV(audiotest.Format{Bits: 16}, 44100, audiotest.Mono(audiotest.Tone(44100, 3, 440, 0.5)))
	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := os.WriteFile(path, wav, 0o644); err != nil {
		t.Fatal(err)
	}
	fromFile, err := Decode(ctx, path, 22050, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	fromBytes, err := DecodeBytes(ctx, wav, "tone.wav", 22050, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromFile) != 33075 || len(fromBytes) != len(fromFile) {
		t.Fatalf("1.5 s at 22050 Hz = %d and %d samples, want 33075", len(fromFile), len(fromBytes))
	}
	for i := range fromFile {
		if fromFile[i] != fromBytes[i] {
			t.Fatalf("file and bytes differ at %d", i)
		}
	}
	whole, err := Decode(ctx, path, 22050, 0)
	if err != nil || len(whole) != 3*22050 {
		t.Fatalf("whole file: %d samples, %v", len(whole), err)
	}
	if f := toneFreq(whole, 22050); math.Abs(f-440) > 2 {
		t.Errorf("tone came out at %.1f Hz", f)
	}
}

func TestLenientDataSizes(t *testing.T) {
	noConverters(t)
	const rate = 8000
	x := audiotest.Tone(rate, 0.5, 300, 0.5)
	wav := audiotest.WAV(audiotest.Format{Bits: 16}, rate, audiotest.Mono(x))
	i := strings.Index(string(wav), "data")
	// A streamed WAV (size 0) and one cut off mid-sample both read to the end.
	streamed := append([]byte{}, wav...)
	copy(streamed[i+4:], []byte{0, 0, 0, 0})
	got, err := DecodeBytes(ctx, streamed, "", rate, 0)
	if err != nil || len(got) != len(x) {
		t.Fatalf("streamed: %d samples, %v", len(got), err)
	}
	cut := wav[:len(wav)-101]
	got, err = DecodeBytes(ctx, cut, "", rate, 0)
	if err != nil || len(got) != len(x)-51 {
		t.Fatalf("cut: %d samples, want %d, %v", len(got), len(x)-51, err)
	}
}

func TestUnreadableAudio(t *testing.T) {
	noConverters(t)
	privateTemp(t)
	adpcm := audiotest.WAV(audiotest.Format{Bits: 16}, 8000, audiotest.Mono(make([]float64, 100)))
	adpcm[20] = 2 // WAVE_FORMAT_ADPCM
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"empty":   {nil, "empty"},
		"garbage": {[]byte("this is not audio at all, just some text"), "install ffmpeg"},
		"adpcm":   {adpcm, "encoding it can't read (format 0x0002)"},
		"no-data": {[]byte("RIFF\x04\x00\x00\x00WAVE"), "broken WAV"},
	} {
		_, err := DecodeBytes(ctx, c.data, "x.mp3", 22050, 0)
		var fe *FormatError
		if !errors.As(err, &fe) || !strings.Contains(fe.Reason, c.want) {
			t.Errorf("%s: %v, want a FormatError about %q", name, err, c.want)
		}
	}
	if _, err := DecodeBytes(ctx, make([]byte, MaxBytes+1), "", 22050, 0); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized: %v", err)
	}
}

func TestCancelled(t *testing.T) {
	privateTemp(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	wav := audiotest.WAV16(44100, audiotest.Tone(44100, 1, 440, 0.5))
	if _, err := DecodeBytes(c, wav, "", 22050, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("native: %v", err)
	}
	if _, err := os.Stat(afconvertPath); err != nil {
		t.Skip("no afconvert")
	}
	if _, err := DecodeBytes(c, []byte("fLaC not really"), "", 22050, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("converter: %v", err)
	}
}

func TestSniff(t *testing.T) {
	for in, want := range map[string]string{
		"RIFF\x00\x00\x00\x00WAVEfmt ": ".wav", "FORM\x00\x00\x00\x00AIFC": ".aiff", "fLaC\x00": ".flac",
		"\x00\x00\x00\x20ftypM4A ": ".m4a", "ID3\x04": ".mp3", "\xff\xfb\x90\x00": ".mp3", "\xff\xf1\x50\x80": ".aac",
		"OggS\x00\x02" + strings.Repeat("\x00", 22) + "OpusHead": ".opus", "OggS\x00\x02": ".ogg", "caff": ".caf",
		"hello": "",
	} {
		if _, got := sniff([]byte(in)); got != want {
			t.Errorf("sniff(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Track.MP3": ".mp3", "a/b.flac": ".flac", `c:\x\y.m4a`: ".m4a", "x.exe": "", "": ""} {
		if got := hintExt(in); got != want {
			t.Errorf("hintExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- resampler ---------------------------------------------------------------------

func rms(x []float32, skip int) float64 {
	var s float64
	x = x[skip : len(x)-skip]
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)))
}

func resample(in, out int, x []float64) []float32 {
	r := newResampler(in, out)
	f := make([]float32, len(x))
	for i, v := range x {
		f[i] = float32(v)
	}
	for len(f) > 0 { // in uneven pieces, as the reader feeds it
		n := min(len(f), 7919)
		r.push(f[:n])
		f = f[n:]
	}
	return r.finish()
}

func TestResamplerRejectsAliases(t *testing.T) {
	for _, c := range []struct {
		in, out int
		hz      float64
	}{
		{44100, 22050, 11500}, {44100, 22050, 15000}, {44100, 22050, 20000},
		{48000, 22050, 11200}, {48000, 22050, 16000}, {96000, 22050, 30000}, {32000, 22050, 12000},
	} {
		y := resample(c.in, c.out, audiotest.Tone(c.in, 2, c.hz, 0.7))
		if db := 20 * math.Log10(rms(y, 500)/(0.7/math.Sqrt2)); db > -40 {
			t.Errorf("%d→%d Hz: a %g Hz tone came through at %.1f dB", c.in, c.out, c.hz, db)
		}
	}
}

func TestResamplerPassBand(t *testing.T) {
	for _, c := range []struct {
		in, out int
		hz      float64
	}{
		{44100, 22050, 1000}, {44100, 22050, 8000}, {48000, 22050, 5000}, {8000, 22050, 1000},
		{11025, 22050, 3000}, {44056, 22050, 1000}, // an odd rate: phases are rounded
	} {
		n := 2 * c.in
		y := resample(c.in, c.out, audiotest.Tone(c.in, 2, c.hz, 0.5))
		if want := (int64(n)*int64(c.out) + int64(c.in) - 1) / int64(c.in); int64(len(y)) != want {
			t.Errorf("%d→%d: %d outputs, want %d", c.in, c.out, len(y), want)
		}
		// Same amplitude and no delay: compare with the tone sampled at the new rate.
		ideal := audiotest.Tone(c.out, 2, c.hz, 0.5)
		worst := 0.0
		for i := 500; i < len(ideal)-500; i++ {
			worst = max(worst, math.Abs(float64(y[i])-ideal[i]))
		}
		if worst > 0.01 {
			t.Errorf("%d→%d Hz, %g Hz tone: off by up to %.4f", c.in, c.out, c.hz, worst)
		}
	}
}

// toneFreq estimates a pure tone's frequency from its zero crossings.
func toneFreq(x []float32, rate int) float64 {
	n := 0
	for i := 1; i < len(x); i++ {
		if (x[i-1] < 0) != (x[i] < 0) {
			n++
		}
	}
	return float64(n) / 2 / (float64(len(x)) / float64(rate))
}

// --- converters ---------------------------------------------------------------------

// encodeWith writes a 4 s stereo 440 Hz WAV and converts it with the given command
// (whose last two arguments are the input and output files).
func encodeWith(t *testing.T, out string, cmd ...string) []byte {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.wav")
	tone := audiotest.Tone(44100, 4, 440, 0.5)
	if err := os.WriteFile(in, audiotest.WAV(audiotest.Format{Bits: 16}, 44100, audiotest.Stereo(tone, tone)), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, out)
	args := append(append([]string{}, cmd[1:]...), in, dst)
	if b, err := exec.Command(cmd[0], args...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %v %s", cmd, err, b)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func checkTone(t *testing.T, name string, got []float32, seconds float64) {
	t.Helper()
	if want := seconds * 22050; math.Abs(float64(len(got))-want) > 0.05*want {
		t.Errorf("%s: %d samples, want about %.0f", name, len(got), want)
	}
	if f := toneFreq(got, 22050); math.Abs(f-440) > 5 {
		t.Errorf("%s: tone at %.1f Hz, want 440", name, f)
	}
}

func TestAfconvert(t *testing.T) {
	if _, err := os.Stat(afconvertPath); err != nil {
		t.Skip("no /usr/bin/afconvert")
	}
	findFFmpeg = func() (string, error) { return "", exec.ErrNotFound } // afconvert alone
	t.Cleanup(func() { findFFmpeg = func() (string, error) { return exec.LookPath("ffmpeg") } })
	files := map[string][]byte{
		"aac.m4a":   encodeWith(t, "x.m4a", afconvertPath, "-f", "m4af", "-d", "aac"),
		"alac.m4a":  encodeWith(t, "x.m4a", afconvertPath, "-f", "m4af", "-d", "alac"),
		"flac.flac": encodeWith(t, "x.flac", afconvertPath, "-f", "flac", "-d", "flac"),
		"caf.caf":   encodeWith(t, "x.caf", afconvertPath, "-f", "caff", "-d", "LEI16"),
	}
	privateTemp(t)
	for name, b := range files {
		got, err := DecodeBytes(ctx, b, "", 22050, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checkTone(t, name, got, 4)
		// A misleading name does not matter: the bytes say what it is.
		got, err = DecodeBytes(ctx, b, "song.mp3", 22050, 1.5)
		if err != nil || len(got) != 33075 {
			t.Fatalf("%s named .mp3, capped at 1.5 s: %d samples, %v", name, len(got), err)
		}
	}
	// From a file on disk, with the wrong extension.
	path := filepath.Join(t.TempDir(), "really-flac.mp3")
	if err := os.WriteFile(path, files["flac.flac"], 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(ctx, path, 22050, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkTone(t, "flac file named .mp3", got, 4)
	// afconvert's error is a FormatError.
	var fe *FormatError
	if _, err := DecodeBytes(ctx, []byte("fLaC but not really a flac file"), "", 22050, 0); !errors.As(err, &fe) || fe.Reason != "macOS can't decode it" {
		t.Errorf("broken FLAC: %v", err)
	}
}

func TestAfconvertStopsAtTheCap(t *testing.T) {
	if _, err := os.Stat(afconvertPath); err != nil {
		t.Skip("no /usr/bin/afconvert")
	}
	// 10 minutes of mono 8 kHz upsampled to 22050 Hz: afconvert is stopped early and
	// the half-written WAV is read.
	dir := t.TempDir()
	in := filepath.Join(dir, "long.wav")
	if err := os.WriteFile(in, audiotest.WAV16(8000, audiotest.Tone(8000, 600, 440, 0.5)), 0o644); err != nil {
		t.Fatal(err)
	}
	flac := filepath.Join(dir, "long.flac")
	if b, err := exec.Command(afconvertPath, "-f", "flac", "-d", "flac", in, flac).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	privateTemp(t)
	got, err := Decode(ctx, flac, 22050, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3*22050 {
		t.Fatalf("%d samples, want %d", len(got), 3*22050)
	}
	if f := toneFreq(got, 22050); math.Abs(f-440) > 5 {
		t.Errorf("tone at %.1f Hz", f)
	}
}

func TestFFmpegFallback(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on PATH")
	}
	flac := encodeWith(t, "x.flac", ff, "-nostdin", "-loglevel", "error", "-y", "-i")
	af := afconvertPath
	afconvertPath = filepath.Join(t.TempDir(), "no-afconvert") // as on Linux
	t.Cleanup(func() { afconvertPath = af })
	privateTemp(t)
	got, err := DecodeBytes(ctx, flac, "", 22050, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkTone(t, "ffmpeg flac", got, 4)
	got, err = DecodeBytes(ctx, flac, "", 22050, 2)
	if err != nil || math.Abs(float64(len(got))-2*22050) > 10 {
		t.Fatalf("capped at 2 s: %d samples, %v", len(got), err)
	}
	var fe *FormatError
	if _, err := DecodeBytes(ctx, []byte("fLaC but not really a flac file"), "", 22050, 0); !errors.As(err, &fe) || fe.Reason != "ffmpeg can't decode it" {
		t.Errorf("broken FLAC: %v", err)
	}
}

func ExampleDecodeBytes() {
	wav := audiotest.WAV16(44100, audiotest.Clicks(44100, 2, 120))
	samples, err := DecodeBytes(context.Background(), wav, "clicks.wav", 22050, 0)
	fmt.Println(len(samples), err)
	// Output: 44100 <nil>
}
