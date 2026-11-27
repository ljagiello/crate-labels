package printer

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, dir string) *Catalog {
	t.Helper()
	c, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ids(ms []*Model) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

func TestBuiltinModelsLoad(t *testing.T) {
	c := load(t, t.TempDir())
	for _, id := range []string{"phomemo-m110", "phomemo-m220", "phomemo-d30", "munbyn-fm226",
		"clabel-ct230b", "clabel-ct320b", "ipp-sheet", "ipp-label", "cups"} {
		if _, err := c.Model(id); err != nil {
			t.Errorf("missing %s: %v", id, err)
		}
	}
	m110, _ := c.Model("phomemo-m110")
	if m110.Link != LinkBLE || m110.HeadDots != 384 || math.Abs(m110.DotsPerMM()-8) > 1e-9 {
		t.Errorf("m110 = %+v", m110)
	}
	if got := m110.Options.Int("darkness", 0); got != 8 {
		t.Errorf("m110 darkness option = %d", got)
	}
	if hello, _ := m110.Options["hello_frames"].([]any); len(hello) != 2 {
		t.Errorf("m110 hello frames = %v", m110.Options["hello_frames"])
	}
}

func TestUncutGrid(t *testing.T) {
	// 50×30 on Letter: 4 columns in 203.9 mm of clear width, 8 rows in 267.4 mm, and the
	// block centred on the page.
	s, _ := load(t, t.TempDir()).Stock("letter-uncut-50x30")
	g := s.Sheet
	if g.Cols != 4 || g.Rows != 8 || math.Abs(g.MarginMM[0]-7.95) > 1e-9 || math.Abs(g.MarginMM[1]-19.70) > 1e-9 {
		t.Errorf("grid = %+v", g)
	}
	for _, tc := range []struct {
		paper, cell [2]float64
		cols, rows  int
		origin      [2]float64
	}{
		{[2]float64{210, 297}, [2]float64{50, 30}, 3, 9, [2]float64{30, 13.5}},
		{[2]float64{215.9, 279.4}, [2]float64{101.6, 152.4}, 2, 1, [2]float64{6.35, 63.5}},
		// Two 101.95 mm cells fill Letter's 203.9 mm clear width exactly: whole
		// hundredths keep a cell that just fits.
		{[2]float64{215.9, 279.4}, [2]float64{101.95, 100}, 2, 2, [2]float64{6, 39.7}},
		{[2]float64{215.9, 279.4}, [2]float64{50.8, 25.4}, 4, 10, [2]float64{6.35, 12.7}},
	} {
		g, err := uncutGrid(tc.paper, tc.cell)
		if err != nil {
			t.Fatal(err)
		}
		if g.Cols != tc.cols || g.Rows != tc.rows || math.Abs(g.MarginMM[0]-tc.origin[0]) > 1e-9 || math.Abs(g.MarginMM[1]-tc.origin[1]) > 1e-9 {
			t.Errorf("%v on %v: %+v", tc.cell, tc.paper, g)
		}
	}
	for _, s := range load(t, t.TempDir()).StockList() {
		if g := s.Sheet; g != nil && g.PitchMM == g.CellMM && (g.MarginMM[0] < UncutClearMM || g.MarginMM[1] < UncutClearMM) {
			t.Errorf("%s leaves %v mm, less than UncutClearMM", s.ID, g.MarginMM)
		}
	}
	if _, err := uncutGrid([2]float64{100, 100}, [2]float64{95, 10}); err == nil {
		t.Error("a cell wider than the clear area must fail")
	}
}

func TestBLENameMatching(t *testing.T) {
	c := load(t, t.TempDir())
	for name, want := range map[string]string{
		"M110-1A2B": "phomemo-m110", "CT320B_99": "clabel-ct320b", "D30-xyz": "phomemo-d30",
		"FM226_ABC": "munbyn-fm226", "Bose QC45": "", "Phomemo M220": "phomemo-m220",
		"CT-230B": "clabel-ct230b", "Phomemo_M110": "phomemo-m110", "XM110": "", "": "",
	} {
		if got := ids(c.MatchBLEName(name)); got != want {
			t.Errorf("%q -> %q, want %q", name, got, want)
		}
	}
}

func TestFitsByRule(t *testing.T) {
	c := load(t, t.TempDir())
	cases := []struct {
		model, stock string
		want         bool
	}{
		{"phomemo-m110", "50x30", true},
		{"phomemo-m110", "70x80", false}, // 70 mm roll > 50 mm range
		{"phomemo-m220", "70x80", true},
		{"phomemo-d30", "d30-15mm-tape", true},
		{"phomemo-d30", "50x30", false},
		{"clabel-ct230b", "50x30", true},
		{"cups", "avery-5160", true},
		{"ipp-sheet", "d30-15mm-tape", false}, // continuous media on a page printer
	}
	for _, tc := range cases {
		m, _ := c.Model(tc.model)
		s, _ := c.Stock(tc.stock)
		if got := c.Fits(m, s); got != tc.want {
			t.Errorf("Fits(%s, %s) = %v, want %v", tc.model, tc.stock, got, tc.want)
		}
	}
}

func TestUserFileAddsAPrinterWithoutCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "printers"), 0o755); err != nil {
		t.Fatal(err)
	}
	toml := `
[[model]]
id = "phomemo-m120"
label = "Phomemo M120"
family = "phomemo"
link = "ble"
dpi = 203.2
head_dots = 384
paper_width_mm = [20, 50]
[model.ble]
name_patterns = ["m120"]
service = "0000ff00-0000-1000-8000-00805f9b34fb"
write = { data = "0000ff02-0000-1000-8000-00805f9b34fb" }
notify = "0000ff03-0000-1000-8000-00805f9b34fb"
`
	if err := os.WriteFile(filepath.Join(dir, "printers", "mine.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	c := load(t, dir)
	m, err := c.Model("phomemo-m120")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(c.MatchBLEName("M120_ABC")); got != "phomemo-m120" {
		t.Errorf("match = %q", got)
	}
	// No stocks listed: it still takes every stock that physically fits.
	var got []string
	for _, s := range c.StocksFor(m) {
		got = append(got, s.ID)
	}
	if want := "50x80-two-track,50x30,40x30"; strings.Join(got, ",") != want {
		t.Errorf("StocksFor = %v, want %s", got, want)
	}
}

func TestUnknownStockReferenceIsRejected(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "printers"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "printers", "bad.toml"),
		[]byte("[[model]]\nid=\"x\"\nlabel=\"X\"\nfamily=\"cups\"\nlink=\"cups\"\ndpi=300\nstocks=[\"nope\"]\n"), 0o644)
	if _, err := LoadCatalog(dir); err == nil || !strings.Contains(err.Error(), "unknown stock") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultCanvas(t *testing.T) {
	c := load(t, t.TempDir())
	m, _ := c.Model("phomemo-m110")
	s, _ := c.Stock("50x80-two-track")
	if got := DefaultCanvas(m, s); got.Width != 640 || got.Height != 384 {
		t.Errorf("canvas = %+v, want 640x384", got)
	}
}
