package printer

import (
	"context"
	"fmt"
	"image"
	"slices"
	"sort"
	"strings"
	"sync"
)

// LinkOpener opens a byte link for a target. Transport packages register one per kind
// in init(), so this package never imports bluetooth or net code.
type LinkOpener func(ctx context.Context, t *Target, m *Model) (Link, error)

var (
	mu      sync.RWMutex
	drivers = map[string]Driver{}
	openers = map[LinkKind]LinkOpener{}
)

// Register makes a driver available under d.Family(). Call it from init().
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := drivers[d.Family()]; dup {
		panic(fmt.Sprintf("printer: driver family %q registered twice", d.Family()))
	}
	drivers[d.Family()] = d
}

// RegisterLinkOpener installs the opener for a byte link kind. Call it from init().
func RegisterLinkOpener(kind LinkKind, open LinkOpener) {
	mu.Lock()
	defer mu.Unlock()
	openers[kind] = open
}

// DriverFor returns the registered driver for a family.
func DriverFor(family string) (Driver, error) {
	mu.RLock()
	defer mu.RUnlock()
	if d, ok := drivers[family]; ok {
		return d, nil
	}
	return nil, Errorf("no driver for family %q (known: %s)", family, strings.Join(familiesLocked(), ", "))
}

// Families lists registered driver families.
func Families() []string {
	mu.RLock()
	defer mu.RUnlock()
	return familiesLocked()
}

func familiesLocked() []string {
	out := make([]string, 0, len(drivers))
	for f := range drivers {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func driverForModel(m *Model) (Driver, error) {
	d, err := DriverFor(m.Family)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(d.Links(), m.Link) {
		return nil, Errorf("%s: model %q uses link %q but the %q driver supports %v",
			m.Source, m.ID, m.Link, m.Family, d.Links())
	}
	return d, nil
}

// CanvasFor is the render size for a model and stock.
func (c *Catalog) CanvasFor(modelID, stockID string) (Canvas, error) {
	m, s, d, err := c.resolve(modelID, stockID)
	if err != nil {
		return Canvas{}, err
	}
	return d.Canvas(m, s), nil
}

// Preview returns exactly what the printer will produce for label.
func (c *Catalog) Preview(t *Target, stockID string, label *image.Gray, opts Settings) (*image.Gray, error) {
	m, s, d, err := c.resolve(t.ModelID, stockID)
	if err != nil {
		return nil, err
	}
	return d.Preview(m, s, label, Merge(m.Options, t.Settings, opts))
}

func (c *Catalog) resolve(modelID, stockID string) (*Model, *Stock, Driver, error) {
	m, err := c.Model(modelID)
	if err != nil {
		return nil, nil, nil, err
	}
	s, err := c.Stock(stockID)
	if err != nil {
		return nil, nil, nil, err
	}
	d, err := driverForModel(m)
	if err != nil {
		return nil, nil, nil, err
	}
	return m, s, d, nil
}

// OpenLink opens the byte link a target's model needs.
func OpenLink(ctx context.Context, t *Target, m *Model) (Link, error) {
	mu.RLock()
	open, ok := openers[m.Link]
	mu.RUnlock()
	if !ok {
		return nil, Errorf("no link opener registered for %q", m.Link)
	}
	return open(ctx, t, m)
}

// PrintOptions tunes one Print call.
type PrintOptions struct {
	Settings Settings // per-job overrides
	Progress Progress
	Link     Link // reuse an open link (or a fake in tests); otherwise one is opened and closed
}

// Print sends labels (rendered at CanvasFor) to target.
func (c *Catalog) Print(ctx context.Context, t *Target, stockID string, labels []*image.Gray, o PrintOptions) (Result, error) {
	m, s, d, err := c.resolve(t.ModelID, stockID)
	if err != nil {
		return Result{}, err
	}
	if !c.Fits(m, s) {
		var takes []string
		for _, st := range c.StocksFor(m) {
			takes = append(takes, st.ID)
		}
		return Result{}, Errorf("%s does not take stock %q. It takes: %s", m.Label, s.ID, strings.Join(takes, ", "))
	}
	want := d.Canvas(m, s)
	for i, l := range labels {
		if b := l.Bounds(); b.Dx() != want.Width || b.Dy() != want.Height {
			return Result{}, Errorf("label %d is %dx%d px; %s on %s needs %dx%d",
				i, b.Dx(), b.Dy(), m.Label, s.Name, want.Width, want.Height)
		}
	}
	pc := &PrintContext{Catalog: c, Model: m, Stock: s, Target: t, Settings: Merge(m.Options, t.Settings, o.Settings), Progress: o.Progress}
	if m.Link.IsByteLink() {
		pc.Link = o.Link
		if pc.Link == nil {
			l, err := OpenLink(ctx, t, m)
			if err != nil {
				return Result{}, err
			}
			defer l.Close()
			pc.Link = l
		}
	}
	return d.Send(ctx, pc, labels)
}
