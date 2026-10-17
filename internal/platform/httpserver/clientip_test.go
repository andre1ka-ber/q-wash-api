package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func remoteAddrSeenBy(t *testing.T, trust bool, headers map[string]string) string {
	t.Helper()
	r, _ := NewRouter(nil, []string{"*"}, "", trust)
	// /health needs a DB; use a bare route on the root router instead.
	var got string
	r.Get("/ip", func(_ http.ResponseWriter, req *http.Request) { got = req.RemoteAddr })

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestProxyClientIP(t *testing.T) {
	cases := []struct {
		name    string
		trust   bool
		headers map[string]string
		want    string
	}{
		{"untrusted ignores forwarded headers", false, map[string]string{"X-Forwarded-For": "6.6.6.6"}, "10.0.0.1:5555"},
		{"trusted uses the proxy's single entry", true, map[string]string{"X-Forwarded-For": "203.0.113.7"}, "203.0.113.7"},
		{"trusted uses the rightmost entry, not the client-supplied leftmost", true, map[string]string{"X-Forwarded-For": "6.6.6.6, 203.0.113.7"}, "203.0.113.7"},
		{"trusted falls back to X-Real-IP", true, map[string]string{"X-Real-IP": "203.0.113.9"}, "203.0.113.9"},
		{"trusted without headers keeps the peer", true, nil, "10.0.0.1:5555"},
		{"trusted ignores garbage", true, map[string]string{"X-Forwarded-For": "not-an-ip"}, "10.0.0.1:5555"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := remoteAddrSeenBy(t, tc.trust, tc.headers); got != tc.want {
				t.Fatalf("RemoteAddr = %q, want %q", got, tc.want)
			}
		})
	}
}
