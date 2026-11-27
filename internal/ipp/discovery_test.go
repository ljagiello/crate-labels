package ipp

import (
	"encoding/json"
	"net"
	"slices"
	"testing"

	"github.com/grandcat/zeroconf"
)

// The Brother's TXT record as miekg/dns presents it.
var brotherTXT = []string{
	"txtvers=1",
	"ty=Brother HL-L3290CDW series",
	"pdl=application/octet-stream,image/urf,image/jpeg,image/pwg-raster",
	"rp=ipp/print",
	"URF=SRGB24,W8,CP1,IS4-1,MT1-3-4-5-8-11,OB10,PQ4,RS600,V1.4,DM1",
	"UUID=e3248000-80ce-11db-8000-0123456789ab",
	"kind=document,envelope,label,postcard",
	"PaperMax=legal-A4",
	"Color",
}

func TestTXTParsingLowercasesKeys(t *testing.T) {
	txt := ParseTXT(brotherTXT)
	if txt["urf"][:6] != "SRGB24" || txt["uuid"][:4] != "e324" || txt["color"] != "" {
		t.Fatalf("txt %v", txt)
	}
	info := NewPrinterInfo("Brother HL-L3290CDW series", []string{"fe80::200:5eff:fe00:5301", "198.51.100.9"}, 631, txt, "BRW0123456789AB.local.")
	if info.Host != "198.51.100.9" || info.URI() != brotherURI {
		t.Fatalf("host %s uri %s", info.Host, info.URI())
	}
	if info.HostnameURI() != "ipp://brw0123456789ab.local:631/ipp/print" || info.Resource != "/ipp/print" {
		t.Fatalf("hostname uri %s", info.HostnameURI())
	}
	if info.UUID != "e3248000-80ce-11db-8000-0123456789ab" || info.Model != "Brother HL-L3290CDW series" {
		t.Fatalf("uuid %s model %s", info.UUID, info.Model)
	}
	if !slices.Contains(info.URF, "W8") || !slices.Contains(info.Formats, MimeURF) ||
		!slices.Equal(info.Kind, []string{"document", "envelope", "label", "postcard"}) {
		t.Fatalf("lists %v %v %v", info.URF, info.Formats, info.Kind)
	}
	if !info.Printable() || !slices.Equal(sendable(info.Formats, info.URF), []string{MimeURF, MimeJPEG}) {
		t.Fatal("filter")
	}
}

func TestTXTDefaultsAndFilter(t *testing.T) {
	info := NewPrinterInfo("P", nil, 0, map[string]string{"rp": " /", "uuid": "URN:UUID:ABC"}, "p.local.")
	if info.Host != "p.local" || info.Port != 631 || info.Resource != "/ipp/print" || info.UUID != "abc" {
		t.Fatalf("%+v", info)
	}
	if !info.Printable() {
		t.Fatal("no pdl: the driver asks over IPP")
	}
	for pdl, want := range map[string]bool{
		"application/pdf":            true,
		"image/jpeg":                 true,
		"image/pwg-raster":           false,
		"image/urf":                  false, // URF, but the TXT lists no W8
		"image/urf,image/pwg-raster": false,
	} {
		if got := NewPrinterInfo("Q", []string{"10.0.0.2"}, 631, map[string]string{"pdl": pdl, "urf": "SRGB24,RS300"}, "").Printable(); got != want {
			t.Errorf("pdl %s: printable %v", pdl, got)
		}
	}
	if !NewPrinterInfo("R", nil, 631, map[string]string{"pdl": "image/urf", "urf": "W8,RS300"}, "").Printable() {
		t.Fatal("grey URF")
	}
	for rp, want := range map[string]string{"ipp/print": "/ipp/print", "/ipp/label/": "/ipp/label", "": "/ipp/print", "printers/../q": "/q"} {
		if got := NewPrinterInfo("S", nil, 631, map[string]string{"rp": rp}, "").Resource; got != want {
			t.Errorf("rp %q: %s", rp, got)
		}
	}
	if got := NewPrinterInfo("S", []string{"fe80::1"}, 631, nil, "").Host; got != "S" {
		t.Fatalf("link-local only falls back to the name, got %s", got)
	}
	if got := NewPrinterInfo("S", []string{"fe80::1", "2001:db8::5"}, 631, nil, "").URI(); got != "ipp://[2001:db8::5]:631/ipp/print" {
		t.Fatalf("routable v6: %s", got)
	}
}

func TestEntryUnescapesDNSNames(t *testing.T) {
	e := zeroconf.NewServiceEntry(`Brother\ HL-L3290CDW\ series`, ServiceType, "local.")
	e.HostName = "BRW0123456789AB.local."
	e.Port = 631
	e.Text = append(slices.Clone(brotherTXT), `note=caf\195\169 \"x\"`)
	e.AddrIPv4 = []net.IP{net.ParseIP("198.51.100.9")}
	info := infoFromEntry(e)
	if info.Name != "Brother HL-L3290CDW series" || info.URI() != brotherURI {
		t.Fatalf("name %q uri %s", info.Name, info.URI())
	}
	if info.TXT["note"] != `café "x"` {
		t.Fatalf("note %q", info.TXT["note"])
	}
}

func TestPrinterInfoJSONMatchesTheAPI(t *testing.T) {
	info := NewPrinterInfo("Brother HL-L3290CDW series", []string{"198.51.100.9"}, 631, ParseTXT(brotherTXT), "BRW0123456789AB.local.")
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["uri"] != brotherURI || got["hostname_uri"] != "ipp://brw0123456789ab.local:631/ipp/print" || got["printable"] != true ||
		got["port"] != 631.0 || got["model"] != "Brother HL-L3290CDW series" || got["txt"].(map[string]any)["urf"] == nil {
		t.Fatalf("%s", b)
	}
	b, _ = json.Marshal(NewPrinterInfo("P", []string{"10.0.0.2"}, 631, map[string]string{}, ""))
	want := `{"name":"P","host":"10.0.0.2","port":631,"resource":"/ipp/print","uuid":null,"model":null,"formats":[],"urf":[],"kind":[],"server":null,"txt":{},"uri":"ipp://10.0.0.2:631/ipp/print","hostname_uri":null,"printable":true}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}
