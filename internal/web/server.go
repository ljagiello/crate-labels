// Package web serves crate-labels' HTTP API (docs/API.md) and the embedded browser UI
// on 127.0.0.1. Requests whose Host is not local are refused (DNS rebinding), and so
// are cross-site writes (CSRF).
package web

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ljagiello/crate-labels/internal/audio"
	"github.com/ljagiello/crate-labels/internal/service"
	"github.com/ljagiello/crate-labels/internal/store"
)

//go:embed static
var staticFiles embed.FS

// DefaultPort is where `crate-labels serve` listens.
const DefaultPort = 8787

// localHosts are the only Host and Origin host names the server accepts.
var localHosts = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// BLEDevice is one Bluetooth device a scan saw (API.md "Printers").
type BLEDevice struct {
	Address      string   `json:"address"`
	Name         string   `json:"name"`
	RSSI         int      `json:"rssi"`
	ServiceUUIDs []string `json:"service_uuids"`
	ModelIDs     []string `json:"model_ids"`
	MaybeFamily  *string  `json:"maybe_family"`
}

// BLEProbe is what connecting to a device revealed.
type BLEProbe struct {
	Services  []string `json:"services"`
	Handshake *string  `json:"handshake"` // hex
	Family    *string  `json:"family"`
	ModelID   *string  `json:"model_id"`
}

// IPPPrinter is one printer DNS-SD found.
type IPPPrinter struct {
	Name        string            `json:"name"`
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	Resource    string            `json:"resource"`
	UUID        string            `json:"uuid"`
	Model       string            `json:"model"`
	Formats     []string          `json:"formats"`
	URF         []string          `json:"urf"`
	Kind        []string          `json:"kind"`
	Server      string            `json:"server"`
	TXT         map[string]string `json:"txt"`
	URI         string            `json:"uri"`
	HostnameURI *string           `json:"hostname_uri"`
	Printable   bool              `json:"printable"`
	// SuggestedModelID is set by the server (SuggestIPPModel); adapters leave it empty.
	SuggestedModelID string `json:"suggested_model_id"`
}

// SuggestIPPModel picks the catalog model a discovered IPP printer most likely is:
// "ipp-label" for a dedicated label printer (kind has "label" or "roll" but no
// "document"), else "ipp-sheet". Office printers such as the Brother HL-L3290CDW send
// "document,envelope,label,postcard" and are sheet printers. This is the only place the
// rule lives; the UI preselects the suggestion and the user may change it.
func SuggestIPPModel(kind []string) string {
	has := map[string]bool{}
	for _, k := range kind {
		has[strings.ToLower(strings.TrimSpace(k))] = true
	}
	if (has["label"] || has["roll"]) && !has["document"] {
		return "ipp-label"
	}
	return "ipp-sheet"
}

// CUPSQueue is one local CUPS queue.
type CUPSQueue struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	State       string `json:"state"`
	Enabled     bool   `json:"enabled"`
	Message     string `json:"message"`
	IsDefault   bool   `json:"is_default"`
}

// Discovery finds printers. cmd/crate-labels fills it from the BLE, IPP and CUPS
// packages; a nil function makes its endpoint reply 501.
type Discovery struct {
	BLEScan     func(ctx context.Context, timeout time.Duration, showAll bool) ([]BLEDevice, error)
	BLEProbe    func(ctx context.Context, address string, timeout time.Duration) (BLEProbe, error)
	IPPDiscover func(ctx context.Context, timeout time.Duration) ([]IPPPrinter, error)
	CUPSQueues  func(ctx context.Context) ([]CUPSQueue, error)
}

// Options configure a Server.
type Options struct {
	Discovery Discovery
	DataDir   string
	Log       *slog.Logger
}

// Server is the HTTP API and the UI.
type Server struct {
	svc     *service.Service
	opts    Options
	static  fs.FS
	handler http.Handler

	stopOnce sync.Once
	stopping chan struct{} // closed on shutdown; ends the SSE streams

	maxAudio int64 // the largest audio upload, in bytes (tests lower it)
}

// New builds a Server around svc.
func New(svc *service.Service, o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.DataDir == "" {
		o.DataDir = store.DataDir()
	}
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	s := &Server{svc: svc, opts: o, static: sub, stopping: make(chan struct{}), maxAudio: audio.MaxBytes}
	mux := http.NewServeMux()
	s.routes(mux)
	s.handler = s.guard(mux)
	return s
}

// Handler is the whole site, guards included.
func (s *Server) Handler() http.Handler { return s.handler }

// Stop ends open event streams so a graceful shutdown is not held up by them.
func (s *Server) Stop() { s.stopOnce.Do(func() { close(s.stopping) }) }

// hostname strips the port and IPv6 brackets from a Host header.
func hostname(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"))
}

// serverPort is the port the request arrived on (the listener's), else the Host's.
func serverPort(r *http.Request) string {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, port, err := net.SplitHostPort(a.String()); err == nil {
			return port
		}
	}
	_, port, _ := net.SplitHostPort(r.Host)
	return port
}

// sameOrigin reports whether an Origin header names this server: http on a local host
// name and on the port this server listens on. "null" and other localhost ports fail.
func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	return localHosts[strings.ToLower(u.Hostname())] && port == serverPort(r)
}

// guard applies the DNS-rebinding and CSRF rules of API.md "Ground rules" and the
// security headers every response carries.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		if !localHosts[hostname(r.Host)] {
			writeError(w, http.StatusBadRequest, "Invalid host header.")
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeError(w, http.StatusForbidden, "Cross-site requests are not allowed.")
				return
			}
			if origin, ok := r.Header["Origin"]; ok && (len(origin) != 1 || !sameOrigin(origin[0], r)) {
				writeError(w, http.StatusForbidden, "Cross-site requests are not allowed.")
				return
			}
			// A JSON content type cannot be sent cross-site without a CORS preflight,
			// which this server never grants. Neither can application/octet-stream (it
			// is not CORS-safelisted), which only the audio upload POST /api/tempo takes.
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || (mt != "application/json" && !(mt == "application/octet-stream" && isAudioUpload(r))) {
				msg := "Send requests as JSON (Content-Type: application/json)."
				if isAudioUpload(r) {
					msg = audioUploadHint
				}
				writeError(w, http.StatusUnsupportedMediaType, msg)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe serves on 127.0.0.1:port until ctx ends, then shuts down gracefully.
// ready, if set, runs once the port is bound and before any request is served, so work
// that must happen only in the one live server (job recovery) goes there; an error from
// it closes the socket and is returned.
func ListenAndServe(ctx context.Context, srv *Server, port int, ready func(addr string) error) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	if ready != nil {
		if err := ready(ln.Addr().String()); err != nil {
			ln.Close()
			return err
		}
	}
	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(srv.opts.Log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	srv.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
