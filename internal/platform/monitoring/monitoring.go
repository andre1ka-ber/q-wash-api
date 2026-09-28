// Package monitoring wraps Sentry error reporting, mirroring
// internal/platform/push's shape: an empty DSN disables it (Init becomes a
// no-op, Middleware becomes a passthrough), so local dev and tests run with
// no Sentry project configured at all.
package monitoring

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
)

// Init configures the Sentry SDK. Called once at startup; a no-op when dsn
// is empty. environment is attached to every reported event (e.g.
// "production").
func Init(dsn, environment string) error {
	if dsn == "" {
		slog.Warn("error monitoring disabled: SENTRY_DSN not configured")
		return nil
	}
	return sentry.Init(sentry.ClientOptions{
		Dsn:         dsn,
		Environment: environment,
	})
}

// Middleware reports panics from the wrapped handler to Sentry and
// re-panics (Repanic: true) so the existing chi.Recoverer, mounted outside
// this middleware, still turns them into a 500 response. A no-op passthrough
// when dsn is empty, so it's safe to mount unconditionally.
func Middleware(dsn string) func(http.Handler) http.Handler {
	if dsn == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	handler := sentryhttp.New(sentryhttp.Options{Repanic: true})
	return handler.Handle
}

// Flush blocks until buffered events are sent or the timeout elapses. Call
// before process exit so a crash's own report isn't lost.
func Flush(timeout time.Duration) {
	sentry.Flush(timeout)
}
