package web

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/ljagiello/crate-labels/internal/service"
)

// tempoPath is the audio upload. It is the one write that is not JSON: its body is the
// audio file, sent as application/octet-stream (see guard).
const tempoPath = "/api/tempo"

const audioUploadHint = "Send the audio file itself as the body, with Content-Type: application/octet-stream."

func isAudioUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == tempoPath
}

// detectTempo is POST /api/tempo?styles=&min=&max= with the audio file as the body.
func (s *Server) detectTempo(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/octet-stream" {
		writeError(w, http.StatusUnsupportedMediaType, audioUploadHint)
		return
	}
	q := newQuery(r)
	styles := service.SplitStyles(q.str("styles", ""))
	lo, hi := q.number("min", 0, 0, 1000), q.number("max", 0, 0, 1000)
	if q.err != nil {
		s.fail(w, r, q.err)
		return
	}
	opt, err := service.TempoOptions(styles, lo, hi)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tooBig := func() {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("This audio file is over %s. Use one track rather than a whole mix.", sizeText(s.maxAudio)))
	}
	if r.ContentLength > s.maxAudio {
		tooBig()
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxAudio))
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		tooBig()
		return
	case err != nil:
		if r.Context().Err() == nil {
			writeError(w, http.StatusBadRequest, "The upload was cut off. Try again.")
		}
		return
	case len(body) == 0:
		writeError(w, http.StatusBadRequest, "Send an audio file as the request body.")
		return
	}
	res, err := s.svc.DetectTempo(r.Context(), body, filenameHint(r.Header.Get("X-Filename")), opt)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// filenameHint reduces the X-Filename header (percent-encoded UTF-8) to a bare file
// name: no folders, no control characters, at most 255 bytes. It is only ever used as a
// format hint.
func filenameHint(v string) string {
	if u, err := url.PathUnescape(v); err == nil {
		v = u
	}
	v = path.Base(strings.ReplaceAll(v, `\`, "/"))
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F {
			return -1
		}
		return r
	}, strings.ToValidUTF8(v, ""))
	if v == "." || v == ".." || v == "/" {
		return ""
	}
	if len(v) > 255 {
		v = strings.ToValidUTF8(v[len(v)-255:], "") // keep the extension
	}
	return v
}

func sizeText(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", max(1, n>>10))
}

// detectTempoVideo is POST /api/tempo/video {"url", "styles", "min", "max", "refresh"}:
// the tempo of a YouTube video linked from the release on Discogs. The server fetches the
// audio with yt-dlp, analyses it and throws it away.
func (s *Server) detectTempoVideo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL     string   `json:"url"`
		Styles  []string `json:"styles"`
		Min     float64  `json:"min"`
		Max     float64  `json:"max"`
		Refresh bool     `json:"refresh"`
	}
	if err := decode(r, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	opt, err := service.TempoOptions(req.Styles, req.Min, req.Max)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.svc.DetectTempoVideo(r.Context(), req.URL, opt, req.Refresh)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}
