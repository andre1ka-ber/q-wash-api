//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

func TestQueue_OverflowBox(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(230)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(231), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(232), "")

	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	staffAccess := fx.staffAccess
	wp := "/api/v1/washing-points/" + fx.washingPointID
	manualPath := wp + "/queue/manual"
	dayPath := wp + "/queue/day?date=" + futureBookingDate(5)
	availPath := wp + "/availability?service_id=" + fx.serviceID + "&date=" + futureBookingDate(5)
	overflowSlotsPath := wp + "/queue/overflow-slots?service_id=" + fx.serviceID + "&date=" + futureBookingDate(5)
	start := futureBookingTime(5)

	body := func(phone string, overflow bool, box int) map[string]any {
		return map[string]any{
			"service_id": fx.serviceID, "price_option_id": fx.priceOptionID, "box_number": box,
			"overflow": overflow, "scheduled_start_at": start, "client_phone": phone,
		}
	}
	slotCount := func(t *testing.T, path, token string) int {
		t.Helper()
		resp := env.do(t, http.MethodGet, path, token, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d (%v)", path, resp.status, resp.body)
		}
		items, _ := resp.body["items"].([]any)
		return len(items)
	}

	// A real booking holds every box at `start`, so the slot is taken.
	realBooking := env.do(t, http.MethodPost, manualPath, staffAccess, body(uniquePhone(233), false, 1))
	if realBooking.status != http.StatusCreated {
		t.Fatalf("real booking: expected 201, got %d (%v)", realBooking.status, realBooking.body)
	}
	availBefore := slotCount(t, availPath, "")
	overflowSlots := slotCount(t, overflowSlotsPath, staffAccess)

	var overflowID string
	t.Run("overflow booking is accepted even though the slot is taken", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body(uniquePhone(234), true, 0))
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
		if resp.body["is_overflow"] != true || resp.body["box_number"] != float64(0) || resp.str("ticket") != "O-11" {
			t.Fatalf("unexpected response: %v", resp.body)
		}
		overflowID = resp.str("id")
	})

	t.Run("overflow bookings may overlap each other", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body(uniquePhone(235), true, 0))
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("one active booking per client still applies", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, staffAccess, body(uniquePhone(234), true, 0))
		if resp.status != http.StatusConflict {
			t.Fatalf("expected 409, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("public availability is unchanged by overflow bookings", func(t *testing.T) {
		if got := slotCount(t, availPath, ""); got != availBefore {
			t.Fatalf("availability changed: %d -> %d slots", availBefore, got)
		}
	})

	t.Run("overflow slot list is the full list, taken slots included", func(t *testing.T) {
		if got := slotCount(t, overflowSlotsPath, staffAccess); got != overflowSlots {
			t.Fatalf("overflow slot list changed: %d -> %d", overflowSlots, got)
		}
		if overflowSlots <= availBefore {
			t.Fatalf("expected more overflow slots (%d) than public free slots (%d)", overflowSlots, availBefore)
		}
	})

	t.Run("customers cannot reach overflow slots or create overflow bookings", func(t *testing.T) {
		if resp := env.do(t, http.MethodGet, overflowSlotsPath, customerAccess, nil); resp.status != http.StatusForbidden {
			t.Fatalf("slots: expected 403, got %d", resp.status)
		}
		if resp := env.do(t, http.MethodPost, manualPath, customerAccess, body(uniquePhone(236), true, 0)); resp.status != http.StatusForbidden {
			t.Fatalf("create: expected 403, got %d", resp.status)
		}
	})

	t.Run("day list shows overflow rows flagged", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, dayPath, staffAccess, nil)
		items, _ := resp.body["items"].([]any)
		overflow := 0
		for _, it := range items {
			if it.(map[string]any)["is_overflow"] == true {
				overflow++
			}
		}
		if resp.status != http.StatusOK || overflow != 2 || len(items) != 3 {
			t.Fatalf("expected 2 overflow of 3 rows, got %d/%d (%v)", overflow, len(items), resp.body)
		}
	})

	t.Run("live boxes never include overflow rows", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, wp+"/boxes/live", staffAccess, nil)
		items, _ := resp.body["items"].([]any)
		for _, it := range items {
			box := it.(map[string]any)
			if next, ok := box["next"].(map[string]any); ok && next["id"] == overflowID {
				t.Fatalf("overflow row leaked into live boxes: %v", box)
			}
		}
	})

	t.Run("staff can start and finish an overflow booking in place", func(t *testing.T) {
		statusPath := "/api/v1/queue/" + overflowID + "/status"
		for _, st := range []string{"waiting", "washing", "ready"} {
			resp := env.do(t, http.MethodPatch, statusPath, staffAccess, map[string]string{"status": st})
			if resp.status != http.StatusOK || resp.str("status") != st {
				t.Fatalf("-> %s: got %d (%v)", st, resp.status, resp.body)
			}
		}
	})
}
