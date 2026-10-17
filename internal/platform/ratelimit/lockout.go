package ratelimit

import (
	"net/http"
	"sync"
	"time"
)

type lockEntry struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

// Lockout blocks a key (e.g. a username) for a while after too many
// consecutive failures. A nil *Lockout never locks anything, so callers can
// leave it unset to disable the feature.
type Lockout struct {
	maxFailures int
	duration    time.Duration
	now         func() time.Time

	mu      sync.Mutex
	entries map[string]*lockEntry
}

// NewLockout locks a key for duration once it has maxFailures failures with no
// success and no gap longer than duration between them. It returns nil
// (disabled) when maxFailures or duration is not positive.
func NewLockout(maxFailures int, duration time.Duration) *Lockout {
	if maxFailures <= 0 || duration <= 0 {
		return nil
	}
	return &Lockout{maxFailures: maxFailures, duration: duration, now: time.Now, entries: map[string]*lockEntry{}}
}

// Locked reports whether key is currently locked and, if so, for how much
// longer.
func (l *Lockout) Locked(key string) (locked bool, retryAfter time.Duration) {
	if l == nil {
		return false, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok || !now.Before(e.lockedUntil) {
		return false, 0
	}
	return true, e.lockedUntil.Sub(now)
}

// Fail records one failure for key and locks it when the limit is reached.
func (l *Lockout) Fail(key string) {
	if l == nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Opportunistic sweep so a flood of distinct keys (including made-up
	// usernames) cannot grow the map without bound.
	if len(l.entries) > 10000 {
		for k, e := range l.entries {
			if !now.Before(e.lockedUntil) && now.Sub(e.lastFailure) >= l.duration {
				delete(l.entries, k)
			}
		}
	}

	e, ok := l.entries[key]
	if !ok || (!now.Before(e.lockedUntil) && now.Sub(e.lastFailure) >= l.duration) {
		e = &lockEntry{}
		l.entries[key] = e
	}
	e.failures++
	e.lastFailure = now
	if e.failures >= l.maxFailures {
		e.failures = 0
		e.lockedUntil = now.Add(l.duration)
	}
}

// Reset clears key's failure count (call after a successful login).
func (l *Lockout) Reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// Passthrough is a middleware that does nothing, for a limiter that is
// configured off.
func Passthrough(next http.Handler) http.Handler { return next }

// PerIP returns a middleware allowing at most limit requests per client IP per
// window, or Passthrough when limit is not positive.
func PerIP(limit int, per time.Duration) func(http.Handler) http.Handler {
	if limit <= 0 {
		return Passthrough
	}
	return New(limit, per).Middleware
}
