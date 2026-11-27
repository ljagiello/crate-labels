package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ljagiello/crate-labels/internal/service"
)

// maxBody caps request bodies (500 edited labels fit comfortably).
const maxBody = 8 << 20

// apiError is a reply with a status and a message the UI shows verbatim.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &apiError{status: http.StatusUnprocessableEntity, msg: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) error {
	return &apiError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not encode the reply: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Del("Cache-Control")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// fail replies with the status that fits err.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	var fe *service.FieldError
	switch {
	case errors.As(err, &ae):
		writeError(w, ae.status, ae.msg)
		return
	case errors.As(err, &fe):
		writeError(w, http.StatusUnprocessableEntity, fe.Error())
		return
	case r.Context().Err() != nil:
		return // the client went away
	}
	status, user := service.StatusOf(err)
	if !user {
		s.opts.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, status, "Something went wrong: "+err.Error())
		return
	}
	writeError(w, status, err.Error())
}

// decode reads a JSON body into dst. An empty body is allowed when optional. Malformed
// JSON and wrong types are 422.
func decode(r *http.Request, dst any, optional bool) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return invalid("Could not read the request body.")
	}
	if len(body) > maxBody {
		return &apiError{status: http.StatusRequestEntityTooLarge, msg: "The request body is too large."}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if optional {
			return nil
		}
		return invalid("The request needs a JSON body.")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		var te *json.UnmarshalTypeError
		var fe *service.FieldError
		var se *json.SyntaxError
		switch {
		case errors.As(err, &fe):
			return fe
		case errors.As(err, &te):
			field := te.Field
			if field == "" {
				return invalid("The request body must be a JSON object.")
			}
			return invalid("%s: must be %s", field, jsonKind(te.Type.Kind().String()))
		case errors.As(err, &se):
			return invalid("The request body is not valid JSON.")
		}
		return invalid("The request body is not valid: %v", err)
	}
	return nil
}

func jsonKind(goKind string) string {
	switch goKind {
	case "string":
		return "a string"
	case "bool":
		return "true or false"
	case "int", "int64", "int32":
		return "a whole number"
	case "float64", "float32":
		return "a number"
	case "slice", "array":
		return "a list"
	case "map", "struct", "ptr":
		return "an object"
	}
	return "of another type"
}

// query reads URL query parameters with 422 for bad values.
type query struct {
	v   url.Values
	err error
}

func newQuery(r *http.Request) *query { return &query{v: r.URL.Query()} }

func (q *query) str(key, def string) string {
	if vs, ok := q.v[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return def
}

func (q *query) has(key string) bool { _, ok := q.v[key]; return ok }

func (q *query) boolean(key string, def bool) bool {
	s := strings.ToLower(strings.TrimSpace(q.str(key, "")))
	if !q.has(key) {
		return def
	}
	switch s {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	q.setErr(invalid("%s: must be true or false", key))
	return def
}

func (q *query) integer(key string, def, lo, hi int64) int64 {
	if !q.has(key) || strings.TrimSpace(q.str(key, "")) == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(q.str(key, "")), 10, 64)
	if err != nil {
		q.setErr(invalid("%s: must be a whole number", key))
		return def
	}
	if n < lo || n > hi {
		q.setErr(invalid("%s: must be between %d and %d", key, lo, hi))
		return def
	}
	return n
}

func (q *query) number(key string, def, lo, hi float64) float64 {
	if !q.has(key) || strings.TrimSpace(q.str(key, "")) == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(q.str(key, "")), 64)
	if err != nil || math.IsNaN(f) {
		q.setErr(invalid("%s: must be a number", key))
		return def
	}
	if f < lo || f > hi {
		q.setErr(invalid("%s: must be between %g and %g", key, lo, hi))
		return def
	}
	return f
}

func (q *query) oneOf(key, def string, allowed ...string) string {
	v := q.str(key, def)
	if !q.has(key) {
		return def
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	q.setErr(invalid("%s: must be one of %s", key, strings.Join(allowed, ", ")))
	return def
}

func (q *query) setErr(err error) {
	if q.err == nil {
		q.err = err
	}
}

// pathInt reads an integer path segment (422 when it is not one).
func pathInt(r *http.Request, name string) (int64, error) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, invalid("%s: must be a whole number", name)
	}
	return n, nil
}

// labelSource is API.md LabelSource as posted.
type labelSource struct {
	Data      *service.Label `json:"data"`
	ReleaseID *int64         `json:"release_id"`
	Ref       *string        `json:"ref"`
}

func (l labelSource) source() service.Source {
	src := service.Source{Data: l.Data, ReleaseID: l.ReleaseID}
	if l.Ref != nil {
		src.Ref = *l.Ref
	}
	return src
}

// headerNumber writes a number for a preview header with at least one decimal place
// ("203.2", "300.0", "48.0"), so the value always reads as a measurement.
func headerNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func jsonBytes(v any) ([]byte, error) { return json.Marshal(v) }
