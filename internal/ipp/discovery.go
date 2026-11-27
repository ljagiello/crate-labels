// DNS-SD discovery of IPP printers: _ipp._tcp services (RFC 6763) and the TXT keys
// IPP Everywhere (PWG 5100.14) and AirPrint printers publish: rp (resource path), ty
// (make and model), pdl (document formats), URF (raster capabilities), UUID and kind.

package ipp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// ServiceType is the DNS-SD service type browsed.
const ServiceType = "_ipp._tcp"

// PrinterInfo is one printer seen on the network. It marshals to the docs/API.md
// discovery shape, including the derived uri, hostname_uri and printable.
type PrinterInfo struct {
	Name     string            `json:"name"`     // service instance name, e.g. "Brother HL-L3290CDW series"
	Host     string            `json:"host"`     // an IP literal when one resolved (IPv4 preferred), else the mDNS host
	Port     int               `json:"port"`     // SRV port
	Resource string            `json:"resource"` // TXT rp as an absolute path
	UUID     string            `json:"uuid"`     // TXT UUID, lower-case
	Model    string            `json:"model"`    // TXT ty
	Formats  []string          `json:"formats"`  // TXT pdl
	URF      []string          `json:"urf"`      // TXT URF
	Kind     []string          `json:"kind"`     // TXT kind
	Server   string            `json:"server"`   // mDNS host name, e.g. "BRW0123456789AB.local."
	TXT      map[string]string `json:"txt"`
}

// MarshalJSON writes the API shape: empty uuid, model, server and hostname_uri are null,
// empty lists are [] and txt is {}.
func (p PrinterInfo) MarshalJSON() ([]byte, error) {
	orNil := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	list := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	txt := p.TXT
	if txt == nil {
		txt = map[string]string{}
	}
	return json.Marshal(struct {
		Name        string            `json:"name"`
		Host        string            `json:"host"`
		Port        int               `json:"port"`
		Resource    string            `json:"resource"`
		UUID        *string           `json:"uuid"`
		Model       *string           `json:"model"`
		Formats     []string          `json:"formats"`
		URF         []string          `json:"urf"`
		Kind        []string          `json:"kind"`
		Server      *string           `json:"server"`
		TXT         map[string]string `json:"txt"`
		URI         string            `json:"uri"`
		HostnameURI *string           `json:"hostname_uri"`
		Printable   bool              `json:"printable"`
	}{p.Name, p.Host, p.Port, p.Resource, orNil(p.UUID), orNil(p.Model), list(p.Formats), list(p.URF), list(p.Kind),
		orNil(p.Server), txt, p.URI(), orNil(p.HostnameURI()), p.Printable()})
}

// Endpoint is the printer's IPP endpoint.
func (p PrinterInfo) Endpoint() Endpoint {
	return Endpoint{Host: strings.ToLower(p.Host), Port: p.Port, Resource: p.Resource}
}

// URI is the printer-uri, e.g. ipp://198.51.100.9:631/ipp/print.
func (p PrinterInfo) URI() string { return p.Endpoint().PrinterURI() }

// HostnameURI addresses the printer by its mDNS name, which survives DHCP changes. It
// is "" when the host name is unknown.
func (p PrinterInfo) HostnameURI() string {
	if p.Server == "" {
		return ""
	}
	return Endpoint{Host: strings.ToLower(p.Server), Port: p.Port, Resource: p.Resource}.PrinterURI()
}

// Printable reports whether the IPP driver could send this printer something: one of
// the advertised formats is one we produce (see sendable), or there is no pdl key at
// all, in which case Get-Printer-Attributes will tell.
func (p PrinterInfo) Printable() bool {
	return len(p.Formats) == 0 || len(sendable(p.Formats, p.URF)) > 0
}

// unescapeDNS undoes the presentation-format escapes miekg/dns applies to names and TXT
// strings: "\X" is X and "\DDD" is the byte DDD.
func unescapeDNS(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		if i+3 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
			if n, err := strconv.Atoi(s[i+1 : i+4]); err == nil && n < 256 {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i+1])
		i++
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// ParseTXT turns TXT strings ("key=value") into a map. RFC 6763 section 6.4: keys are
// compared without regard to case (so they are stored lower-case), the first of a
// repeated key counts, and a key without "=" is present with no value.
func ParseTXT(records []string) map[string]string {
	out := make(map[string]string, len(records))
	for _, r := range records {
		k, v, _ := strings.Cut(unescapeDNS(r), "=")
		k = strings.ToLower(k)
		if _, seen := out[k]; k != "" && !seen {
			out[k] = v
		}
	}
	return out
}

// txtRecord reads values from a parsed TXT record.
type txtRecord map[string]string

func (t txtRecord) text(key string) string { return strings.TrimSpace(t[key]) }

// list splits a comma-separated value, dropping empty items.
func (t txtRecord) list(key string) []string {
	var out []string
	for _, item := range strings.Split(t[key], ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// resource turns rp, a path without its leading slash, into an absolute path; an
// empty rp means the usual /ipp/print.
func (t txtRecord) resource() string {
	if p := path.Clean("/" + t.text("rp")); p != "/" {
		return p
	}
	return DefaultResource
}

// bareUUID lower-cases a UUID and drops the "urn:uuid:" prefix of its URN form
// (RFC 4122 section 3), so TXT UUID and printer-uuid compare equal.
func bareUUID(v string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "urn:uuid:")
}

// pickAddress prefers IPv4, then a routable IPv6. A link-local IPv6 address needs a zone
// to connect, so that case is left to the mDNS host name.
func pickAddress(addrs []string) string {
	var v6 string
	for _, a := range addrs {
		bare, _, _ := strings.Cut(a, "%")
		ip, err := netip.ParseAddr(bare)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if ip.Is4() {
			return ip.String()
		}
		if v6 == "" && !ip.IsLinkLocalUnicast() {
			v6 = ip.String()
		}
	}
	return v6
}

// NewPrinterInfo builds a PrinterInfo from a resolved service. The host is the best
// address, else the mDNS host name, else the instance name.
func NewPrinterInfo(name string, addrs []string, port int, txt map[string]string, server string) PrinterInfo {
	t := txtRecord(txt)
	host := pickAddress(addrs)
	if host == "" {
		host = strings.TrimRight(server, ".")
	}
	if host == "" {
		host = name
	}
	if port == 0 {
		port = DefaultPort
	}
	return PrinterInfo{
		Name:     name,
		Host:     host,
		Port:     port,
		Resource: t.resource(),
		UUID:     bareUUID(t["uuid"]),
		Model:    t.text("ty"),
		Formats:  t.list("pdl"),
		URF:      t.list("urf"),
		Kind:     t.list("kind"),
		Server:   server,
		TXT:      txt,
	}
}

func infoFromEntry(e *zeroconf.ServiceEntry) PrinterInfo {
	var addrs []string
	for _, ip := range append(append([]net.IP(nil), e.AddrIPv4...), e.AddrIPv6...) {
		addrs = append(addrs, ip.String())
	}
	return NewPrinterInfo(unescapeDNS(e.Instance), addrs, e.Port, ParseTXT(e.Text), unescapeDNS(e.HostName))
}

// Discover browses _ipp._tcp on the local network for timeout (or until ctx ends) and
// returns every printer that resolved, sorted by name. Check Printable to filter. It
// sends only mDNS queries; nothing is sent to the printers themselves.
func Discover(ctx context.Context, timeout time.Duration) ([]PrinterInfo, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("start mDNS: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 16)
	if err := resolver.Browse(ctx, ServiceType, "local.", entries); err != nil {
		return nil, fmt.Errorf("browse %s: %w", ServiceType, err)
	}
	found := map[string]PrinterInfo{}
	// The resolver closes entries when ctx ends, so ranging also drains it and lets the
	// resolver's goroutine finish.
	for e := range entries {
		if e == nil {
			continue
		}
		info := infoFromEntry(e)
		// A later answer may come without the TXT record an earlier one carried.
		if old, seen := found[info.Name]; seen && len(info.TXT) == 0 {
			info.TXT, info.UUID, info.Model = old.TXT, old.UUID, old.Model
			info.Formats, info.URF, info.Kind, info.Resource = old.Formats, old.URF, old.Kind, old.Resource
		}
		found[info.Name] = info
	}
	out := make([]PrinterInfo, 0, len(found))
	for _, p := range found {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}
