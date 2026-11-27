package ipp

import (
	"image"
	"math"
	"path/filepath"
	"slices"
	"testing"
)

// members is the collection value of a media-col attribute.
func members(a Attribute) Collection { return a.First().(Collection) }

func memberNames(c Collection) []string {
	var out []string
	for _, a := range c {
		out = append(out, a.Name)
	}
	return out
}

// sizedMedia is a media-col entry; a negative y is a roll from 10 mm to 1 m.
func sizedMedia(x, y int, extra ...Attribute) Collection {
	var dy Attribute
	if y < 0 {
		dy = Attr("y-dimension", TagRangeOfInteger, Range{1000, 100000})
	} else {
		dy = Integer("y-dimension", y)
	}
	return append(Collection{NewCollection("media-size", Integer("x-dimension", x), dy)}, extra...)
}

func printerWith(attrs ...Attribute) *Printer {
	return &Printer{Response: NewMessage(0, 1, Group{Tag: TagPrinterGroup, Attributes: attrs})}
}

func media(name string, cols ...Collection) Attribute {
	vs := make([]any, len(cols))
	for i, c := range cols {
		vs[i] = c
	}
	return Attr(name, TagBeginCollection, vs...)
}

// brother-summary.json records what this package reads from the Brother capture and
// what it would choose for some page sizes, so a change in policy shows up as a diff.
func TestBrotherSummary(t *testing.T) {
	p := brother(t)
	loadedMedia, loaded := p.Media()
	var described []string
	for _, m := range loadedMedia {
		described = append(described, m.Describe())
	}
	pages := []struct {
		name string
		mm   [2]float64
	}{
		{"letter", [2]float64{215.9, 279.4}}, {"a4", [2]float64{210, 297}}, {"4x6in", [2]float64{101.6, 152.4}},
		{"50x30", [2]float64{50, 30}}, {"50x80", [2]float64{50, 80}}, {"15x50", [2]float64{15, 50}},
	}
	choices := map[string]any{}
	for _, pg := range pages {
		for _, exact := range []bool{false, true} {
			key := pg.name
			if exact {
				key += " exact-only"
			}
			c := ChooseMedia(p, pg.mm, exact)
			if c == nil {
				choices[key] = nil
				continue
			}
			choices[key] = map[string]any{
				"media": c.Media.Describe(), "size": c.Size, "turned": c.Turned, "exact": c.Exact,
				"media_col_members": memberNames(members(MediaCol(c, p.Strings("media-col-supported")))),
			}
		}
	}
	summary := map[string]any{
		"name":              p.Name(),
		"state":             p.State(),
		"formats":           p.Formats(),
		"sendable":          p.Sendable(),
		"urf_dpi_for_300":   p.URFResolution(300),
		"urf_dpi_for_203":   p.URFResolution(203.2),
		"default_quality":   p.DefaultQuality(),
		"job_page_limit":    p.JobPageLimit(),
		"validate_job":      p.Supports(OpValidateJob),
		"media_from_ready":  loaded,
		"media":             described,
		"database_size":     len(p.attr("media-col-database").Collections()),
		"label_job_ticket":  attrNames(Ticket{Media: ChooseMedia(p, [2]float64{50, 30}, false), Grey: true, Trim: true}.Attributes(p)),
		"choices_by_page":   choices,
		"problem_when_idle": PrinterProblem(p.State(), p.Strings("printer-state-reasons")),
	}
	goldenJSON(t, filepath.Join(goldenDir, "ipp", "brother-summary.json"), summary)
	if !loaded || len(loadedMedia) != 1 || p.URFResolution(300) != 600 || p.DefaultQuality() != 4 || p.JobPageLimit() != 999 {
		t.Fatalf("summary %v", summary)
	}
}

func attrNames(attrs []Attribute) []string {
	var out []string
	for _, a := range attrs {
		out = append(out, a.Name)
	}
	return out
}

func TestChooseMedia(t *testing.T) {
	letter := sizedMedia(21590, 27940)
	a4 := sizedMedia(21000, 29700)
	card := sizedMedia(10200, 15200) // 4×6 in as a printer might round it
	roll := sizedMedia(6200, -1)
	ready := func(cols ...Collection) *Printer { return printerWith(media("media-col-ready", cols...)) }
	for _, tc := range []struct {
		name      string
		p         *Printer
		page      [2]float64
		exactOnly bool
		want      Collection
		size      [2]int
		turned    bool
		exact     bool
	}{
		{"exact beats larger", ready(letter, card), [2]float64{101.6, 152.4}, false, card, [2]int{10200, 15200}, false, true},
		{"exact either way round", ready(letter, card), [2]float64{152.4, 101.6}, false, card, [2]int{10200, 15200}, true, true},
		{"least waste", ready(a4, letter), [2]float64{50, 30}, false, letter, [2]int{21590, 27940}, false, false},
		{"roll takes the page length", ready(letter, roll), [2]float64{50, 30}, false, roll, [2]int{6200, 3000}, false, false},
		{"roll of the page width is exact", ready(letter, roll), [2]float64{62, 30}, false, roll, [2]int{6200, 3000}, false, true},
		{"roll turns a wide page", ready(roll), [2]float64{40, 62}, false, roll, [2]int{6200, 4000}, true, true},
		{"sheet stock on its own size", ready(a4, letter), [2]float64{215.9, 279.4}, true, letter, [2]int{21590, 27940}, false, true},
	} {
		c := ChooseMedia(tc.p, tc.page, tc.exactOnly)
		if c == nil || !slices.EqualFunc(c.Media.Col, tc.want, func(a, b Attribute) bool { return a.Name == b.Name }) ||
			c.Media.X.Min != tc.want.First("media-size").(Collection).First("x-dimension") ||
			c.Size != tc.size || c.Turned != tc.turned || c.Exact != tc.exact {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}
	for _, tc := range []struct {
		name      string
		p         *Printer
		page      [2]float64
		exactOnly bool
	}{
		{"nothing large enough", ready(card), [2]float64{215.9, 279.4}, false},
		{"exact only", ready(letter), [2]float64{50, 30}, true},
		{"62 mm is not 2.4 in", ready(sizedMedia(6096, 2900)), [2]float64{62, 29}, true},
		{"roll too short", ready(roll), [2]float64{62, 1200}, false},
		{"no sizes at all", ready(Collection{Keyword("media-source", "tray-1")}), [2]float64{50, 30}, false},
	} {
		if c := ChooseMedia(tc.p, tc.page, tc.exactOnly); c != nil {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}
	// Without media-col-ready only exact sizes from the database count.
	db := printerWith(media("media-col-database", letter, sizedMedia(5000, 3000)))
	if c := ChooseMedia(db, [2]float64{50, 30}, false); c == nil || !c.Exact || c.Media.Loaded {
		t.Fatalf("database: %+v", c)
	}
	if c := ChooseMedia(db, [2]float64{40, 30}, false); c != nil {
		t.Fatalf("database substitute: %+v", c)
	}
}

func TestMediaColEchoesTheEntry(t *testing.T) {
	p := brother(t)
	c := ChooseMedia(p, [2]float64{215.9, 279.4}, true)
	col := members(MediaCol(c, p.Strings("media-col-supported")))
	// The Brother's ready entry, in its own order, without media-auto-dimension (not a
	// registered member) or media-source-properties (a tray description).
	want := []string{"media-size", "media-bottom-margin", "media-left-margin", "media-right-margin", "media-top-margin", "media-source"}
	if got := memberNames(col); !slices.Equal(got, want) {
		t.Fatalf("members %v", got)
	}
	if a := col.Get("media-source"); a.Values[0].Tag != TagKeyword || a.First() != "tray-1" {
		t.Fatalf("media-source %v", a)
	}

	named := &Media{Col: sizedMedia(21590, 27940, Attr("media-type", TagName, "labels"), Keyword("media-size-name", "na_letter_8.5x11in"))}
	named.X, named.Y = Extent{21590, 21590}, Extent{27940, 27940}
	col = members(MediaCol(&Choice{Media: named, Size: [2]int{21590, 27940}}, nil))
	if got := memberNames(col); !slices.Equal(got, []string{"media-size", "media-type", "media-size-name"}) || col.Get("media-type").Values[0].Tag != TagName {
		t.Fatalf("named %v", col)
	}

	rollCol := sizedMedia(6200, -1, Keyword("media-size-name", "roll_max_62x1000mm"), Keyword("media-type", "labels-continuous"))
	m, _ := MediaFrom(rollCol, true)
	col = members(MediaCol(&Choice{Media: m, Size: [2]int{6200, 3000}}, []string{"media-size", "media-size-name", "media-type"}))
	size := col.First("media-size").(Collection)
	if size.First("x-dimension") != 6200 || size.First("y-dimension") != 3000 || col.Get("media-size-name") != nil || col.Get("media-type") == nil {
		t.Fatalf("roll %v", col)
	}
}

func inkBox(g *image.Gray) image.Rectangle {
	var r image.Rectangle
	b := g.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if g.GrayAt(x, y).Y != 255 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

func black(w, h int) *image.Gray { return image.NewGray(image.Rect(0, 0, w, h)) }

func TestPlace(t *testing.T) {
	letter, _ := MediaFrom(sizedMedia(21590, 27940), true)
	img, err := Place(black(300, 150), &Choice{Media: letter, Size: [2]int{21590, 27940}}, 600, 300)
	if err != nil || img.Bounds() != image.Rect(0, 0, 5100, 6600) {
		t.Fatalf("%v %v", img.Bounds(), err)
	}
	if got := inkBox(img); got != image.Rect(2550-300, 3300-150, 2550+300, 3300+150) {
		t.Fatalf("unturned ink %v", got)
	}
	img, _ = Place(black(300, 150), &Choice{Media: letter, Size: [2]int{21590, 27940}, Turned: true}, 600, 300)
	if got := inkBox(img); got != image.Rect(2550-150, 3300-300, 2550+150, 3300+300) {
		t.Fatalf("turned ink %v", got)
	}
	roll, _ := MediaFrom(sizedMedia(6200, -1), true)
	img, _ = Place(black(591, 354), &Choice{Media: roll, Size: [2]int{6200, 3000}}, 300, 300)
	if img.Bounds() != image.Rect(0, 0, 732, 354) || inkBox(img) != image.Rect(70, 0, 661, 354) {
		t.Fatalf("roll %v ink %v", img.Bounds(), inkBox(img))
	}
	if _, err := Place(black(0, 0), &Choice{Media: roll, Size: [2]int{6200, 3000}}, 300, 300); err == nil {
		t.Fatal("empty page")
	}
}

func TestURFResolution(t *testing.T) {
	urf := func(tokens ...string) Attribute {
		return Keyword("urf-supported", append([]string{"W8"}, tokens...)...)
	}
	for _, tc := range []struct {
		p    *Printer
		src  float64
		want int
	}{
		{printerWith(urf("RS600")), 300, 600},
		{printerWith(urf("RS300-600")), 300, 300},
		{printerWith(urf("RS203-406")), 300, 406},
		{printerWith(urf("RS150-200")), 300, 200},
		{printerWith(urf("RS300-600")), 203.2, 300},
		{printerWith(urf(), Attr("printer-resolution-default", TagResolution, Resolution{118, 118, UnitsDPCM})), 300, 300},
		{printerWith(urf(), Attr("printer-resolution-default", TagResolution, Resolution{600, 600, UnitsDPI})), 300, 600},
		{printerWith(), 300, 300},
	} {
		if got := tc.p.URFResolution(tc.src); got != tc.want {
			t.Errorf("%v at %v dpi: %d, want %d", tc.p.Strings("urf-supported"), tc.src, got, tc.want)
		}
	}
}

func TestPrinterDefaults(t *testing.T) {
	for q, want := range map[int]int{QualityHigh: QualityHigh, QualityDraft: QualityDraft, 7: QualityNormal} {
		if got := printerWith(Enum("print-quality-default", q)).DefaultQuality(); got != want {
			t.Errorf("default %d: %d", q, got)
		}
	}
	empty := printerWith()
	if empty.DefaultQuality() != QualityNormal || empty.Name() != "the printer" || empty.JobPageLimit() != 0 || empty.State() != 0 {
		t.Fatal("defaults")
	}
	named := printerWith(Name("printer-name", "BRW0123"), Attr("printer-info", TagText, " "))
	if named.Name() != "BRW0123" {
		t.Fatalf("name %q", named.Name())
	}
	// Attributes outside the printer group are not the printer's.
	misplaced := &Printer{Response: NewMessage(0, 1, Group{Tag: TagOperationGroup, Attributes: []Attribute{Enum("printer-state", 5)}})}
	if misplaced.State() != 0 {
		t.Fatal("read printer-state from the operation group")
	}
}

func TestTicketAttributes(t *testing.T) {
	p := brother(t)
	c := ChooseMedia(p, [2]float64{50, 30}, false)
	if got := attrNames(Ticket{Media: c, Grey: true, Trim: true, Quality: QualityHigh}.Attributes(p)); !slices.Equal(got, []string{"media-col", "print-color-mode", "print-scaling"}) {
		t.Fatalf("brother %v", got)
	}
	if got := attrNames(Ticket{Media: c}.Attributes(p)); !slices.Equal(got, []string{"media-col", "print-scaling"}) {
		t.Fatalf("colour allowed %v", got)
	}
	labeller := printerWith(Enum("finishings-supported", 3, FinishTrimAfterPages), Enum("print-quality-supported", 3, 4, 5),
		Enum("operations-supported", OpPrintJob, OpValidateJob))
	if got := attrNames(Ticket{Media: c, Trim: true, Quality: QualityHigh}.Attributes(labeller)); !slices.Equal(got, []string{"media-col", "finishings", "print-quality"}) {
		t.Fatalf("labeller %v", got)
	}
	// A printer that cannot validate is held to job-creation-attributes-supported.
	strict := printerWith(Keyword("print-scaling-supported", "none"), Keyword("job-creation-attributes-supported", "media-col"))
	if got := attrNames(Ticket{Media: c}.Attributes(strict)); !slices.Equal(got, []string{"media-col"}) {
		t.Fatalf("strict %v", got)
	}
	all := Ticket{Media: c, Grey: true}.Attributes(p)
	if got := attrNames(Without(all, []string{"print-scaling"})); !slices.Equal(got, []string{"media-col", "print-color-mode"}) || len(all) != 3 {
		t.Fatalf("without %v", got)
	}
	if JobName("labels", 2, 3) != "labels (2 of 3)" || JobName("labels", 1, 1) != "labels" {
		t.Fatal("job names")
	}
}

func TestExtents(t *testing.T) {
	for v, want := range map[any]Extent{5000: {5000, 5000}, 0: {1, math.MaxInt32}, Range{1000, 9000}: {1000, 9000}} {
		if got, ok := extentOf(v); !ok || got != want {
			t.Errorf("%v: %v", v, got)
		}
	}
	if _, ok := extentOf(Range{9000, 1000}); ok {
		t.Error("inverted range")
	}
	if got := (Extent{1000, 9000}).nearest(12000); got != 9000 {
		t.Errorf("nearest %d", got)
	}
}
