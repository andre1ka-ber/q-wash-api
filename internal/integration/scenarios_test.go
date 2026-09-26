//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/queue"
	"q-wash-api/internal/user"
)

func TestAuthFlow(t *testing.T) {
	env := newTestEnv(t)
	phone := uniquePhone(1)

	access := env.loginAs(t, phone, "")

	t.Run("protected route requires a token", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/me", "", nil)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401 with no token, got %d", resp.status)
		}
	})

	t.Run("protected route works with a valid token", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/me", access, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		if resp.str("phone_number") != phone {
			t.Errorf("expected phone_number %q, got %q", phone, resp.str("phone_number"))
		}
		if resp.str("role") != "customer" {
			t.Errorf("expected a freshly-created user to default to role customer, got %q", resp.str("role"))
		}
	})

	t.Run("a consumed OTP code cannot be reused", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{
			"phone_number": phone,
			"code":         env.sms.lastCodeFor(t, phone), // the code loginAs already consumed above
		})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected reusing a consumed OTP code to 400, got %d", resp.status)
		}
	})

	// Request a fresh code so we have a refresh token to rotate.
	env.do(t, http.MethodPost, "/api/v1/auth/otp/request", "", map[string]string{"phone_number": phone})
	refreshToken := env.mustVerifyAndGetRefreshToken(t, phone)

	t.Run("refresh rotates the pair and revokes the old token", func(t *testing.T) {
		refreshed := env.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"refresh_token": refreshToken})
		if refreshed.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", refreshed.status, refreshed.body)
		}
		newAccess, _ := refreshed.body["access_token"].(string)
		if newAccess == "" {
			t.Fatal("expected a new access_token in the refresh response")
		}

		reuse := env.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"refresh_token": refreshToken})
		if reuse.status != http.StatusUnauthorized {
			t.Fatalf("expected reusing a rotated-away refresh token to 401, got %d", reuse.status)
		}

		meResp := env.do(t, http.MethodGet, "/api/v1/me", newAccess, nil)
		if meResp.status != http.StatusOK {
			t.Fatalf("expected the new access token to work, got %d", meResp.status)
		}
	})

	t.Run("logout is idempotent", func(t *testing.T) {
		first := env.do(t, http.MethodPost, "/api/v1/auth/logout", access, nil)
		if first.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", first.status)
		}
		second := env.do(t, http.MethodPost, "/api/v1/auth/logout", access, nil)
		if second.status != http.StatusNoContent {
			t.Fatalf("expected logout to be idempotent (204 again), got %d", second.status)
		}
	})
}

// mustVerifyAndGetRefreshToken assumes RequestOTP was already called for
// phone and pulls the latest code, verifying to get a fresh refresh token.
func (e *testEnv) mustVerifyAndGetRefreshToken(t *testing.T, phone string) string {
	t.Helper()
	code := e.sms.lastCodeFor(t, phone)
	resp := e.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": phone, "code": code})
	if resp.status != http.StatusOK {
		t.Fatalf("verify: expected 200, got %d (%v)", resp.status, resp.body)
	}
	token, _ := resp.body["refresh_token"].(string)
	if token == "" {
		t.Fatalf("verify response had no refresh_token: %v", resp.body)
	}
	return token
}

func TestPasswordLogin(t *testing.T) {
	env := newTestEnv(t)

	staffPhone := uniquePhone(10)
	env.loginAs(t, staffPhone, user.RoleStaff) // creates + promotes the user
	env.setPasswordCredentials(t, staffPhone, "board-staff", "correct-horse-battery")

	customerPhone := uniquePhone(11)
	env.loginAs(t, customerPhone, "") // plain customer, no credentials set

	t.Run("correct username/password succeeds and the token works", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
			"username": "board-staff", "password": "correct-horse-battery",
		})
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		access, _ := resp.body["access_token"].(string)
		if access == "" {
			t.Fatal("expected a non-empty access_token")
		}
		userObj, _ := resp.body["user"].(map[string]any)
		if userObj["role"] != "staff" {
			t.Errorf("expected role staff in the response, got %v", userObj["role"])
		}

		// The token from password login must work exactly like an OTP
		// token — same staff-gated endpoint.
		board := env.do(t, http.MethodGet, "/api/v1/washing-points/00000000-0000-0000-0000-000000000000/queue", access, nil)
		if board.status == http.StatusUnauthorized || board.status == http.StatusForbidden {
			t.Fatalf("expected the password-login token to pass RBAC, got %d", board.status)
		}
	})

	t.Run("wrong password is rejected generically", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
			"username": "board-staff", "password": "wrong-password",
		})
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "invalid_credentials" {
			t.Errorf("expected code invalid_credentials, got %v", resp.body["error"])
		}
	})

	t.Run("unknown username gets the same generic error (no enumeration)", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
			"username": "no-such-user", "password": "whatever",
		})
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "invalid_credentials" {
			t.Errorf("expected code invalid_credentials, got %v", resp.body["error"])
		}
	})

	t.Run("a customer account (no credentials set) cannot log in this way", func(t *testing.T) {
		env.setPasswordCredentials(t, customerPhone, "sneaky-customer", "some-password")
		resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
			"username": "sneaky-customer", "password": "some-password",
		})
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for a customer role even with correct credentials, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("missing fields are a 400, not a 401", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "board-staff"})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
	})
}

func TestRBAC_WashingPointManagement(t *testing.T) {
	env := newTestEnv(t)
	customerAccess := env.loginAs(t, uniquePhone(2), "")
	staffAccess := env.loginAs(t, uniquePhone(3), user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(30), user.RoleAdmin)

	body := map[string]any{
		"name": "Test Point", "address": "1 Test St",
		"latitude": 1.0, "longitude": 2.0,
	}

	t.Run("customer is forbidden", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/washing-points", customerAccess, body)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for a customer, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("staff is forbidden (creating a point directly is admin-only; staff onboard via connection requests instead)", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/washing-points", staffAccess, body)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for staff, got %d (%v)", resp.status, resp.body)
		}
	})

	var createdID string
	t.Run("admin can create", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, body)
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201 for admin, got %d (%v)", resp.status, resp.body)
		}
		if resp.str("id") == "" {
			t.Error("expected a non-empty id in the response")
		}
		createdID = resp.str("id")
	})

	t.Run("unauthenticated is rejected", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/washing-points", "", body)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401 with no token, got %d", resp.status)
		}
	})

	t.Run("staff cannot manage a point they're not scoped to (IDOR regression check)", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, "/api/v1/washing-points/"+createdID, staffAccess, map[string]any{"name": "Hijacked"})
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404 (hidden as not-found, same pattern as other ownership checks) for staff acting on a point that isn't theirs, got %d (%v)", resp.status, resp.body)
		}
	})
}

// bookingFixture creates a washing point (boxes_count=1, to make the
// double-booking test need only two requests) and one service with one
// price option through the real HTTP API. Creating a point is admin-only
// (see TestRBAC_WashingPointManagement), so this takes an admin token for
// that step and then scopes staffPhone's account to the new point directly
// in the DB (staffAccess, returned on the fixture, reflects that scoping —
// it's a fresh token obtained after the DB change, since the
// washing_point_id JWT claim is fixed at login time).
type bookingFixture struct {
	washingPointID string
	serviceID      string
	priceOptionID  string
	staffAccess    string
}

func (e *testEnv) setUpBookingFixture(t *testing.T, adminAccess, staffPhone string) bookingFixture {
	t.Helper()

	wp := e.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Single Box Point", "address": "1 Test St",
		"latitude": 1.0, "longitude": 2.0, "boxes_count": 1,
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create washing point: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")

	e.setWashingPointID(t, staffPhone, wpID)
	staffAccess := e.reLogin(t, staffPhone)

	svc := e.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", staffAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 500}},
	})
	if svc.status != http.StatusCreated {
		t.Fatalf("create service: expected 201, got %d (%v)", svc.status, svc.body)
	}
	priceOptions, _ := svc.body["price_options"].([]any)
	if len(priceOptions) != 1 {
		t.Fatalf("expected 1 price option, got %v", svc.body["price_options"])
	}
	priceOptionID, _ := priceOptions[0].(map[string]any)["id"].(string)

	return bookingFixture{washingPointID: wpID, serviceID: svc.str("id"), priceOptionID: priceOptionID, staffAccess: staffAccess}
}

func (e *testEnv) createCar(t *testing.T, access, name string) string {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/api/v1/me/cars", access, map[string]string{"name": name})
	if resp.status != http.StatusCreated {
		t.Fatalf("create car: expected 201, got %d (%v)", resp.status, resp.body)
	}
	return resp.str("id")
}

func TestBooking_DoubleBookingPreventionAndCancelRebook(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(4)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(40), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(5), "")

	fixture := env.setUpBookingFixture(t, adminAccess, staffPhone)
	carID := env.createCar(t, customerAccess, "My Car")

	startAt := futureBookingTime(2)
	bookingBody := map[string]any{
		"car_id": carID, "service_id": fixture.serviceID, "box_number": 1,
		"price_option_id": fixture.priceOptionID, "scheduled_start_at": startAt,
	}

	first := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, bookingBody)
	if first.status != http.StatusCreated {
		t.Fatalf("first booking: expected 201, got %d (%v)", first.status, first.body)
	}
	if got := first.body["box_number"]; got != float64(1) {
		t.Errorf("expected box_number 1, got %v", got)
	}

	t.Run("same customer double-booking is rejected (already has an active booking)", func(t *testing.T) {
		second := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, bookingBody)
		if second.status != http.StatusConflict {
			t.Fatalf("expected 409 active_booking_exists, got %d (%v)", second.status, second.body)
		}
		if second.body["error"].(map[string]any)["code"] != "active_booking_exists" {
			t.Errorf("expected code active_booking_exists, got %v", second.body["error"])
		}
	})

	t.Run("different customer competing for the same box/time gets slot_unavailable", func(t *testing.T) {
		otherAccess := env.loginAs(t, uniquePhone(45), "")
		otherCarID := env.createCar(t, otherAccess, "Other Car")
		otherBody := map[string]any{
			"car_id": otherCarID, "service_id": fixture.serviceID, "box_number": 1,
			"price_option_id": fixture.priceOptionID, "scheduled_start_at": startAt,
		}
		resp := env.do(t, http.MethodPost, "/api/v1/queue", otherAccess, otherBody)
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409 slot_unavailable, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "slot_unavailable" {
			t.Errorf("expected code slot_unavailable, got %v", resp.body["error"])
		}
	})

	bookingID := first.str("id")

	t.Run("cancel then rebook the freed box", func(t *testing.T) {
		cancel := env.do(t, http.MethodPatch, "/api/v1/queue/"+bookingID+"/cancel", customerAccess, nil)
		if cancel.status != http.StatusOK {
			t.Fatalf("cancel: expected 200, got %d (%v)", cancel.status, cancel.body)
		}
		if cancel.str("status") != "canceled" {
			t.Errorf("expected status canceled, got %q", cancel.str("status"))
		}

		rebook := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, bookingBody)
		if rebook.status != http.StatusCreated {
			t.Fatalf("rebook after cancel: expected 201, got %d (%v)", rebook.status, rebook.body)
		}
	})
}

func TestQueue_ForwardOnlyStatusTransitions(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(6)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(60), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(7), "")

	fixture := env.setUpBookingFixture(t, adminAccess, staffPhone)
	staffAccess := fixture.staffAccess
	carID := env.createCar(t, customerAccess, "My Car")

	startAt := futureBookingTime(3)
	booking := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
		"car_id": carID, "service_id": fixture.serviceID, "box_number": 1,
		"price_option_id": fixture.priceOptionID, "scheduled_start_at": startAt,
	})
	if booking.status != http.StatusCreated {
		t.Fatalf("booking: expected 201, got %d (%v)", booking.status, booking.body)
	}
	bookingID := booking.str("id")
	statusPath := "/api/v1/queue/" + bookingID + "/status"

	t.Run("customer cannot transition status", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, statusPath, customerAccess, map[string]string{"status": "waiting"})
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", resp.status)
		}
	})

	t.Run("staff cannot skip a stage", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, statusPath, staffAccess, map[string]string{"status": "washing"})
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("staff advances one step at a time", func(t *testing.T) {
		for _, want := range []string{"waiting", "washing", "ready"} {
			resp := env.do(t, http.MethodPatch, statusPath, staffAccess, map[string]string{"status": want})
			if resp.status != http.StatusOK {
				t.Fatalf("advance to %s: expected 200, got %d (%v)", want, resp.status, resp.body)
			}
			if resp.str("status") != want {
				t.Fatalf("expected status %s, got %s", want, resp.str("status"))
			}
		}
	})

	t.Run("ready is terminal", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, statusPath, staffAccess, map[string]string{"status": "waiting"})
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409 from a terminal state, got %d", resp.status)
		}
	})
}

// TestAdmin_NetworkWideViews covers docs/PLAN_WEB_APPS.md phase 3: the
// admin-only network list/stats endpoints, and the network-wide GET /queue
// filter's RBAC (admin may scope or omit washing_point_id; staff/worker
// are always forced to their own point).
func TestAdmin_NetworkWideViews(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(70), user.RoleAdmin)
	staffPhone := uniquePhone(71)
	env.loginAs(t, staffPhone, user.RoleStaff)

	ownerResp := env.do(t, http.MethodPost, "/api/v1/owners", adminAccess, map[string]any{"name": "Titan LLC"})
	if ownerResp.status != http.StatusCreated {
		t.Fatalf("create owner: expected 201, got %d (%v)", ownerResp.status, ownerResp.body)
	}
	ownerID := ownerResp.str("id")

	wp1 := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Point One", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
		"owner_id": ownerID,
	})
	if wp1.status != http.StatusCreated {
		t.Fatalf("create wp1: expected 201, got %d (%v)", wp1.status, wp1.body)
	}
	if wp1.str("owner_id") != ownerID {
		t.Errorf("expected owner_id %q on the create response, got %q", ownerID, wp1.str("owner_id"))
	}

	wp2 := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Point Two", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
	})
	if wp2.status != http.StatusCreated {
		t.Fatalf("create wp2: expected 201, got %d (%v)", wp2.status, wp2.body)
	}

	env.setWashingPointID(t, staffPhone, wp2.str("id"))
	staffAccess := env.reLogin(t, staffPhone)

	t.Run("staff is forbidden from the admin network views", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/admin/washing-points", staffAccess, nil)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for staff, got %d (%v)", resp.status, resp.body)
		}
		resp = env.do(t, http.MethodGet, "/api/v1/admin/stats", staffAccess, nil)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for staff, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("admin sees both points with owner attribution", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/admin/washing-points", adminAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		if len(items) < 2 {
			t.Fatalf("expected at least 2 points, got %d", len(items))
		}
		var found bool
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["id"] == wp1.str("id") {
				found = true
				if item["owner_name"] != "Titan LLC" {
					t.Errorf("expected owner_name %q, got %v", "Titan LLC", item["owner_name"])
				}
			}
		}
		if !found {
			t.Fatal("expected to find wp1 in the admin list")
		}
	})

	t.Run("admin stats reflect the created points", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/admin/stats", adminAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		pointsTotal, _ := resp.body["points_total"].(float64)
		if pointsTotal < 2 {
			t.Errorf("expected points_total >= 2, got %v", pointsTotal)
		}
	})

	t.Run("network-wide GET /queue: admin can scope by washing_point_id, staff is forced to their own", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/queue?washing_point_id="+wp1.str("id"), adminAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200 for admin, got %d (%v)", resp.status, resp.body)
		}

		resp = env.do(t, http.MethodGet, "/api/v1/queue", staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200 for staff (forced to own point), got %d (%v)", resp.status, resp.body)
		}
	})
}

// fakeJPEG is just enough for http.DetectContentType to sniff image/jpeg —
// the real content bytes don't matter for these tests.
var fakeJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0}, 16)...)

// TestPhotos_UploadOwnershipAndCoverPromotion covers docs/PLAN_WEB_APPS.md
// phase 4: multipart upload, content-type sniffing, the "first photo (or
// an explicit is_cover) becomes the cover" rule, auto-promotion of the
// oldest remaining photo on deleting the current cover, and the same
// own-point-only ownership pattern as every other staff-gated resource.
func TestPhotos_UploadOwnershipAndCoverPromotion(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(80), user.RoleAdmin)
	staffPhone := uniquePhone(81)
	env.loginAs(t, staffPhone, user.RoleStaff)
	otherStaffPhone := uniquePhone(82)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)

	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Photo Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	photosPath := "/api/v1/washing-points/" + wpID + "/photos"

	env.setWashingPointID(t, staffPhone, wpID)
	staffAccess := env.reLogin(t, staffPhone)

	otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Point", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
	})
	if otherWP.status != http.StatusCreated {
		t.Fatalf("create other wp: expected 201, got %d (%v)", otherWP.status, otherWP.body)
	}
	env.setWashingPointID(t, otherStaffPhone, otherWP.str("id"))
	otherStaffAccess := env.reLogin(t, otherStaffPhone)

	t.Run("unauthenticated cannot upload", func(t *testing.T) {
		resp := env.uploadFile(t, photosPath, "", "photo.jpg", fakeJPEG, false)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("staff at a different point cannot upload here (IDOR check)", func(t *testing.T) {
		resp := env.uploadFile(t, photosPath, otherStaffAccess, "photo.jpg", fakeJPEG, false)
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a non-image upload is rejected", func(t *testing.T) {
		resp := env.uploadFile(t, photosPath, staffAccess, "notes.txt", []byte("just some text"), false)
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "invalid_content_type" {
			t.Errorf("expected code invalid_content_type, got %v", resp.body["error"])
		}
	})

	first := env.uploadFile(t, photosPath, staffAccess, "photo.jpg", fakeJPEG, false)
	if first.status != http.StatusCreated {
		t.Fatalf("upload first photo: expected 201, got %d (%v)", first.status, first.body)
	}
	if first.body["is_cover"] != true {
		t.Errorf("expected the first photo to auto-become the cover, got is_cover=%v", first.body["is_cover"])
	}
	firstID := first.str("id")

	second := env.uploadFile(t, photosPath, staffAccess, "photo2.jpg", fakeJPEG, true)
	if second.status != http.StatusCreated {
		t.Fatalf("upload second photo: expected 201, got %d (%v)", second.status, second.body)
	}
	if second.body["is_cover"] != true {
		t.Errorf("expected the explicitly-cover second photo to be the cover, got is_cover=%v", second.body["is_cover"])
	}
	secondID := second.str("id")

	t.Run("listing (public, no token) shows both with exactly one cover", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, photosPath, "", nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("expected 2 photos, got %d", len(items))
		}
		coverCount := 0
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["is_cover"] == true {
				coverCount++
				if item["id"] != secondID {
					t.Errorf("expected the cover to be the second photo, got %v", item["id"])
				}
			}
		}
		if coverCount != 1 {
			t.Errorf("expected exactly 1 cover photo, got %d", coverCount)
		}
	})

	t.Run("staff at a different point cannot delete a photo here (IDOR check)", func(t *testing.T) {
		resp := env.do(t, http.MethodDelete, photosPath+"/"+firstID, otherStaffAccess, nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("deleting the cover auto-promotes the oldest remaining photo", func(t *testing.T) {
		del := env.do(t, http.MethodDelete, photosPath+"/"+secondID, staffAccess, nil)
		if del.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%v)", del.status, del.body)
		}

		list := env.do(t, http.MethodGet, photosPath, "", nil)
		items, _ := list.body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected 1 remaining photo, got %d", len(items))
		}
		remaining, _ := items[0].(map[string]any)
		if remaining["id"] != firstID {
			t.Errorf("expected the remaining photo to be the first one, got %v", remaining["id"])
		}
		if remaining["is_cover"] != true {
			t.Errorf("expected the remaining photo to be auto-promoted to cover, got is_cover=%v", remaining["is_cover"])
		}
	})
}

// TestSchedule_SeedingValidationAndOwnership covers docs/PLAN_WEB_APPS.md
// phase 5's new per-weekday schedule surface: a newly created washing
// point is auto-seeded with a bookable default (closing the "new point has
// no schedule" gap), the bulk-replace endpoint validates its 7-row body,
// and the same own-point-only ownership pattern as every other
// staff-gated resource applies here too. The availability algorithm's own
// correctness (closed days, break windows) is covered by the pure
// DaySchedule/Windows/Contains unit tests in internal/queue — this test
// only exercises the new HTTP surface around it.
func TestSchedule_SeedingValidationAndOwnership(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(90), user.RoleAdmin)
	staffPhone := uniquePhone(91)
	env.loginAs(t, staffPhone, user.RoleStaff)
	otherStaffPhone := uniquePhone(92)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)

	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Schedule Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	schedulePath := "/api/v1/washing-points/" + wpID + "/schedule"

	env.setWashingPointID(t, staffPhone, wpID)
	staffAccess := env.reLogin(t, staffPhone)

	otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Schedule Point", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
	})
	if otherWP.status != http.StatusCreated {
		t.Fatalf("create other wp: expected 201, got %d (%v)", otherWP.status, otherWP.body)
	}
	env.setWashingPointID(t, otherStaffPhone, otherWP.str("id"))
	otherStaffAccess := env.reLogin(t, otherStaffPhone)

	t.Run("a newly created point is auto-seeded with 7 bookable rows", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, schedulePath, "", nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		if len(items) != 7 {
			t.Fatalf("expected 7 seeded rows, got %d", len(items))
		}
		seenWeekdays := map[float64]bool{}
		for _, raw := range items {
			row, _ := raw.(map[string]any)
			seenWeekdays[row["weekday"].(float64)] = true
			if row["is_open"] != true || row["open_time"] != "08:00" || row["close_time"] != "20:00" {
				t.Errorf("expected the default 08:00-20:00 every-day seed, got %v", row)
			}
		}
		if len(seenWeekdays) != 7 {
			t.Errorf("expected all 7 distinct weekdays present, got %v", seenWeekdays)
		}
	})

	t.Run("unauthenticated cannot replace the schedule", func(t *testing.T) {
		resp := env.do(t, http.MethodPut, schedulePath, "", map[string]any{"items": defaultWeekSchedule()})
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("staff at a different point cannot replace this schedule (IDOR check)", func(t *testing.T) {
		resp := env.do(t, http.MethodPut, schedulePath, otherStaffAccess, map[string]any{"items": defaultWeekSchedule()})
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("fewer than 7 rows is rejected", func(t *testing.T) {
		rows := defaultWeekSchedule()[:6]
		resp := env.do(t, http.MethodPut, schedulePath, staffAccess, map[string]any{"items": rows})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "invalid_schedule" {
			t.Errorf("expected code invalid_schedule, got %v", resp.body["error"])
		}
	})

	t.Run("a duplicate weekday is rejected", func(t *testing.T) {
		rows := defaultWeekSchedule()
		rows[6]["weekday"] = 0 // duplicate Monday, no Sunday row
		resp := env.do(t, http.MethodPut, schedulePath, staffAccess, map[string]any{"items": rows})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "duplicate_weekday" {
			t.Errorf("expected code duplicate_weekday, got %v", resp.body["error"])
		}
	})

	t.Run("is_open true without hours is rejected", func(t *testing.T) {
		rows := defaultWeekSchedule()
		delete(rows[0], "open_time")
		resp := env.do(t, http.MethodPut, schedulePath, staffAccess, map[string]any{"items": rows})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "invalid_hours" {
			t.Errorf("expected code invalid_hours, got %v", resp.body["error"])
		}
	})

	t.Run("a valid replacement with a closed day and a break is accepted", func(t *testing.T) {
		rows := defaultWeekSchedule()
		rows[0] = map[string]any{"weekday": 0, "is_open": false} // Monday closed
		rows[1]["break_start"] = "13:00"                         // Tuesday has a lunch break
		rows[1]["break_end"] = "14:00"

		resp := env.do(t, http.MethodPut, schedulePath, staffAccess, map[string]any{"items": rows})
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		if len(items) != 7 {
			t.Fatalf("expected 7 rows back, got %d", len(items))
		}

		list := env.do(t, http.MethodGet, schedulePath, "", nil)
		listItems, _ := list.body["items"].([]any)
		for _, raw := range listItems {
			row, _ := raw.(map[string]any)
			switch row["weekday"].(float64) {
			case 0:
				if row["is_open"] != false {
					t.Errorf("expected Monday closed, got %v", row)
				}
			case 1:
				if row["break_start"] != "13:00" || row["break_end"] != "14:00" {
					t.Errorf("expected Tuesday's break to persist, got %v", row)
				}
			}
		}
	})
}

// defaultWeekSchedule returns a fresh 7-row, every-day-08:00-20:00, no-break
// schedule body (matching the auto-seeded default) for tests to mutate.
func defaultWeekSchedule() []map[string]any {
	rows := make([]map[string]any, 7)
	for weekday := 0; weekday < 7; weekday++ {
		rows[weekday] = map[string]any{
			"weekday": weekday, "is_open": true, "open_time": "08:00", "close_time": "20:00",
		}
	}
	return rows
}

func TestBoxes_CRUDOwnershipAndAvailabilityFilter(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(93), user.RoleAdmin)
	staffPhone := uniquePhone(94)
	env.loginAs(t, staffPhone, user.RoleStaff)
	otherStaffPhone := uniquePhone(95)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(96), "")

	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Box Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0, "boxes_count": 2,
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	boxesPath := "/api/v1/washing-points/" + wpID + "/boxes"

	env.setWashingPointID(t, staffPhone, wpID)
	staffAccess := env.reLogin(t, staffPhone)

	otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Box Point", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
	})
	if otherWP.status != http.StatusCreated {
		t.Fatalf("create other wp: expected 201, got %d (%v)", otherWP.status, otherWP.body)
	}
	env.setWashingPointID(t, otherStaffPhone, otherWP.str("id"))
	otherStaffAccess := env.reLogin(t, otherStaffPhone)

	var boxIDs []string

	t.Run("a newly created point is auto-seeded with boxes_count open boxes", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boxesPath, "", nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("expected 2 seeded boxes, got %d", len(items))
		}
		for _, raw := range items {
			row, _ := raw.(map[string]any)
			if row["is_open"] != true {
				t.Errorf("expected seeded box open, got %v", row)
			}
			boxIDs = append(boxIDs, row["id"].(string))
		}
	})

	t.Run("unauthenticated cannot create a box", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, boxesPath, "", map[string]any{"label": "Detailing"})
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("staff at a different point cannot manage this point's boxes (IDOR check)", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, boxesPath+"/"+boxIDs[0], otherStaffAccess, map[string]any{"is_open": false})
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("staff can add a third box, auto-numbered", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, boxesPath, staffAccess, map[string]any{"label": "Detailing lift"})
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["number"] != float64(3) {
			t.Errorf("expected auto-assigned number 3, got %v", resp.body["number"])
		}
		if resp.body["label"] != "Detailing lift" {
			t.Errorf("expected label to persist, got %v", resp.body["label"])
		}
	})

	svc := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", staffAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 500}},
	})
	if svc.status != http.StatusCreated {
		t.Fatalf("create service: expected 201, got %d (%v)", svc.status, svc.body)
	}
	priceOptions, _ := svc.body["price_options"].([]any)
	priceOptionID, _ := priceOptions[0].(map[string]any)["id"].(string)
	svcID := svc.str("id")
	availabilityPath := "/api/v1/washing-points/" + wpID + "/availability?service_id=" + svcID + "&date=" + futureBookingDate(3)

	t.Run("closing box 2 drops it out of every availability slot", func(t *testing.T) {
		before := env.do(t, http.MethodGet, availabilityPath, "", nil)
		if before.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", before.status, before.body)
		}
		beforeItems, _ := before.body["items"].([]any)
		if len(beforeItems) == 0 || !anySlotOffersBox(beforeItems, 2) {
			t.Fatalf("expected box 2 to be offered before it's closed, got %v", beforeItems)
		}

		closeResp := env.do(t, http.MethodPatch, boxesPath+"/"+boxIDs[1], staffAccess, map[string]any{"is_open": false})
		if closeResp.status != http.StatusOK {
			t.Fatalf("close box 2: expected 200, got %d (%v)", closeResp.status, closeResp.body)
		}

		after := env.do(t, http.MethodGet, availabilityPath, "", nil)
		if after.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", after.status, after.body)
		}
		afterItems, _ := after.body["items"].([]any)
		if len(afterItems) == 0 {
			t.Fatalf("expected box 1 and box 3 to keep the point bookable")
		}
		if anySlotOffersBox(afterItems, 2) {
			t.Fatalf("expected box 2 to never appear once closed, got %v", afterItems)
		}
	})

	t.Run("booking directly against a closed box is rejected", func(t *testing.T) {
		carID := env.createCar(t, customerAccess, "Test Car")
		resp := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
			"car_id": carID, "service_id": svcID, "box_number": 2,
			"price_option_id": priceOptionID, "scheduled_start_at": futureBookingTime(3),
		})
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["error"].(map[string]any)["code"] != "box_closed" {
			t.Errorf("expected code box_closed, got %v", resp.body["error"])
		}
	})

	t.Run("staff can delete the box it added", func(t *testing.T) {
		list := env.do(t, http.MethodGet, boxesPath, "", nil)
		items, _ := list.body["items"].([]any)
		var thirdBoxID string
		for _, raw := range items {
			row, _ := raw.(map[string]any)
			if row["number"] == float64(3) {
				thirdBoxID, _ = row["id"].(string)
			}
		}
		if thirdBoxID == "" {
			t.Fatalf("expected to find box number 3 to delete, got %v", items)
		}
		resp := env.do(t, http.MethodDelete, boxesPath+"/"+thirdBoxID, staffAccess, nil)
		if resp.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%v)", resp.status, resp.body)
		}
	})
}

// anySlotOffersBox reports whether any availability slot in items lists
// box among its available_boxes.
func anySlotOffersBox(items []any, box float64) bool {
	for _, raw := range items {
		slot, _ := raw.(map[string]any)
		boxes, _ := slot["available_boxes"].([]any)
		for _, b := range boxes {
			if b == box {
				return true
			}
		}
	}
	return false
}

func TestQueue_WorkerRBACPauseResumeCancelAndDateFilter(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(97), user.RoleAdmin)
	workerPhone := uniquePhone(98)
	env.loginAs(t, workerPhone, user.RoleWorker)
	otherStaffPhone := uniquePhone(99)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(100), "")
	// A separate customer for booking2 below — the first customer's
	// booking1 is still active (washing) by that point in the test, and
	// the one-active-booking-per-user rule would reject a second booking
	// on the same account.
	customer2Access := env.loginAs(t, uniquePhone(101), "")

	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Worker RBAC Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0, "boxes_count": 2,
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	env.setWashingPointID(t, workerPhone, wpID)
	workerAccess := env.reLogin(t, workerPhone)

	otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Worker Point", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
	})
	if otherWP.status != http.StatusCreated {
		t.Fatalf("create other wp: expected 201, got %d (%v)", otherWP.status, otherWP.body)
	}
	env.setWashingPointID(t, otherStaffPhone, otherWP.str("id"))
	otherStaffAccess := env.reLogin(t, otherStaffPhone)

	svc := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", workerAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 500}},
	})
	if svc.status != http.StatusForbidden {
		t.Fatalf("worker creating a service: expected 403 (requireStaff excludes worker), got %d (%v)", svc.status, svc.body)
	}
	// Same service, created by admin instead — worker's own RBAC is what
	// this test cares about, not who may manage the catalog.
	svc = env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", adminAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 500}},
	})
	if svc.status != http.StatusCreated {
		t.Fatalf("create service: expected 201, got %d (%v)", svc.status, svc.body)
	}
	priceOptions, _ := svc.body["price_options"].([]any)
	priceOptionID, _ := priceOptions[0].(map[string]any)["id"].(string)
	svcID := svc.str("id")
	carID := env.createCar(t, customerAccess, "Test Car")

	startAt := futureBookingTime(4)
	booking1 := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
		"car_id": carID, "service_id": svcID, "box_number": 1,
		"price_option_id": priceOptionID, "scheduled_start_at": startAt,
	})
	if booking1.status != http.StatusCreated {
		t.Fatalf("create booking 1: expected 201, got %d (%v)", booking1.status, booking1.body)
	}
	booking1ID := booking1.str("id")

	boxesLivePath := "/api/v1/washing-points/" + wpID + "/boxes/live"
	boardPath := "/api/v1/washing-points/" + wpID + "/queue"

	t.Run("unauthenticated cannot reach the live-boxes view", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boxesLivePath, "", nil)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a different point's staff can't see this point's live boxes (IDOR check)", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boxesLivePath, otherStaffAccess, nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("worker sees the booking as box 1's next-up before it starts", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boxesLivePath, workerAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		box1 := findBoxItem(t, items, 1)
		next, _ := box1["next"].(map[string]any)
		if next == nil || next["id"] != booking1ID {
			t.Fatalf("expected box 1's next to be booking1, got %v", box1)
		}
		if box1["current"] != nil {
			t.Fatalf("expected box 1 to have no current booking yet, got %v", box1)
		}
	})

	t.Run("worker can advance the booking to washing", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/status", workerAccess, map[string]any{"status": "waiting"})
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		resp = env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/status", workerAccess, map[string]any{"status": "washing"})
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a different point's staff can't pause this booking (IDOR check)", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/pause", otherStaffAccess, nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("worker can pause, and double-pausing 409s", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/pause", workerAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["paused_at"] == nil {
			t.Fatalf("expected paused_at to be set, got %v", resp.body)
		}
		again := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/pause", workerAccess, nil)
		if again.status != http.StatusConflict {
			t.Fatalf("expected 409, got %d (%v)", again.status, again.body)
		}
	})

	t.Run("live-boxes reflects the pause", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boxesLivePath, workerAccess, nil)
		items, _ := resp.body["items"].([]any)
		box1 := findBoxItem(t, items, 1)
		current, _ := box1["current"].(map[string]any)
		if current == nil || current["paused_at"] == nil {
			t.Fatalf("expected box 1's current booking to show paused_at, got %v", box1)
		}
	})

	t.Run("worker can resume", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/resume", workerAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["paused_at"] != nil {
			t.Fatalf("expected paused_at cleared, got %v", resp.body)
		}
	})

	t.Run("resuming a non-paused booking 409s", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/resume", workerAccess, nil)
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("today's board is empty, but the booking's own date shows it", func(t *testing.T) {
		today := env.do(t, http.MethodGet, boardPath, workerAccess, nil)
		if today.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", today.status, today.body)
		}
		todayItems, _ := today.body["items"].([]any)
		if len(todayItems) != 0 {
			t.Fatalf("expected an empty board for today (booking is in the future), got %v", todayItems)
		}

		scoped := env.do(t, http.MethodGet, boardPath+"?date="+futureBookingDate(4), workerAccess, nil)
		if scoped.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", scoped.status, scoped.body)
		}
		scopedItems, _ := scoped.body["items"].([]any)
		if len(scopedItems) != 1 || scopedItems[0].(map[string]any)["id"] != booking1ID {
			t.Fatalf("expected exactly booking1 on its own scheduled date, got %v", scopedItems)
		}
	})

	t.Run("an invalid date is rejected", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boardPath+"?date=not-a-date", workerAccess, nil)
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("worker can cancel (\"Снять\") a not-yet-started booking at their own point", func(t *testing.T) {
		car2ID := env.createCar(t, customer2Access, "Test Car 2")
		booking2 := env.do(t, http.MethodPost, "/api/v1/queue", customer2Access, map[string]any{
			"car_id": car2ID, "service_id": svcID, "box_number": 2,
			"price_option_id": priceOptionID, "scheduled_start_at": futureBookingTime(5),
		})
		if booking2.status != http.StatusCreated {
			t.Fatalf("create booking 2: expected 201, got %d (%v)", booking2.status, booking2.body)
		}
		cancel := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking2.str("id")+"/cancel", workerAccess, nil)
		if cancel.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", cancel.status, cancel.body)
		}
		if cancel.body["status"] != "canceled" {
			t.Fatalf("expected status canceled, got %v", cancel.body)
		}
	})
}

func TestDisplayBoard_RBACAndTodayScoping(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(110), user.RoleAdmin)
	staffPhone := uniquePhone(111)
	env.loginAs(t, staffPhone, user.RoleStaff)
	workerAccess := env.loginAs(t, uniquePhone(112), user.RoleWorker)
	otherStaffPhone := uniquePhone(113)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(114), "")
	customer2Access := env.loginAs(t, uniquePhone(115), "")

	// Wide-open hours (00:00-23:59) so todayBookingTime's real-wall-clock
	// timestamps never trip outside_operating_hours regardless of when this
	// test actually runs.
	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Display Board Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
		"boxes_count": 2, "open_time": "00:00", "close_time": "23:59",
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	env.setWashingPointID(t, staffPhone, wpID)
	staffAccess := env.reLogin(t, staffPhone)

	otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Display Point", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
	})
	if otherWP.status != http.StatusCreated {
		t.Fatalf("create other wp: expected 201, got %d (%v)", otherWP.status, otherWP.body)
	}
	env.setWashingPointID(t, otherStaffPhone, otherWP.str("id"))
	otherStaffAccess := env.reLogin(t, otherStaffPhone)

	svc := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", adminAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 500}},
	})
	if svc.status != http.StatusCreated {
		t.Fatalf("create service: expected 201, got %d (%v)", svc.status, svc.body)
	}
	priceOptions, _ := svc.body["price_options"].([]any)
	priceOptionID, _ := priceOptions[0].(map[string]any)["id"].(string)
	svcID := svc.str("id")

	boardPath := "/api/v1/washing-points/" + wpID + "/board"

	t.Run("unauthenticated cannot reach the board", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boardPath, "", nil)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("worker cannot reach the board (requireStaff excludes worker, unlike boxes/live)", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boardPath, workerAccess, nil)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a different point's staff can't see this point's board (IDOR check)", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boardPath, otherStaffAccess, nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("empty board before any bookings", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boardPath, staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["boxes_active"] != float64(0) || resp.body["boxes_total"] != float64(2) {
			t.Fatalf("expected boxes_active=0 boxes_total=2, got %v", resp.body)
		}
		waiting, _ := resp.body["waiting"].([]any)
		if len(waiting) != 0 {
			t.Fatalf("expected empty waiting list, got %v", waiting)
		}
	})

	customerPhone := uniquePhone(114)
	customerLast4 := customerPhone[len(customerPhone)-4:]
	carID := env.createCar(t, customerAccess, "Board Test Car")
	booking1 := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
		"car_id": carID, "service_id": svcID, "box_number": 1,
		"price_option_id": priceOptionID, "scheduled_start_at": todayBookingTime(15),
	})
	if booking1.status != http.StatusCreated {
		t.Fatalf("create booking 1: expected 201, got %d (%v)", booking1.status, booking1.body)
	}
	booking1ID := booking1.str("id")

	customer2Phone := uniquePhone(115)
	customer2Last4 := customer2Phone[len(customer2Phone)-4:]
	car2ID := env.createCar(t, customer2Access, "Board Test Car 2")
	booking2 := env.do(t, http.MethodPost, "/api/v1/queue", customer2Access, map[string]any{
		"car_id": car2ID, "service_id": svcID, "box_number": 2,
		"price_option_id": priceOptionID, "scheduled_start_at": todayBookingTime(20),
	})
	if booking2.status != http.StatusCreated {
		t.Fatalf("create booking 2: expected 201, got %d (%v)", booking2.status, booking2.body)
	}

	t.Run("both bookings start out in the waiting list, no box active yet", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boardPath, staffAccess, nil)
		if resp.body["boxes_active"] != float64(0) {
			t.Fatalf("expected boxes_active=0, got %v", resp.body)
		}
		waiting, _ := resp.body["waiting"].([]any)
		if len(waiting) != 2 {
			t.Fatalf("expected 2 waiting items, got %v", waiting)
		}
	})

	if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/status", staffAccess, map[string]any{"status": "waiting"}); r.status != http.StatusOK {
		t.Fatalf("advance to waiting: expected 200, got %d (%v)", r.status, r.body)
	}
	if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+booking1ID+"/status", staffAccess, map[string]any{"status": "washing"}); r.status != http.StatusOK {
		t.Fatalf("advance to washing: expected 200, got %d (%v)", r.status, r.body)
	}

	t.Run("board reflects box 1 washing (with enriched car/phone/service), box 2 still waiting", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, boardPath, staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["boxes_active"] != float64(1) {
			t.Fatalf("expected boxes_active=1, got %v", resp.body)
		}
		boxes, _ := resp.body["boxes"].([]any)
		box1 := findBoxItem(t, boxes, 1)
		current, _ := box1["current"].(map[string]any)
		if current == nil {
			t.Fatalf("expected box 1 to have a current booking, got %v", box1)
		}
		if current["status"] != "washing" || current["service_name"] != "Quick Wash" ||
			current["car_name"] != "Board Test Car" || current["customer_phone_last4"] != customerLast4 {
			t.Fatalf("box 1 current didn't match expected enrichment, got %v", current)
		}
		if current["id"] != nil {
			t.Fatalf("board's current booking should have no id field (summary screen, not a detail link), got %v", current)
		}

		box2 := findBoxItem(t, boxes, 2)
		if box2["current"] != nil {
			t.Fatalf("expected box 2 to still be free, got %v", box2)
		}

		waiting, _ := resp.body["waiting"].([]any)
		if len(waiting) != 1 {
			t.Fatalf("expected exactly 1 waiting item (booking1 moved to washing), got %v", waiting)
		}
		item, _ := waiting[0].(map[string]any)
		if item["id"] != booking2.str("id") || item["box_number"] != float64(2) ||
			item["service_name"] != "Quick Wash" || item["car_name"] != "Board Test Car 2" ||
			item["customer_phone_last4"] != customer2Last4 {
			t.Fatalf("waiting item didn't match booking2's expected enrichment, got %v", item)
		}
	})
}

// TestReports_RBACAndAggregation covers the cabinet "Отчёты" tab's backend
// (docs/PLAN_WEB_APPS.md phase 10): same RBAC/ownership shape as the
// display board (requireStaff, IDOR-checked via OwnsWashingPoint), plus a
// real end-to-end revenue/cars aggregation once a booking reaches
// StatusReady — everything short of that (queue/waiting/washing, or
// canceled) must not count.
func TestReports_RBACAndAggregation(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(140), user.RoleAdmin)
	staffPhone := uniquePhone(141)
	env.loginAs(t, staffPhone, user.RoleStaff)
	workerAccess := env.loginAs(t, uniquePhone(142), user.RoleWorker)
	otherStaffPhone := uniquePhone(143)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(144), "")

	// Wide-open hours, same reasoning as todayBookingTime's own doc
	// comment — keeps this test's "today" bookings inside operating hours
	// regardless of real time of day.
	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Reports Point", "address": "1 Reports St", "latitude": 1.0, "longitude": 2.0,
		"boxes_count": 2, "open_time": "00:00", "close_time": "23:59",
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	env.setWashingPointID(t, staffPhone, wpID)
	staffAccess := env.reLogin(t, staffPhone)

	otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Reports Point", "address": "2 Reports St", "latitude": 3.0, "longitude": 4.0,
	})
	if otherWP.status != http.StatusCreated {
		t.Fatalf("create other wp: expected 201, got %d (%v)", otherWP.status, otherWP.body)
	}
	env.setWashingPointID(t, otherStaffPhone, otherWP.str("id"))
	otherStaffAccess := env.reLogin(t, otherStaffPhone)

	svc := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", adminAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 12800}},
	})
	if svc.status != http.StatusCreated {
		t.Fatalf("create service: expected 201, got %d (%v)", svc.status, svc.body)
	}
	priceOptions, _ := svc.body["price_options"].([]any)
	priceOptionID, _ := priceOptions[0].(map[string]any)["id"].(string)
	svcID := svc.str("id")

	reportsPath := "/api/v1/washing-points/" + wpID + "/reports?period=today"

	t.Run("unauthenticated cannot reach reports", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, reportsPath, "", nil)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("worker cannot reach reports (requireStaff excludes worker)", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, reportsPath, workerAccess, nil)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a different point's staff can't see this point's reports (IDOR check)", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, reportsPath, otherStaffAccess, nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("invalid period is rejected", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/reports?period=year", staffAccess, nil)
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("zero activity before any booking reaches ready", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, reportsPath, staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		kpis, _ := resp.body["kpis"].(map[string]any)
		if kpis["revenue_cents"] != float64(0) || kpis["cars"] != float64(0) {
			t.Fatalf("expected zero revenue/cars before any completed booking, got %v", kpis)
		}
	})

	carID := env.createCar(t, customerAccess, "Reports Test Car")
	booking := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
		"car_id": carID, "service_id": svcID, "box_number": 1,
		"price_option_id": priceOptionID, "scheduled_start_at": todayBookingTime(5),
	})
	if booking.status != http.StatusCreated {
		t.Fatalf("create booking: expected 201, got %d (%v)", booking.status, booking.body)
	}
	bookingID := booking.str("id")

	t.Run("still doesn't count while only queued", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, reportsPath, staffAccess, nil)
		kpis, _ := resp.body["kpis"].(map[string]any)
		if kpis["cars"] != float64(0) {
			t.Fatalf("a merely-queued booking shouldn't count yet, got %v", kpis)
		}
	})

	for _, status := range []string{"waiting", "washing", "ready"} {
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+bookingID+"/status", staffAccess, map[string]any{"status": status}); r.status != http.StatusOK {
			t.Fatalf("advance to %s: expected 200, got %d (%v)", status, r.status, r.body)
		}
	}

	t.Run("counts once ready, broken down by service and box", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, reportsPath, staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		kpis, _ := resp.body["kpis"].(map[string]any)
		if kpis["revenue_cents"] != float64(12800) || kpis["cars"] != float64(1) {
			t.Fatalf("expected revenue_cents=12800 cars=1 once ready, got %v", kpis)
		}
		if kpis["avg_receipt_cents"] != float64(12800) {
			t.Fatalf("expected avg_receipt_cents=12800, got %v", kpis)
		}

		services, _ := resp.body["services"].([]any)
		if len(services) != 1 {
			t.Fatalf("expected exactly 1 service row, got %v", services)
		}
		svcRow, _ := services[0].(map[string]any)
		if svcRow["name"] != "Quick Wash" || svcRow["count"] != float64(1) || svcRow["revenue_cents"] != float64(12800) || svcRow["share_pct"] != float64(100) {
			t.Fatalf("unexpected service row: %v", svcRow)
		}

		boxes, _ := resp.body["boxes"].([]any)
		box1 := findBoxItem(t, boxes, 1)
		if box1["cars"] != float64(1) || box1["revenue_cents"] != float64(12800) {
			t.Fatalf("unexpected box 1 row: %v", box1)
		}
		box2 := findBoxItem(t, boxes, 2)
		if box2["cars"] != float64(0) {
			t.Fatalf("expected box 2 untouched, got %v", box2)
		}

		bars, _ := resp.body["bars"].([]any)
		if len(bars) != 24 {
			t.Fatalf("expected 24 hourly bars for period=today, got %d", len(bars))
		}
		var totalBarRevenue float64
		for _, raw := range bars {
			bar, _ := raw.(map[string]any)
			totalBarRevenue += bar["revenue_cents"].(float64)
		}
		if totalBarRevenue != 12800 {
			t.Fatalf("expected bars to sum to 12800, got %v", totalBarRevenue)
		}
	})
}

// TestReports_DeltasAndMonthPeriod closes two gaps TestReports_RBACAndAggregation
// left open: it never exercised a real (non-null) delta — its bookings were
// all "current period", so every *_delta* field only ever hit the
// no-prior-data nil branch — and it never hit period=month at all. There's
// no way to seed a completed booking in the past through the public API
// (POST /queue requires a future scheduled_start_at), so the "previous
// period" row is inserted directly via env.db — same shortcut
// setWashingPointID/promoteToRole already use for setup the API itself
// doesn't expose.
func TestReports_DeltasAndMonthPeriod(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(150), user.RoleAdmin)
	staffPhone := uniquePhone(151)
	env.loginAs(t, staffPhone, user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(152), "")
	customer2Access := env.loginAs(t, uniquePhone(153), "")

	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Deltas Point", "address": "1 Deltas St", "latitude": 1.0, "longitude": 2.0,
		"boxes_count": 1, "open_time": "00:00", "close_time": "23:59",
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	env.setWashingPointID(t, staffPhone, wpID)
	staffAccess := env.reLogin(t, staffPhone)

	svc := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", adminAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 10000}},
	})
	if svc.status != http.StatusCreated {
		t.Fatalf("create service: expected 201, got %d (%v)", svc.status, svc.body)
	}
	priceOptions, _ := svc.body["price_options"].([]any)
	priceOptionID, _ := priceOptions[0].(map[string]any)["id"].(string)
	svcID := svc.str("id")

	// Seed the "previous week" with one completed 10000-cent booking,
	// inserted directly since the API can't create a past booking. 9 days
	// ago safely lands in [today-13d, today-6d) — period=week's previous
	// period — regardless of what time of day this test happens to run.
	var pastUser user.User
	if err := env.db.Where("phone_number = ?", uniquePhone(152)).First(&pastUser).Error; err != nil {
		t.Fatalf("find customer user: %v", err)
	}
	pastCarID := env.createCar(t, customerAccess, "Past Car")
	pastCarUUID, err := uuid.Parse(pastCarID)
	if err != nil {
		t.Fatalf("parse car id: %v", err)
	}
	svcUUID, _ := uuid.Parse(svcID)
	priceUUID, _ := uuid.Parse(priceOptionID)
	wpUUID, _ := uuid.Parse(wpID)
	pastStart := time.Now().Add(-9 * 24 * time.Hour)
	pastRow := queue.Queue{
		Status:           queue.StatusReady,
		UserID:           pastUser.ID,
		CarID:            &pastCarUUID,
		ServiceID:        svcUUID,
		PriceOptionID:    priceUUID,
		WashingPointID:   wpUUID,
		BoxNumber:        1,
		ScheduledStartAt: pastStart,
		ScheduledEndAt:   pastStart.Add(30 * time.Minute),
	}
	if err := env.db.Create(&pastRow).Error; err != nil {
		t.Fatalf("insert past booking: %v", err)
	}

	// Two "current period" bookings today, 20000 cents each, from two
	// different customers (queue_one_active_booking_per_user forbids the
	// same customer holding two active bookings at once) and spaced far
	// enough apart in the one box that they don't overlap.
	svc2 := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", adminAccess, map[string]any{
		"name": "Full Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 20000}},
	})
	if svc2.status != http.StatusCreated {
		t.Fatalf("create service 2: expected 201, got %d (%v)", svc2.status, svc2.body)
	}
	priceOptions2, _ := svc2.body["price_options"].([]any)
	priceOptionID2, _ := priceOptions2[0].(map[string]any)["id"].(string)
	svcID2 := svc2.str("id")

	car1ID := env.createCar(t, customerAccess, "Current Car 1")
	car2ID := env.createCar(t, customer2Access, "Current Car 2")
	booking1 := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
		"car_id": car1ID, "service_id": svcID2, "box_number": 1,
		"price_option_id": priceOptionID2, "scheduled_start_at": todayBookingTime(5),
	})
	if booking1.status != http.StatusCreated {
		t.Fatalf("create booking 1: expected 201, got %d (%v)", booking1.status, booking1.body)
	}
	booking2 := env.do(t, http.MethodPost, "/api/v1/queue", customer2Access, map[string]any{
		"car_id": car2ID, "service_id": svcID2, "box_number": 1,
		"price_option_id": priceOptionID2, "scheduled_start_at": todayBookingTime(60),
	})
	if booking2.status != http.StatusCreated {
		t.Fatalf("create booking 2: expected 201, got %d (%v)", booking2.status, booking2.body)
	}

	for _, bookingID := range []string{booking1.str("id"), booking2.str("id")} {
		for _, status := range []string{"waiting", "washing", "ready"} {
			if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+bookingID+"/status", staffAccess, map[string]any{"status": status}); r.status != http.StatusOK {
				t.Fatalf("advance %s to %s: expected 200, got %d (%v)", bookingID, status, r.status, r.body)
			}
		}
	}

	t.Run("week: real deltas against real previous-period data", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/reports?period=week", staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		kpis, _ := resp.body["kpis"].(map[string]any)

		// current: 2 cars, 40000 revenue, 20000 avg. previous: 1 car, 10000
		// revenue, 10000 avg. revenue_delta_pct = (40000-10000)/10000*100 = 300.
		// cars_delta = 2-1 = 1. avg_receipt_delta_pct = (20000-10000)/10000*100 = 100.
		if kpis["revenue_cents"] != float64(40000) || kpis["cars"] != float64(2) || kpis["avg_receipt_cents"] != float64(20000) {
			t.Fatalf("unexpected current-period kpis: %v", kpis)
		}
		if kpis["revenue_delta_pct"] != float64(300) {
			t.Fatalf("expected revenue_delta_pct=300 (real, non-null), got %v", kpis["revenue_delta_pct"])
		}
		if kpis["cars_delta"] != float64(1) {
			t.Fatalf("expected cars_delta=1 (real, non-null), got %v", kpis["cars_delta"])
		}
		if kpis["avg_receipt_delta_pct"] != float64(100) {
			t.Fatalf("expected avg_receipt_delta_pct=100 (real, non-null), got %v", kpis["avg_receipt_delta_pct"])
		}
		// The previous period had real booked minutes too (not zero), so
		// this must be a real number, not the "no prior data" nil.
		if kpis["box_utilization_delta_pp"] == nil {
			t.Fatalf("expected a real (non-nil) box_utilization_delta_pp, got nil: %v", kpis)
		}
	})

	t.Run("month: month-to-date includes today's bookings", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/reports?period=month", staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["period"] != "month" {
			t.Fatalf("expected period=month, got %v", resp.body["period"])
		}
		rangeLabel, _ := resp.body["range_label"].(string)
		if !strings.HasPrefix(rangeLabel, "1 ") && !strings.Contains(rangeLabel, "1 –") {
			t.Fatalf("expected month range_label to start from the 1st of the month, got %q", rangeLabel)
		}
		kpis, _ := resp.body["kpis"].(map[string]any)
		// >= rather than == : whether the "9 days ago" previous-period
		// fixture also falls inside month-to-date depends on which day of
		// the real calendar month this test happens to run on (it does
		// once the month is at least 10 days in) — only today's 2
		// bookings are guaranteed present regardless of run date.
		if cars, _ := kpis["cars"].(float64); cars < 2 {
			t.Fatalf("expected at least today's 2 bookings in month-to-date, got %v", kpis)
		}
		if revenue, _ := kpis["revenue_cents"].(float64); revenue < 40000 {
			t.Fatalf("expected at least today's 40000 cents in month-to-date, got %v", kpis)
		}
		bars, _ := resp.body["bars"].([]any)
		wantDays := int(time.Now().Day())
		if len(bars) != wantDays {
			t.Fatalf("expected %d daily bars (1st through today), got %d", wantDays, len(bars))
		}
		last, _ := bars[len(bars)-1].(map[string]any)
		if last["highlighted"] != true {
			t.Fatalf("expected the last (today's) bar to be highlighted, got %v", last)
		}
	})
}

// sseFrames reads "data: ...\n\n" frames off an open SSE response body,
// decoding each as JSON and skipping ": ping\n\n" heartbeat comment lines.
// A read blocks until the request's own context deadline fires, so a
// missing push fails the test instead of hanging forever.
type sseFrames struct {
	r *bufio.Reader
}

func (s *sseFrames) next(t *testing.T) map[string]any {
	t.Helper()
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("decode SSE payload: %v (%q)", err, line)
		}
		return payload
	}
}

// TestDisplayBoardEvents_PushesOnBoxToggleAndStatusChange covers the new
// GET .../board/events SSE route: an initial snapshot on connect, then a
// fresh push when a box is closed (internal/box's new bus.Publish call
// site) and again when a booking's status changes (queue's pre-existing
// call sites, reused here for the first time by a board-shaped consumer).
func TestDisplayBoardEvents_PushesOnBoxToggleAndStatusChange(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(120), user.RoleAdmin)
	staffPhone := uniquePhone(121)
	env.loginAs(t, staffPhone, user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(122), "")

	wp := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Board Events Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
		"boxes_count": 1, "open_time": "00:00", "close_time": "23:59",
	})
	if wp.status != http.StatusCreated {
		t.Fatalf("create wp: expected 201, got %d (%v)", wp.status, wp.body)
	}
	wpID := wp.str("id")
	env.setWashingPointID(t, staffPhone, wpID)
	staffAccess := env.reLogin(t, staffPhone)

	boxes := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/boxes", staffAccess, nil)
	items, _ := boxes.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 auto-seeded box, got %v", items)
	}
	boxID, _ := items[0].(map[string]any)["id"].(string)

	svc := env.do(t, http.MethodPost, "/api/v1/washing-points/"+wpID+"/services", adminAccess, map[string]any{
		"name": "Quick Wash", "duration_minutes": 30,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 500}},
	})
	priceOptions, _ := svc.body["price_options"].([]any)
	priceOptionID, _ := priceOptions[0].(map[string]any)["id"].(string)
	svcID := svc.str("id")

	t.Run("unauthenticated cannot open the stream", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/board/events", "", nil)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%v)", resp.status, resp.body)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.baseURL+"/api/v1/washing-points/"+wpID+"/board/events", nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+staffAccess)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
	frames := &sseFrames{r: bufio.NewReader(resp.Body)}

	t.Run("initial snapshot on connect", func(t *testing.T) {
		frame := frames.next(t)
		if frame["boxes_active"] != float64(0) || frame["boxes_total"] != float64(1) {
			t.Fatalf("expected boxes_active=0 boxes_total=1, got %v", frame)
		}
	})

	t.Run("closing the box pushes a fresh snapshot reflecting is_open:false", func(t *testing.T) {
		toggle := env.do(t, http.MethodPatch, "/api/v1/washing-points/"+wpID+"/boxes/"+boxID, staffAccess, map[string]any{"is_open": false})
		if toggle.status != http.StatusOK {
			t.Fatalf("close box: expected 200, got %d (%v)", toggle.status, toggle.body)
		}
		frame := frames.next(t)
		pushedBoxes, _ := frame["boxes"].([]any)
		if len(pushedBoxes) != 1 {
			t.Fatalf("expected 1 box in pushed snapshot, got %v", frame)
		}
		box, _ := pushedBoxes[0].(map[string]any)
		if box["is_open"] != false {
			t.Fatalf("expected pushed snapshot to show is_open:false, got %v", box)
		}
	})

	t.Run("advancing a booking to washing pushes another snapshot reflecting boxes_active:1", func(t *testing.T) {
		reopen := env.do(t, http.MethodPatch, "/api/v1/washing-points/"+wpID+"/boxes/"+boxID, staffAccess, map[string]any{"is_open": true})
		if reopen.status != http.StatusOK {
			t.Fatalf("reopen box: expected 200, got %d (%v)", reopen.status, reopen.body)
		}
		frames.next(t) // the reopen's own push, not under test here

		carID := env.createCar(t, customerAccess, "Board Events Car")
		booking := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
			"car_id": carID, "service_id": svcID, "box_number": 1,
			"price_option_id": priceOptionID, "scheduled_start_at": todayBookingTime(15),
		})
		if booking.status != http.StatusCreated {
			t.Fatalf("create booking: expected 201, got %d (%v)", booking.status, booking.body)
		}
		frames.next(t) // the booking creation's own push

		bookingID := booking.str("id")
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+bookingID+"/status", staffAccess, map[string]any{"status": "waiting"}); r.status != http.StatusOK {
			t.Fatalf("advance to waiting: expected 200, got %d (%v)", r.status, r.body)
		}
		frames.next(t) // the "waiting" transition's own push

		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+bookingID+"/status", staffAccess, map[string]any{"status": "washing"}); r.status != http.StatusOK {
			t.Fatalf("advance to washing: expected 200, got %d (%v)", r.status, r.body)
		}
		frame := frames.next(t)
		if frame["boxes_active"] != float64(1) {
			t.Fatalf("expected boxes_active=1 after advancing to washing, got %v", frame)
		}
	})
}

// findBoxItem returns the live-boxes item with the given number, failing
// the test if it's missing.
func findBoxItem(t *testing.T, items []any, number float64) map[string]any {
	t.Helper()
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["number"] == number {
			return item
		}
	}
	t.Fatalf("box %v not found in %v", number, items)
	return nil
}

// TestConnectionRequests_ApproveRejectLifecycle covers the admin app's
// onboarding queue (internal/connectionrequest): every route is
// admin-only, approving one creates an Owner + a pending_review
// WashingPoint with its default schedule/boxes seeded (Manager.Approve),
// rejecting one leaves no such side effects, and neither can be reviewed
// twice.
func TestConnectionRequests_ApproveRejectLifecycle(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(80), user.RoleAdmin)
	staffAccess := env.loginAs(t, uniquePhone(81), user.RoleStaff)

	newRequestBody := func(businessName string) map[string]any {
		return map[string]any{
			"business_name": businessName, "contact_name": "Jane Doe",
			"contact_phone": "+15559990000", "address": "1 Onboarding Way",
			"boxes_count": 2,
		}
	}

	t.Run("staff is forbidden from the connection-requests queue", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/connection-requests", staffAccess, newRequestBody("Staff Attempt LLC"))
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for staff creating a connection request, got %d (%v)", resp.status, resp.body)
		}
		resp = env.do(t, http.MethodGet, "/api/v1/connection-requests", staffAccess, nil)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for staff listing connection requests, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("approving creates a pending_review washing point with default schedule and boxes", func(t *testing.T) {
		businessName := "Approved Wash Co"
		created := env.do(t, http.MethodPost, "/api/v1/connection-requests", adminAccess, newRequestBody(businessName))
		if created.status != http.StatusCreated {
			t.Fatalf("create connection request: expected 201, got %d (%v)", created.status, created.body)
		}
		if created.str("status") != "new" {
			t.Fatalf("expected status=new on creation, got %q", created.str("status"))
		}
		requestID := created.str("id")

		approved := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+requestID, adminAccess, map[string]any{"status": "approved"})
		if approved.status != http.StatusOK {
			t.Fatalf("approve: expected 200, got %d (%v)", approved.status, approved.body)
		}
		if approved.str("status") != "approved" {
			t.Errorf("expected status=approved, got %q", approved.str("status"))
		}
		if approved.str("reviewed_by") == "" {
			t.Error("expected reviewed_by to be set")
		}
		if approved.body["reviewed_at"] == nil {
			t.Error("expected reviewed_at to be set")
		}

		list := env.do(t, http.MethodGet, "/api/v1/admin/washing-points", adminAccess, nil)
		if list.status != http.StatusOK {
			t.Fatalf("list washing points: expected 200, got %d (%v)", list.status, list.body)
		}
		items, _ := list.body["items"].([]any)
		var wpID string
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["name"] == businessName {
				wpID = item["id"].(string)
				if item["status"] != "pending_review" {
					t.Errorf("expected the approved point's status to be pending_review, got %v", item["status"])
				}
			}
		}
		if wpID == "" {
			t.Fatalf("expected a washing point named %q to have been created on approval", businessName)
		}

		schedule := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/schedule", "", nil)
		if schedule.status != http.StatusOK {
			t.Fatalf("get schedule: expected 200, got %d (%v)", schedule.status, schedule.body)
		}
		scheduleItems, _ := schedule.body["items"].([]any)
		if len(scheduleItems) != 7 {
			t.Errorf("expected a seeded 7-row schedule, got %d rows", len(scheduleItems))
		}

		boxes := env.do(t, http.MethodGet, "/api/v1/washing-points/"+wpID+"/boxes", "", nil)
		if boxes.status != http.StatusOK {
			t.Fatalf("get boxes: expected 200, got %d (%v)", boxes.status, boxes.body)
		}
		boxItems, _ := boxes.body["items"].([]any)
		if len(boxItems) != 2 {
			t.Errorf("expected 2 seeded boxes (matching boxes_count on the request), got %d", len(boxItems))
		}

		reReview := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+requestID, adminAccess, map[string]any{"status": "rejected"})
		if reReview.status != http.StatusConflict {
			t.Fatalf("expected 409 re-reviewing an already-approved request, got %d (%v)", reReview.status, reReview.body)
		}
	})

	t.Run("rejecting leaves no washing point behind and can't be re-reviewed", func(t *testing.T) {
		businessName := "Rejected Wash Co"
		created := env.do(t, http.MethodPost, "/api/v1/connection-requests", adminAccess, newRequestBody(businessName))
		if created.status != http.StatusCreated {
			t.Fatalf("create connection request: expected 201, got %d (%v)", created.status, created.body)
		}
		requestID := created.str("id")

		rejected := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+requestID, adminAccess, map[string]any{"status": "rejected"})
		if rejected.status != http.StatusOK {
			t.Fatalf("reject: expected 200, got %d (%v)", rejected.status, rejected.body)
		}
		if rejected.str("status") != "rejected" {
			t.Errorf("expected status=rejected, got %q", rejected.str("status"))
		}

		list := env.do(t, http.MethodGet, "/api/v1/admin/washing-points", adminAccess, nil)
		if list.status != http.StatusOK {
			t.Fatalf("list washing points: expected 200, got %d (%v)", list.status, list.body)
		}
		items, _ := list.body["items"].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["name"] == businessName {
				t.Fatalf("expected no washing point to be created for a rejected request, found %v", item)
			}
		}

		reReview := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+requestID, adminAccess, map[string]any{"status": "approved"})
		if reReview.status != http.StatusConflict {
			t.Fatalf("expected 409 re-reviewing an already-rejected request, got %d (%v)", reReview.status, reReview.body)
		}
	})
}
