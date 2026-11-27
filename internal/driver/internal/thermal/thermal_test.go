package thermal

import (
	"errors"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

func TestSettingsHelpers(t *testing.T) {
	s := printer.Settings{"i": int64(7), "f": 7.0, "half": 7.5, "str": " 9 ", "bad": "x", "b": true, "nil": nil}
	for _, tc := range []struct {
		key  string
		want int
		err  bool
	}{{"i", 7, false}, {"f", 7, false}, {"str", 9, false}, {"missing", 3, false}, {"nil", 3, false},
		{"half", 0, true}, {"bad", 0, true}, {"b", 0, true}} {
		got, err := Int(s, tc.key, 3, 0, 10)
		if (err != nil) != tc.err || (!tc.err && got != tc.want) {
			t.Errorf("Int(%q) = %d, %v", tc.key, got, err)
		}
		var perr *printer.Error
		if err != nil && !errors.As(err, &perr) {
			t.Errorf("Int(%q) error %v is not a printer.Error", tc.key, err)
		}
	}
	if _, err := Int(s, "i", 0, 0, 5); err == nil {
		t.Error("Int accepted 7 outside 0..5")
	}
	if v, err := ClampedInt(printer.Settings{"o": -4.0}, "o", 0, 0, 384); err != nil || v != 0 {
		t.Errorf("ClampedInt(-4) = %d, %v", v, err)
	}
	if v, err := ClampedInt(printer.Settings{"o": 999.0}, "o", 0, 0, 384); err != nil || v != 384 {
		t.Errorf("ClampedInt(999) = %d, %v", v, err)
	}
	if _, err := Float(printer.Settings{"g": 1001.0}, "g", 0, 0, 1000); err == nil {
		t.Error("Float accepted 1001 outside 0..1000")
	}
}

func TestRotation(t *testing.T) {
	stock := &printer.Stock{RotateCW: 90}
	for extra, want := range map[any]int{nil: 90, int64(90): 180, 180.0: 270, 270.0: 0} {
		got, err := Rotation(stock, printer.Settings{"extra_rotate_cw": extra})
		if err != nil || got != want {
			t.Errorf("Rotation(90 + %v) = %d, %v; want %d", extra, got, err, want)
		}
	}
	for _, bad := range []any{45.0, 360.0, -90.0} {
		if _, err := Rotation(stock, printer.Settings{"extra_rotate_cw": bad}); err == nil {
			t.Errorf("Rotation accepted extra_rotate_cw %v", bad)
		}
	}
}

func TestChunkGapPrefersCalibration(t *testing.T) {
	m := &printer.Model{BLE: &printer.BLEProfile{ChunkBytes: 238, ChunkGapMS: 5}}
	if g, _ := ChunkGap(m, nil, 20); g != 5*time.Millisecond {
		t.Errorf("model gap = %v", g)
	}
	if g, _ := ChunkGap(m, printer.Settings{"ble_gap_ms": 12.5}, 20); g != 12500*time.Microsecond {
		t.Errorf("calibrated gap = %v", g)
	}
	if g, _ := ChunkGap(&printer.Model{}, nil, 20); g != 20*time.Millisecond {
		t.Errorf("default gap = %v", g)
	}
	if ChunkBytes(m, 128) != 238 || ChunkBytes(&printer.Model{}, 128) != 128 {
		t.Error("ChunkBytes ignores the model or the default")
	}
}

func TestExtraRotationIsOneOfFour(t *testing.T) {
	for raw, want := range map[any]int{nil: 0, int64(0): 0, 90.0: 90, int64(180): 180, "270": 270} {
		got, err := ExtraRotation(printer.Settings{"extra_rotate_cw": raw})
		if err != nil || got != want {
			t.Errorf("ExtraRotation(%v) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, bad := range []any{45.0, 360.0, -90.0, 90.5, "sideways", true} {
		_, err := ExtraRotation(printer.Settings{"extra_rotate_cw": bad})
		var perr *printer.Error
		if !errors.As(err, &perr) {
			t.Errorf("ExtraRotation(%v) = %v, want a printer.Error", bad, err)
		}
	}
}

func TestOffsetDotsClampsIntoRange(t *testing.T) {
	for raw, want := range map[any]int{nil: 7, -5.0: 0, int64(10): 10, 999.0: 384} {
		got, err := OffsetDots(printer.Settings{"offset_dots": raw}, 7, 0, 384)
		if err != nil || got != want {
			t.Errorf("OffsetDots(%v) = %d, %v; want %d", raw, got, err, want)
		}
	}
	if got, err := OffsetDots(printer.Settings{"offset_dots": -300.0}, 0, -200, 600); err != nil || got != -200 {
		t.Errorf("OffsetDots(-300) in [-200, 600] = %d, %v", got, err)
	}
	if _, err := OffsetDots(printer.Settings{"offset_dots": 1.5}, 0, 0, 384); err == nil {
		t.Error("OffsetDots accepted 1.5")
	}
}

func TestMessages(t *testing.T) {
	if Labels(1) != "1 label" || Labels(3) != "3 labels" {
		t.Errorf("Labels = %q, %q", Labels(1), Labels(3))
	}
	if Seconds(4*time.Second) != "4 s" || Seconds(1500*time.Millisecond) != "1.5 s" {
		t.Error("Seconds formats oddly")
	}
}
