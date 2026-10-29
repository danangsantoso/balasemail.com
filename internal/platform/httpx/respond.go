// Package httpx contains HTTP helpers shared by every module's handlers.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
)

// MaxBodyBytes caps JSON request bodies.
const MaxBodyBytes = 1 << 20

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// JSON writes v as a JSON response.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Error writes err as a JSON error, mapping apperr kinds to status codes.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	e := apperr.As(err)
	status := StatusFor(e.Kind)
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path)
	}
	var body errorBody
	body.Error.Code = e.Code
	body.Error.Message = e.Message
	JSON(w, status, body)
}

// StatusFor maps an error kind to an HTTP status.
func StatusFor(k apperr.Kind) int {
	switch k {
	case apperr.KindInvalid:
		return http.StatusUnprocessableEntity
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindUnauthorized:
		return http.StatusUnauthorized
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindRateLimited:
		return http.StatusTooManyRequests
	case apperr.KindPaymentRequired:
		return http.StatusPaymentRequired
	default:
		return http.StatusInternalServerError
	}
}

// Decode reads a JSON body into v, rejecting unknown fields and oversize bodies.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.Invalid("invalid_body", "request body is empty")
		}
		return apperr.Invalid("invalid_body", "malformed JSON: "+err.Error())
	}
	return nil
}

// Page holds limit/offset pagination parameters.
type Page struct {
	Limit  int
	Offset int
}

// PageFrom parses ?limit=&offset= with sane bounds.
func PageFrom(r *http.Request) Page {
	p := Page{Limit: 50}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		p.Limit = min(v, 200)
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		p.Offset = v
	}
	return p
}

// List is the envelope for collection responses.
type List[T any] struct {
	Data   []T `json:"data"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// Respond writes v with status, or the error if err is non-nil.
func Respond(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, status, v)
}
