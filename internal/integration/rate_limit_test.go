//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"q-wash-api/internal/config"
	"q-wash-api/internal/user"
)

// rawRequest sends a request with arbitrary headers (testEnv.do cannot) and
// returns status plus the decoded JSON error code, if any.
func (e *testEnv) rawRequest(t *testing.T, method, path string, headers map[string]string, body any) (int, string) {
	t.Helper()
	var buf bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = *bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.baseURL+path, &buf)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&parsed) // HTML/empty bodies just leave Code empty
	return resp.StatusCode, parsed.Error.Code
}

func withRateLimit(rl config.RateLimitConfig) func(*config.Config) {
	return func(c *config.Config) { c.RateLimit = rl }
}

func TestRateLimit_LoginLockoutPerUsername(t *testing.T) {
	env := newTestEnv(t, withRateLimit(config.RateLimitConfig{LoginMaxFailures: 3, LoginLockout: 15 * time.Minute}))
	phone := uniquePhone(70)
	env.loginAs(t, phone, user.RoleStaff)
	env.setPasswordCredentials(t, phone, "locky", "Correct-pass-1")

	login := func(username, password string) (int, string) {
		return env.rawRequest(t, http.MethodPost, "/api/v1/auth/login", nil, map[string]any{"username": username, "password": password})
	}

	t.Run("successful login resets the failure count", func(t *testing.T) {
		for round := 0; round < 2; round++ {
			for i := 0; i < 2; i++ {
				if status, _ := login("locky", "wrong"); status != http.StatusUnauthorized {
					t.Fatalf("round %d wrong #%d: want 401, got %d", round, i+1, status)
				}
			}
			if status, _ := login("locky", "Correct-pass-1"); status != http.StatusOK {
				t.Fatalf("round %d: correct password must still work, got %d", round, status)
			}
		}
	})

	t.Run("third consecutive failure locks the username, even for the right password", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if status, _ := login("locky", "wrong"); status != http.StatusUnauthorized {
				t.Fatalf("wrong #%d: want 401, got %d", i+1, status)
			}
		}
		status, code := login("locky", "Correct-pass-1")
		if status != http.StatusTooManyRequests || code != "login_locked" {
			t.Fatalf("want 429 login_locked, got %d %q", status, code)
		}
	})

	t.Run("lockout is case-insensitive on the username", func(t *testing.T) {
		if status, code := login("LOCKY", "Correct-pass-1"); status != http.StatusTooManyRequests || code != "login_locked" {
			t.Fatalf("want 429 login_locked, got %d %q", status, code)
		}
	})

	t.Run("an unknown username locks the same way, so lockout reveals nothing", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if status, code := login("no-such-user", "x"); status != http.StatusUnauthorized || code != "invalid_credentials" {
				t.Fatalf("attempt %d: want 401 invalid_credentials, got %d %q", i+1, status, code)
			}
		}
		if status, code := login("no-such-user", "x"); status != http.StatusTooManyRequests || code != "login_locked" {
			t.Fatalf("want 429 login_locked, got %d %q", status, code)
		}
	})
}

func TestRateLimit_PublicAuthEndpointsPerIP(t *testing.T) {
	env := newTestEnv(t, withRateLimit(config.RateLimitConfig{LoginPerMinute: 2, OTPRequestPerHour: 2, OTPVerifyPerHour: 2}))

	expectBlockedOnThird := func(name, path string, body func(i int) any) {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 2; i++ {
				if status, _ := env.rawRequest(t, http.MethodPost, path, nil, body(i)); status == http.StatusTooManyRequests {
					t.Fatalf("request %d must not be rate limited", i+1)
				}
			}
			status, code := env.rawRequest(t, http.MethodPost, path, nil, body(2))
			if status != http.StatusTooManyRequests || code != "too_many_requests" {
				t.Fatalf("third request: want 429 too_many_requests, got %d %q", status, code)
			}
		})
	}

	expectBlockedOnThird("login", "/api/v1/auth/login", func(int) any {
		return map[string]any{"username": "nobody", "password": "x"}
	})
	// Different phone each time: the existing per-phone cooldown would not
	// stop this, which is exactly the SMS-pumping case.
	expectBlockedOnThird("otp request", "/api/v1/auth/otp/request", func(i int) any {
		return map[string]any{"phone_number": uniquePhone(int64(80 + i))}
	})
	expectBlockedOnThird("otp verify", "/api/v1/auth/otp/verify", func(i int) any {
		return map[string]any{"phone_number": uniquePhone(int64(90 + i)), "code": "000000"}
	})
}

func TestRateLimit_QRScanPerIP(t *testing.T) {
	env := newTestEnv(t, withRateLimit(config.RateLimitConfig{QRScanPerMinute: 2}))

	// Both scan paths draw on one budget. The token does not need to exist:
	// an unknown token still renders the "invalid code" page.
	paths := []string{"/q/NOSUCHTOKEN", "/api/v1/qr-codes/scan/NOSUCHTOKEN", "/q/NOSUCHTOKEN"}
	want := []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests}
	for i, p := range paths {
		if status, _ := env.rawRequest(t, http.MethodGet, p, nil, nil); status != want[i] {
			t.Fatalf("scan #%d %s: want %d, got %d", i+1, p, want[i], status)
		}
	}
}

func TestRateLimit_ForwardedForOnlyHonoredWhenProxyTrusted(t *testing.T) {
	login := func(env *testEnv, xff string) int {
		status, _ := env.rawRequest(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"X-Forwarded-For": xff}, map[string]any{"username": "nobody", "password": "x"})
		return status
	}
	limits := withRateLimit(config.RateLimitConfig{LoginPerMinute: 2})

	t.Run("untrusted: a spoofed header does not buy a fresh bucket", func(t *testing.T) {
		env := newTestEnv(t, limits)
		for i, xff := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
			status := login(env, xff)
			if wantBlocked := i == 2; wantBlocked != (status == http.StatusTooManyRequests) {
				t.Fatalf("request %d (%s): status %d", i+1, xff, status)
			}
		}
	})

	t.Run("trusted: each client IP behind the proxy has its own bucket", func(t *testing.T) {
		env := newTestEnv(t, limits, func(c *config.Config) { c.HTTP.TrustProxyHeaders = true })
		for _, xff := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
			if status := login(env, xff); status == http.StatusTooManyRequests {
				t.Fatalf("%s must not be limited by other clients' requests", xff)
			}
		}
	})

	t.Run("trusted: a client-prepended leftmost entry does not help", func(t *testing.T) {
		env := newTestEnv(t, limits, func(c *config.Config) { c.HTTP.TrustProxyHeaders = true })
		// The proxy appended the real client (9.9.9.9) after whatever the client sent.
		for i, spoof := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
			status := login(env, spoof+", 9.9.9.9")
			if wantBlocked := i == 2; wantBlocked != (status == http.StatusTooManyRequests) {
				t.Fatalf("request %d: status %d", i+1, status)
			}
		}
	})
}
