package ble

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
)

// ProbeResult is what one connection to a device revealed.
type ProbeResult struct {
	Services  []string // every service UUID the device offers, lower-case
	Handshake []byte   // the first notification after subscribing (the hello), or nil
	Family    string   // the printer family its services point to, or ""
	ModelID   string   // the identified catalog model, or ""
}

// MarshalJSON writes the docs/API.md shape: handshake as hex, empty fields as null.
func (p ProbeResult) MarshalJSON() ([]byte, error) {
	var hs *string
	if len(p.Handshake) > 0 {
		s := hex.EncodeToString(p.Handshake)
		hs = &s
	}
	return json.Marshal(struct {
		Services  []string `json:"services"`
		Handshake *string  `json:"handshake"`
		Family    *string  `json:"family"`
		ModelID   *string  `json:"model_id"`
	}{nonNil(p.Services), hs, optional(p.Family), optional(p.ModelID)})
}

// Hint is what the caller already knows about the device it probes.
type Hint struct {
	Name       string // the advertised name from the scan; "" uses the OS name
	KnownModel string // the model id saved for this address, if any
}

// ModelResolver picks a model from a family's first notification (its hello), the
// advertised name and the saved model. models are the catalog's models of that family
// in file order. It returns nil when it cannot tell.
type ModelResolver func(hello []byte, name string, known *printer.Model, models []*printer.Model) *printer.Model

var (
	resolverMu sync.RWMutex
	resolvers  = map[string]ModelResolver{}
)

// RegisterModelResolver makes Probe capture the hello for family and identify the
// model with r. Drivers call it from init().
func RegisterModelResolver(family string, r ModelResolver) {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	resolvers[family] = r
}

func resolverFor(family string) ModelResolver {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return resolvers[family]
}

// Probe connects once, lists the services, picks the family they point to and, if it
// can, the model (see [Classify] for how services and the name are weighed). Nothing
// is written to the device except the notification subscription.
func Probe(ctx context.Context, address string, cat *printer.Catalog) (ProbeResult, error) {
	return ProbeHint(ctx, address, cat, Hint{})
}

// ProbeHint is [Probe] with the advertised name and saved model, when known.
func ProbeHint(ctx context.Context, address string, cat *printer.Catalog, h Hint) (ProbeResult, error) {
	in, err := currentBackend()
	if err != nil {
		return ProbeResult{}, err
	}
	p, err := connect(ctx, in, address)
	if err != nil {
		return ProbeResult{}, err
	}
	defer func() { _ = p.Disconnect() }()
	return probe(ctx, p, cat, h)
}

// probe identifies a connected device. Unlike a scan it sees every service the device
// has, so a device without a catalog service is never matched by name alone. A family
// with a registered resolver decides the model itself. For the others the saved
// model wins if the device offers its service, then a model whose name matches, then
// the only model on the offered service.
func probe(ctx context.Context, p Peripheral, cat *printer.Catalog, h Hint) (ProbeResult, error) {
	raw, err := p.Services(ctx)
	if err != nil {
		return ProbeResult{}, printer.Errorf("Could not list the device's Bluetooth services: %v", err)
	}
	res := ProbeResult{Services: make([]string, 0, len(raw))}
	for _, s := range raw {
		res.Services = append(res.Services, normalize(s))
	}
	name := h.Name
	if name == "" {
		name = p.Name()
	}
	e := recognise(res.Services, name, cat)
	if e.family == "" {
		return res, nil
	}
	res.Family = e.family
	known := cat.Models[h.KnownModel]
	if resolve := resolverFor(e.family); resolve != nil {
		res.Handshake = captureHello(ctx, p, e.offering[0].BLE)
		var family []*printer.Model
		for _, m := range cat.ModelList() {
			if m.Family == e.family {
				family = append(family, m)
			}
		}
		if m := resolve(res.Handshake, name, known, family); m != nil {
			res.ModelID = m.ID
		}
		return res, nil
	}
	switch {
	case known != nil && slices.Contains(e.offering, known):
		res.ModelID = known.ID
	case len(e.named) > 0:
		res.ModelID = e.named[0].ID
	case len(e.offering) == 1:
		res.ModelID = e.offering[0].ID
	}
	return res, nil
}

// captureHello subscribes to the profile's notify characteristic and returns the first
// non-empty frame within [HelloWait].
func captureHello(ctx context.Context, p Peripheral, profile *printer.BLEProfile) []byte {
	if profile == nil || profile.Notify == "" {
		return nil
	}
	chars, err := p.Characteristics(ctx, normalize(profile.Service))
	if err != nil {
		slog.Warn("ble probe: could not list characteristics", "service", short(profile.Service), "err", err)
		return nil
	}
	var notify Characteristic
	for _, c := range chars {
		if normalize(c.UUID()) == normalize(profile.Notify) {
			notify = c
		}
	}
	if notify == nil {
		return nil
	}
	first := make(chan []byte, 1)
	err = notify.Subscribe(func(frame []byte) {
		if len(frame) == 0 {
			return
		}
		select {
		case first <- slices.Clone(frame):
		default:
		}
	})
	if err != nil {
		slog.Warn("ble probe: could not subscribe", "uuid", short(profile.Notify), "err", err)
		return nil
	}
	defer func() { _ = notify.Subscribe(nil) }()
	timer := time.NewTimer(HelloWait)
	defer timer.Stop()
	select {
	case f := <-first:
		return f
	case <-timer.C:
	case <-ctx.Done():
	case <-p.Done():
	}
	return nil
}
