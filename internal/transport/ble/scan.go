package ble

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// Discovered is one nearby device from a scan.
type Discovered struct {
	Address      string   // CoreBluetooth peripheral UUID on macOS
	Name         string   // the last non-empty advertised name
	RSSI         int      // dBm of the newest advertisement
	ServiceUUIDs []string // every service UUID seen in its adverts
	ModelIDs     []string // catalog models the device matches (see Classify)
	// MaybeFamily is the printer family of an advertised catalog service when no
	// model matched by name, else "".
	MaybeFamily string
}

// MarshalJSON writes the docs/API.md shape: maybe_family is null when empty.
func (d Discovered) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Address      string   `json:"address"`
		Name         string   `json:"name"`
		RSSI         int      `json:"rssi"`
		ServiceUUIDs []string `json:"service_uuids"`
		ModelIDs     []string `json:"model_ids"`
		MaybeFamily  *string  `json:"maybe_family"`
	}{d.Address, d.Name, d.RSSI, nonNil(d.ServiceUUIDs), nonNil(d.ModelIDs), optional(d.MaybeFamily)})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Scan listens for adverts for timeout and returns the devices that look like label
// printers (see [Classify]), strongest signal first. It asks the backend for every
// advert, not only those listing a printer service, because many printers leave the
// service list out of their adverts and are then known only by name.
func Scan(ctx context.Context, timeout time.Duration, cat *printer.Catalog) ([]Discovered, error) {
	return scan(ctx, timeout, cat, false)
}

// ScanAll is [Scan] without the printer filter: every device that advertised.
func ScanAll(ctx context.Context, timeout time.Duration, cat *printer.Catalog) ([]Discovered, error) {
	return scan(ctx, timeout, cat, true)
}

func scan(ctx context.Context, timeout time.Duration, cat *printer.Catalog, all bool) ([]Discovered, error) {
	in, err := currentBackend()
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var mu sync.Mutex
	seen := map[string]*Advertisement{}
	err = in.b.Scan(sctx, func(a Advertisement) {
		mu.Lock()
		defer mu.Unlock()
		merge(seen, a)
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, printer.Errorf("Bluetooth scan failed: %v. Is Bluetooth on, and does crate-labels have Bluetooth permission (System Settings > Privacy & Security > Bluetooth)?", err)
	}
	mu.Lock()
	defer mu.Unlock()
	out := make([]Discovered, 0, len(seen))
	for _, a := range seen {
		d := Classify(*a, cat)
		if all || len(d.ModelIDs) > 0 || d.MaybeFamily != "" {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(x, y Discovered) int {
		return cmp.Or(cmp.Compare(y.RSSI, x.RSSI), cmp.Compare(x.Name, y.Name), cmp.Compare(x.Address, y.Address))
	})
	return out, nil
}

// merge folds a new advert into the table: the newest RSSI wins, an empty name keeps the
// previous one, and service UUIDs accumulate. A scan response often repeats the
// address without the name or the services of the advert before it.
func merge(seen map[string]*Advertisement, a Advertisement) {
	if a.Address == "" {
		return
	}
	prev, ok := seen[a.Address]
	if !ok {
		prev = &Advertisement{Address: a.Address}
		seen[a.Address] = prev
	}
	prev.RSSI = a.RSSI
	if name := strings.TrimSpace(a.Name); name != "" {
		prev.Name = name
	}
	for _, u := range a.ServiceUUIDs {
		if u = normalize(u); !slices.Contains(prev.ServiceUUIDs, u) {
			prev.ServiceUUIDs = append(prev.ServiceUUIDs, u)
		}
	}
}

// Classify matches an advertisement to catalog models.
//
// The advertised service UUIDs come first: a device that lists a catalog model's
// service can only be one of the models using that service, and its name picks
// among them. Such a device whose name matches none of them still gets the service's
// family as MaybeFamily. Only a device that lists no catalog service is matched by
// name alone. A generic serial service (see [sharedServices]) counts only together
// with a matching name.
func Classify(a Advertisement, cat *printer.Catalog) Discovered {
	services := make([]string, 0, len(a.ServiceUUIDs))
	for _, u := range a.ServiceUUIDs {
		services = append(services, normalize(u))
	}
	d := Discovered{Address: a.Address, Name: a.Name, RSSI: a.RSSI, ServiceUUIDs: services, ModelIDs: []string{}}
	e := recognise(services, a.Name, cat)
	matched := e.named
	if len(e.offering) == 0 {
		matched = cat.MatchBLEName(a.Name)
	}
	for _, m := range matched {
		d.ModelIDs = append(d.ModelIDs, m.ID)
	}
	if len(matched) == 0 {
		d.MaybeFamily = e.family
	}
	return d
}

// sharedServices are GATT services that many unrelated devices offer, so offering one
// says nothing on its own. The Microchip (formerly ISSC) transparent UART service is
// the default of common BLE serial modules; Clabel printers use it.
var sharedServices = []string{ClabelService}

// evidence is what the catalog makes of a device's services and name.
type evidence struct {
	family   string           // the family the services point to, or ""
	offering []*printer.Model // that family's models whose service the device offers
	named    []*printer.Model // those of them whose name pattern also matches
}

// recognise weighs a device's service UUIDs (normalised) and name against the
// catalog. A model counts when the device offers its service; a model on a shared
// service counts only when its name pattern matches too. When the counted models
// span several families, the first model whose name also matches picks the family,
// else the first counted model does, in catalog order.
func recognise(services []string, name string, cat *printer.Catalog) evidence {
	var offering, named []*printer.Model
	for _, m := range cat.ModelList() {
		if m.BLE == nil || !slices.Contains(services, normalize(m.BLE.Service)) {
			continue
		}
		byName := m.BLE.MatchesName(name)
		if slices.Contains(sharedServices, normalize(m.BLE.Service)) && !byName {
			continue
		}
		offering = append(offering, m)
		if byName {
			named = append(named, m)
		}
	}
	if len(offering) == 0 {
		return evidence{}
	}
	lead := offering[0]
	if len(named) > 0 {
		lead = named[0]
	}
	other := func(m *printer.Model) bool { return m.Family != lead.Family }
	return evidence{
		family:   lead.Family,
		offering: slices.DeleteFunc(offering, other),
		named:    slices.DeleteFunc(named, other),
	}
}
