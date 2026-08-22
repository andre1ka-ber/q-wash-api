// Package httputil holds small shared helpers for writing JSON responses
// and decoding JSON requests consistently across every feature handler.
package httputil

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
)

func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// WriteError translates err into the API's standard error envelope. If err
// is (or wraps) an *apperror.Error, its status/code/message are used as-is;
// otherwise it's treated as an unexpected internal error, logged, and
// masked from the client.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		if appErr.Status >= http.StatusInternalServerError {
			slog.ErrorContext(r.Context(), "request failed", "err", appErr.Err, "code", appErr.Code)
		}
		WriteJSON(w, appErr.Status, errorBody(appErr.Code, appErr.Message))
		return
	}

	slog.ErrorContext(r.Context(), "unhandled request error", "err", err)
	WriteJSON(w, http.StatusInternalServerError, errorBody("internal_error", "internal error"))
}

func errorBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}

// DecodeJSON decodes the request body into v, rejecting unknown fields so
// clients get an early error instead of silently ignored typos.
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperror.BadRequest("invalid_body", "invalid request body")
	}
	return nil
}

// ParseUUIDParam reads and parses the chi URL param named param as a UUID.
func ParseUUIDParam(r *http.Request, param string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		return uuid.Nil, apperror.BadRequest("invalid_id", "invalid "+param)
	}
	return id, nil
}

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

type Pagination struct {
	Page     int
	PageSize int
}

// ParsePagination reads ?page= and ?page_size= (both optional, 1-indexed),
// clamping invalid or out-of-range values to sane defaults rather than
// erroring — pagination params are a convenience, not something worth
// rejecting a request over.
func ParsePagination(r *http.Request) Pagination {
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parsePositiveInt(r.URL.Query().Get("page_size"), DefaultPageSize)
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return Pagination{Page: page, PageSize: pageSize}
}

func (p Pagination) Offset() int { return (p.Page - 1) * p.PageSize }
func (p Pagination) Limit() int  { return p.PageSize }

func parsePositiveInt(raw string, fallback int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

type PaginatedBody[T any] struct {
	Items    []T   `json:"items"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
}

// WritePaginated writes the standard {items, page, page_size, total} envelope.
func WritePaginated[T any](w http.ResponseWriter, status int, items []T, p Pagination, total int64) {
	WriteJSON(w, status, PaginatedBody[T]{Items: items, Page: p.Page, PageSize: p.PageSize, Total: total})
}
