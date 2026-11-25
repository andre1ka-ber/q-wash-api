//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

func TestDisplayRole_SeesOnlyItsBoard(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(950)
	env.loginAs(t, staffPhone, user.RoleStaff)
	otherStaffPhone := uniquePhone(952)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)
	admin := env.loginAs(t, uniquePhone(951), user.RoleAdmin)
	fx := env.setUpBookingFixture(t, admin, staffPhone)
	other := env.setUpBookingFixture(t, admin, otherStaffPhone)
	point := "/api/v1/washing-points/" + fx.washingPointID

	created := env.do(t, http.MethodPost, point+"/credentials/display", fx.staffAccess, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("staff creates the display login: got %d (%v)", created.status, created.body)
	}
	login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": created.str("username"), "password": created.str("password"),
	})
	if login.status != http.StatusOK || login.body["user"].(map[string]any)["role"] != "display" {
		t.Fatalf("display login: got %d (%v)", login.status, login.body)
	}
	display := login.str("access_token")

	listed := env.do(t, http.MethodGet, point+"/credentials", fx.staffAccess, nil)
	if account, ok := listed.body["display"].(map[string]any); !ok || account["username"] != created.str("username") {
		t.Fatalf("credentials list must include the display account: %v", listed.body)
	}

	booking := env.do(t, http.MethodPost, point+"/queue/manual", fx.staffAccess, map[string]any{
		"service_id": fx.serviceID, "price_option_id": fx.priceOptionID, "box_number": 1,
		"scheduled_start_at": futureBookingTime(3), "client_phone": uniquePhone(953),
	})
	if booking.status != http.StatusCreated {
		t.Fatalf("manual booking: %d (%v)", booking.status, booking.body)
	}
	queuePath := "/api/v1/queue/" + booking.str("id")

	for _, path := range []string{point + "/board", "/api/v1/me"} {
		if resp := env.do(t, http.MethodGet, path, display, nil); resp.status != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d", path, resp.status)
		}
	}
	if resp := env.do(t, http.MethodGet, "/api/v1/washing-points/"+other.washingPointID+"/board", display, nil); resp.status != http.StatusNotFound {
		t.Fatalf("another point's board: expected 404, got %d", resp.status)
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, queuePath},
		{http.MethodGet, queuePath + "/events"},
		{http.MethodPatch, queuePath + "/cancel"},
		{http.MethodPatch, queuePath + "/status"},
		{http.MethodGet, queuePath + "/photos"},
		{http.MethodPost, "/api/v1/queue"},
		{http.MethodGet, "/api/v1/queue"},
		{http.MethodGet, point + "/queue"},
		{http.MethodGet, point + "/queue/day"},
		{http.MethodPost, point + "/queue/manual"},
		{http.MethodGet, point + "/boxes/live"},
		{http.MethodGet, point + "/reports"},
		{http.MethodGet, point + "/credentials"},
		{http.MethodPost, point + "/credentials/staff/reset"},
		{http.MethodPatch, point},
		{http.MethodGet, "/api/v1/me/queue"},
		{http.MethodPost, "/api/v1/me/cars"},
		{http.MethodGet, "/api/v1/me/notifications"},
		{http.MethodPatch, "/api/v1/me"},
		{http.MethodPatch, "/api/v1/auth/password"},
	} {
		if resp := env.do(t, c.method, c.path, display, map[string]any{}); resp.status != http.StatusForbidden {
			t.Errorf("%s %s: expected 403, got %d (%v)", c.method, c.path, resp.status, resp.body)
		}
	}

	if resp := env.do(t, http.MethodPost, "/api/v1/auth/logout", display, nil); resp.status != http.StatusNoContent {
		t.Fatalf("display logout: got %d", resp.status)
	}
}

func TestCredentials_ResettingYourOwnLoginNeedsTheCurrentPassword(t *testing.T) {
	env := newTestEnv(t)
	admin := env.loginAs(t, uniquePhone(960), user.RoleAdmin)
	created := env.do(t, http.MethodPost, "/api/v1/washing-points", admin, map[string]any{
		"name": "Own Reset", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
	})
	if created.status != http.StatusCreated {
		t.Fatalf("create point: %d (%v)", created.status, created.body)
	}
	account := created.body["credentials"].(map[string]any)["staff"].(map[string]any)
	password := account["password"].(string)
	login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": account["username"], "password": password})
	staff := login.str("access_token")
	reset := "/api/v1/washing-points/" + created.str("id") + "/credentials/staff/reset"

	for name, body := range map[string]any{"no body": nil, "wrong password": map[string]string{"current_password": "nope"}} {
		resp := env.do(t, http.MethodPost, reset, staff, body)
		errBody, _ := resp.body["error"].(map[string]any)
		if resp.status != http.StatusForbidden || errBody["code"] != "current_password_required" {
			t.Fatalf("%s: expected 403 current_password_required, got %d (%v)", name, resp.status, resp.body)
		}
	}
	if resp := env.do(t, http.MethodPost, reset, staff, map[string]string{"current_password": password}); resp.status != http.StatusOK {
		t.Fatalf("right password: got %d (%v)", resp.status, resp.body)
	}
	worker := "/api/v1/washing-points/" + created.str("id") + "/credentials/worker/reset"
	if resp := env.do(t, http.MethodPost, worker, staff, nil); resp.status != http.StatusOK {
		t.Fatalf("resetting another account needs no password: got %d (%v)", resp.status, resp.body)
	}
	if resp := env.do(t, http.MethodPost, reset, admin, nil); resp.status != http.StatusOK {
		t.Fatalf("admin reset: got %d (%v)", resp.status, resp.body)
	}
}
