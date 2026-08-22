//go:build integration

package integration

import (
	"bytes"
	"net/http"
	"testing"

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
