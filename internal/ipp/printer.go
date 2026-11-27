// What a printer says about itself in a Get-Printer-Attributes response.
//
// Attributes: RFC 8011 section 5.4 (printer description and defaults), PWG 5100.7
// (media-col-ready, media-col-database, media-col-supported, job-creation-attributes-
// supported), PWG 5100.13 (print-color-mode, print-scaling) and PWG 5100.1
// (finishings). urf-supported is the AirPrint capability list, whose tokens also appear
// in the "URF" DNS-SD TXT key: W8 means 8-bit grey and RSa-b lists resolutions in dpi.

package ipp

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// Document formats this package can produce.
const (
	MimePDF  = "application/pdf"
	MimeJPEG = "image/jpeg"
)

// printer-state values, RFC 8011 section 5.4.11.
const (
	PrinterIdle       = 3
	PrinterProcessing = 4
	PrinterStopped    = 5
)

// print-quality values, RFC 8011 section 5.2.13.
const (
	QualityDraft  = 3
	QualityNormal = 4
	QualityHigh   = 5
)

// FinishTrimAfterPages is the finishings enum PWG 5100.1 registers as
// trim-after-pages: cut the media after every page.
const FinishTrimAfterPages = 60

// Printer is a decoded Get-Printer-Attributes response.
type Printer struct {
	Response *Message
}

func (p *Printer) attr(name string) *Attribute { return p.Response.Lookup(TagPrinterGroup, name) }

// Strings returns the text values of a printer attribute.
func (p *Printer) Strings(name string) []string { return p.attr(name).Strings() }

// Ints returns the integer and enum values of a printer attribute.
func (p *Printer) Ints(name string) []int { return p.attr(name).Ints() }

// Lists reports whether a printer attribute contains the keyword.
func (p *Printer) Lists(name, keyword string) bool { return slices.Contains(p.Strings(name), keyword) }

func (p *Printer) first(name string) string {
	for _, s := range p.Strings(name) {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// Name is how messages refer to the printer: printer-info, which RFC 8011 describes as
// the administrator's description of it, else the required printer-name.
func (p *Printer) Name() string {
	if v := p.first("printer-info"); v != "" {
		return v
	}
	if v := p.first("printer-name"); v != "" {
		return v
	}
	return "the printer"
}

// State is printer-state, or 0 when the printer did not send it.
func (p *Printer) State() int {
	if v := p.Ints("printer-state"); len(v) > 0 {
		return v[0]
	}
	return 0
}

// Status gathers printer-state, printer-state-reasons and printer-state-message.
func (p *Printer) Status() PrinterStatus {
	return PrinterStatus{State: p.State(), Reasons: p.Strings("printer-state-reasons"), Message: p.first("printer-state-message")}
}

// Supports reports an operation in operations-supported.
func (p *Printer) Supports(op int) bool { return slices.Contains(p.Ints("operations-supported"), op) }

// Formats is document-format-supported.
func (p *Printer) Formats() []string { return p.Strings("document-format-supported") }

// Sendable lists the formats this package can produce that the printer takes, best
// first. See sendable.
func (p *Printer) Sendable() []string { return sendable(p.Formats(), p.Strings("urf-supported")) }

// sendable ranks the formats a printer lists by how well we can produce them: grey
// URF is made at the printer's own resolution, so nothing is rescaled after us; PDF
// carries the page at its exact size; JPEG is lossy and holds one page per job. URF
// counts only when its capability tokens include W8, the grey format we write.
func sendable(formats, urfTokens []string) []string {
	var out []string
	if slices.Contains(formats, MimeURF) && slices.Contains(urfTokens, "W8") {
		out = append(out, MimeURF)
	}
	for _, f := range []string{MimePDF, MimeJPEG} {
		if slices.Contains(formats, f) {
			out = append(out, f)
		}
	}
	return out
}

// urfResolutions reads the RS token of urf-supported, e.g. "RS300-600".
func (p *Printer) urfResolutions() []int {
	var out []int
	for _, tok := range p.Strings("urf-supported") {
		list, ok := strings.CutPrefix(tok, "RS")
		if !ok {
			continue
		}
		for _, part := range strings.Split(list, "-") {
			if n, err := strconv.Atoi(part); err == nil && n > 0 {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// defaultDPI is printer-resolution-default in dots per inch, or 0.
func (p *Printer) defaultDPI() int {
	a := p.attr("printer-resolution-default")
	r, ok := a.First().(Resolution)
	switch {
	case !ok || r.X <= 0:
		return 0
	case r.Units == UnitsDPCM:
		return int(math.Round(float64(r.X) * 2.54))
	}
	return r.X
}

// URFResolution picks the resolution for URF pages drawn at sourceDPI. The smallest
// listed resolution that is a whole multiple of sourceDPI wins, because pixels then
// scale up exactly; otherwise the smallest above sourceDPI, so no detail is lost;
// otherwise the largest listed. With no RS token it is printer-resolution-default, and
// failing that sourceDPI itself.
func (p *Printer) URFResolution(sourceDPI float64) int {
	src := int(math.Round(sourceDPI))
	listed := p.urfResolutions()
	for _, r := range listed {
		if src > 0 && r >= src && r%src == 0 {
			return r
		}
	}
	for _, r := range listed {
		if r >= src {
			return r
		}
	}
	if len(listed) > 0 {
		return listed[len(listed)-1]
	}
	if d := p.defaultDPI(); d > 0 {
		return d
	}
	return src
}

// DefaultQuality is print-quality-default when it is a defined value, else normal.
// Jobs leave print-quality to the printer, so this only fills the URF page header.
func (p *Printer) DefaultQuality() int {
	if q := p.Ints("print-quality-default"); len(q) > 0 && q[0] >= QualityDraft && q[0] <= QualityHigh {
		return q[0]
	}
	return QualityNormal
}

// JobPageLimit is the upper bound of job-impressions-supported, or 0 when the printer
// states none.
func (p *Printer) JobPageLimit() int {
	if r, ok := p.attr("job-impressions-supported").First().(Range); ok && r.Upper > 0 {
		return r.Upper
	}
	return 0
}

// Media returns the media to choose from: the sized entries of media-col-ready, which
// describe what is loaded, or, when the printer reports none, those of
// media-col-database. loaded tells which list it was.
func (p *Printer) Media() (media []*Media, loaded bool) {
	for _, list := range []struct {
		name   string
		loaded bool
	}{{"media-col-ready", true}, {"media-col-database", false}} {
		for _, c := range p.attr(list.name).Collections() {
			if m, ok := MediaFrom(c, list.loaded); ok {
				media = append(media, m)
			}
		}
		if len(media) > 0 {
			return media, list.loaded
		}
	}
	return nil, false
}
