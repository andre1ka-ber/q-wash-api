package ratelimit

import (
	"net/http"
	"sync"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
)

// StreamsPerUser returns a middleware allowing at most limit requests of one
// authenticated user to be in progress at once, or Passthrough when limit is
// not positive. It is meant for long-lived event streams and must run after
// authentication. Every route wrapped by the same returned value shares the
// budget.
func StreamsPerUser(limit int) func(http.Handler) http.Handler {
	if limit <= 0 {
		return Passthrough
	}
	var mu sync.Mutex
	open := map[uuid.UUID]int{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authUser, ok := httputil.AuthUser(w, r)
			if !ok {
				return
			}
			mu.Lock()
			if open[authUser.ID] >= limit {
				mu.Unlock()
				httputil.WriteError(w, r, apperror.TooManyRequests("too_many_requests", "too many open streams, close one and try again"))
				return
			}
			open[authUser.ID]++
			mu.Unlock()

			defer func() {
				mu.Lock()
				defer mu.Unlock()
				if open[authUser.ID]--; open[authUser.ID] == 0 {
					delete(open, authUser.ID)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
