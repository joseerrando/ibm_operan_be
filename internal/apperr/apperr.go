// Package apperr defines the uniform API error: { "error": { code, message, details } }.
package apperr

import "net/http"

type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func (e *Error) With(details any) *Error {
	c := *e
	c.Details = details
	return &c
}

func BadRequest(msg string) *Error { return New(http.StatusBadRequest, "BAD_REQUEST", msg) }
func Validation(msg string) *Error { return New(http.StatusUnprocessableEntity, "VALIDATION", msg) }
func NotFound(msg string) *Error   { return New(http.StatusNotFound, "NOT_FOUND", msg) }
func Forbidden() *Error {
	return New(http.StatusForbidden, "FORBIDDEN", "Kamu bukan anggota keluarga ini.")
}
func Unauthorized(msg string) *Error   { return New(http.StatusUnauthorized, "UNAUTHORIZED", msg) }
func Conflict(code, msg string) *Error { return New(http.StatusConflict, code, msg) }
func Internal() *Error {
	return New(http.StatusInternalServerError, "INTERNAL", "Terjadi gangguan di server. Coba lagi sebentar lagi.")
}
func RateLimited() *Error {
	return New(http.StatusTooManyRequests, "RATE_LIMITED", "Terlalu banyak permintaan. Coba lagi beberapa menit lagi.")
}
func AIUnavailable(msg string) *Error {
	return New(http.StatusBadGateway, "AI_UNAVAILABLE", msg)
}
