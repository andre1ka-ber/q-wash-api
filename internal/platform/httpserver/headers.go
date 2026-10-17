package httpserver

import "net/http"

// securityHeaders sets the response headers every API response should carry:
//   - X-Content-Type-Options: nosniff — browsers must not guess a type for a
//     JSON or uploaded-file response;
//   - X-Frame-Options: DENY — nothing here is meant to be framed;
//   - Referrer-Policy: no-referrer;
//   - in production, Strict-Transport-Security (one year) so a browser that
//     has seen the API over HTTPS refuses to talk plain HTTP to it. Browsers
//     ignore the header on plain-HTTP responses, so it is harmless behind a
//     TLS-terminating proxy.
//
// No Content-Security-Policy is set globally because the QR scan page is HTML
// with an inline style; uploaded files get a strict one in MountStatic.
func securityHeaders(production bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			if production {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
