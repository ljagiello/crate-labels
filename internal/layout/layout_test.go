package layout

import (
	"encoding/json"
	"errors"
	"image"
	"io/fs"
	"math"
	"slices"
	"sort"
	"testing"

	"github.com/ljagiello/crate-labels/internal/printer"
)

func darkCount(img *image.Gray) int {
	n := 0
	for _, v := range img.Pix {
		if v < 128 {
			n++
		}
	}
	return n
}

func TestTemplatesAreUniqueAndDescribed(t *testing.T) {
	want := []string{"dj-single", "dj-two-track", "dj-four-track", "dj-tape", "seller-small",
		"seller-large", "center-label", "obi-strip", "test-pattern"}
	var ids []string
	for _, tmpl := range Templates() {
		ids = append(ids, tmpl.ID())
		if tmpl.Name() == "" || tmpl.Description() == "" {
			t.Errorf("%s: missing name or description", tmpl.ID())
		}
		if _, ok := tmpl.(drawer); !ok && tmpl.ID() != "test-pattern" {
			t.Errorf("%s does not lay out on a sheet", tmpl.ID())
		}
		seen := map[string]bool{}
		for _, spec := range tmpl.Options() {
			if seen[spec.ID] {
				t.Errorf("%s: option %s twice", tmpl.ID(), spec.ID)
			}
			seen[spec.ID] = true
			if got := spec.Coerce(spec.Default); got != spec.Default {
				t.Errorf("%s.%s: default %v coerces to %v", tmpl.ID(), spec.ID, spec.Default, got)
			}
			if len(spec.ChoiceLabels) != 0 && len(spec.ChoiceLabels) != len(spec.Choices) {
				t.Errorf("%s.%s: %d labels for %d choices", tmpl.ID(), spec.ID, len(spec.ChoiceLabels), len(spec.Choices))
			}
			if spec.Choices == nil || spec.ChoiceLabels == nil {
				t.Errorf("%s.%s: nil choices would marshal as null", tmpl.ID(), spec.ID)
			}
		}
	}
	if !slices.Equal(ids, want) {
		t.Errorf("template ids %v, want %v", ids, want)
	}
	for _, id := range want {
		if got, err := Get(id); err != nil || got.ID() != id {
			t.Errorf("Get(%q) = %v, %v", id, got, err)
		}
	}
}

func TestExpectedStockSupport(t *testing.T) {
	cat := testCatalog(t)
	ids := func(stockID string) []string {
		s, err := cat.Stock(stockID)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tmpl := range TemplatesFor(s) {
			out = append(out, tmpl.ID())
		}
		sort.Strings(out)
		return out
	}
	cases := map[string][]string{
		"40x30":              {"dj-single", "seller-small", "test-pattern"},
		"50x30":              {"dj-single", "seller-small", "test-pattern"},
		"letter-uncut-50x30": {"dj-single", "seller-small", "test-pattern"},
		"avery-5160":         {"dj-single", "seller-small", "test-pattern"},
		"50x80-two-track":    {"dj-two-track", "seller-large", "test-pattern"},
		"letter-uncut-50x80": {"dj-two-track", "seller-large", "test-pattern"},
		"avery-5163":         {"dj-two-track", "test-pattern"},
		"70x80":              {"dj-four-track", "seller-large", "test-pattern"},
		"4x6":                {"dj-four-track", "seller-large", "test-pattern"},
		"d30-15mm-tape":      {"dj-tape", "test-pattern"},
		"15x50":              {"dj-tape", "test-pattern"},
		"center-7in":         {"center-label", "test-pattern"},
		"obi-legal":          {"obi-strip", "test-pattern"},
		"obi-mini-letter":    {"obi-strip", "test-pattern"},
	}
	for stock, want := range cases {
		if got := ids(stock); !slices.Equal(got, want) {
			t.Errorf("%s: templates %v, want %v", stock, got, want)
		}
	}
}

// TestEveryTemplateAndStockRenders checks the output contract for every template × stock
// it supports × record (including a blank one) at both resolutions.
func TestEveryTemplateAndStockRenders(t *testing.T) {
	cat := testCatalog(t)
	records := append(append([]string(nil), SampleNames...), "blank")
	for _, dpi := range sampleDPIs {
		for _, record := range records {
			for _, tmpl := range Templates() {
				for _, s := range stocks(cat) {
					if !tmpl.Supports(s) {
						continue
					}
					d := &LabelData{}
					if record != "blank" {
						d = Sample(record)
					}
					c := canvasFor(s, dpi)
					img, err := Render(tmpl.ID(), d, c, s, nil)
					if err != nil {
						t.Fatalf("%s on %s: %v", tmpl.ID(), s.ID, err)
					}
					if img.Rect != image.Rect(0, 0, c.Width, c.Height) {
						t.Fatalf("%s on %s: size %v, want %dx%d", tmpl.ID(), s.ID, img.Rect, c.Width, c.Height)
					}
					dark := darkCount(img)
					if (record != "blank" || tmpl.ID() == "test-pattern" || tmpl.ID() == "center-label") && dark == 0 {
						t.Errorf("%s on %s (%s, %g dpi) drew nothing", tmpl.ID(), s.ID, record, dpi)
					}
					if dark*2 >= len(img.Pix) {
						t.Errorf("%s on %s (%s): background is not white", tmpl.ID(), s.ID, record)
					}
				}
			}
		}
	}
}

func TestRenderRefusesUnsupportedStockAndUnknownTemplate(t *testing.T) {
	cat := testCatalog(t)
	tape, _ := cat.Stock("d30-15mm-tape")
	var perr *printer.Error
	if _, err := Render("dj-two-track", Sample("ep12"), canvasFor(tape, 203.2), tape, nil); !errors.As(err, &perr) {
		t.Errorf("dj-two-track on tape: err %v, want a *printer.Error", err)
	}
	if _, err := Get("no-such-template"); !errors.As(err, &perr) {
		t.Errorf("unknown template: err %v, want a *printer.Error", err)
	}
	if _, err := Render("dj-single", nil, printer.Canvas{}, tape, nil); err == nil {
		t.Error("a zero canvas must be refused")
	}
	if _, err := Render("dj-single", nil, printer.Canvas{Width: 10, Height: 10, DPI: 203.2}, nil, nil); err == nil {
		t.Error("a missing stock must be refused")
	}
}

func TestRenderFollowsTheCanvasNotTheStock(t *testing.T) {
	s, _ := testCatalog(t).Stock("40x30")
	img, err := Render("dj-single", Sample("ep12"), printer.Canvas{Width: 384, Height: 240, DPI: 203.2}, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Rect.Dx() != 384 || img.Rect.Dy() != 240 {
		t.Errorf("size %v, want 384x240", img.Rect)
	}
}

func TestOptionsAreCoercedFromStringsAndClamped(t *testing.T) {
	tmpl, _ := Get("dj-single")
	got := CoerceOptions(tmpl, map[string]any{"show_qr": "false", "font_scale": "9",
		"key_notation": "MUSICAL", "bogus": 1, "bpm_range": "nope", "font": "atkinson"})
	want := map[string]any{"show_qr": false, "font_scale": fontScaleMax, "key_notation": "musical",
		"bpm_range": bpmOff, "font": "atkinson", "track": ""}
	if len(got) != len(want) {
		t.Errorf("got keys %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

// TestStoredOptionsFromEarlierBuildsFallBack feeds values an older build may have saved in
// ui.options or a print history entry: they coerce to the current defaults.
func TestStoredOptionsFromEarlierBuildsFallBack(t *testing.T) {
	old := map[string]any{"font": "geist-mono", "bpm_range": "88", "font_scale": 0.6,
		"spine": "2lp", "center_format": "7-large-hole", "tracklist_bpm": true}
	for _, tmpl := range Templates() {
		o := CoerceOptions(tmpl, old)
		for _, spec := range tmpl.Options() {
			switch spec.ID {
			case "font", "bpm_range", "spine", "center_format":
				if o[spec.ID] != spec.Default {
					t.Errorf("%s.%s = %v, want the default %v", tmpl.ID(), spec.ID, o[spec.ID], spec.Default)
				}
			case "font_scale":
				if o[spec.ID] != fontScaleMin {
					t.Errorf("%s.font_scale = %v, want it clamped to %v", tmpl.ID(), o[spec.ID], fontScaleMin)
				}
			}
		}
		if _, ok := o["tracklist_bpm"]; ok {
			t.Errorf("%s keeps an unknown option", tmpl.ID())
		}
	}
}

func TestOptionSpecCoerce(t *testing.T) {
	intSpec := OptionSpec{ID: "n", Type: OptionInt, Default: 3, Min: floatPtr(1), Max: floatPtr(5)}
	cases := []struct {
		spec OptionSpec
		in   any
		want any
	}{
		{intSpec, "7.6", 5},
		{intSpec, "x", 3},
		{intSpec, 2.5, 2}, // half to even
		{intSpec, json.Number("4"), 4},
		{OptionSpec{Type: OptionFloat, Default: 1.0}, "nan", 1.0},
		{OptionSpec{Type: OptionFloat, Default: 1.0}, int64(2), 2.0},
		{OptionSpec{Type: OptionBool, Default: true}, "off", false},
		{OptionSpec{Type: OptionBool, Default: true}, " YES ", true},
		{OptionSpec{Type: OptionBool, Default: true}, "maybe", true},
		{OptionSpec{Type: OptionBool, Default: false}, 1.0, true},
		{OptionSpec{Type: OptionText, Default: ""}, nil, ""},
		{OptionSpec{Type: OptionText, Default: ""}, 12, "12"},
		{bpmRangeOption(), "85-169", "85-169"},
		{bpmRangeOption(), 85.0, bpmOff},
		{keyNotationOption(), " Both ", "both"},
		{keyNotationOption(), " Off ", "off"},
	}
	for _, c := range cases {
		if got := c.spec.Coerce(c.in); got != c.want {
			t.Errorf("%s Coerce(%#v) = %#v, want %#v", c.spec.Type, c.in, got, c.want)
		}
	}
}

func TestOptionSpecJSON(t *testing.T) {
	data, err := json.Marshal(fontScaleOption())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "label", "type", "default", "min", "max", "choices", "choice_labels", "help"} {
		if _, ok := got[key]; !ok {
			t.Errorf("OptionSpec JSON lacks %q: %s", key, data)
		}
	}
	if got["min"] != fontScaleMin || got["max"] != fontScaleMax {
		t.Errorf("min/max %v/%v", got["min"], got["max"])
	}
}

func TestLabelDataJSONIsLenient(t *testing.T) {
	var d LabelData
	in := `{"artist":"A","year":1993,"styles":"Techno, Electro ,","genres":["Electronic",""],
		"release_id":"2360917","bogus":1,"title":null,
		"tracks":[{"position":"A1","title":"x","bpm":null,"key":null},
		          {"position":"A2","included":false,"bpm":174.5,"key":"8A"},
		          {"position":"B1","bpm":"150","included":"no","extra":true},
		          {"position":"B2","bpm":"174.5"}]}`
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	if d.Artist != "A" || d.Title != "" || d.Year != "1993" || d.ReleaseID != 2360917 {
		t.Errorf("label %+v", d)
	}
	if !slices.Equal(d.Styles, []string{"Techno", "Electro"}) || !slices.Equal(d.Genres, []string{"Electronic"}) {
		t.Errorf("styles %q genres %q", d.Styles, d.Genres)
	}
	want := []Track{
		{Position: "A1", Title: "x", Included: true},
		{Position: "A2", BPM: 175, Key: "8A"},
		{Position: "B1", BPM: 150},
		{Position: "B2", BPM: 175, Included: true},
	}
	if !slices.Equal(d.Tracks, want) {
		t.Errorf("tracks %+v, want %+v", d.Tracks, want)
	}
}

func TestLabelDataJSONOutput(t *testing.T) {
	out, err := json.Marshal(LabelData{Artist: "A", Tracks: []Track{{Position: "A1", Included: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["release_id"] != nil || got["genres"] == nil || got["styles"] == nil {
		t.Errorf("label JSON %s", out)
	}
	tr := got["tracks"].([]any)[0].(map[string]any)
	if tr["bpm"] != nil || tr["key"] != nil || tr["included"] != true {
		t.Errorf("track JSON %s", out)
	}
	for _, key := range []string{"artist", "title", "label", "catno", "year", "country", "format",
		"genres", "styles", "tracks", "notes", "price", "condition", "qr_url", "release_id"} {
		if _, ok := got[key]; !ok {
			t.Errorf("label JSON lacks %q", key)
		}
	}
	// A round trip keeps a sample intact.
	var back LabelData
	data, _ := json.Marshal(Sample("longform"))
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if want := Sample("longform"); back.ReleaseID != want.ReleaseID || !slices.Equal(back.Tracks, want.Tracks) {
		t.Errorf("round trip changed the sample: %+v", back)
	}
}

func TestNoQRWhenDisabledOrMissing(t *testing.T) {
	cat := testCatalog(t)
	for _, pair := range [][2]string{{"dj-single", "40x30"}, {"seller-large", "70x80"}, {"obi-strip", "obi-mini-letter"}} {
		s, _ := cat.Stock(pair[1])
		c := canvasFor(s, 203.2)
		with, _ := Render(pair[0], Sample("ep12"), c, s, nil)
		off, _ := Render(pair[0], Sample("ep12"), c, s, map[string]any{"show_qr": false})
		d := Sample("ep12")
		d.QRURL = ""
		missing, _ := Render(pair[0], d, c, s, nil)
		if darkCount(with) <= darkCount(off) {
			t.Errorf("%s: the QR adds no ink", pair[0])
		}
		if !slices.Equal(off.Pix, missing.Pix) {
			t.Errorf("%s: show_qr=false and an empty URL must render the same", pair[0])
		}
	}
}

// TestBandsShareSizes checks that the track bands of one label use one badge size, one
// title size and one position size.
func TestBandsShareSizes(t *testing.T) {
	cat := testCatalog(t)
	for _, pair := range [][2]string{{"dj-four-track", "70x80"}, {"dj-two-track", "50x80-two-track"}, {"obi-strip", "obi-legal"}} {
		s, _ := cat.Stock(pair[1])
		tmpl, _ := Get(pair[0])
		sh := sketch(tmpl, Sample("ep12"), canvasFor(s, 203.2), s, nil)
		sizes := map[string]map[float64]bool{"title": {}, "bpm": {}, "position": {}}
		for _, m := range sh.marks {
			for kind := range sizes {
				if len(m.name) > len(kind) && m.name[len(m.name)-len(kind):] == kind && m.name[:5] == "track" {
					sizes[kind][math.Round(m.size*1000)] = true
				}
			}
		}
		for kind, set := range sizes {
			if len(set) != 1 {
				t.Errorf("%s: %d different %s sizes", pair[0], len(set), kind)
			}
		}
	}
}

// TestCenterLabelTextStaysOnTheDisc checks that everything set on the disc lies inside
// its cut circle and outside its hole.
func TestCenterLabelTextStaysOnTheDisc(t *testing.T) {
	s, _ := testCatalog(t).Stock("center-7in")
	tmpl, _ := Get("center-label")
	for _, format := range []string{discAuto, disc7Large, disc7Small, disc12Small} {
		for _, n := range SampleNames {
			c := canvasFor(s, 300)
			sh := sketch(tmpl, Sample(n), c, s, map[string]any{"center_format": format})
			disc, _ := sh.find("disc")
			hole, _ := sh.find("hole")
			cx, cy, r := disc.box.x+disc.box.w/2, disc.box.y+disc.box.h/2, disc.box.w/2
			hr := hole.box.w / 2
			for _, m := range sh.marks {
				if !m.kind.content() || m.box.y > disc.box.bottom() {
					continue
				}
				for _, p := range [][2]float64{{m.box.x, m.box.y}, {m.box.right(), m.box.y}, {m.box.x, m.box.bottom()}, {m.box.right(), m.box.bottom()}} {
					if math.Hypot(p[0]-cx, p[1]-cy) > r {
						t.Errorf("%s/%s: %s %q leaves the disc", format, n, m.name, m.text)
						break
					}
				}
				nx := math.Max(m.box.x, math.Min(cx, m.box.right()))
				ny := math.Max(m.box.y, math.Min(cy, m.box.bottom()))
				if math.Hypot(nx-cx, ny-cy) < hr {
					t.Errorf("%s/%s: %s %q covers the hole", format, n, m.name, m.text)
				}
			}
			if format == disc12Small && math.Abs(disc.box.w-disc12MM) > 1e-9 {
				t.Errorf("12″ disc is %.1f mm", disc.box.w)
			}
		}
	}
}

func TestCenterLabelChoices(t *testing.T) {
	s, _ := testCatalog(t).Stock("center-7in")
	d := Sample("single7")
	if dc := chooseDisc(discAuto, d, s, 101.6, 152.4); dc.d != 90 || dc.hole != 38 {
		t.Errorf("7″ from the stock: %+v", dc)
	}
	if dc := chooseDisc(discAuto, Sample("ep12"), s, 101.6, 152.4); dc.d != disc12MM || dc.hole != spindleHole {
		t.Errorf("12″ from the format: %+v", dc)
	}
	if dc := chooseDisc(disc7Small, Sample("ep12"), s, 101.6, 152.4); dc.hole != spindleHole || dc.d != 90 {
		t.Errorf("explicit spindle hole: %+v", dc)
	}
	if dc := chooseDisc(discAuto, d, s, 101.6, 152.4); dc.cx != 50.8 || dc.cy != 76.2 {
		t.Errorf("disc not centred on the label: %+v", dc)
	}
	top := &printer.Stock{Round: true, DesignMM: s.DesignMM, Extra: map[string]any{"disc_top_mm": 4.0}}
	if dc := chooseDisc(discAuto, d, top, 101.6, 152.4); dc.cy != 4+disc7MM/2 {
		t.Errorf("disc_top_mm ignored: %+v", dc)
	}
	if got := speedOf("auto", d); got != "45" {
		t.Errorf("7″ speed %q", got)
	}
	if got := speedOf("auto", Sample("ep12")); got != "33⅓" {
		t.Errorf("12″ speed %q", got)
	}
	if got := speedOf("auto", &LabelData{Format: `12" 45 RPM`}); got != "45" {
		t.Errorf("12″ at 45: %q", got)
	}
	if got := speedOf("off", d); got != "" {
		t.Errorf("hidden speed %q", got)
	}
}

func TestOBISpineFollowsTheDiscs(t *testing.T) {
	s, _ := testCatalog(t).Stock("obi-legal")
	tmpl, _ := Get("obi-strip")
	c := canvasFor(s, 300)
	spineX := func(o map[string]any, d *LabelData) float64 {
		sh := sketch(tmpl, d, c, s, o)
		var xs []float64
		for _, m := range sh.marks {
			if m.name == "fold" {
				xs = append(xs, m.box.x)
			}
		}
		if len(xs) < 4 {
			t.Fatalf("fold guides %v", xs)
		}
		return math.Round(xs[2] - xs[0])
	}
	if got := spineX(nil, Sample("ep12")); got != spineMM[1] {
		t.Errorf("one disc: spine %v mm", got)
	}
	d := Sample("ep12")
	d.Tracks = append(d.Tracks, Track{Position: "D1", Title: "x", Included: true})
	if got := spineX(nil, d); got != spineMM[2] {
		t.Errorf("sides A–D: spine %v mm", got)
	}
	if got := spineX(map[string]any{"spine": "3"}, Sample("ep12")); got != spineMM[3] {
		t.Errorf("three discs chosen: spine %v mm", got)
	}
	if sh := sketch(tmpl, Sample("ep12"), c, s, map[string]any{"fold_lines": false}); func() bool {
		_, ok := sh.find("fold")
		return ok
	}() {
		t.Error("fold guides drawn when switched off")
	}
}

func TestFeedArrowPointsAtRasterRowZero(t *testing.T) {
	for _, rot := range []int{0, 90, 180, 270} {
		// Mark the design edge the arrow points at, rotate like the driver, and check the
		// mark lands on row 0 of the raster.
		const w, h = 6, 4
		design := newCanvas(w, h).img
		dx, dy := feedDirection(rot)
		for y := range h {
			for x := range w {
				if (dx < 0 && x == 0) || (dx > 0 && x == w-1) || (dy < 0 && y == 0) || (dy > 0 && y == h-1) {
					design.Pix[y*w+x] = black
				}
			}
		}
		raster := design
		for range rot / 90 {
			raster = turnClockwise(raster)
		}
		for x := range raster.Rect.Dx() {
			if raster.Pix[x] != black {
				t.Errorf("rotate_cw %d: row 0 not fully marked", rot)
				break
			}
		}
		if darkCount(raster) != raster.Rect.Dx() {
			t.Errorf("rotate_cw %d: marks outside row 0", rot)
		}
	}
}

// turnClockwise turns src a quarter clockwise, as the drivers do for rotate_cw = 90.
func turnClockwise(src *image.Gray) *image.Gray {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewGray(image.Rect(0, 0, h, w))
	for y := range h {
		for x := range w {
			dst.Pix[x*dst.Stride+(h-1-y)] = src.Pix[y*src.Stride+x]
		}
	}
	return dst
}

func TestTestPatternMarks(t *testing.T) {
	cat := testCatalog(t)
	for _, id := range []string{"40x30", "50x80-two-track", "d30-15mm-tape", "center-7in", "avery-5160", "70x80"} {
		s, _ := cat.Stock(id)
		c := canvasFor(s, 203.2)
		sh := newSheet(newCanvas(c.Width, c.Height), dotsPerMM(c))
		drawPattern(sh, c, s)
		img := sh.image()
		const border = 8 // 1 mm at 8 dots/mm
		for y := range c.Height {
			for x := range c.Width {
				edge := x < border || y < border || x >= c.Width-border || y >= c.Height-border
				if edge && img.Pix[y*img.Stride+x] >= 128 {
					t.Fatalf("%s: border pixel (%d, %d) is not black", id, x, y)
				}
			}
		}
		w, h := canvasMM(c)
		tl, _ := sh.find("top_left")
		if tl.box.right() >= w/2 || tl.box.bottom() >= h/2 {
			t.Errorf("%s: TOP-LEFT at %+v", id, tl.box)
		}
		a, _ := sh.find("feed_arrow")
		if a.box.x < 0 || a.box.w <= 0 || a.box.right() > w || a.box.y < 0 || a.box.h <= 0 || a.box.bottom() > h {
			t.Errorf("%s: feed arrow box %+v", id, a.box)
		}
		dx, dy := feedDirection(s.RotateCW)
		if want := map[int]string{-1: "-1", 0: "0", 1: "1"}[dx] + "," + map[int]string{-1: "-1", 0: "0", 1: "1"}[dy]; a.text != want {
			t.Errorf("%s: arrow %s, want %s", id, a.text, want)
		}
		if size, ok := sh.find("size"); !ok || len(size.text) < 3 || size.text[len(size.text)-2:] != "mm" {
			t.Errorf("%s: size caption %q", id, size.text)
		}
		if s.Round {
			if _, ok := sh.find("disc"); !ok {
				t.Errorf("%s: no disc guide", id)
			}
		}
	}
}

func TestFontFSServesFontsAndLicences(t *testing.T) {
	fsys := FontFS()
	for _, fam := range FontFamilies() {
		for _, file := range []string{fam.Regular, fam.Bold} {
			if _, err := fs.Stat(fsys, file); err != nil {
				t.Errorf("%s: %v", file, err)
			}
		}
	}
	licences, _ := fs.Glob(fsys, "*_ofl.txt")
	if len(licences) != len(FontFamilies()) {
		t.Errorf("%d OFL licences for %d families", len(licences), len(FontFamilies()))
	}
}

func TestConcurrentRenders(t *testing.T) {
	cat := testCatalog(t)
	s, _ := cat.Stock("center-7in")
	c := canvasFor(s, 203.2)
	want, err := Render("center-label", Sample("ep12"), c, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			got, err := Render("center-label", Sample("ep12"), c, s, map[string]any{})
			if err == nil && !slices.Equal(got.Pix, want.Pix) {
				err = errors.New("concurrent render differs")
			}
			errs <- err
		}()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestMaxTracks(t *testing.T) {
	want := map[string]int{"dj-single": 1, "dj-two-track": 2, "dj-four-track": 4, "dj-tape": 1,
		"seller-small": 0, "seller-large": 0, "center-label": 0, "obi-strip": 0, "test-pattern": 0}
	for _, tmpl := range Templates() {
		got, ok := want[tmpl.ID()]
		if !ok {
			t.Errorf("template %s has no expected MaxTracks", tmpl.ID())
			continue
		}
		if n := MaxTracks(tmpl); n != got {
			t.Errorf("MaxTracks(%s) = %d, want %d", tmpl.ID(), n, got)
		}
	}
}

func BenchmarkRenderDJSingle(b *testing.B) {
	s, _ := testCatalog(b).Stock("40x30")
	c := canvasFor(s, 203.2)
	d := Sample("ep12")
	for b.Loop() {
		if _, err := Render("dj-single", d, c, s, nil); err != nil {
			b.Fatal(err)
		}
	}
}
