// An IPP client over plain HTTP: requests are POSTed as application/ipp (RFC 8010
// section 4). Operations are those of RFC 8011 section 4: Print-Job, Validate-Job,
// Cancel-Job, Get-Job-Attributes and Get-Printer-Attributes.

package ipp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Operation ids, RFC 8011 section 5.4.15.
const (
	OpPrintJob             = 0x0002
	OpValidateJob          = 0x0004
	OpCancelJob            = 0x0008
	OpGetJobAttributes     = 0x0009
	OpGetPrinterAttributes = 0x000b
)

// Status codes the client looks at, RFC 8011 Appendix B.
const (
	StatusOK                     = 0x0000
	StatusOKIgnoredOrSubstituted = 0x0001
	StatusNotFound               = 0x0406
)

// maxReply caps an IPP reply body. A full Get-Printer-Attributes is tens of KB.
const maxReply = 8 << 20

// job-state values, RFC 8011 section 5.3.7.
const (
	JobPending    = 3
	JobHeld       = 4
	JobProcessing = 5
	JobStopped    = 6
	JobCanceled   = 7
	JobAborted    = 8
	JobCompleted  = 9
)

// Defaults for endpoints and requests.
const (
	DefaultPort     = 631 // RFC 8010 section 4: the IPP port
	DefaultResource = "/ipp/print"
	DefaultUser     = "crate-labels"
	ContentType     = "application/ipp"
)

// Timeouts. A printer on the same network accepts a TCP connection within
// milliseconds, or a few seconds when its Wi-Fi is asleep, hence connectTimeout.
// Requests without a document get quickTimeout in all. Print-Job has no overall limit,
// because a printer may take the document only as fast as it prints, but it must start
// its reply within replyTimeout of the upload ending: some printers answer only once
// they have spooled the whole document.
const (
	connectTimeout = 5 * time.Second
	quickTimeout   = 20 * time.Second
	replyTimeout   = 2 * time.Minute
)

// StatusError is an error status from the printer.
type StatusError struct {
	Status  int
	Message string // status-message, if the printer sent one
}

func (e *StatusError) Error() string { return "the printer refused: " + Refusal(e.Status, e.Message) }

// TransportError means the printer could not be reached, or did not answer with IPP.
type TransportError struct {
	Msg        string
	HTTPStatus int // the HTTP status when the printer answered with one other than 2xx
}

func (e *TransportError) Error() string { return e.Msg }

// Retryable reports a failure that may clear up by itself: no connection, an HTTP 5xx,
// an unreadable reply, or an IPP server-error status (0x05xx, e.g. busy). Client errors
// (HTTP 4xx, IPP 0x04xx) are not.
func Retryable(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return te.HTTPStatus == 0 || te.HTTPStatus >= 500
	}
	var se *StatusError
	return errors.As(err, &se) && se.Status >= 0x0500 && se.Status <= 0x05ff
}

// ErrPrintingDisabled is returned by PrintJob on a client without AllowPrint, so a dry
// run can never use paper.
var ErrPrintingDisabled = errors.New("printing is disabled for this connection (dry run)")

// Endpoint is an IPP printer address, e.g. ipp://198.51.100.9:631/ipp/print.
type Endpoint struct {
	Host     string // host name or IP literal, without brackets
	Port     int
	Resource string // path, starting with "/"
}

// ParseEndpoint accepts ipp:// and http:// URIs, or a bare host[:port][/path]. The port
// defaults to 631 and the path to /ipp/print.
func ParseEndpoint(uri string) (Endpoint, error) {
	raw := strings.TrimSpace(uri)
	if !strings.Contains(raw, "://") {
		raw = "ipp://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Endpoint{}, fmt.Errorf("printer address %q is not a valid URI: %w", uri, err)
	}
	if u.Scheme != "ipp" && u.Scheme != "http" {
		return Endpoint{}, fmt.Errorf("printer address %q uses %s://, but only ipp:// (plain HTTP on port 631) is supported", uri, u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return Endpoint{}, fmt.Errorf("printer address %q has no host name", uri)
	}
	ep := Endpoint{Host: host, Port: DefaultPort, Resource: DefaultResource}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return Endpoint{}, fmt.Errorf("printer address %q has an invalid port %q", uri, p)
		}
		ep.Port = n
	}
	if path := strings.TrimSpace(u.Path); strings.Trim(path, "/") != "" {
		ep.Resource = "/" + strings.TrimLeft(path, "/")
	}
	return ep, nil
}

// Authority is host:port, with IPv6 literals bracketed and any zone removed.
func (e Endpoint) Authority() string {
	host := e.Host
	bare, _, _ := strings.Cut(host, "%")
	if ip, err := netip.ParseAddr(bare); err == nil && ip.Is6() {
		host = "[" + bare + "]"
	} else {
		host = strings.TrimRight(host, ".")
	}
	return host + ":" + strconv.Itoa(e.Port)
}

// PrinterURI is the printer-uri operation attribute; the port is always explicit.
func (e Endpoint) PrinterURI() string { return "ipp://" + e.Authority() + e.Resource }

// HTTPURL is where requests are POSTed.
func (e Endpoint) HTTPURL() string { return "http://" + e.Authority() + e.Resource }

func (e Endpoint) String() string { return e.PrinterURI() }

// --- requests (pure, so tests compare bytes) ---------------------------------------------

// JobRequest is a Print-Job or Validate-Job without its document.
type JobRequest struct {
	Name           string
	DocumentFormat string
	JobAttributes  []Attribute
}

// OperationGroup starts every request with the attributes RFC 8011 section 4.1.4
// requires first (charset, then natural language), the target printer-uri and
// requesting-user-name, followed by the operation's own attributes.
func OperationGroup(ep Endpoint, user string, extras ...Attribute) Group {
	return Group{Tag: TagOperationGroup, Attributes: append([]Attribute{
		Charset("attributes-charset", "utf-8"),
		Language("attributes-natural-language", "en"),
		URI("printer-uri", ep.PrinterURI()),
		Name("requesting-user-name", user),
	}, extras...)}
}

// GetPrinterAttributesRequest asks for the requested attributes. With none it asks for
// 'all' and, by name, media-col-database, which a printer need not include in 'all'.
func GetPrinterAttributesRequest(ep Endpoint, requestID int, user string, requested ...string) *Message {
	if len(requested) == 0 {
		requested = []string{"all", "media-col-database"}
	}
	return NewMessage(OpGetPrinterAttributes, requestID,
		OperationGroup(ep, user, Keyword("requested-attributes", requested...)))
}

// statusAttributes are what PrinterStatus asks for.
var statusAttributes = []string{"printer-state", "printer-state-reasons", "printer-state-message"}

// JobRequestMessage is Print-Job or Validate-Job: the operation group with job-name and
// document-format, a job group when there are job attributes, then the document (none
// for Validate-Job).
func JobRequestMessage(op int, ep Endpoint, requestID int, user string, job JobRequest, document []byte) *Message {
	m := NewMessage(op, requestID, OperationGroup(ep, user,
		Name("job-name", job.Name),
		Mime("document-format", job.DocumentFormat)))
	if len(job.JobAttributes) > 0 {
		m.Groups = append(m.Groups, Group{Tag: TagJobGroup, Attributes: job.JobAttributes})
	}
	m.Data = document
	return m
}

// CancelJobRequest cancels one job, addressed by printer-uri and job-id (RFC 8011
// section 4.3.3).
func CancelJobRequest(ep Endpoint, requestID int, user string, jobID int) *Message {
	return NewMessage(OpCancelJob, requestID, OperationGroup(ep, user, Integer("job-id", jobID)))
}

// GetJobAttributesRequest polls one job, addressed by printer-uri and job-id (RFC 8011
// section 4.3.4), for what Watch needs.
func GetJobAttributesRequest(ep Endpoint, requestID int, user string, jobID int) *Message {
	return NewMessage(OpGetJobAttributes, requestID, OperationGroup(ep, user,
		Integer("job-id", jobID),
		Keyword("requested-attributes", "job-state", "job-state-reasons", "job-state-message", "job-impressions-completed")))
}

// --- responses ---------------------------------------------------------------------------

// JobStatus is a parsed Get-Job-Attributes response.
type JobStatus struct {
	State                int // job-state; 0 when the printer left it out
	Reasons              []string
	Message              string
	ImpressionsCompleted int
	HasImpressions       bool
	// Gone is set by Watch when the printer stopped knowing the job.
	Gone bool
}

// Terminal reports one of the three terminal states of RFC 8011 section 5.3.7.2.
func (s JobStatus) Terminal() bool {
	switch s.State {
	case JobCanceled, JobAborted, JobCompleted:
		return true
	}
	return false
}

// Stopped reports a job held up by its printer: processing-stopped, or a job whose
// reasons say the printer stopped under it (RFC 8011 section 5.3.8).
func (s JobStatus) Stopped() bool {
	return s.State == JobStopped || slices.ContainsFunc(s.Reasons, func(r string) bool {
		return r == "printer-stopped" || r == "printer-stopped-partly"
	})
}

// ParseJobStatus reads a Get-Job-Attributes response.
func ParseJobStatus(m *Message) JobStatus {
	get := func(name string) *Attribute { return m.Lookup(TagJobGroup, name) }
	st := JobStatus{Reasons: get("job-state-reasons").Strings()}
	if v := get("job-state").Ints(); len(v) > 0 {
		st.State = v[0]
	}
	for _, s := range get("job-state-message").Strings() {
		if s = strings.TrimSpace(s); s != "" {
			st.Message = s
			break
		}
	}
	if v := get("job-impressions-completed").Ints(); len(v) > 0 {
		st.ImpressionsCompleted, st.HasImpressions = v[0], true
	}
	return st
}

// PrinterStatus is printer-state, printer-state-reasons and printer-state-message.
type PrinterStatus struct {
	State   int
	Reasons []string
	Message string
}

// Problem is the most urgent error-level reason, or "" (see PrinterProblem).
func (s PrinterStatus) Problem() string { return PrinterProblem(s.State, s.Reasons) }

// UnsupportedAttributes names the attributes in the unsupported group. Validate-Job
// answers 0x0001 with this group when the printer would ignore or substitute them.
func UnsupportedAttributes(m *Message) []string {
	g := m.Group(TagUnsupportedGroup)
	if g == nil {
		return nil
	}
	out := make([]string, len(g.Attributes))
	for i, a := range g.Attributes {
		out[i] = a.Name
	}
	return out
}

func statusMessage(m *Message) string {
	if s := m.Lookup(TagOperationGroup, "status-message").Strings(); len(s) > 0 {
		return s[0]
	}
	return ""
}

// --- client ------------------------------------------------------------------------------

// NewHTTPClient returns the transport used for printers; see the timeout constants.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: connectTimeout}).DialContext,
		ResponseHeaderTimeout: replyTimeout,
		MaxIdleConnsPerHost:   1,
		IdleConnTimeout:       45 * time.Second,
	}}
}

// Client speaks to one endpoint. Request ids count up from 1 per client.
type Client struct {
	Endpoint Endpoint
	User     string       // requesting-user-name; DefaultUser when empty
	HTTP     *http.Client // NewHTTPClient() when nil
	// AllowPrint must be set for PrintJob to send anything.
	AllowPrint bool

	mu     sync.Mutex
	nextID int
}

// NewClient returns a client for ep that may print only when allowPrint is set.
func NewClient(ep Endpoint, allowPrint bool) *Client {
	return &Client{Endpoint: ep, AllowPrint: allowPrint}
}

func (c *Client) requestID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *Client) user() string {
	if c.User == "" {
		return DefaultUser
	}
	return c.User
}

func (c *Client) httpClient() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HTTP == nil {
		c.HTTP = NewHTTPClient()
	}
	return c.HTTP
}

// CloseIdle closes the client's idle keep-alive connections. Printers accept only a
// few connections at a time, so a caller done with a printer should not leave one
// open until the transport's idle timeout.
func (c *Client) CloseIdle() { c.httpClient().CloseIdleConnections() }

// Send posts one request and decodes the answer. A status above 0x00ff is a
// *StatusError; network, HTTP and decoding failures are *TransportError. Every
// operation but Print-Job is bounded by quickTimeout.
func (c *Client) Send(ctx context.Context, req *Message) (*Message, error) {
	if req.Code != OpPrintJob {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, quickTimeout)
		defer cancel()
	}
	body, err := Encode(req)
	if err != nil {
		return nil, err
	}
	who := c.Endpoint.Authority()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint.HTTPURL(), bytes.NewReader(body))
	if err != nil {
		return nil, &TransportError{Msg: fmt.Sprintf("cannot build a request for %s: %v", c.Endpoint.PrinterURI(), err)}
	}
	hr.Header.Set("Content-Type", ContentType)
	resp, err := c.httpClient().Do(hr)
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		return nil, &TransportError{Msg: fmt.Sprintf("could not reach %s: %v", who, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, &TransportError{Msg: fmt.Sprintf("%s replied with HTTP status %d, not an IPP response", who, resp.StatusCode), HTTPStatus: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		return nil, &TransportError{Msg: fmt.Sprintf("the reply from %s was cut off: %v", who, err)}
	}
	if len(data) > maxReply {
		return nil, &TransportError{Msg: fmt.Sprintf("the reply from %s is over the %d MB this client reads", who, maxReply>>20)}
	}
	m, err := Decode(data)
	if err != nil {
		return nil, &TransportError{Msg: fmt.Sprintf("could not decode the reply from %s (%v)", who, err)}
	}
	if m.Code > 0x00ff {
		return nil, &StatusError{Status: m.Code, Message: statusMessage(m)}
	}
	return m, nil
}

// GetPrinterAttributes asks for requested (default: all, media-col-database).
func (c *Client) GetPrinterAttributes(ctx context.Context, requested ...string) (*Printer, error) {
	m, err := c.Send(ctx, GetPrinterAttributesRequest(c.Endpoint, c.requestID(), c.user(), requested...))
	if err != nil {
		return nil, err
	}
	return &Printer{Response: m}, nil
}

// PrinterStatus asks only for printer-state, printer-state-reasons and
// printer-state-message.
func (c *Client) PrinterStatus(ctx context.Context) (PrinterStatus, error) {
	p, err := c.GetPrinterAttributes(ctx, statusAttributes...)
	if err != nil {
		return PrinterStatus{}, err
	}
	return p.Status(), nil
}

// ValidateJob sends the job's attributes without a document; it prints nothing. A
// 0x0001 answer with an unsupported group (see UnsupportedAttributes) means the printer
// would ignore or substitute those attributes, so callers must check the response.
func (c *Client) ValidateJob(ctx context.Context, job JobRequest) (*Message, error) {
	return c.Send(ctx, JobRequestMessage(OpValidateJob, c.Endpoint, c.requestID(), c.user(), job, nil))
}

// PrintJob sends a job and returns its job-id. It fails with ErrPrintingDisabled, and
// sends nothing, unless the client has AllowPrint.
func (c *Client) PrintJob(ctx context.Context, job JobRequest, document []byte) (int, error) {
	if !c.AllowPrint {
		return 0, ErrPrintingDisabled
	}
	m, err := c.Send(ctx, JobRequestMessage(OpPrintJob, c.Endpoint, c.requestID(), c.user(), job, document))
	if err != nil {
		return 0, err
	}
	ids := m.Lookup(TagJobGroup, "job-id").Ints()
	if len(ids) == 0 {
		return 0, &TransportError{Msg: "Print-Job succeeded, but the response has no job-id attribute"}
	}
	return ids[0], nil
}

// GetJobAttributes polls one job.
func (c *Client) GetJobAttributes(ctx context.Context, jobID int) (JobStatus, error) {
	m, err := c.Send(ctx, GetJobAttributesRequest(c.Endpoint, c.requestID(), c.user(), jobID))
	if err != nil {
		return JobStatus{}, err
	}
	return ParseJobStatus(m), nil
}

// CancelJob asks the printer to cancel a job.
func (c *Client) CancelJob(ctx context.Context, jobID int) error {
	_, err := c.Send(ctx, CancelJobRequest(c.Endpoint, c.requestID(), c.user(), jobID))
	return err
}

// Watch tunes Client.Watch.
type Watch struct {
	// Every is the pause before each poll, the first included: a job just created has
	// nothing new to report yet.
	Every time.Duration
	// Patience is the longest stretch without progress Watch waits through. Progress is
	// a new job-state or a higher job-impressions-completed. Time the job spends held
	// up by a stopped printer (out of paper, a jam, an open cover) is not counted,
	// because it ends when a person acts, not when the printer gives up.
	Patience time.Duration
	// OnStatus, if set, sees every status the printer reports.
	OnStatus func(JobStatus)
	// OnError, if set, sees every failed poll that will be retried.
	OnError func(error)
}

// maxPollGap caps the pause between polls after failures.
const maxPollGap = 10 * time.Second

// Watch polls a job until it reaches a terminal state, until it goes Patience without
// progress, or until ctx ends, and returns the last status. A poll that fails with a
// Retryable error is tried again, pausing one Every longer after each failure in a row
// (up to maxPollGap); failures are not progress. When the printer answers
// client-error-not-found the job has been removed, which RFC 8011 section 5.3.7.2
// allows once a job has finished, so its outcome is unknown: Watch returns the last
// status with Gone set. When ctx ends, the last status is returned with ctx.Err().
func (c *Client) Watch(ctx context.Context, jobID int, w Watch) (JobStatus, error) {
	every := max(w.Every, time.Millisecond)
	var last JobStatus
	seen, failures := false, 0
	progressAt := time.Now()
	for {
		pause := min(every*time.Duration(failures+1), max(every, maxPollGap))
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, ctx.Err()
		case <-timer.C:
		}
		st, err := c.GetJobAttributes(ctx, jobID)
		if err != nil {
			var se *StatusError
			switch {
			case ctx.Err() != nil:
				return last, ctx.Err()
			case errors.As(err, &se) && se.Status == StatusNotFound:
				last.Gone = true
				return last, nil
			case !Retryable(err):
				return last, err
			case time.Since(progressAt) >= w.Patience:
				return last, err
			}
			failures++
			if w.OnError != nil {
				w.OnError(err)
			}
			continue
		}
		failures = 0
		if w.OnStatus != nil {
			w.OnStatus(st)
		}
		if st.Terminal() {
			return st, nil
		}
		switch {
		case !seen, st.Stopped(), st.State != last.State, st.ImpressionsCompleted > last.ImpressionsCompleted:
			progressAt = time.Now()
		case time.Since(progressAt) >= w.Patience:
			return st, nil
		}
		seen, last = true, st
	}
}
