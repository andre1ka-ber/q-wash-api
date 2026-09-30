//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

// TestChangePassword covers the self-service "change my own password"
// endpoint (PATCH /auth/password) — distinct from the admin/staff-
// triggered reset covered in credentials_test.go, which regenerates a
// random one-time password instead of letting the caller pick one.
func TestChangePassword(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(92), user.RoleAdmin)

	newStaffSession := func(t *testing.T, name string) (username, password, access, refreshToken string) {
		t.Helper()
		created := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
			"name": name, "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
		})
		if created.status != http.StatusCreated {
			t.Fatalf("create wp: expected 201, got %d (%v)", created.status, created.body)
		}
		staffCreds := credential(t, created.body, "credentials", "staff")
		username, _ = staffCreds["username"].(string)
		password, _ = staffCreds["password"].(string)

		login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": password})
		if login.status != http.StatusOK {
			t.Fatalf("staff login: expected 200, got %d (%v)", login.status, login.body)
		}
		access, _ = login.body["access_token"].(string)
		refreshToken, _ = login.body["refresh_token"].(string)
		return
	}

	t.Run("changing to a valid new password works, revokes the old session, and keeps the current one working", func(t *testing.T) {
		username, oldPassword, access, oldRefreshToken := newStaffSession(t, "Change Password Wash")

		resp := env.do(t, http.MethodPatch, "/api/v1/auth/password", access, map[string]any{
			"current_password": oldPassword, "new_password": "NewPass123",
		})
		if resp.status != http.StatusOK {
			t.Fatalf("change password: expected 200, got %d (%v)", resp.status, resp.body)
		}
		newAccess, _ := resp.body["access_token"].(string)
		if newAccess == "" {
			t.Fatal("expected a fresh access token in the response")
		}

		if r := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": oldPassword}); r.status != http.StatusUnauthorized {
			t.Errorf("expected the old password to stop working, got %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refresh_token": oldRefreshToken}); r.status != http.StatusUnauthorized {
			t.Errorf("expected the pre-change refresh token to be revoked, got %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": "NewPass123"}); r.status != http.StatusOK {
			t.Errorf("expected the new password to work, got %d (%v)", r.status, r.body)
		}

		// The token pair returned by the change-password call itself keeps working
		// (this session wasn't logged out by its own request).
		if r := env.do(t, http.MethodGet, "/api/v1/washing-points/does-not-matter/credentials", newAccess, nil); r.status == http.StatusUnauthorized {
			t.Errorf("expected the freshly issued access token to be valid, got 401 (%v)", r.body)
		}
	})

	t.Run("wrong current password is rejected and nothing changes", func(t *testing.T) {
		username, oldPassword, access, _ := newStaffSession(t, "Wrong Current Password Wash")

		resp := env.do(t, http.MethodPatch, "/api/v1/auth/password", access, map[string]any{
			"current_password": "not-the-real-password", "new_password": "NewPass123",
		})
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
		if r := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": oldPassword}); r.status != http.StatusOK {
			t.Errorf("expected the original password to still work, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("a new password failing the policy is rejected", func(t *testing.T) {
		_, oldPassword, access, _ := newStaffSession(t, "Weak Password Wash")

		cases := map[string]string{
			"too short":    "Ab1",
			"no digit":     "Abcdefgh",
			"no uppercase": "abcdefg1",
			"no lowercase": "ABCDEFG1",
		}
		for name, weak := range cases {
			t.Run(name, func(t *testing.T) {
				resp := env.do(t, http.MethodPatch, "/api/v1/auth/password", access, map[string]any{
					"current_password": oldPassword, "new_password": weak,
				})
				if resp.status != http.StatusBadRequest {
					t.Errorf("expected 400 for %q, got %d (%v)", weak, resp.status, resp.body)
				}
			})
		}
	})

	t.Run("requires authentication", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, "/api/v1/auth/password", "", map[string]any{
			"current_password": "whatever", "new_password": "NewPass123",
		})
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a customer account (no password login) is rejected", func(t *testing.T) {
		customerAccess := env.loginAs(t, uniquePhone(93), "")

		resp := env.do(t, http.MethodPatch, "/api/v1/auth/password", customerAccess, map[string]any{
			"current_password": "whatever", "new_password": "NewPass123",
		})
		if resp.status != http.StatusForbidden {
			t.Errorf("expected 403, got %d (%v)", resp.status, resp.body)
		}
	})
}
