// Package apperr defines typed application errors that map cleanly to
// transport-level status codes (HTTP, SMTP) without leaking internals.
package apperr

import (
	"errors"
	"fmt"
)

// Kind classifies an error for transport mapping.
type Kind int

const (
	KindInternal Kind = iota
	KindInvalid
	KindNotFound
	KindConflict
	KindUnauthorized
	KindForbidden
	KindRateLimited
	KindPaymentRequired
)

// Error is an application error with a stable machine-readable code.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func newErr(k Kind, code, msg string) *Error { return &Error{Kind: k, Code: code, Message: msg} }

func Invalid(code, msg string) *Error         { return newErr(KindInvalid, code, msg) }
func NotFound(code, msg string) *Error        { return newErr(KindNotFound, code, msg) }
func Conflict(code, msg string) *Error        { return newErr(KindConflict, code, msg) }
func Unauthorized(code, msg string) *Error    { return newErr(KindUnauthorized, code, msg) }
func Forbidden(code, msg string) *Error       { return newErr(KindForbidden, code, msg) }
func RateLimited(code, msg string) *Error     { return newErr(KindRateLimited, code, msg) }
func PaymentRequired(code, msg string) *Error { return newErr(KindPaymentRequired, code, msg) }

// Internal wraps an unexpected error. The cause is logged, never shown to clients.
func Internal(err error) *Error {
	return &Error{Kind: KindInternal, Code: "internal", Message: "internal error", Err: err}
}

// As extracts an *Error from err, wrapping unknown errors as internal.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}

// Is reports whether err is an application error of the given kind.
func Is(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}
