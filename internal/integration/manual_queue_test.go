//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/car"
	"q-wash-api/internal/user"
)

func TestQueue_ManualBookingDayListAndRestore(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(210)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(211), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(212), "")
	otherStaffPhone := uniquePhone(213)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)

	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	staffAccess := fx.staffAccess
	manualPath := "/api/v1/washing-points/" + fx.washingPointID + "/queue/manual"
	dayPath := "/api/v1/washing-points/" + fx.washingPointID + "/queue/day?date=" + futureBookingDate(5)

	body := func(phone string, box int, start string) map[string]any {
		return map[string]any{
			"service_id": fx.serviceID, "price_option_id": fx.priceOptionID, "box_number": box,
			"scheduled_start_at": start, "car_name": "Toyota Camry", "plate": "1234 AB 01",
			"client_phone": phone, "client_name": "Walk In",
		}
	}
	walkInPhone := uniquePhone(214)
	start := futureBookingTime(5)

	var manualID string
	t.Run("staff creates a walk-in booking, user found-or-created by phone", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body(walkInPhone, 1, start))
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
		if resp.str("status") != "queue" || resp.str("source") != "manual" || resp.str("client_phone") != walkInPhone {
			t.Fatalf("unexpected response: %v", resp.body)
		}
		manualID = resp.str("id")
	})

	t.Run("phone is required and validated", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body("", 1, start))
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("overlapping slot in the same box is rejected", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body(uniquePhone(215), 1, start))
		errBody, _ := resp.body["error"].(map[string]any)
		if resp.status != http.StatusConflict || errBody["code"] != "slot_unavailable" {
			t.Fatalf("expected 409 slot_unavailable, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("same client cannot hold two active bookings", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body(walkInPhone, 1, futureBookingTime(6)))
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409 active_booking_exists, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("staff phone cannot be booked as a client", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body(otherStaffPhone, 1, futureBookingTime(7)))
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409 phone_not_customer, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("customers are blocked", func(t *testing.T) {
		if resp := env.do(t, http.MethodPost, manualPath, customerAccess, body(uniquePhone(216), 1, start)); resp.status != http.StatusForbidden {
			t.Fatalf("customer: expected 403, got %d", resp.status)
		}
	})

	t.Run("day list returns the booking with staff-only detail and a ticket", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, dayPath, staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected 1 item, got %v", resp.body)
		}
		item := items[0].(map[string]any)
		if item["ticket"] != "A-11" || item["plate"] != "1234 AB 01" || item["client_phone"] != walkInPhone ||
			item["service_name"] != "Quick Wash" || item["price_cents"] != float64(500) || item["source"] != "manual" {
			t.Fatalf("unexpected item: %v", item)
		}
	})

	t.Run("day list is not reachable by customers", func(t *testing.T) {
		if resp := env.do(t, http.MethodGet, dayPath, customerAccess, nil); resp.status != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", resp.status)
		}
	})

	statusPath := "/api/v1/queue/" + manualID + "/status"
	t.Run("no_show frees the slot; restore re-takes it unless someone else did", func(t *testing.T) {
		if resp := env.do(t, http.MethodPatch, statusPath, staffAccess, map[string]string{"status": "no_show"}); resp.status != http.StatusOK || resp.str("status") != "no_show" {
			t.Fatalf("no_show: got %d (%v)", resp.status, resp.body)
		}
		taker := env.do(t, http.MethodPost, manualPath, staffAccess, body(uniquePhone(217), 1, start))
		if taker.status != http.StatusCreated {
			t.Fatalf("expected the freed slot to be bookable, got %d (%v)", taker.status, taker.body)
		}
		if resp := env.do(t, http.MethodPatch, statusPath, staffAccess, map[string]string{"status": "queue"}); resp.status != http.StatusConflict {
			t.Fatalf("restore into a taken slot: expected 409, got %d (%v)", resp.status, resp.body)
		}
		if resp := env.do(t, http.MethodPatch, "/api/v1/queue/"+taker.str("id")+"/cancel", staffAccess, nil); resp.status != http.StatusOK {
			t.Fatalf("cancel taker: got %d (%v)", resp.status, resp.body)
		}
		resp := env.do(t, http.MethodPatch, statusPath, staffAccess, map[string]string{"status": "queue"})
		if resp.status != http.StatusOK || resp.str("status") != "queue" {
			t.Fatalf("restore: got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("only the phone is required; a local number gets +992 and no car is created", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, map[string]any{
			"service_id": fx.serviceID, "price_option_id": fx.priceOptionID, "box_number": 1,
			"scheduled_start_at": futureBookingTime(8), "client_phone": "90 022-23-33",
		})
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
		if resp.str("client_phone") != "+992900222333" || resp.str("car_name") != "" {
			t.Fatalf("unexpected response: %v", resp.body)
		}
	})

	t.Run("a client's existing car is reused when it matches and never rewritten", func(t *testing.T) {
		carsOf := func() []car.Car {
			var cars []car.Car
			if err := env.db.Joins("JOIN users ON users.id = cars.user_id").Where("users.phone_number = ?", walkInPhone).Order("cars.created_at").Find(&cars).Error; err != nil {
				t.Fatal(err)
			}
			return cars
		}
		cancel := func(id string) {
			if c := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/cancel", staffAccess, nil); c.status != http.StatusOK {
				t.Fatalf("cancel: got %d (%v)", c.status, c.body)
			}
		}
		book := func(day int, name, plate string) {
			b := body(walkInPhone, 1, futureBookingTime(day))
			b["car_name"], b["plate"] = name, plate
			resp := env.do(t, http.MethodPost, manualPath, staffAccess, b)
			if resp.status != http.StatusCreated {
				t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
			}
			cancel(resp.str("id"))
		}
		cancel(manualID)
		book(9, "toyota camry", "1234 ab 01")
		if cars := carsOf(); len(cars) != 1 {
			t.Fatalf("a matching car must be reused, got %+v", cars)
		}
		book(10, "Toyota Camry 2020", "1234 AB 01")
		cars := carsOf()
		if len(cars) != 2 || cars[0].Name != "Toyota Camry" || cars[1].Name != "Toyota Camry 2020" {
			t.Fatalf("expected the old car untouched and a new one, got %+v", cars)
		}
	})
}

func TestQueue_ManualBookingLimits(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(970)
	env.loginAs(t, staffPhone, user.RoleStaff)
	otherStaffPhone := uniquePhone(971)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)
	admin := env.loginAs(t, uniquePhone(972), user.RoleAdmin)
	customerPhone := uniquePhone(973)
	customer := env.loginAs(t, customerPhone, "")
	if resp := env.do(t, http.MethodPatch, "/api/v1/me", customer, map[string]string{"name": "Private Name"}); resp.status != http.StatusOK {
		t.Fatalf("set name: %d", resp.status)
	}
	here := env.setUpBookingFixture(t, admin, staffPhone)
	there := env.setUpBookingFixture(t, admin, otherStaffPhone)

	manual := func(fx bookingFixture, phone, start string) apiResponse {
		return env.do(t, http.MethodPost, "/api/v1/washing-points/"+fx.washingPointID+"/queue/manual", fx.staffAccess, map[string]any{
			"service_id": fx.serviceID, "price_option_id": fx.priceOptionID, "box_number": 1,
			"scheduled_start_at": start, "client_phone": phone,
		})
	}

	t.Run("start must be within the last day and the next 60 days", func(t *testing.T) {
		for _, day := range []int{-3, 62} {
			resp := manual(here, uniquePhone(974), futureBookingTime(day))
			errBody, _ := resp.body["error"].(map[string]any)
			if resp.status != http.StatusBadRequest || errBody["code"] != "invalid_scheduled_start_at" {
				t.Errorf("day %d: expected 400 invalid_scheduled_start_at, got %d (%v)", day, resp.status, resp.body)
			}
		}
	})

	t.Run("a point does not learn the name of a client it never served", func(t *testing.T) {
		resp := manual(here, customerPhone, futureBookingTime(3))
		if resp.status != http.StatusCreated || resp.str("client_name") != "" {
			t.Fatalf("expected 201 without the account name, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("manual bookings do not take the client's one active booking", func(t *testing.T) {
		if resp := manual(there, customerPhone, futureBookingTime(4)); resp.status != http.StatusCreated {
			t.Fatalf("another point's manual booking: got %d (%v)", resp.status, resp.body)
		}
		carResp := env.do(t, http.MethodPost, "/api/v1/me/cars", customer, map[string]string{"name": "Mine"})
		own := map[string]any{
			"car_id": carResp.str("id"), "service_id": here.serviceID, "price_option_id": here.priceOptionID,
			"box_number": 1, "scheduled_start_at": futureBookingTime(5),
		}
		if resp := env.do(t, http.MethodPost, "/api/v1/queue", customer, own); resp.status != http.StatusCreated {
			t.Fatalf("the client's own booking: got %d (%v)", resp.status, resp.body)
		}
		own["scheduled_start_at"] = futureBookingTime(6)
		if resp := env.do(t, http.MethodPost, "/api/v1/queue", customer, own); resp.status != http.StatusConflict {
			t.Fatalf("a second app booking: expected 409, got %d", resp.status)
		}
	})

	t.Run("once the client books in the app, the point sees the name", func(t *testing.T) {
		day := env.do(t, http.MethodGet, "/api/v1/washing-points/"+here.washingPointID+"/queue/day?date="+futureBookingDate(5), here.staffAccess, nil)
		items, _ := day.body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["client_name"] != "Private Name" {
			t.Fatalf("expected the name for an app booking, got %v", day.body)
		}
	})
}
