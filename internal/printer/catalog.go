package printer

import (
	"embed"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed data/stocks.toml data/models/*.toml
var builtin embed.FS

// UncutClearMM is the least paper an uncut grid leaves clear on every side; the grid is
// centred, so a side may get more. Desktop printers cannot print the outer 3 to 5 mm of
// a sheet; 6 mm clears that with room for the paper sitting slightly askew. The trim
// marks internal/sheets draws in this margin need all of it, so do not lower it alone.
const UncutClearMM = 6.0

// uncutClear is UncutClearMM in hundredths of a millimetre.
const uncutClear = int(UncutClearMM * 100)

// ConfigDir is where user overrides live: $CRATE_LABELS_CONFIG or ~/.config/crate-labels.
func ConfigDir() string {
	if d := os.Getenv("CRATE_LABELS_CONFIG"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "crate-labels")
}

// Catalog is every known model and stock.
type Catalog struct {
	Models map[string]*Model
	Stocks map[string]*Stock
	// order keeps file order for stable listings.
	modelOrder, stockOrder []string
}

// LoadCatalog reads the built-in TOML and then userDir/stocks.toml and
// userDir/printers/*.toml. A user entry with an existing id replaces the built-in one.
// Pass "" for userDir to use ConfigDir().
func LoadCatalog(userDir string) (*Catalog, error) {
	if userDir == "" {
		userDir = ConfigDir()
	}
	c := &Catalog{Models: map[string]*Model{}, Stocks: map[string]*Stock{}}
	if err := c.loadFS(builtin, "data/stocks.toml"); err != nil {
		return nil, err
	}
	names, _ := fs.Glob(builtin, "data/models/*.toml")
	sort.Strings(names)
	for _, n := range names {
		if err := c.loadFS(builtin, n); err != nil {
			return nil, err
		}
	}
	if err := c.loadFileIfExists(filepath.Join(userDir, "stocks.toml")); err != nil {
		return nil, err
	}
	user, _ := filepath.Glob(filepath.Join(userDir, "printers", "*.toml"))
	sort.Strings(user)
	for _, p := range user {
		if err := c.loadFileIfExists(p); err != nil {
			return nil, err
		}
	}
	return c, c.validate()
}

func (c *Catalog) loadFS(fsys fs.FS, name string) error {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	return c.loadBytes(data, "builtin:"+name)
}

func (c *Catalog) loadFileIfExists(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.loadBytes(data, path)
}

type rawFile struct {
	Stock []rawStock `toml:"stock"`
	Model []rawModel `toml:"model"`
}

type rawStock struct {
	ID       string         `toml:"id"`
	Name     string         `toml:"name"`
	Media    string         `toml:"media"`
	DesignMM []float64      `toml:"design_mm"`
	FeedMM   []float64      `toml:"feed_mm"`
	RotateCW int            `toml:"rotate_cw"`
	Sheet    *rawSheet      `toml:"sheet"`
	Round    bool           `toml:"round"`
	Extra    map[string]any `toml:"extra"`
}

type rawSheet struct {
	PaperMM  []float64 `toml:"paper_mm"`
	Grid     string    `toml:"grid"`
	CellMM   []float64 `toml:"cell_mm"`
	Rows     int       `toml:"rows"`
	Cols     int       `toml:"cols"`
	MarginMM []float64 `toml:"margin_mm"`
	PitchMM  []float64 `toml:"pitch_mm"`
}

type rawModel struct {
	ID           string         `toml:"id"`
	Label        string         `toml:"label"`
	Family       string         `toml:"family"`
	Link         string         `toml:"link"`
	DPI          float64        `toml:"dpi"`
	HeadDots     int            `toml:"head_dots"`
	PaperWidthMM []float64      `toml:"paper_width_mm"`
	Stocks       []string       `toml:"stocks"`
	Options      map[string]any `toml:"options"`
	BLE          *struct {
		NamePatterns []string          `toml:"name_patterns"`
		Service      string            `toml:"service"`
		Write        map[string]string `toml:"write"`
		Notify       string            `toml:"notify"`
		ChunkBytes   int               `toml:"chunk_bytes"`
		ChunkGapMS   *float64          `toml:"chunk_gap_ms"`
	} `toml:"ble"`
}

func pair(v []float64, what string) ([2]float64, error) {
	if len(v) != 2 {
		return [2]float64{}, fmt.Errorf("%s: expected [a, b], got %v", what, v)
	}
	return [2]float64{v[0], v[1]}, nil
}

func (c *Catalog) loadBytes(data []byte, source string) error {
	var f rawFile
	if _, err := toml.Decode(string(data), &f); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	for _, r := range f.Stock {
		s, err := buildStock(r)
		if err != nil {
			return fmt.Errorf("%s: stock %q: %w", source, r.ID, err)
		}
		if _, seen := c.Stocks[s.ID]; !seen {
			c.stockOrder = append(c.stockOrder, s.ID)
		}
		c.Stocks[s.ID] = s
	}
	for _, r := range f.Model {
		m, err := buildModel(r, source)
		if err != nil {
			return fmt.Errorf("%s: model %q: %w", source, r.ID, err)
		}
		if _, seen := c.Models[m.ID]; !seen {
			c.modelOrder = append(c.modelOrder, m.ID)
		}
		c.Models[m.ID] = m
	}
	return nil
}

func buildStock(r rawStock) (*Stock, error) {
	if r.ID == "" || r.Name == "" {
		return nil, fmt.Errorf("id and name are required")
	}
	design, err := pair(r.DesignMM, "design_mm")
	if err != nil {
		return nil, err
	}
	var feed [2]float64
	switch len(r.FeedMM) {
	case 1:
		feed = [2]float64{r.FeedMM[0], 0}
	case 2:
		feed = [2]float64{r.FeedMM[0], r.FeedMM[1]}
	default:
		return nil, fmt.Errorf("feed_mm: expected [width] or [width, length]")
	}
	media := MediaKind(r.Media)
	if !slices.Contains([]MediaKind{MediaDieCut, MediaContinuous, MediaSheet}, media) {
		return nil, fmt.Errorf("media %q: want die_cut, continuous or sheet", r.Media)
	}
	if !slices.Contains([]int{0, 90, 180, 270}, r.RotateCW) {
		return nil, fmt.Errorf("rotate_cw %d: want 0, 90, 180 or 270", r.RotateCW)
	}
	s := &Stock{ID: r.ID, Name: r.Name, Media: media, DesignMM: design, FeedMM: feed,
		RotateCW: r.RotateCW, Round: r.Round, Extra: r.Extra}
	if r.Sheet != nil {
		g, err := buildSheet(*r.Sheet)
		if err != nil {
			return nil, err
		}
		s.Sheet = g
	}
	return s, nil
}

func buildSheet(r rawSheet) (*SheetGrid, error) {
	paper, err := pair(r.PaperMM, "sheet.paper_mm")
	if err != nil {
		return nil, err
	}
	cell, err := pair(r.CellMM, "sheet.cell_mm")
	if err != nil {
		return nil, err
	}
	if r.Grid == "uncut" {
		return uncutGrid(paper, cell)
	}
	margin, err := pair(r.MarginMM, "sheet.margin_mm")
	if err != nil {
		return nil, err
	}
	pitch, err := pair(r.PitchMM, "sheet.pitch_mm")
	if err != nil {
		return nil, err
	}
	if r.Rows < 1 || r.Cols < 1 {
		return nil, fmt.Errorf("sheet rows and cols must be positive")
	}
	return &SheetGrid{PaperMM: paper, Rows: r.Rows, Cols: r.Cols, MarginMM: margin, PitchMM: pitch, CellMM: cell}, nil
}

// uncutGrid packs as many abutting cells as fit inside the clear border and centres the
// block on the paper. The arithmetic is in whole hundredths of a millimetre, so a
// cell that fits exactly is never lost to rounding.
func uncutGrid(paper, cell [2]float64) (*SheetGrid, error) {
	var n, origin [2]int
	for i := range 2 {
		p, c := hundredths(paper[i]), hundredths(cell[i])
		if c <= 0 {
			return nil, fmt.Errorf("sheet.cell_mm must be positive, got %v", cell)
		}
		n[i] = (p - 2*uncutClear) / c
		origin[i] = p - n[i]*c // twice the margin, halved below
	}
	if n[0] < 1 || n[1] < 1 {
		return nil, fmt.Errorf("a %gx%g mm cell does not fit on the paper", cell[0], cell[1])
	}
	margin := [2]float64{float64(origin[0]) / 200, float64(origin[1]) / 200}
	return &SheetGrid{PaperMM: paper, Rows: n[1], Cols: n[0], MarginMM: margin, PitchMM: cell, CellMM: cell}, nil
}

// hundredths is mm in whole hundredths of a millimetre.
func hundredths(mm float64) int { return int(math.Round(mm * 100)) }

func buildModel(r rawModel, source string) (*Model, error) {
	if r.ID == "" || r.Label == "" || r.Family == "" || r.DPI <= 0 {
		return nil, fmt.Errorf("id, label, family and dpi are required")
	}
	link := LinkKind(r.Link)
	if !slices.Contains([]LinkKind{LinkBLE, LinkTCP, LinkIPP, LinkCUPS, LinkFile}, link) {
		return nil, fmt.Errorf("link %q: want ble, tcp, ipp, cups or file", r.Link)
	}
	m := &Model{ID: r.ID, Label: r.Label, Family: r.Family, Link: link, DPI: r.DPI,
		HeadDots: r.HeadDots, Stocks: r.Stocks, Options: Settings(r.Options), Source: source}
	if m.Options == nil {
		m.Options = Settings{}
	}
	if r.PaperWidthMM != nil {
		pw, err := pair(r.PaperWidthMM, "paper_width_mm")
		if err != nil {
			return nil, err
		}
		m.PaperWidthMM = &pw
	}
	if b := r.BLE; b != nil {
		p := &BLEProfile{NamePatterns: b.NamePatterns, Service: strings.ToLower(b.Service),
			Write: map[string]string{}, Notify: strings.ToLower(b.Notify), ChunkBytes: b.ChunkBytes, ChunkGapMS: 20}
		for role, uuid := range b.Write {
			p.Write[role] = strings.ToLower(uuid)
		}
		if p.ChunkBytes == 0 {
			p.ChunkBytes = 128
		}
		if b.ChunkGapMS != nil {
			p.ChunkGapMS = *b.ChunkGapMS
		}
		for _, pat := range p.NamePatterns {
			if _, err := regexp.Compile("(?i)" + pat); err != nil {
				return nil, fmt.Errorf("ble.name_patterns: %w", err)
			}
		}
		m.BLE = p
	}
	if link == LinkBLE && m.BLE == nil {
		return nil, fmt.Errorf("link is ble but there is no [model.ble] table")
	}
	return m, nil
}

func (c *Catalog) validate() error {
	for _, id := range c.modelOrder {
		m := c.Models[id]
		for _, s := range m.Stocks {
			if _, ok := c.Stocks[s]; !ok {
				return fmt.Errorf("%s: model %q lists unknown stock %q", m.Source, m.ID, s)
			}
		}
	}
	return nil
}

// Model looks up a model by id.
func (c *Catalog) Model(id string) (*Model, error) {
	if m, ok := c.Models[id]; ok {
		return m, nil
	}
	return nil, Errorf("unknown printer model %q", id)
}

// Stock looks up a stock by id.
func (c *Catalog) Stock(id string) (*Stock, error) {
	if s, ok := c.Stocks[id]; ok {
		return s, nil
	}
	return nil, Errorf("unknown label stock %q", id)
}

// ModelList returns models in file order.
func (c *Catalog) ModelList() []*Model {
	out := make([]*Model, 0, len(c.modelOrder))
	for _, id := range c.modelOrder {
		out = append(out, c.Models[id])
	}
	return out
}

// StockList returns stocks in file order (built-ins first, then user stocks).
func (c *Catalog) StockList() []*Stock {
	out := make([]*Stock, 0, len(c.stockOrder))
	for _, id := range c.stockOrder {
		out = append(out, c.Stocks[id])
	}
	return out
}

// Fits reports whether m can print s.
//
// A stock listed in m.Stocks always fits. Otherwise a thermal printer takes a roll it
// can load (the roll width is inside its paper range) whose printable width, in dots,
// is no wider than its head. Page printers (HeadDots == 0) take every sheet and
// die-cut stock, one label per page.
func (c *Catalog) Fits(m *Model, s *Stock) bool {
	if slices.Contains(m.Stocks, s.ID) {
		return true
	}
	if m.HeadDots == 0 {
		return s.Media != MediaContinuous
	}
	if s.Media == MediaSheet || m.PaperWidthMM == nil {
		return false
	}
	roll := s.FeedMM[0]
	printable := s.DesignMM[0]
	if s.RotateCW == 90 || s.RotateCW == 270 {
		printable = s.DesignMM[1]
	}
	loads := m.PaperWidthMM[0] <= roll && roll <= m.PaperWidthMM[1]
	return loads && int(math.Round(printable*m.DotsPerMM())) <= m.HeadDots
}

// StocksFor lists the stocks m takes: its listed ones first, then others that fit by rule.
func (c *Catalog) StocksFor(m *Model) []*Stock {
	var out []*Stock
	for _, id := range m.Stocks {
		out = append(out, c.Stocks[id])
	}
	for _, id := range c.stockOrder {
		s := c.Stocks[id]
		if !slices.Contains(m.Stocks, id) && c.Fits(m, s) {
			out = append(out, s)
		}
	}
	return out
}

// MatchBLEName returns, in file order, the models whose advertised-name patterns match
// name.
func (c *Catalog) MatchBLEName(name string) []*Model {
	var out []*Model
	for _, m := range c.ModelList() {
		if m.BLE != nil && m.BLE.MatchesName(name) {
			out = append(out, m)
		}
	}
	return out
}
