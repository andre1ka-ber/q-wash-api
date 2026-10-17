package httpserver

import (
	"net"
	"net/http"
	"strings"
)

// proxyClientIP rewrites r.RemoteAddr to the client IP reported by the single
// reverse proxy in front of the API. Install it only when that proxy is
// trusted (config TRUST_PROXY_HEADERS).
//
// It deliberately does not use chi's middleware.RealIP, which takes the
// leftmost X-Forwarded-For entry (the one a client controls) and so lets any
// client pick its own IP even behind a proxy that appends to the header. The
// entry that is trustworthy is the rightmost one, added by our own proxy, so
// that is the one used; X-Real-IP is the fallback when X-Forwarded-For is
// absent. A proxy chain longer than one hop is not supported. A missing or
// unparsable header leaves RemoteAddr as the TCP peer.
func proxyClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := clientIPFromHeaders(r); ip != "" {
			r.RemoteAddr = ip
		}
		next.ServeHTTP(w, r)
	})
}

func clientIPFromHeaders(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := validIP(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	return validIP(r.Header.Get("X-Real-IP"))
}

func validIP(s string) string {
	s = strings.TrimSpace(s)
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}
