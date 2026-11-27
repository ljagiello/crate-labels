// Package page holds the settings helpers the page-printer drivers (ipp and cups) share.
package page

import (
	"strings"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/sheets"
)

// Flag reads a boolean setting. Per-job options may arrive from JSON or a form, so the
// strings "1", "true", "yes" and "on" (any case) are true and any other string is false.
// A missing key, or a value of another type, gives def.
func Flag(s printer.Settings, key string, def bool) bool {
	switch v := s[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	}
	return def
}

// CropMarks is the crop_marks setting: trim marks on sheet stocks (see
// sheets.DrawTrimMarks). It defaults to on only for uncut sheets, which the user cuts
// apart by hand.
func CropMarks(s *printer.Stock, settings printer.Settings) bool {
	return Flag(settings, "crop_marks", sheets.DefaultCropMarks(s))
}
