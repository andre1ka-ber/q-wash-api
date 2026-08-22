// Package httpserver builds the chi router and top-level middleware stack.
package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"gorm.io/gorm"
)

// NewRouter builds the root router with top-level middleware and /health,
// and returns the "/api/v1" sub-router so main.go can mount each feature's
// routes onto it (auth, users, washing points, services, cars, queue,
// notifications, ...) without this package needing to know about any of
// them.
//
// corsAllowedOrigins lets a separately-hosted frontend call this API from a
// browser; pass []string{"*"} to allow any origin (fine for local dev).
func NewRouter(database *gorm.DB, corsAllowedOrigins []string) (*chi.Mux, chi.Router) {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(skipTimeoutForSSE(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false, // auth is a Bearer token, not cookies
		MaxAge:           300,
	}))

	r.Get("/health", healthHandler(database))
	r.Get("/openapi.yaml", openapiSpecHandler)
	r.Get("/docs", swaggerUIHandler)

	v1 := chi.NewRouter()
	r.Mount("/api/v1", v1)

	return r, v1
}

// skipTimeoutForSSE applies middleware.Timeout to every request except
// SSE streams (identified by a path ending in "/events"), which are
// intentionally long-lived. middleware.Timeout's wrapped ResponseWriter
// doesn't implement http.Flusher, so a streaming handler bypasses it
// entirely rather than just getting a longer duration.
func skipTimeoutForSSE(d time.Duration) func(http.Handler) http.Handler {
	timeoutMW := middleware.Timeout(d)
	return func(next http.Handler) http.Handler {
		wrapped := timeoutMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/events") {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

func healthHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		code := http.StatusOK

		sqlDB, err := database.DB()
		if err != nil || sqlDB.PingContext(r.Context()) != nil {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	}
}
