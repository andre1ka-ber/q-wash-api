// Package audit writes structured security-relevant events (logins, lockouts,
// credential resets, password changes) to the application log, so abuse and
// account takeovers leave a trail. Events are ordinary slog records with a
// fixed message "audit" and an "event" attribute to grep or alert on; the
// request's client IP and request id are added automatically.
//
// Never pass secrets (passwords, OTP codes, tokens) as attributes, and mask
// phone numbers with MaskPhone.
package audit

import (
	"errors"
	"log/slog"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"q-wash-api/internal/apperror"
)

// Info records a successful or neutral security event.
func Info(r *http.Request, event string, attrs ...any) {
	slog.InfoContext(r.Context(), "audit", withRequest(r, event, attrs)...)
}

// Warn records a failed or suspicious security event.
func Warn(r *http.Request, event string, attrs ...any) {
	slog.WarnContext(r.Context(), "audit", withRequest(r, event, attrs)...)
}

func withRequest(r *http.Request, event string, attrs []any) []any {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	base := []any{"event", event, "ip", ip}
	if id := middleware.GetReqID(r.Context()); id != "" {
		base = append(base, "request_id", id)
	}
	return append(base, attrs...)
}

// MaskPhone keeps only the last four digits, enough to tell numbers apart in
// a log without storing the whole number.
func MaskPhone(phone string) string {
	if len(phone) <= 4 {
		return "***"
	}
	return "***" + phone[len(phone)-4:]
}

// ErrorCode is the stable API error code carried by err ("internal_error" for
// anything that is not an *apperror.Error), for logging why an attempt failed.
func ErrorCode(err error) string {
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return "internal_error"
}

// Truncate bounds an attacker-controlled string (e.g. a submitted username)
// before it is logged.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
