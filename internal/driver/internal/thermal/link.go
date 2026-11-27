package thermal

import (
	"fmt"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// ChunkBytes is the model's largest single write (model.ble.chunk_bytes), or def when
// the model has no BLE profile. ble.WriteChunked caps it further by link.MaxWrite().
func ChunkBytes(m *printer.Model, def int) int {
	if m.BLE != nil && m.BLE.ChunkBytes > 0 {
		return m.BLE.ChunkBytes
	}
	return def
}

// ChunkGap is the pause between chunks: the ble_gap_ms calibration, else the model's
// chunk_gap_ms, else def milliseconds.
func ChunkGap(m *printer.Model, s printer.Settings, def float64) (time.Duration, error) {
	if m.BLE != nil {
		def = m.BLE.ChunkGapMS
	}
	ms, err := Float(s, "ble_gap_ms", def, 0, 1000)
	if err != nil {
		return 0, err
	}
	return time.Duration(ms * float64(time.Millisecond)), nil
}

// Seconds formats a duration for a user message: 4 s, 1.5 s.
func Seconds(d time.Duration) string {
	return fmt.Sprintf("%g s", d.Seconds())
}
