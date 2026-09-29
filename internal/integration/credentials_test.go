//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

// credential pulls {username, password} (or {username} alone) out of a
// nested credentials/staff/worker object in a decoded JSON response body.
func credential(t *testing.T, body map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := body
	for _, key := range path {
		next, ok := cur[key].(map[string]any)
		if !ok {
			t.Fatalf("expected %v to be present and an object in %v", path, body)
		}
		cur = next
	}
	return cur
}

// TestWashingPointCredentials covers auto-provisioning staff+worker logins
// on washing point creation (both POST /washing-points and
// connectionrequest.Manager.Approve), username collision handling, RBAC on
// the new admin credentials endpoints, and password reset revoking the
// account's existing sessions.
func TestWashingPointCredentials(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(90), user.RoleAdmin)
	staffPhone := uniquePhone(91)
	env.loginAs(t, staffPhone, user.RoleStaff)

	t.Run("creating a point returns working staff and worker logins", func(t *testing.T) {
		created := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
			"name": "Pegasus Detailing", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
		})
		if created.status != http.StatusCreated {
			t.Fatalf("create wp: expected 201, got %d (%v)", created.status, created.body)
		}
		wpID := created.str("id")

		staffCreds := credential(t, created.body, "credentials", "staff")
		workerCreds := credential(t, created.body, "credentials", "worker")
		staffUsername, _ := staffCreds["username"].(string)
		staffPassword, _ := staffCreds["password"].(string)
		workerUsername, _ := workerCreds["username"].(string)
		workerPassword, _ := workerCreds["password"].(string)
		if staffUsername == "" || staffPassword == "" || workerUsername == "" || workerPassword == "" {
			t.Fatalf("expected non-empty staff/worker username+password, got %v", created.body["credentials"])
		}
		if staffUsername == workerUsername {
			t.Fatalf("expected distinct staff/worker usernames, both were %q", staffUsername)
		}

		staffLogin := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": staffUsername, "password": staffPassword})
		if staffLogin.status != http.StatusOK {
			t.Fatalf("staff login: expected 200, got %d (%v)", staffLogin.status, staffLogin.body)
		}
		loggedInUser, _ := staffLogin.body["user"].(map[string]any)
		if loggedInUser["role"] != "staff" || loggedInUser["washing_point_id"] != wpID {
			t.Errorf("expected staff login to yield role=staff washing_point_id=%s, got %v", wpID, loggedInUser)
		}

		workerLogin := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": workerUsername, "password": workerPassword})
		if workerLogin.status != http.StatusOK {
			t.Fatalf("worker login: expected 200, got %d (%v)", workerLogin.status, workerLogin.body)
		}
		loggedInWorker, _ := workerLogin.body["user"].(map[string]any)
		if loggedInWorker["role"] != "worker" || loggedInWorker["washing_point_id"] != wpID {
			t.Errorf("expected worker login to yield role=worker washing_point_id=%s, got %v", wpID, loggedInWorker)
		}

		t.Run("a second point with the same name gets a disambiguated username", func(t *testing.T) {
			second := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
				"name": "Pegasus Detailing", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
			})
			if second.status != http.StatusCreated {
				t.Fatalf("create second wp: expected 201, got %d (%v)", second.status, second.body)
			}
			secondStaffUsername, _ := credential(t, second.body, "credentials", "staff")["username"].(string)
			if secondStaffUsername == staffUsername {
				t.Errorf("expected a disambiguated username for the second point's staff account, got the same %q", staffUsername)
			}
		})
	})

	t.Run("staff can read and reset their own point's credentials, not another point's", func(t *testing.T) {
		created := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
			"name": "RBAC Wash", "address": "3 Test St", "latitude": 5.0, "longitude": 6.0,
		})
		wpID := created.str("id")
		env.setWashingPointID(t, staffPhone, wpID)
		staffAccess := env.reLogin(t, staffPhone)

		otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
			"name": "Other RBAC Wash", "address": "3b Test St", "latitude": 5.5, "longitude": 6.5,
		})
		otherWPID := otherWP.str("id")

		if resp := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/credentials", "", nil); resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401 unauthenticated, got %d (%v)", resp.status, resp.body)
		}
		if resp := env.do(t, http.MethodGet, "/api/v1/washing-points/"+otherWPID+"/credentials", staffAccess, nil); resp.status != http.StatusNotFound {
			t.Errorf("expected 404 for staff reading another point's credentials (IDOR check), got %d (%v)", resp.status, resp.body)
		}
		if resp := env.do(t, http.MethodPost, "/api/v1/washing-points/"+otherWPID+"/credentials/staff/reset", staffAccess, nil); resp.status != http.StatusNotFound {
			t.Errorf("expected 404 for staff resetting another point's credentials, got %d (%v)", resp.status, resp.body)
		}

		get := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/credentials", staffAccess, nil)
		if get.status != http.StatusOK {
			t.Fatalf("staff get own credentials: expected 200, got %d (%v)", get.status, get.body)
		}
		if _, ok := get.body["staff"].(map[string]any)["password"]; ok {
			t.Error("expected GET credentials to never include a password")
		}

		adminGet := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/credentials", adminAccess, nil)
		if adminGet.status != http.StatusOK {
			t.Fatalf("admin get credentials: expected 200, got %d (%v)", adminGet.status, adminGet.body)
		}
	})

	t.Run("resetting a password revokes the old session and old password stops working", func(t *testing.T) {
		created := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
			"name": "Reset Wash", "address": "4 Test St", "latitude": 7.0, "longitude": 8.0,
		})
		staffCreds := credential(t, created.body, "credentials", "staff")
		username, _ := staffCreds["username"].(string)
		oldPassword, _ := staffCreds["password"].(string)
		wpID := created.str("id")

		login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": oldPassword})
		if login.status != http.StatusOK {
			t.Fatalf("initial staff login: expected 200, got %d (%v)", login.status, login.body)
		}
		oldRefreshToken, _ := login.body["refresh_token"].(string)

		reset := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/credentials/staff/reset", adminAccess, nil)
		if reset.status != http.StatusOK {
			t.Fatalf("reset: expected 200, got %d (%v)", reset.status, reset.body)
		}
		newPassword, _ := reset.body["password"].(string)
		if newPassword == "" || newPassword == oldPassword {
			t.Fatalf("expected a fresh, different password, got %q (old was %q)", newPassword, oldPassword)
		}
		if reset.body["username"] != username {
			t.Errorf("expected username to stay %q across a reset, got %v", username, reset.body["username"])
		}

		if resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": oldPassword}); resp.status != http.StatusUnauthorized {
			t.Errorf("expected the old password to stop working, got %d (%v)", resp.status, resp.body)
		}
		if resp := env.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]any{"refresh_token": oldRefreshToken}); resp.status != http.StatusUnauthorized {
			t.Errorf("expected the pre-reset refresh token to be revoked, got %d (%v)", resp.status, resp.body)
		}

		if resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": newPassword}); resp.status != http.StatusOK {
			t.Errorf("expected the new password to work, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("approving a connection request also provisions working credentials", func(t *testing.T) {
		crCreated := env.do(t, http.MethodPost, "/api/v1/connection-requests", adminAccess, map[string]any{
			"business_name": "Onboarded Wash Co", "contact_name": "Jane Doe",
			"contact_phone": "+15559991111", "address": "5 Onboarding Way", "boxes_count": 2,
		})
		if crCreated.status != http.StatusCreated {
			t.Fatalf("create connection request: expected 201, got %d (%v)", crCreated.status, crCreated.body)
		}

		approved := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+crCreated.str("id"), adminAccess, map[string]any{"status": "approved"})
		if approved.status != http.StatusOK {
			t.Fatalf("approve: expected 200, got %d (%v)", approved.status, approved.body)
		}
		staffCreds := credential(t, approved.body, "credentials", "staff")
		username, _ := staffCreds["username"].(string)
		password, _ := staffCreds["password"].(string)
		if username == "" || password == "" {
			t.Fatalf("expected approve's response to include working staff credentials, got %v", approved.body["credentials"])
		}

		if resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": username, "password": password}); resp.status != http.StatusOK {
			t.Errorf("expected the connection-request-provisioned staff login to work, got %d (%v)", resp.status, resp.body)
		}
	})
}
