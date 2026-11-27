package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/service"
)

func (s *Server) printers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.svc.Store.Printers(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]printerView, 0, len(list))
	for _, t := range list {
		out = append(out, s.printerJSON(t))
	}
	var def *string
	if d := s.svc.Store.SettingString(ctx, "default_printer"); d != "" {
		def = &d
	}
	writeJSON(w, http.StatusOK, map[string]any{"printers": out, "default": def, "calibration_keys": service.CalibrationKeys()})
}

func checkName(name string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 1 || n > 80 {
		return invalid("name: 1 to 80 characters")
	}
	return nil
}

func (s *Server) addPrinter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     *string        `json:"name"`
		ModelID  *string        `json:"model_id"`
		Address  string         `json:"address"`
		Settings map[string]any `json:"settings"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Name == nil || body.ModelID == nil {
		s.fail(w, r, invalid("name and model_id: required"))
		return
	}
	if err := checkName(*body.Name); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(body.Address) > 500 {
		s.fail(w, r, invalid("address: at most 500 characters"))
		return
	}
	if _, err := s.svc.Catalog.Model(*body.ModelID); err != nil {
		s.fail(w, r, err)
		return
	}
	settings, err := service.CleanCalibration(body.Settings)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	t := printer.Target{Name: *body.Name, ModelID: *body.ModelID, Address: strings.TrimSpace(body.Address), Settings: settings}
	if err := s.svc.Store.SavePrinter(ctx, &t); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.svc.Store.SettingString(ctx, "default_printer") == "" {
		if err := s.svc.Store.SetSetting(ctx, "default_printer", t.ID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.printerJSON(t))
}

func (s *Server) patchPrinter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     *string        `json:"name"`
		ModelID  *string        `json:"model_id"`
		Address  *string        `json:"address"`
		Settings map[string]any `json:"settings"`
	}
	if err := decode(r, &body, true); err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	t, err := s.svc.Store.Printer(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Name != nil {
		if err := checkName(*body.Name); err != nil {
			s.fail(w, r, err)
			return
		}
		t.Name = *body.Name
	}
	if body.ModelID != nil {
		if _, err := s.svc.Catalog.Model(*body.ModelID); err != nil {
			s.fail(w, r, err)
			return
		}
		t.ModelID = *body.ModelID
	}
	if body.Address != nil {
		if len(*body.Address) > 500 {
			s.fail(w, r, invalid("address: at most 500 characters"))
			return
		}
		t.Address = strings.TrimSpace(*body.Address)
	}
	if body.Settings != nil {
		merged := map[string]any{}
		for k, v := range t.Settings {
			merged[k] = v
		}
		for k, v := range body.Settings {
			if v == nil {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		clean, err := service.CleanCalibration(merged)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		t.Settings = clean
	}
	if err := s.svc.Store.SavePrinter(ctx, &t); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.printerJSON(t))
}

func (s *Server) deletePrinter(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.svc.Store.DeletePrinter(ctx, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.svc.Store.SettingString(ctx, "default_printer") == id {
		if err := s.svc.Store.SetSetting(ctx, "default_printer", nil); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (s *Server) testPrint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stock    *string        `json:"stock"`
		Template string         `json:"template"`
		Options  map[string]any `json:"options"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Stock == nil {
		s.fail(w, r, invalid("stock: required"))
		return
	}
	j, err := s.svc.TestPrint(r.Context(), r.PathValue("id"), body.Template, *body.Stock, body.Options)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(j))
}

func unavailable(what, why string) error {
	return &apiError{status: http.StatusNotImplemented, msg: what + " is not available in this build yet (" + why + ")."}
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func (s *Server) bleScan(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	timeout, showAll := q.number("timeout", 6, 0, 30), q.boolean("show_all", false)
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	if s.opts.Discovery.BLEScan == nil {
		s.fail(w, r, unavailable("Bluetooth scanning", "no BLE transport"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), seconds(timeout)+10*time.Second)
	defer cancel()
	devices, err := s.opts.Discovery.BLEScan(ctx, seconds(timeout), showAll)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if devices == nil {
		devices = []BLEDevice{}
	}
	for i := range devices {
		if devices[i].ServiceUUIDs == nil {
			devices[i].ServiceUUIDs = []string{}
		}
		if devices[i].ModelIDs == nil {
			devices[i].ModelIDs = []string{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) bleProbe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address *string  `json:"address"`
		Timeout *float64 `json:"timeout"`
	}
	if err := decode(r, &body, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Address == nil || strings.TrimSpace(*body.Address) == "" {
		s.fail(w, r, invalid("address: required"))
		return
	}
	timeout := 15.0
	if body.Timeout != nil {
		if *body.Timeout <= 0 || *body.Timeout > 60 {
			s.fail(w, r, invalid("timeout: must be more than 0 and at most 60"))
			return
		}
		timeout = *body.Timeout
	}
	if s.opts.Discovery.BLEProbe == nil {
		s.fail(w, r, unavailable("Bluetooth probing", "no BLE transport"))
		return
	}
	// Connecting to a printer mid-job would break the job's link.
	if j, busy, err := s.svc.DeviceBusy(r.Context(), printer.LinkBLE, *body.Address); err != nil {
		s.fail(w, r, err)
		return
	} else if busy {
		s.fail(w, r, &apiError{status: http.StatusConflict, msg: fmt.Sprintf(
			"%s is printing job %d. Wait for it to finish, then identify the printer.", j.PrinterName, j.ID)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), seconds(timeout))
	defer cancel()
	p, err := s.opts.Discovery.BLEProbe(ctx, strings.TrimSpace(*body.Address), seconds(timeout))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded && r.Context().Err() == nil {
			s.fail(w, r, printer.Errorf("The printer did not answer within %g s. Is it switched on and not connected to your phone?", timeout))
			return
		}
		s.fail(w, r, err)
		return
	}
	if p.Services == nil {
		p.Services = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"probe": p})
}

func (s *Server) ippDiscover(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	timeout := q.number("timeout", 5, 0, 30)
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	if s.opts.Discovery.IPPDiscover == nil {
		s.fail(w, r, unavailable("Wi-Fi printer discovery", "no IPP discovery"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), seconds(timeout)+10*time.Second)
	defer cancel()
	list, err := s.opts.Discovery.IPPDiscover(ctx, seconds(timeout))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if list == nil {
		list = []IPPPrinter{}
	}
	for i := range list {
		p := &list[i]
		for _, l := range []*[]string{&p.Formats, &p.URF, &p.Kind} {
			if *l == nil {
				*l = []string{}
			}
		}
		if p.TXT == nil {
			p.TXT = map[string]string{}
		}
		p.SuggestedModelID = SuggestIPPModel(p.Kind)
	}
	writeJSON(w, http.StatusOK, map[string]any{"printers": list})
}

func (s *Server) cupsQueues(w http.ResponseWriter, r *http.Request) {
	if s.opts.Discovery.CUPSQueues == nil {
		s.fail(w, r, unavailable("CUPS queues", "no CUPS driver"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := s.opts.Discovery.CUPSQueues(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if list == nil {
		list = []CUPSQueue{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queues": list})
}
