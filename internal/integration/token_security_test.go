//go:build integration

package integration

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"q-wash-api/internal/config"
	"q-wash-api/internal/user"
)

func TestAccessTokenLifetime_ShorterForStaffRoles(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) { c.Auth.StaffAccessTokenTTL = 5 * time.Minute })

	staffPhone := uniquePhone(100)
	env.loginAs(t, staffPhone, user.RoleStaff)
	env.setPasswordCredentials(t, staffPhone, "ttl-staff", "Correct-pass-1")

	lifetime := func(resp apiResponse) time.Duration {
		t.Helper()
		exp, err := time.Parse(time.RFC3339, resp.str("access_token_expires_at"))
		if err != nil {
			t.Fatalf("parse expiry from %v: %v", resp.body, err)
		}
		return time.Until(exp)
	}

	staff := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "ttl-staff", "password": "Correct-pass-1"})
	if staff.status != http.StatusOK {
		t.Fatalf("staff login: %d %v", staff.status, staff.body)
	}
	if got := lifetime(staff); got > 5*time.Minute || got < 4*time.Minute {
		t.Errorf("staff access token lifetime = %v, want about 5m", got)
	}

	// A customer keeps the normal 15 minutes.
	customerPhone := uniquePhone(101)
	env.do(t, http.MethodPost, "/api/v1/auth/otp/request", "", map[string]any{"phone_number": customerPhone})
	code := env.sms.lastCodeFor(t, customerPhone)
	customer := env.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]any{"phone_number": customerPhone, "code": code})
	if customer.status != http.StatusOK {
		t.Fatalf("customer verify: %d %v", customer.status, customer.body)
	}
	if got := lifetime(customer); got < 14*time.Minute {
		t.Errorf("customer access token lifetime = %v, want about 15m", got)
	}
}

func TestRefresh_ConcurrentRotationOfOneTokenSucceedsOnce(t *testing.T) {
	env := newTestEnv(t)
	phone := uniquePhone(102)
	env.loginAs(t, phone, user.RoleStaff)
	env.setPasswordCredentials(t, phone, "rotate-staff", "Correct-pass-1")
	login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "rotate-staff", "password": "Correct-pass-1"})
	refreshToken, _ := login.body["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatalf("no refresh token: %v", login.body)
	}

	const attempts = 8
	var wg sync.WaitGroup
	statuses := make([]int, attempts)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = env.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refresh_token": refreshToken}).status
		}()
	}
	wg.Wait()

	ok, unauthorized := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusUnauthorized:
			unauthorized++
		}
	}
	if ok != 1 || unauthorized != attempts-1 {
		t.Fatalf("want exactly 1 success and %d 401s, got statuses %v", attempts-1, statuses)
	}
}

// lockedBuffer lets the server's goroutines write while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAuditLog_AuthEventsAreRecordedWithoutSecrets(t *testing.T) {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	env := newTestEnv(t)
	phone := uniquePhone(103)
	env.loginAs(t, phone, user.RoleStaff)
	env.setPasswordCredentials(t, phone, "audit-staff", "Correct-pass-1")

	env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "audit-staff", "password": "WrongSecret-9"})
	login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "audit-staff", "password": "Correct-pass-1"})
	access, _ := login.body["access_token"].(string)
	refreshToken, _ := login.body["refresh_token"].(string)
	env.do(t, http.MethodPatch, "/api/v1/auth/password", access, map[string]any{"current_password": "Correct-pass-1", "new_password": "NewPass-12345a"})
	// The login above rotated nothing, but the password change revoked this
	// refresh token: presenting it again is a reuse of a revoked token.
	env.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refresh_token": refreshToken})

	out := logs.String()
	for _, want := range []string{
		"event=auth.login_failed", "username=audit-staff", "code=invalid_credentials",
		"event=auth.login ", "event=auth.password_changed",
		"event=auth.refresh_failed", "event=auth.refresh_reuse", "reason=revoked_token_presented",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q", want)
		}
	}
	for _, secret := range []string{"WrongSecret-9", "Correct-pass-1", "NewPass-12345a", refreshToken, access} {
		if strings.Contains(out, secret) {
			t.Errorf("log leaks a secret: %q", secret[:6]+"...")
		}
	}
}
