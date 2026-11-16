//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

// TestBookingConfirmation covers manual confirmation: a point with
// require_confirmation books online bookings as pending, they hold the slot
// and the customer's single active booking, stay off the live boards, and
// staff confirm/reject them (the customer is pushed the decision).
// itemStatus returns the status of the item with the given id in a
// {"items": [...]} list response, or "" when it is absent.
func itemStatus(r apiResponse, id string) string {
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["id"] == id {
			s, _ := m["status"].(string)
			return s
		}
	}
	return ""
}

func TestBookingConfirmation(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(700)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(701), user.RoleAdmin)
	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	staff := fx.staffAccess

	customer := env.loginAs(t, uniquePhone(702), "")
	env.registerDevice(t, customer, "tok-confirm")
	carID := env.createCar(t, customer, "My Car")
	start := futureBookingTime(2)
	body := func(carID string) map[string]any {
		return map[string]any{
			"car_id": carID, "service_id": fx.serviceID, "box_number": 1,
			"price_option_id": fx.priceOptionID, "scheduled_start_at": start,
		}
	}
	book := func(access, carID string) apiResponse {
		return env.do(t, http.MethodPost, "/api/v1/queue", access, body(carID))
	}
	pointPath := "/api/v1/washing-points/" + fx.washingPointID

	t.Run("setting is off by default and only point staff/admin may change it", func(t *testing.T) {
		got := env.do(t, http.MethodGet, pointPath, "", nil)
		if got.body["require_confirmation"] != false {
			t.Fatalf("expected require_confirmation=false by default, got %v", got.body["require_confirmation"])
		}
		if r := env.do(t, http.MethodPatch, pointPath, customer, map[string]any{"require_confirmation": true}); r.status != http.StatusForbidden {
			t.Fatalf("customer: expected 403, got %d", r.status)
		}
		r := env.do(t, http.MethodPatch, pointPath, staff, map[string]any{"require_confirmation": true})
		if r.status != http.StatusOK || r.body["require_confirmation"] != true {
			t.Fatalf("staff enable: %d (%v)", r.status, r.body)
		}
	})

	first := book(customer, carID)
	if first.status != http.StatusCreated {
		t.Fatalf("book: expected 201, got %d (%v)", first.status, first.body)
	}
	if first.str("status") != "pending" {
		t.Fatalf("expected pending, got %q", first.str("status"))
	}
	id := first.str("id")

	t.Run("pending holds the slot and the customer's one active booking", func(t *testing.T) {
		other := env.loginAs(t, uniquePhone(703), "")
		otherCar := env.createCar(t, other, "Other Car")
		if r := book(other, otherCar); r.status != http.StatusConflict || errCode(r) != "slot_unavailable" {
			t.Fatalf("other customer: expected 409 slot_unavailable, got %d (%v)", r.status, r.body)
		}
		if r := book(customer, carID); r.status != http.StatusConflict || errCode(r) != "active_booking_exists" {
			t.Fatalf("same customer: expected 409 active_booking_exists, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("pending shows in the cabinet day view but not on live boards", func(t *testing.T) {
		day := env.do(t, http.MethodGet, pointPath+"/queue/day?date="+futureBookingDate(2), staff, nil)
		if day.status != http.StatusOK || itemStatus(day, id) != "pending" {
			t.Fatalf("day view should list the pending booking: %d (%v)", day.status, day.body)
		}
		live := env.do(t, http.MethodGet, pointPath+"/queue?date="+futureBookingDate(2), staff, nil)
		if live.status != http.StatusOK || itemStatus(live, id) != "" {
			t.Fatalf("live queue must not include pending: %d (%v)", live.status, live.body)
		}
	})

	t.Run("pending cannot be advanced through /status", func(t *testing.T) {
		r := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/status", staff, map[string]string{"status": "waiting"})
		if r.status != http.StatusConflict {
			t.Fatalf("expected 409, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("RBAC: customer cannot confirm; another point's staff gets 404", func(t *testing.T) {
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/confirm", customer, nil); r.status != http.StatusForbidden {
			t.Fatalf("customer confirm: expected 403, got %d", r.status)
		}
		otherStaffPhone := uniquePhone(704)
		env.loginAs(t, otherStaffPhone, user.RoleStaff)
		otherFx := env.setUpBookingFixture(t, adminAccess, otherStaffPhone)
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/confirm", otherFx.staffAccess, nil); r.status != http.StatusNotFound {
			t.Fatalf("other point's staff: expected 404, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("staff confirm: becomes queue and customer is pushed", func(t *testing.T) {
		r := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/confirm", staff, nil)
		if r.status != http.StatusOK || r.str("status") != "queue" {
			t.Fatalf("confirm: %d (%v)", r.status, r.body)
		}
		got := env.push.waitForPush(t, 1)
		if got[0].Msg.Data["kind"] != "booking_confirmed" || got[0].Msg.Data["queue_id"] != id {
			t.Fatalf("unexpected push: %+v", got[0])
		}
		if again := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/confirm", staff, nil); again.status != http.StatusConflict || errCode(again) != "not_pending" {
			t.Fatalf("second confirm: expected 409 not_pending, got %d (%v)", again.status, again.body)
		}
	})

	t.Run("staff reject: canceled, slot freed, customer pushed", func(t *testing.T) {
		// Free the slot from the confirmed booking, then book again as pending.
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/cancel", customer, nil); r.status != http.StatusOK {
			t.Fatalf("cancel: %d (%v)", r.status, r.body)
		}
		next := book(customer, carID)
		if next.status != http.StatusCreated || next.str("status") != "pending" {
			t.Fatalf("rebook: %d (%v)", next.status, next.body)
		}
		nextID := next.str("id")
		r := env.do(t, http.MethodPatch, "/api/v1/queue/"+nextID+"/reject", staff, nil)
		if r.status != http.StatusOK || r.str("status") != "canceled" {
			t.Fatalf("reject: %d (%v)", r.status, r.body)
		}
		got := env.push.waitForPush(t, 2)
		if got[1].Msg.Data["kind"] != "booking_rejected" || got[1].Msg.Data["queue_id"] != nextID {
			t.Fatalf("unexpected push: %+v", got[1])
		}
		// Slot is free again.
		if again := book(customer, carID); again.status != http.StatusCreated {
			t.Fatalf("rebook after reject: expected 201, got %d (%v)", again.status, again.body)
		}
	})

	t.Run("customer can cancel their own pending booking", func(t *testing.T) {
		// The booking made at the end of the reject subtest is still pending.
		cur := env.do(t, http.MethodGet, "/api/v1/me/queue", customer, nil)
		var pendingID string
		for _, it := range cur.body["items"].([]any) {
			if m := it.(map[string]any); m["status"] == "pending" {
				pendingID, _ = m["id"].(string)
			}
		}
		if pendingID == "" {
			t.Fatalf("expected a pending booking in history: %v", cur.body)
		}
		r := env.do(t, http.MethodPatch, "/api/v1/queue/"+pendingID+"/cancel", customer, nil)
		if r.status != http.StatusOK || r.str("status") != "canceled" {
			t.Fatalf("cancel pending: %d (%v)", r.status, r.body)
		}
	})

	t.Run("staff walk-ins are never pending", func(t *testing.T) {
		r := env.do(t, http.MethodPost, pointPath+"/queue/manual", staff, map[string]any{
			"service_id": fx.serviceID, "price_option_id": fx.priceOptionID, "box_number": 1,
			"scheduled_start_at": futureBookingTime(4), "car_name": "Camry", "plate": "1 AB 01",
			"client_phone": uniquePhone(705), "client_name": "Walk In",
		})
		if r.status != http.StatusCreated || r.str("status") != "queue" {
			t.Fatalf("manual: expected 201 status=queue, got %d (%v)", r.status, r.body)
		}
	})
}
