// Package ratelimit is a small in-memory fixed-window limiter keyed by
// client IP, for the few unauthenticated write endpoints (currently the
// public connection-request form). State is per process: with several API
// replicas each enforces its own window, which is acceptable for abuse
// control but not for strict quotas.
package ratelimit

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
)

type window struct {
	start time.Time
	count int
}

// Limiter allows at most limit requests per key per window.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*window
}

func New(limit int, per time.Duration) *Limiter {
	return &Limiter{limit: limit, window: per, now: time.Now, buckets: map[string]*window{}}
}

// Allow records one hit for key and reports whether it is within the limit.
// When it is not, retryAfter is how long until the key's window resets.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Opportunistic sweep so the map cannot grow without bound under a
	// flood of distinct keys.
	if len(l.buckets) > 10000 {
		for k, w := range l.buckets {
			if now.Sub(w.start) >= l.window {
				delete(l.buckets, k)
			}
		}
	}

	w, found := l.buckets[key]
	if !found || now.Sub(w.start) >= l.window {
		l.buckets[key] = &window{start: now, count: 1}
		return true, 0
	}
	if w.count >= l.limit {
		return false, w.start.Add(l.window).Sub(now)
	}
	w.count++
	return true, 0
}

// Middleware rejects over-limit requests with 429 too_many_requests and a
// Retry-After header. The key is the request's remote IP; chi's RealIP
// middleware (installed in httpserver.NewRouter) has already rewritten
// RemoteAddr from X-Forwarded-For / X-Real-IP, so this is only trustworthy
// behind a proxy that sets those headers itself.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, retry := l.Allow(clientIP(r))
		if !ok {
			secs := int(retry.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			httputil.WriteError(w, r, apperror.TooManyRequests("too_many_requests", "too many requests, try again later"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
