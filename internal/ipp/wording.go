// User-facing explanations of what a printer reports.
//
// Sources: status-code keywords from RFC 8011 Appendix B; printer-state-reasons
// severity from RFC 8011 section 5.4.12; reason keywords from RFC 8011 sections 5.3.8
// and 5.4.12 and the IANA IPP registry (PWG 5100.11, 5100.13). Only registered keywords
// get their own wording; any other is shown by name. The phrasing is ours.

package ipp

import (
	"fmt"
	"strings"
)

// LostContact says why a run stopped when the printer took a job and then could no
// longer be reached.
const LostContact = "contact with the printer was lost before it confirmed the job"

// statusKeywords names the RFC 8011 Appendix B error status codes.
var statusKeywords = map[int]string{
	0x0400: "client-error-bad-request",
	0x0401: "client-error-forbidden",
	0x0402: "client-error-not-authenticated",
	0x0403: "client-error-not-authorized",
	0x0404: "client-error-not-possible",
	0x0405: "client-error-timeout",
	0x0406: "client-error-not-found",
	0x0407: "client-error-gone",
	0x0408: "client-error-request-entity-too-large",
	0x0409: "client-error-request-value-too-long",
	0x040a: "client-error-document-format-not-supported",
	0x040b: "client-error-attributes-or-values-not-supported",
	0x040c: "client-error-uri-scheme-not-supported",
	0x040d: "client-error-charset-not-supported",
	0x040e: "client-error-conflicting-attributes",
	0x040f: "client-error-compression-not-supported",
	0x0410: "client-error-compression-error",
	0x0411: "client-error-document-format-error",
	0x0412: "client-error-document-access-error",
	0x0500: "server-error-internal-error",
	0x0501: "server-error-operation-not-supported",
	0x0502: "server-error-service-unavailable",
	0x0503: "server-error-version-not-supported",
	0x0504: "server-error-device-error",
	0x0505: "server-error-temporary-error",
	0x0506: "server-error-not-accepting-jobs",
	0x0507: "server-error-busy",
	0x0508: "server-error-job-canceled",
	0x0509: "server-error-multiple-document-jobs-not-supported",
}

// statusHints adds a plain-language hint for the codes a label job is likely to meet.
var statusHints = map[int]string{
	0x0400: "it could not parse the request",
	0x0401: "it does not allow printing from this computer",
	0x0402: "it wants a user name and password",
	0x0403: "it wants a user name and password",
	0x0408: "the job is larger than it will take",
	0x040a: "it cannot read the page format",
	0x040b: "it rejects one of the job's settings, such as the paper size",
	0x040e: "two of the job's settings contradict each other",
	0x0411: "it found an error in the page data",
	0x0500: "it hit an internal fault",
	0x0501: "it does not offer that operation",
	0x0502: "it is temporarily unavailable; try again shortly",
	0x0504: "it hit a hardware fault",
	0x0505: "it is temporarily unavailable; try again shortly",
	0x0506: "it has been set to refuse new jobs",
	0x0507: "it is busy with other work; try again shortly",
}

// Refusal explains an error status from the printer as one clause. The hint (or the
// printer's own status-message) comes first and the RFC 8011 keyword follows in
// brackets, so the exact code is never lost.
func Refusal(status int, message string) string {
	keyword, ok := statusKeywords[status]
	if !ok {
		keyword = fmt.Sprintf("status 0x%04x", status)
	}
	text := statusHints[status]
	if m := strings.TrimSpace(message); m != "" {
		if text == "" {
			text = m
		} else {
			text += ": " + m
		}
	}
	if text == "" {
		text = "it refused the request"
	}
	return text + " (" + keyword + ")"
}

// severity is the RFC 8011 section 5.4.12 level of a printer-state-reasons value.
type severity int

const (
	sevReport severity = iota + 1
	sevWarning
	sevError
)

// splitReason separates a reason into its keyword and severity. An unsuffixed reason
// is an error while the printer is stopped and a warning otherwise, as section 5.4.12
// tells clients to assume.
func splitReason(reason string, printerState int) (string, severity) {
	for _, s := range []struct {
		suffix string
		sev    severity
	}{{"-error", sevError}, {"-warning", sevWarning}, {"-report", sevReport}} {
		if base, ok := strings.CutSuffix(reason, s.suffix); ok {
			return base, s.sev
		}
	}
	if printerState == PrinterStopped {
		return reason, sevError
	}
	return reason, sevWarning
}

// printerNotes is ordered by how urgently a person has to act. A jam or an open cover
// blocks everything and needs hands on the printer, so it is named before an empty
// tray, which in turn comes before a printer someone has paused on purpose.
var printerNotes = []struct{ keyword, text string }{
	{"media-jam", "paper or labels are jammed inside it"},
	{"media-path-jam", "paper or labels are jammed inside it"},
	{"cover-open", "one of its covers is open"},
	{"door-open", "one of its doors is open"},
	{"interlock-open", "a safety interlock is open (usually a cover that is not fully shut)"},
	{"input-tray-missing", "an input tray has been taken out"},
	{"media-empty", "a tray has no paper or labels left"},
	{"media-needed", "it is asking for paper or labels to be loaded"},
	{"output-tray-missing", "its output tray has been taken out"},
	{"output-area-full", "its output tray is full; take the printed sheets out"},
	{"toner-empty", "it is out of toner"},
	{"marker-supply-empty", "an ink, toner or ribbon supply is empty"},
	{"marker-waste-full", "its waste toner or ink container is full"},
	{"developer-empty", "it is out of developer"},
	{"opc-life-over", "its photoconductor drum has reached the end of its life"},
	{"fuser-over-temp", "its fuser is too hot"},
	{"fuser-under-temp", "its fuser has not warmed up"},
	{"spool-area-full", "its job storage is full"},
	{"paused", "someone has paused it"},
	{"moving-to-paused", "someone is pausing it"},
	{"shutdown", "it has been taken out of service"},
	{"timed-out", "its print engine stopped answering its own controller"},
	{"connecting-to-device", "it is still connecting to its print engine"},
}

// PrinterProblem names the most urgent error-level condition in printer-state-reasons,
// or returns "" when nothing there stops printing. printerState is printer-state (0 when
// unknown); it decides how unsuffixed reasons are read.
func PrinterProblem(printerState int, reasons []string) string {
	best, bestRank := "", len(printerNotes)+1
	for _, r := range reasons {
		base, sev := splitReason(r, printerState)
		if base == "none" || sev != sevError {
			continue
		}
		rank, text := len(printerNotes), fmt.Sprintf("it reports %q", base)
		for i, n := range printerNotes {
			if n.keyword == base {
				rank, text = i, n.text
				break
			}
		}
		if rank < bestRank {
			best, bestRank = text, rank
		}
	}
	return best
}

// jobNotes explains job-state-reasons for a job that did not complete, most specific
// first: a document the printer could not read says more than "aborted".
var jobNotes = []struct{ keyword, text string }{
	{"unsupported-document-format", "the printer cannot read the page format"},
	{"document-format-error", "the printer found an error in the page data"},
	{"document-unprintable-error", "the printer could not print the page data"},
	{"unsupported-compression", "the printer cannot decompress the job"},
	{"compression-error", "the job's compressed data was damaged"},
	{"job-data-insufficient", "the printer received too little data to begin"},
	{"submission-interrupted", "the job's upload was cut off"},
	{"conflicting-attributes", "the printer found the job's settings contradictory"},
	{"resources-are-not-ready", "the printer lacks the paper or other resources the job needs"},
	{"job-canceled-at-device", "someone cancelled it on the printer's own panel"},
	{"job-canceled-by-operator", "an operator cancelled it"},
	{"job-canceled-by-user", "it was cancelled by its owner"},
	{"job-canceled-after-timeout", "the printer cancelled it after waiting too long"},
	{"service-off-line", "the printer's print service went offline"},
	{"job-completed-with-errors", "the printer finished it but reported errors"},
	{"aborted-by-system", "the printer aborted it"},
}

// jobStateText is the fallback headline for a job state (RFC 8011 section 5.3.7).
func jobStateText(state int) string {
	switch state {
	case JobStopped:
		return "the job is stopped at the printer"
	case JobCanceled:
		return "the job was cancelled"
	case JobAborted:
		return "the job was aborted"
	}
	return "the job ended in " + JobStateName(state)
}

// JobOutcome explains a job that did not complete. The headline comes from the job's
// own reasons (else its state); a printer error, when there is one, is added as the
// likely cause, and the printer's job-state-message is quoted last.
func JobOutcome(st JobStatus, printerState int, printerReasons []string) string {
	text := ""
	for _, n := range jobNotes {
		for _, r := range st.Reasons {
			if r == n.keyword {
				text = n.text
				break
			}
		}
		if text != "" {
			break
		}
	}
	if text == "" {
		text = jobStateText(st.State)
	}
	if p := PrinterProblem(printerState, printerReasons); p != "" {
		text += "; " + p
	}
	if m := strings.TrimSpace(st.Message); m != "" {
		text += fmt.Sprintf(" (the printer says %q)", m)
	}
	return text
}

// JobStateName is the RFC 8011 section 5.3.7 keyword for a job-state value.
func JobStateName(state int) string {
	switch state {
	case JobPending:
		return "pending"
	case JobHeld:
		return "pending-held"
	case JobProcessing:
		return "processing"
	case JobStopped:
		return "processing-stopped"
	case JobCanceled:
		return "canceled"
	case JobAborted:
		return "aborted"
	case JobCompleted:
		return "completed"
	}
	return fmt.Sprintf("job-state %d", state)
}
