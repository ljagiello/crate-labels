package ipp

import (
	"context"
	"image"
	"os"
	"testing"

	proto "github.com/ljagiello/crate-labels/internal/ipp"
	"github.com/ljagiello/crate-labels/internal/printer"
)

// TestLiveValidate checks every stock of the IPP models against a real printer. It runs
// only when CRATE_LABELS_IPP_PRINTER holds the printer's ipp:// URI, and it sends only
// Get-Printer-Attributes and Validate-Job: dry_run makes the client refuse Print-Job.
//
//	CRATE_LABELS_IPP_PRINTER=ipp://printer.local:631/ipp/print go test -run TestLiveValidate -v ./internal/driver/ipp
func TestLiveValidate(t *testing.T) {
	uri := os.Getenv("CRATE_LABELS_IPP_PRINTER")
	if uri == "" {
		t.Skip("set CRATE_LABELS_IPP_PRINTER to an ipp:// URI to check against a real printer")
	}
	ep, err := proto.ParseEndpoint(uri)
	if err != nil {
		t.Fatal(err)
	}
	c := proto.NewClient(ep, false)
	defer c.CloseIdle()
	st, err := c.PrinterStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("printer-state %d, reasons %v, message %q, problem %q", st.State, st.Reasons, st.Message, st.Problem())
	cat := catalog(t, "")
	for _, modelID := range []string{"ipp-sheet", "ipp-label"} {
		m, err := cat.Model(modelID)
		if err != nil {
			t.Fatal(err)
		}
		for _, stockID := range m.Stocks {
			s, err := cat.Stock(stockID)
			if err != nil {
				t.Fatal(err)
			}
			cv := printer.DefaultCanvas(m, s)
			pc := &printer.PrintContext{
				Model: m, Stock: s,
				Target:   &printer.Target{ID: "live", Name: "live printer", ModelID: modelID, Address: uri},
				Settings: printer.Merge(m.Options, printer.Settings{"dry_run": true}),
			}
			res, err := (&Driver{}).Send(context.Background(), pc, []*image.Gray{filled(cv.Width, cv.Height, 0)})
			t.Logf("%s/%s: %s media=%v exact=%v unsupported=%v", modelID, stockID, res.Message, res.Details["media"], res.Details["exact"], res.Details["unsupported"])
			if err != nil {
				t.Errorf("%s/%s: %v", modelID, stockID, err)
			}
		}
	}
}
