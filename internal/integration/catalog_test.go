//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

func errCode(r apiResponse) string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func items(t *testing.T, r apiResponse) []map[string]any {
	t.Helper()
	raw, _ := r.body["items"].([]any)
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		out[i], _ = v.(map[string]any)
	}
	return out
}

func priceOptions(r apiResponse) []map[string]any {
	raw, _ := r.body["price_options"].([]any)
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		out[i], _ = v.(map[string]any)
	}
	return out
}

func TestServices_CRUDPriceOptionsAndOwnership(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(300)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(301), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(302), "")
	otherStaffPhone := uniquePhone(303)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)

	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	staff := fx.staffAccess

	// A second point with its own staff, to prove staff cannot cross points.
	other := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Point", "address": "2 Test St", "latitude": 1.0, "longitude": 2.0, "boxes_count": 1,
	})
	env.setWashingPointID(t, otherStaffPhone, other.str("id"))
	otherStaff := env.reLogin(t, otherStaffPhone)

	svcPath := "/api/v1/services/" + fx.serviceID
	listPath := "/api/v1/washing-points/" + fx.washingPointID + "/services"

	t.Run("reads are public", func(t *testing.T) {
		list := env.do(t, http.MethodGet, listPath, "", nil)
		if list.status != http.StatusOK || len(items(t, list)) != 1 {
			t.Fatalf("expected 1 listed service, got %d (%v)", list.status, list.body)
		}
		got := env.do(t, http.MethodGet, svcPath, "", nil)
		if got.status != http.StatusOK || got.str("name") != "Quick Wash" {
			t.Fatalf("unexpected service: %d (%v)", got.status, got.body)
		}
		if env.do(t, http.MethodGet, "/api/v1/services/00000000-0000-0000-0000-000000000000", "", nil).status != http.StatusNotFound {
			t.Fatal("unknown service must be 404")
		}
	})

	t.Run("create validation", func(t *testing.T) {
		po := []map[string]any{{"name": "Standard", "price_cents": 100}}
		cases := []struct {
			name string
			body map[string]any
			code string
		}{
			{"blank name", map[string]any{"name": "  ", "duration_minutes": 30, "price_options": po}, "invalid_name"},
			{"non-positive duration", map[string]any{"name": "X", "duration_minutes": 0, "price_options": po}, "invalid_duration"},
			{"no price options", map[string]any{"name": "X", "duration_minutes": 30, "price_options": []any{}}, "price_options_required"},
			{"negative price", map[string]any{"name": "X", "duration_minutes": 30, "price_options": []map[string]any{{"name": "S", "price_cents": -1}}}, "invalid_price_cents"},
			{"two defaults", map[string]any{"name": "X", "duration_minutes": 30, "price_options": []map[string]any{
				{"name": "A", "price_cents": 1, "is_default": true}, {"name": "B", "price_cents": 2, "is_default": true}}}, "multiple_default_price_options"},
		}
		for _, tc := range cases {
			resp := env.do(t, http.MethodPost, listPath, staff, tc.body)
			if resp.status != http.StatusBadRequest || errCode(resp) != tc.code {
				t.Errorf("%s: expected 400 %s, got %d (%v)", tc.name, tc.code, resp.status, resp.body)
			}
		}
	})

	t.Run("first price option becomes the default when none is marked", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, listPath, staff, map[string]any{
			"name": "Two Tier", "duration_minutes": 45,
			"price_options": []map[string]any{{"name": "Sedan", "price_cents": 100}, {"name": "SUV", "price_cents": 200}},
		})
		opts := priceOptions(resp)
		if resp.status != http.StatusCreated || len(opts) != 2 || opts[0]["is_default"] != true || opts[1]["is_default"] != false {
			t.Fatalf("unexpected: %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("writes need the point's own staff (or admin)", func(t *testing.T) {
		patch := map[string]any{"name": "Renamed"}
		if r := env.do(t, http.MethodPatch, svcPath, customerAccess, patch); r.status != http.StatusForbidden {
			t.Errorf("customer: expected 403, got %d", r.status)
		}
		if r := env.do(t, http.MethodPatch, svcPath, "", patch); r.status != http.StatusUnauthorized {
			t.Errorf("anonymous: expected 401, got %d", r.status)
		}
		if r := env.do(t, http.MethodPatch, svcPath, otherStaff, patch); r.status != http.StatusNotFound || errCode(r) != "service_not_found" {
			t.Errorf("other point's staff: expected 404 service_not_found, got %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodDelete, svcPath, otherStaff, nil); r.status != http.StatusNotFound {
			t.Errorf("other point's staff delete: expected 404, got %d", r.status)
		}
		if r := env.do(t, http.MethodPost, "/api/v1/washing-points/"+fx.washingPointID+"/services", otherStaff, map[string]any{
			"name": "X", "duration_minutes": 30, "price_options": []map[string]any{{"name": "S", "price_cents": 1}},
		}); r.status != http.StatusNotFound {
			t.Errorf("other point's staff create: expected 404, got %d", r.status)
		}
	})

	t.Run("staff can partially update; validation applies", func(t *testing.T) {
		resp := env.do(t, http.MethodPatch, svcPath, staff, map[string]any{"name": " Renamed ", "duration_minutes": 40})
		if resp.status != http.StatusOK || resp.str("name") != "Renamed" || resp.body["duration_minutes"] != float64(40) {
			t.Fatalf("unexpected: %d (%v)", resp.status, resp.body)
		}
		if r := env.do(t, http.MethodPatch, svcPath, staff, map[string]any{"duration_minutes": -5}); r.status != http.StatusBadRequest || errCode(r) != "invalid_duration" {
			t.Errorf("expected 400 invalid_duration, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("price options: default handling, last-one and in-use guards", func(t *testing.T) {
		add := env.do(t, http.MethodPost, svcPath+"/price-options", staff, map[string]any{"name": "SUV", "price_cents": 900, "is_default": true})
		if add.status != http.StatusCreated || add.body["is_default"] != true {
			t.Fatalf("add: %d (%v)", add.status, add.body)
		}
		newID := add.str("id")

		svc := env.do(t, http.MethodGet, svcPath, "", nil)
		defaults := 0
		for _, o := range priceOptions(svc) {
			if o["is_default"] == true {
				defaults++
			}
		}
		if defaults != 1 {
			t.Fatalf("exactly one default expected after promoting a new one, got %d (%v)", defaults, svc.body)
		}

		if r := env.do(t, http.MethodPatch, "/api/v1/price-options/"+newID, staff, map[string]any{"is_default": false}); r.status != http.StatusBadRequest || errCode(r) != "cannot_unset_default" {
			t.Errorf("unsetting the sole default: expected 400 cannot_unset_default, got %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodPatch, "/api/v1/price-options/"+newID, staff, map[string]any{"price_cents": 950}); r.status != http.StatusOK || r.body["price_cents"] != float64(950) {
			t.Errorf("price update: %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodPatch, "/api/v1/price-options/"+newID, otherStaff, map[string]any{"price_cents": 1}); r.status != http.StatusNotFound {
			t.Errorf("other point's staff: expected 404, got %d", r.status)
		}

		// A booking pins the original option: it can't be deleted while in use.
		customer := env.loginAs(t, uniquePhone(304), "")
		car := env.createCar(t, customer, "Car")
		booked := env.do(t, http.MethodPost, "/api/v1/queue", customer, map[string]any{
			"car_id": car, "service_id": fx.serviceID, "box_number": 1,
			"price_option_id": fx.priceOptionID, "scheduled_start_at": futureBookingTime(6),
		})
		if booked.status != http.StatusCreated {
			t.Fatalf("booking: %d (%v)", booked.status, booked.body)
		}
		if r := env.do(t, http.MethodDelete, "/api/v1/price-options/"+fx.priceOptionID, staff, nil); r.status != http.StatusConflict || errCode(r) != "price_option_in_use" {
			t.Errorf("expected 409 price_option_in_use, got %d (%v)", r.status, r.body)
		}

		// Deleting the default promotes another one.
		if r := env.do(t, http.MethodDelete, "/api/v1/price-options/"+newID, staff, nil); r.status != http.StatusNoContent {
			t.Fatalf("delete unused default: expected 204, got %d (%v)", r.status, r.body)
		}
		after := env.do(t, http.MethodGet, svcPath, "", nil)
		opts := priceOptions(after)
		if len(opts) != 1 || opts[0]["is_default"] != true {
			t.Fatalf("the remaining option must be promoted to default, got %v", after.body)
		}
		if r := env.do(t, http.MethodDelete, "/api/v1/price-options/"+fx.priceOptionID, staff, nil); r.status != http.StatusConflict || errCode(r) != "last_price_option" {
			t.Errorf("expected 409 last_price_option, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("deactivate hides the service from booking without deleting it", func(t *testing.T) {
		if r := env.do(t, http.MethodDelete, svcPath, staff, nil); r.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%v)", r.status, r.body)
		}
		got := env.do(t, http.MethodGet, svcPath, "", nil)
		if got.status != http.StatusOK || got.body["is_active"] != false {
			t.Fatalf("service should remain readable but inactive: %d (%v)", got.status, got.body)
		}
	})
}

func TestCars_CRUDOwnershipAndInUse(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(310)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(311), user.RoleAdmin)
	ownerAccess := env.loginAs(t, uniquePhone(312), "")
	strangerAccess := env.loginAs(t, uniquePhone(313), "")
	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)

	if r := env.do(t, http.MethodGet, "/api/v1/me/cars", "", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous list: expected 401, got %d", r.status)
	}

	carID := env.createCar(t, ownerAccess, "  Toyota Camry  ")

	t.Run("name is trimmed and required", func(t *testing.T) {
		list := env.do(t, http.MethodGet, "/api/v1/me/cars", ownerAccess, nil)
		cars := items(t, list)
		if len(cars) != 1 || cars[0]["name"] != "Toyota Camry" {
			t.Fatalf("unexpected list: %v", list.body)
		}
		if r := env.do(t, http.MethodPost, "/api/v1/me/cars", ownerAccess, map[string]string{"name": "  "}); r.status != http.StatusBadRequest || errCode(r) != "invalid_name" {
			t.Errorf("expected 400 invalid_name, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("cars are private to their owner", func(t *testing.T) {
		if got := items(t, env.do(t, http.MethodGet, "/api/v1/me/cars", strangerAccess, nil)); len(got) != 0 {
			t.Errorf("a stranger must not see the owner's cars, got %v", got)
		}
		if r := env.do(t, http.MethodPatch, "/api/v1/cars/"+carID, strangerAccess, map[string]string{"name": "Mine now"}); r.status != http.StatusNotFound {
			t.Errorf("stranger update: expected 404, got %d", r.status)
		}
		if r := env.do(t, http.MethodDelete, "/api/v1/cars/"+carID, strangerAccess, nil); r.status != http.StatusNotFound {
			t.Errorf("stranger delete: expected 404, got %d", r.status)
		}
	})

	t.Run("rename", func(t *testing.T) {
		r := env.do(t, http.MethodPatch, "/api/v1/cars/"+carID, ownerAccess, map[string]string{"name": "Camry 2"})
		if r.status != http.StatusOK || r.str("name") != "Camry 2" {
			t.Fatalf("unexpected: %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodPatch, "/api/v1/cars/"+carID, ownerAccess, map[string]any{}); r.status != http.StatusBadRequest {
			t.Errorf("missing name: expected 400, got %d", r.status)
		}
	})

	t.Run("a car with bookings cannot be deleted; an unused one can", func(t *testing.T) {
		booked := env.do(t, http.MethodPost, "/api/v1/queue", ownerAccess, map[string]any{
			"car_id": carID, "service_id": fx.serviceID, "box_number": 1,
			"price_option_id": fx.priceOptionID, "scheduled_start_at": futureBookingTime(7),
		})
		if booked.status != http.StatusCreated {
			t.Fatalf("booking: %d (%v)", booked.status, booked.body)
		}
		if r := env.do(t, http.MethodDelete, "/api/v1/cars/"+carID, ownerAccess, nil); r.status != http.StatusConflict || errCode(r) != "car_in_use" {
			t.Errorf("expected 409 car_in_use, got %d (%v)", r.status, r.body)
		}
		spare := env.createCar(t, ownerAccess, "Spare")
		if r := env.do(t, http.MethodDelete, "/api/v1/cars/"+spare, ownerAccess, nil); r.status != http.StatusNoContent {
			t.Errorf("expected 204, got %d (%v)", r.status, r.body)
		}
		if got := items(t, env.do(t, http.MethodGet, "/api/v1/me/cars", ownerAccess, nil)); len(got) != 1 {
			t.Errorf("expected only the booked car left, got %v", got)
		}
	})
}

func TestQueue_GetOwnershipAndHistoryPagination(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(320)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(321), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(322), "")
	strangerAccess := env.loginAs(t, uniquePhone(323), "")
	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	carID := env.createCar(t, customerAccess, "Car")

	book := func(daysAhead int) string {
		r := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
			"car_id": carID, "service_id": fx.serviceID, "box_number": 1,
			"price_option_id": fx.priceOptionID, "scheduled_start_at": futureBookingTime(daysAhead),
		})
		if r.status != http.StatusCreated {
			t.Fatalf("booking day+%d: %d (%v)", daysAhead, r.status, r.body)
		}
		return r.str("id")
	}
	// One active booking per customer: cancel each before making the next.
	cancel := func(id string) {
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/cancel", customerAccess, nil); r.status != http.StatusOK {
			t.Fatalf("cancel: %d (%v)", r.status, r.body)
		}
	}
	oldest := book(2)
	cancel(oldest)
	middle := book(3)
	cancel(middle)
	newest := book(4)

	t.Run("a booking is visible to its owner and to the point's staff, hidden from others", func(t *testing.T) {
		path := "/api/v1/queue/" + newest
		if r := env.do(t, http.MethodGet, path, customerAccess, nil); r.status != http.StatusOK || r.str("id") != newest {
			t.Errorf("owner: %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodGet, path, fx.staffAccess, nil); r.status != http.StatusOK {
			t.Errorf("point's staff: %d", r.status)
		}
		if r := env.do(t, http.MethodGet, path, strangerAccess, nil); r.status != http.StatusNotFound || errCode(r) != "queue_not_found" {
			t.Errorf("stranger: expected 404 queue_not_found, got %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodGet, path, "", nil); r.status != http.StatusUnauthorized {
			t.Errorf("anonymous: expected 401, got %d", r.status)
		}
	})

	t.Run("history lists every status, latest scheduled first, and paginates", func(t *testing.T) {
		page1 := env.do(t, http.MethodGet, "/api/v1/me/queue?page=1&page_size=2", customerAccess, nil)
		got := items(t, page1)
		if page1.status != http.StatusOK || len(got) != 2 || page1.body["total"] != float64(3) || page1.body["page"] != float64(1) || page1.body["page_size"] != float64(2) {
			t.Fatalf("page 1: %d (%v)", page1.status, page1.body)
		}
		if got[0]["id"] != newest || got[1]["id"] != middle || got[1]["status"] != "canceled" {
			t.Errorf("expected newest then the canceled middle booking, got %v", got)
		}
		page2 := items(t, env.do(t, http.MethodGet, "/api/v1/me/queue?page=2&page_size=2", customerAccess, nil))
		if len(page2) != 1 || page2[0]["id"] != oldest {
			t.Errorf("page 2 should hold the oldest booking, got %v", page2)
		}
		if got := items(t, env.do(t, http.MethodGet, "/api/v1/me/queue", strangerAccess, nil)); len(got) != 0 {
			t.Errorf("history is per-user; a stranger must see nothing, got %v", got)
		}
	})
}
