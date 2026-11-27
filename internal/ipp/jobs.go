// Job Template attributes for a label job (RFC 8011 section 5.2, PWG 5100.7 media-col,
// PWG 5100.13 print-color-mode and print-scaling, PWG 5100.1 finishings).

package ipp

import (
	"fmt"
	"slices"
)

// Ticket is what a job asks of the printer.
type Ticket struct {
	Media *Choice
	// Grey asks for print-color-mode monochrome: our pages are always grey, so a
	// colour printer need not mix black from colour toner or ink.
	Grey bool
	// Trim asks for finishings trim-after-pages, so a roll is cut after every label.
	Trim bool
	// Quality is print-quality; 0 leaves it to the printer's default.
	Quality int
}

// Attributes turns t into job attributes the printer says it can honour. Each value
// must appear in the matching -supported attribute (media-col members are filtered by
// media-col-supported in MediaCol). print-scaling none is always asked for when listed,
// since every page is already the media's exact size. When the printer cannot check a
// job with Validate-Job, attributes it leaves out of a non-empty
// job-creation-attributes-supported are dropped as well; otherwise Validate-Job has the
// final say (see Without).
func (t Ticket) Attributes(p *Printer) []Attribute {
	attrs := []Attribute{MediaCol(t.Media, p.Strings("media-col-supported"))}
	if t.Grey && p.Lists("print-color-mode-supported", "monochrome") {
		attrs = append(attrs, Keyword("print-color-mode", "monochrome"))
	}
	if p.Lists("print-scaling-supported", "none") {
		attrs = append(attrs, Keyword("print-scaling", "none"))
	}
	if t.Trim && slices.Contains(p.Ints("finishings-supported"), FinishTrimAfterPages) {
		attrs = append(attrs, Enum("finishings", FinishTrimAfterPages))
	}
	if t.Quality != 0 && slices.Contains(p.Ints("print-quality-supported"), t.Quality) {
		attrs = append(attrs, Enum("print-quality", t.Quality))
	}
	if allowed := p.Strings("job-creation-attributes-supported"); len(allowed) > 0 && !p.Supports(OpValidateJob) {
		attrs = slices.DeleteFunc(attrs, func(a Attribute) bool { return !slices.Contains(allowed, a.Name) })
	}
	return attrs
}

// Without returns attrs minus the named ones, e.g. those Validate-Job listed as
// unsupported.
func Without(attrs []Attribute, names []string) []Attribute {
	return slices.DeleteFunc(slices.Clone(attrs), func(a Attribute) bool { return slices.Contains(names, a.Name) })
}

// JobName names part i (from 1) of a run split into n jobs, e.g. "labels (2 of 3)".
func JobName(name string, i, n int) string {
	if n <= 1 {
		return name
	}
	return fmt.Sprintf("%s (%d of %d)", name, i, n)
}
