package sandbox

import (
	"fmt"
	"net/http"
)

// Error is the typed error returned by both the HTTP client and the local
// runner. Status is the HTTP status a server should use when exposing it.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// NewError builds a typed error. Code is a stable machine-readable identifier
// such as "bad_request", "busy" or "timeout".
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Errorf is NewError with formatting.
func Errorf(status int, code, format string, args ...any) *Error {
	return NewError(status, code, fmt.Sprintf(format, args...))
}

func BadRequest(message string) *Error {
	return NewError(http.StatusBadRequest, "bad_request", message)
}

func Unauthorized(message string) *Error {
	return NewError(http.StatusUnauthorized, "unauthorized", message)
}

func Forbidden(message string) *Error {
	return NewError(http.StatusForbidden, "forbidden", message)
}

func NotFound(message string) *Error {
	return NewError(http.StatusNotFound, "not_found", message)
}

func Conflict(code, message string) *Error {
	return NewError(http.StatusConflict, code, message)
}

func TooLarge(message string) *Error {
	return NewError(http.StatusRequestEntityTooLarge, "too_large", message)
}

func UnsupportedMediaType(message string) *Error {
	return NewError(http.StatusUnsupportedMediaType, "unsupported_media_type", message)
}

func Internal(message string) *Error {
	return NewError(http.StatusInternalServerError, "internal", message)
}
