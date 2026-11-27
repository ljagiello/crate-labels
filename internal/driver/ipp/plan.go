// Package ipp is the "ipp" printer family: page and label printers reached over IPP
// on the local network (AirPrint and IPP Everywhere printers).
//
// Labels are imposed onto pages (sheets.Impose). One medium is chosen for the page size
// from what the printer reports loaded (proto.ChooseMedia), every page is placed on it
// at actual size, and the pages go out as 8-bit grey URF, or as PDF or JPEG for
// printers that do not take URF. When the printer offers Validate-Job, the job is
// checked first and any attribute the printer says it would ignore is dropped. With the
// dry_run setting only that check is made and nothing prints.
package ipp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"slices"
	"strings"

	"github.com/ljagiello/crate-labels/internal/driver/internal/page"
	proto "github.com/ljagiello/crate-labels/internal/ipp"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/sheets"
)

// DefaultJobName is the job-name when the job_name setting is empty.
const DefaultJobName = "crate-labels"

// jpegQuality is high so that small text keeps clean edges.
const jpegQuality = 93

// jobByteBudget caps the document of one job. Each document is built in memory before
// it is sent, and 32 MiB still holds dozens of label pages at 600 dpi.
const jobByteBudget = 32 << 20

// PickFormat returns preferred when the printer takes it and we can produce it, else
// the first of the printer's sendable formats, else "".
func PickFormat(p *proto.Printer, preferred string) string {
	usable := p.Sendable()
	if slices.Contains(usable, preferred) {
		return preferred
	}
	if len(usable) > 0 {
		return usable[0]
	}
	return ""
}

// Page is one composed page, encoded for the document format.
type Page struct {
	Labels int             // labels on this page
	Data   []byte          // a URF page or a whole JPEG file
	PDF    sheets.PDFImage // the page image, for PDF
}

func (p *Page) size() int { return len(p.Data) + len(p.PDF.Flate) }

// Job is one IPP job: its request (attributes), pages and encoded document.
type Job struct {
	Request  proto.JobRequest
	Pages    []*Page
	Document []byte
}

// Labels is the number of labels in the job.
func (j *Job) Labels() int { return j.labelsInFirst(len(j.Pages)) }

func (j *Job) labelsInFirst(pages int) int {
	n := 0
	for _, p := range j.Pages[:max(0, min(pages, len(j.Pages)))] {
		n += p.Labels
	}
	return n
}

// Plan is everything decided before any job is sent.
type Plan struct {
	Printer        *proto.Printer
	DocumentFormat string
	DPI            int // resolution of the document sent
	Choice         *proto.Choice
	Jobs           []*Job
}

// Pages counts the pages over all jobs.
func (p *Plan) Pages() int {
	n := 0
	for _, j := range p.Jobs {
		n += len(j.Pages)
	}
	return n
}

// SetAttributes gives every job the same job attributes.
func (p *Plan) SetAttributes(attrs []proto.Attribute) {
	for _, j := range p.Jobs {
		j.Request.JobAttributes = attrs
	}
}

// addJFIF inserts a JFIF APP0 segment carrying dpi after the SOI marker, since Go's
// encoder writes none and printers size JPEG pages by it.
func addJFIF(data []byte, dpi int) []byte {
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 || bytes.Contains(data[:min(len(data), 32)], []byte("JFIF")) {
		return data
	}
	seg := []byte{0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(dpi))
	seg = binary.BigEndian.AppendUint16(seg, uint16(dpi))
	seg = append(seg, 0, 0)
	out := make([]byte, 0, len(data)+len(seg))
	out = append(out, data[:2]...)
	out = append(out, seg...)
	return append(out, data[2:]...)
}

func encodeJPEG(g *image.Gray, dpi int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, g, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return addJFIF(buf.Bytes(), dpi), nil
}

// split groups pages into jobs in order. A job closes when it holds maxPages pages
// (0: no limit) or when the next page would take its document past maxBytes; a job
// always gets at least one page.
func split(pages []*Page, maxPages, maxBytes int) [][]*Page {
	var groups [][]*Page
	var cur []*Page
	size := 0
	for _, pg := range pages {
		if len(cur) > 0 && ((maxPages > 0 && len(cur) >= maxPages) || size+pg.size() > maxBytes) {
			groups = append(groups, cur)
			cur, size = nil, 0
		}
		cur = append(cur, pg)
		size += pg.size()
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

func loadedText(p *proto.Printer) string {
	media, loaded := p.Media()
	if !loaded || len(media) == 0 {
		return "does not say what paper is loaded"
	}
	var names []string
	for _, m := range media {
		names = append(names, m.Describe())
	}
	return "has " + strings.Join(names, " and ") + " loaded"
}

// PlanJobs imposes, chooses media, composes and encodes. It touches no network.
func PlanJobs(p *proto.Printer, m *printer.Model, s *printer.Stock, labels []*image.Gray, settings printer.Settings) (*Plan, error) {
	format := PickFormat(p, settings.String("document_format", ""))
	if format == "" {
		formats := strings.Join(p.Formats(), ", ")
		if formats == "" {
			formats = "no formats at all"
		}
		return nil, printer.Errorf("%s takes none of the formats crate-labels can send over IPP (8-bit grey URF, PDF or JPEG); it lists %s. Set it up as a CUPS printer instead.", p.Name(), formats)
	}
	canvasDPI := m.DPI
	outDPI := int(math.Round(canvasDPI))
	if format == proto.MimeURF {
		outDPI = p.URFResolution(canvasDPI)
	}
	paper := sheets.PageMM(s)
	// A label sheet's grid only lines up on its own paper size; a single label can sit
	// in the middle of anything larger.
	choice := proto.ChooseMedia(p, paper, s.Sheet != nil)
	if choice == nil {
		if s.Sheet != nil {
			return nil, printer.Errorf("%s %s, but %s needs %g×%g mm sheets. Load them, or set the tray to that paper size, and try again.",
				p.Name(), loadedText(p), s.Name, paper[0], paper[1])
		}
		return nil, printer.Errorf("%s %s, and none of it is large enough for a %g×%g mm label.", p.Name(), loadedText(p), paper[0], paper[1])
	}

	quality := p.DefaultQuality()
	counts := sheets.PagesFor(s, len(labels))
	var prepared []*Page
	for i, pg := range sheets.Impose(labels, s, canvasDPI, page.CropMarks(s, settings)) {
		img, err := proto.Place(pg, choice, outDPI, canvasDPI)
		if err != nil {
			return nil, printer.Errorf("%s: %v", p.Name(), err)
		}
		item := &Page{Labels: counts[i]}
		switch format {
		case proto.MimeURF:
			item.Data = proto.AppendURFPage(nil, proto.URFPage{Image: img, DPI: outDPI, Quality: quality})
		case proto.MimeJPEG:
			if item.Data, err = encodeJPEG(img, outDPI); err != nil {
				return nil, err
			}
		default:
			item.PDF = sheets.CompressForPDF(img)
		}
		prepared = append(prepared, item)
	}

	ticket := proto.Ticket{Media: choice, Grey: !page.Flag(settings, "color", false), Trim: s.Sheet == nil}
	attrs := ticket.Attributes(p)
	limit := p.JobPageLimit()
	if format == proto.MimeJPEG {
		limit = 1 // a JPEG file holds one page
	}
	groups := split(prepared, limit, jobByteBudget)
	name := settings.String("job_name", "")
	if name == "" {
		name = DefaultJobName
	}
	plan := &Plan{Printer: p, DocumentFormat: format, DPI: outDPI, Choice: choice}
	paperOut := [2]float64{float64(choice.Size[0]) / 100, float64(choice.Size[1]) / 100}
	for i, group := range groups {
		job := &Job{Request: proto.JobRequest{Name: proto.JobName(name, i+1, len(groups)), DocumentFormat: format, JobAttributes: attrs}, Pages: group}
		switch format {
		case proto.MimeURF:
			parts := make([][]byte, len(group))
			for k, pg := range group {
				parts[k] = pg.Data
			}
			job.Document = proto.URFDocument(parts)
		case proto.MimeJPEG:
			job.Document = group[0].Data
		default:
			imgs := make([]sheets.PDFImage, len(group))
			for k, pg := range group {
				imgs[k] = pg.PDF
			}
			job.Document = sheets.WritePDF(imgs, paperOut, float64(outDPI))
		}
		plan.Jobs = append(plan.Jobs, job)
	}
	return plan, nil
}

// summary is a one-line description of a plan for messages.
func (p *Plan) summary() string {
	return fmt.Sprintf("%d job(s) with %d page(s) in all", len(p.Jobs), p.Pages())
}
