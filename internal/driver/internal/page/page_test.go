package page

import (
	"testing"

	"github.com/ljagiello/crate-labels/internal/printer"
)

func TestFlag(t *testing.T) {
	s := printer.Settings{"a": true, "b": false, "c": "Yes", "d": " on ", "e": "0", "f": "nope", "g": 1}
	for key, want := range map[string]bool{"a": true, "b": false, "c": true, "d": true, "e": false, "f": false} {
		if got := Flag(s, key, !want); got != want {
			t.Errorf("%s: %v", key, got)
		}
	}
	if !Flag(s, "missing", true) || Flag(s, "missing", false) || !Flag(s, "g", true) {
		t.Error("defaults")
	}
}

func TestCropMarks(t *testing.T) {
	uncut := &printer.Stock{Sheet: &printer.SheetGrid{PitchMM: [2]float64{50, 30}, CellMM: [2]float64{50, 30}}}
	avery := &printer.Stock{Sheet: &printer.SheetGrid{PitchMM: [2]float64{69.85, 25.4}, CellMM: [2]float64{66.675, 25.4}}}
	single := &printer.Stock{}
	if !CropMarks(uncut, nil) || CropMarks(avery, nil) || CropMarks(single, nil) {
		t.Error("defaults")
	}
	if CropMarks(uncut, printer.Settings{"crop_marks": false}) || !CropMarks(avery, printer.Settings{"crop_marks": "true"}) {
		t.Error("overrides")
	}
}
