package phomemo

import (
	"encoding/binary"
	"fmt"
	"time"
)

// Header and footer values the printers accept. Speed and darkness are single header
// bytes; 3 and 8 are the middle of the range these models print well at.
const (
	DefaultSpeed    = 3
	DefaultDarkness = 8

	GapMedia        byte = 0x0a // die-cut labels separated by gaps
	ContinuousMedia byte = 0x0b // tape or receipt
)

// jobRowLimit is the most raster rows the printer accepts in one job. Taller labels
// are sent as several jobs.
const jobRowLimit = 1200

// seamReach is how far, in rows, a job boundary may move to land on a blank row: 8 mm
// either way at 8 dots per mm. A boundary through printed rows can leave a faint line
// where the printer stops and restarts its motor, so a nearby blank row is worth a
// small imbalance between jobs.
const seamReach = 64

var (
	cmdSpeed    = []byte{0x1b, 0x4e, 0x0d} // ESC N 0x0d <speed>
	cmdDarkness = []byte{0x1b, 0x4e, 0x04} // ESC N 0x04 <darkness>
	cmdMedia    = []byte{0x1f, 0x11}       // US 0x11 <media>
	cmdRaster   = []byte{0x1d, 0x76, 0x30, 0x00}
	cmdEnd      = []byte{0x1f, 0xf0, 0x05, 0x00} // ends the job
	cmdFeed     = []byte{0x1f, 0xf0, 0x03, 0x00} // feeds to the next label's start

	// FirmwareQuery asks the printer for its firmware version.
	FirmwareQuery = []byte{0x1f, 0x11, 0x07}
)

// JobSettings are the values every job of one label shares.
type JobSettings struct {
	Speed, Darkness int
	Media           byte
	// SkipFeed leaves out the feed command at the end of each job. Stocks whose
	// printer already stops at the right place set it (70×80 mm).
	SkipFeed bool
	// BlockRows, when positive, sends each job's raster as several GS v 0 blocks of
	// at most this many rows, for firmware that cannot take one tall block.
	BlockRows int
}

// Job is one complete printer job: header, raster blocks and footer.
type Job struct {
	Data []byte
	Rows int // raster rows in the job, which set the pause before the next one
}

// Pause is how long the printer needs after this job before it takes another. It is
// treated as feeding 640 rows (80 mm) a second plus 1.5 s to finish, and is never less
// than 3 s.
func (j Job) Pause() time.Duration {
	const (
		rowsPerSecond = 640
		finish        = 1500 * time.Millisecond
		floor         = 3 * time.Second
	)
	return max(floor, finish+time.Duration(j.Rows)*time.Second/rowsPerSecond)
}

// BuildJobs turns packed raster rows (bytesPerRow bytes each, MSB-first, 1 = burn)
// into the jobs to send in order. Each job holds at most 1200 rows; see [jobCuts]
// for where a tall raster is divided.
func BuildJobs(raster []byte, bytesPerRow int, s JobSettings) ([]Job, error) {
	if bytesPerRow <= 0 || bytesPerRow > 0xffff {
		return nil, fmt.Errorf("phomemo: %d bytes per row is out of range", bytesPerRow)
	}
	if len(raster)%bytesPerRow != 0 {
		return nil, fmt.Errorf("phomemo: %d bytes is not a whole number of %d-byte rows", len(raster), bytesPerRow)
	}
	rowAt := func(r int) []byte { return raster[r*bytesPerRow : (r+1)*bytesPerRow] }
	cuts := jobCuts(len(raster)/bytesPerRow, func(r int) bool { return isBlank(rowAt(r)) })
	jobs := make([]Job, 0, len(cuts))
	for i := 1; i < len(cuts); i++ {
		from, to := cuts[i-1], cuts[i]
		jobs = append(jobs, Job{Data: encodeJob(raster[from*bytesPerRow:to*bytesPerRow], bytesPerRow, s), Rows: to - from})
	}
	return jobs, nil
}

// jobCuts returns the row boundaries of the jobs for a raster of rows rows: 0, then
// each boundary, then rows. It uses as few jobs as the row limit allows. Boundaries
// start evenly spaced and each moves to the nearest blank row within [seamReach],
// as long as no job grows past the limit or becomes empty.
func jobCuts(rows int, blank func(int) bool) []int {
	if rows == 0 {
		return nil
	}
	n := (rows + jobRowLimit - 1) / jobRowLimit
	cuts := []int{0}
	for k := 1; k < n; k++ {
		prev := cuts[len(cuts)-1]
		lo := max(prev+1, rows-(n-k)*jobRowLimit) // the jobs after it can still hold the rest
		hi := min(prev+jobRowLimit, rows-(n-k))   // and none of them is empty
		even := min(max(rows*k/n, lo), hi)
		cuts = append(cuts, nearestBlank(even, lo, hi, blank))
	}
	return append(cuts, rows)
}

// nearestBlank is the blank row closest to want within [lo, hi] and seamReach of
// want, the earlier one on a tie, or want when there is none.
func nearestBlank(want, lo, hi int, blank func(int) bool) int {
	for d := 0; d <= seamReach; d++ {
		if r := want - d; r >= lo && blank(r) {
			return r
		}
		if r := want + d; d > 0 && r <= hi && blank(r) {
			return r
		}
	}
	return want
}

func isBlank(row []byte) bool {
	for _, b := range row {
		if b != 0 {
			return false
		}
	}
	return true
}

// encodeJob is one job around rows: the header, the GS v 0 blocks and the footer.
func encodeJob(rows []byte, bytesPerRow int, s JobSettings) []byte {
	n := len(rows) / bytesPerRow
	block := n
	if s.BlockRows > 0 {
		block = min(s.BlockRows, n)
	}
	out := make([]byte, 0, 11+8*((n+block-1)/block)+len(rows)+8)
	out = append(append(out, cmdSpeed...), byte(s.Speed))
	out = append(append(out, cmdDarkness...), byte(s.Darkness))
	out = append(append(out, cmdMedia...), s.Media)
	for from := 0; from < n; from += block {
		to := min(from+block, n)
		out = appendRaster(out, rows[from*bytesPerRow:to*bytesPerRow], bytesPerRow)
	}
	out = append(out, cmdEnd...)
	if !s.SkipFeed {
		out = append(out, cmdFeed...)
	}
	return out
}

// appendRaster appends one GS v 0 (mode 0) block: the command, the width in bytes and
// the height in rows as little-endian 16-bit numbers, then the rows.
func appendRaster(out, rows []byte, bytesPerRow int) []byte {
	out = append(out, cmdRaster...)
	out = binary.LittleEndian.AppendUint16(out, uint16(bytesPerRow))
	out = binary.LittleEndian.AppendUint16(out, uint16(len(rows)/bytesPerRow))
	return append(out, rows...)
}
