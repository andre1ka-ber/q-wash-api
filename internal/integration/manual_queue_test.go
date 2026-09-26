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

	t.Run("car data given for an existing client upserts their car instead of duplicating it", func(t *testing.T) {
		if resp := env.do(t, http.MethodPatch, "/api/v1/queue/"+manualID+"/cancel", staffAccess, nil); resp.status != http.StatusOK {
			t.Fatalf("cancel: got %d (%v)", resp.status, resp.body)
		}
		b := body(walkInPhone, 1, futureBookingTime(9))
		b["car_name"] = "Toyota Camry 2020"
		b["plate"] = "1234 ab 01"
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, b)
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
		var cars []car.Car
		if err := env.db.Joins("JOIN users ON users.id = cars.user_id").Where("users.phone_number = ?", walkInPhone).Find(&cars).Error; err != nil {
			t.Fatal(err)
		}
		if len(cars) != 1 || cars[0].Name != "Toyota Camry 2020" {
			t.Fatalf("expected the one existing car renamed, got %+v", cars)
		}
	})
}
