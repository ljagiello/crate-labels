package phomemo

import (
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/driver/internal/thermal"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/raster"
)

// Calibration and job settings this driver reads. Values may come from TOML (int64,
// float64) or JSON (float64). They are validated by the thermal helpers that Munbyn and
// Clabel share, so the rules are the same for every thermal family.
const (
	SettingSpeed         = "speed"           // 0..255, default 3
	SettingDarkness      = "darkness"        // 0..255, default 8
	SettingPaperAlign    = "paper_align"     // "right" (against the rod, flush to high x; default) or "center" (between the guides)
	SettingOffsetDots    = "offset_dots"     // white dots on the low-x side; overrides paper_align; clamped to the head
	SettingExtraRotateCW = "extra_rotate_cw" // 0, 90, 180 or 270 on top of the stock's own turn
	SettingBLEGapMS      = "ble_gap_ms"      // pause between BLE writes, 0..1000 ms
	SettingBLEName       = "ble_name"        // advertised name, for model identification

	// SettingHelloFrames lists, as hex, the frames the model may send when the host
	// subscribes to its notifications (model option).
	SettingHelloFrames = "hello_frames"
	// SettingLegacyFirmware and SettingLegacyBlockRows describe firmware that cannot
	// take a tall raster in one block (model options; the M110 before version 2):
	// such printers get GS v 0 blocks of at most SettingLegacyBlockRows rows.
	SettingLegacyFirmware  = "legacy_firmware"
	SettingLegacyBlockRows = "legacy_block_rows"
)

// Stock extras this driver reads.
const (
	extraMaxLabelRows = "max_label_rows"       // continuous stock: the longest a label may print
	extraTailRows     = "blank_rows_after"     // blank rows fed after each label
	extraSkipFeed     = "phomemo_short_footer" // leave out the feed command
)

// maxOffsetDots bounds offset_dots in both directions before the lane is clamped to
// the head.
const maxOffsetDots = 1 << 16

// paperAligns maps paper_align to where the label sits across the head: a roll held
// against the rod lies at the high-x end, one held between guides in the middle.
var paperAligns = map[string]raster.Justify{"right": raster.End, "center": raster.Center}

// wholeNumber reads an integer from a TOML or JSON value (catalog options and stock
// extras): an int, an int64, a whole float64 or a numeric string.
func wholeNumber(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

// offsetSetting is nil when offset_dots is unset, so paper_align decides. Unlike Munbyn
// and Clabel, Phomemo takes negative values here; the lane is then clamped onto the
// head, so a negative offset prints the same as 0.
func offsetSetting(s printer.Settings) (*int, error) {
	if v, ok := s[SettingOffsetDots]; !ok || v == nil {
		return nil, nil
	}
	n, err := thermal.OffsetDots(s, 0, -maxOffsetDots, maxOffsetDots)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func paperAlignSetting(s printer.Settings) (raster.Justify, error) {
	a := strings.ToLower(strings.TrimSpace(s.String(SettingPaperAlign, "right")))
	j, ok := paperAligns[a]
	if !ok {
		return 0, printer.Errorf("Setting %q must be \"right\" or \"center\", not %q.", SettingPaperAlign, a)
	}
	return j, nil
}

// legacyBlockRows is the GS v 0 block size for a printer reporting firmware, or 0 when
// it can take a whole job in one block. Unknown firmware is never treated as legacy.
func legacyBlockRows(s printer.Settings, firmware Version) int {
	rows, _ := wholeNumber(s[SettingLegacyBlockRows])
	below, err := ParseVersion(s.String(SettingLegacyFirmware, ""))
	if rows <= 0 || err != nil || firmware == nil || !firmware.Before(below) {
		return 0
	}
	return rows
}
