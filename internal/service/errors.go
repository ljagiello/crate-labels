// Package service is crate-labels' application layer: Discogs lookups for the UI,
// previews, and an in-process print queue with one sequential worker per printer.
//
// Job state lives in SQLite (store.Job) and is pushed to subscribers such as the web
// UI's SSE stream. A job left queued or printing when the server stops is marked
// interrupted at the next server start and never resumes on its own.
package service

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/ljagiello/crate-labels/internal/discogs"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/store"
)

// Error is a request the service cannot fulfil. Status is the HTTP status to reply with
// (400 unless set) and Msg is fit for the user.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Errorf builds a 400 *Error.
func Errorf(format string, args ...any) error {
	return &Error{Status: http.StatusBadRequest, Msg: fmt.Sprintf(format, args...)}
}

// StatusOf maps any error from the service, store, Discogs or printer layers to an HTTP
// status, and reports whether its message is meant for the user as it is.
func StatusOf(err error) (status int, userFacing bool) {
	var se *Error
	var de *discogs.Error
	var te *discogs.TokenError
	var nf *store.NotFoundError
	var inv *store.InvalidError
	var pe *printer.Error
	switch {
	case errors.As(err, &se):
		if se.Status == 0 {
			return http.StatusBadRequest, true
		}
		return se.Status, true
	case errors.As(err, &de):
		return de.Status, true
	case errors.As(err, &te), errors.As(err, &inv), errors.As(err, &pe):
		return http.StatusBadRequest, true
	case errors.As(err, &nf):
		return http.StatusNotFound, true
	}
	return http.StatusInternalServerError, false
}
