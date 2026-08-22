// Package apperror defines a typed application error that carries an HTTP
// status and a stable machine-readable code, so handlers can translate any
// error into a consistent JSON response without switching on error strings.
package apperror

import "net/http"

type Error struct {
	Status  int
	Code    string
	Message string
	Err     error // underlying cause, never exposed to the client
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func BadRequest(code, message string) *Error { return New(http.StatusBadRequest, code, message) }
func Unauthorized(code, message string) *Error {
	return New(http.StatusUnauthorized, code, message)
}
func Forbidden(code, message string) *Error { return New(http.StatusForbidden, code, message) }
func NotFound(code, message string) *Error  { return New(http.StatusNotFound, code, message) }
func Conflict(code, message string) *Error  { return New(http.StatusConflict, code, message) }
func TooManyRequests(code, message string) *Error {
	return New(http.StatusTooManyRequests, code, message)
}
func UnprocessableEntity(code, message string) *Error {
	return New(http.StatusUnprocessableEntity, code, message)
}

func Internal(err error) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal error", Err: err}
}
