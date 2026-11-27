package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// The converters. Tests replace them to exercise the fallbacks.
var (
	afconvertPath = "/usr/bin/afconvert"
	findFFmpeg    = func() (string, error) { return exec.LookPath("ffmpeg") }
)

// convert decodes in (an absolute path) with afconvert, else ffmpeg, writing any
// intermediate file into dir.
func convert(ctx context.Context, in, dir string, rate int, maxSeconds float64) ([]float32, error) {
	// Absolute paths start with "/", so a converter can never read one as an option.
	var err error
	if in, err = filepath.Abs(in); err != nil {
		return nil, err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, err
	}
	var firstErr error
	if info, err := os.Stat(afconvertPath); err == nil && !info.IsDir() {
		samples, err := afconvert(ctx, in, dir, rate, maxSeconds)
		if err == nil || ctx.Err() != nil {
			return samples, ctxErr(ctx, err)
		}
		firstErr = err
	}
	if ff, err := findFFmpeg(); err == nil {
		samples, err := ffmpeg(ctx, ff, in, rate, maxSeconds)
		if err == nil || ctx.Err() != nil {
			return samples, ctxErr(ctx, err)
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, formatErr("only WAV and AIFF can be read without macOS's afconvert; install ffmpeg for other formats")
}

func ctxErr(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// afconvert runs `afconvert -f WAVE -d LEI16@<rate> -c 1 --mix in out.wav` (--mix
// averages the channels; without it -c 1 keeps only the first) and reads the result.
// afconvert has no duration limit, so with maxSeconds it is stopped once the output
// holds enough; the WAV it leaves then has placeholder sizes, which parseWAV reads to
// the end of the file.
func afconvert(ctx context.Context, in, dir string, rate int, maxSeconds float64) ([]float32, error) {
	out := filepath.Join(dir, "decoded.wav")
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cctx, afconvertPath,
		"-f", "WAVE", "-d", "LEI16@"+strconv.Itoa(rate), "-c", "1", "--mix", in, out)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var capped atomic.Bool
	done := make(chan struct{})
	if maxSeconds > 0 {
		enough := 64<<10 + 2*int64(math.Ceil(maxSeconds*float64(rate)))
		go func() {
			tick := time.NewTicker(40 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					if info, err := os.Stat(out); err == nil && info.Size() >= enough {
						capped.Store(true)
						cancel()
						return
					}
				}
			}
		}()
	}
	err := cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !capped.Load() {
		return nil, formatErr("macOS can't decode it")
	}
	f, err := os.Open(out)
	if err != nil {
		return nil, formatErr("macOS can't decode it")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	samples, err := decodeNative(ctx, kindWAV, f, info.Size(), rate, maxSeconds)
	if err != nil && ctx.Err() == nil {
		return nil, formatErr("macOS can't decode it")
	}
	return samples, err
}

// ffmpeg decodes in to raw float32 on stdout:
// `ffmpeg -nostdin -hide_banner -loglevel error -i file:in -vn [-t max] -ac 1 -ar rate -f f32le -`.
func ffmpeg(ctx context.Context, ff, in string, rate int, maxSeconds float64) ([]float32, error) {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-i", "file:" + in, "-vn"}
	if maxSeconds > 0 {
		args = append(args, "-t", strconv.FormatFloat(maxSeconds, 'f', -1, 64))
	}
	args = append(args, "-ac", "1", "-ar", strconv.Itoa(rate), "-f", "f32le", "-")
	cmd := exec.CommandContext(ctx, ff, args...)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var out []float32
	if maxSeconds > 0 {
		out = make([]float32, 0, int(maxSeconds*float64(rate))+1)
	}
	buf := make([]byte, 1<<16)
	carry := 0
	for {
		n, rerr := stdout.Read(buf[carry:])
		n += carry
		whole := n / 4 * 4
		for i := 0; i < whole; i += 4 {
			out = append(out, finite(math.Float32frombits(binary.LittleEndian.Uint32(buf[i:]))))
		}
		carry = copy(buf, buf[whole:n])
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) && ctx.Err() == nil {
				_ = cmd.Wait()
				return nil, rerr
			}
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, formatErr("ffmpeg can't decode it")
	}
	return out, nil
}
