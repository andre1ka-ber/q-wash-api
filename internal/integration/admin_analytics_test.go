//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

// TestAdminAnalytics covers the admin dashboard additions: per-point
// accounts_count, the day-over-day stats fields, the period analytics and
// the platform view that is fed by login tracking.
func TestAdminAnalytics(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(95), user.RoleAdmin)
	staffAccess := env.loginAs(t, uniquePhone(96), user.RoleStaff)

	created := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Analytics Wash", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
	})
	if created.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", created.status, created.body)
	}
	wpID := created.str("id")
	staffCreds := credential(t, created.body, "credentials", "staff")
	staffUsername, _ := staffCreds["username"].(string)
	staffPassword, _ := staffCreds["password"].(string)

	t.Run("admin point list reports how many login accounts each point has", func(t *testing.T) {
		list := env.do(t, http.MethodGet, "/api/v1/admin/washing-points", adminAccess, nil)
		if list.status != http.StatusOK {
			t.Fatalf("expected 200, got %d", list.status)
		}
		items, _ := list.body["items"].([]any)
		for _, it := range items {
			p, _ := it.(map[string]any)
			if p["id"] == wpID {
				if n, _ := p["accounts_count"].(float64); n != 2 {
					t.Errorf("expected accounts_count 2 (staff+worker), got %v", p["accounts_count"])
				}
				return
			}
		}
		t.Fatalf("point %s missing from admin list", wpID)
	})

	t.Run("stats carry the yesterday and 30-day fields", func(t *testing.T) {
		stats := env.do(t, http.MethodGet, "/api/v1/admin/stats", adminAccess, nil)
		if stats.status != http.StatusOK {
			t.Fatalf("expected 200, got %d", stats.status)
		}
		for _, f := range []string{"points_added_30d", "bookings_yesterday", "average_utilization_yesterday"} {
			if _, ok := stats.body[f]; !ok {
				t.Errorf("stats missing %q: %v", f, stats.body)
			}
		}
		if n, _ := stats.body["points_added_30d"].(float64); n < 1 {
			t.Errorf("expected the point created just now to count, got %v", stats.body["points_added_30d"])
		}
	})

	t.Run("analytics is admin-only and validates the period", func(t *testing.T) {
		for _, path := range []string{"/api/v1/admin/analytics", "/api/v1/admin/analytics/platform"} {
			if got := env.do(t, http.MethodGet, path, staffAccess, nil); got.status != http.StatusForbidden {
				t.Errorf("%s as staff: expected 403, got %d", path, got.status)
			}
			if got := env.do(t, http.MethodGet, path, "", nil); got.status != http.StatusUnauthorized {
				t.Errorf("%s anonymous: expected 401, got %d", path, got.status)
			}
			if got := env.do(t, http.MethodGet, path+"?period=year", adminAccess, nil); got.status != http.StatusBadRequest {
				t.Errorf("%s bad period: expected 400, got %d", path, got.status)
			}
		}
		for _, p := range []string{"today", "7", "30"} {
			got := env.do(t, http.MethodGet, "/api/v1/admin/analytics?period="+p, adminAccess, nil)
			if got.status != http.StatusOK {
				t.Fatalf("period %s: expected 200, got %d (%v)", p, got.status, got.body)
			}
			if series, _ := got.body["series"].([]any); len(series) == 0 {
				t.Errorf("period %s: empty series", p)
			}
		}
	})

	t.Run("a staff login shows up in the platform view", func(t *testing.T) {
		login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": staffUsername, "password": staffPassword})
		if login.status != http.StatusOK {
			t.Fatalf("staff login: expected 200, got %d (%v)", login.status, login.body)
		}

		view := env.do(t, http.MethodGet, "/api/v1/admin/analytics/platform?period=7", adminAccess, nil)
		if view.status != http.StatusOK {
			t.Fatalf("platform: expected 200, got %d (%v)", view.status, view.body)
		}
		washes, _ := view.body["washes"].([]any)
		for _, w := range washes {
			row, _ := w.(map[string]any)
			if row["washing_point_id"] != wpID {
				continue
			}
			if n, _ := row["sessions_7d"].(float64); n != 1 {
				t.Errorf("expected 1 session, got %v", row["sessions_7d"])
			}
			if row["health"] != "ok" || row["last_role"] != "staff" {
				t.Errorf("expected health ok and last_role staff, got %v", row)
			}
			if n, _ := row["staff"].(float64); n != 2 {
				t.Errorf("expected 2 staff accounts, got %v", row["staff"])
			}
			return
		}
		t.Fatalf("point %s missing from platform washes: %v", wpID, washes)
	})
}
