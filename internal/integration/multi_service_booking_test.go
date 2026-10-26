//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"q-wash-api/internal/user"
)

func TestQueue_MultiServiceBooking(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(230)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(231), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(232), "")

	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	svc2 := env.do(t, http.MethodPost, "/api/v1/washing-points/"+fx.washingPointID+"/services", fx.staffAccess, map[string]any{
		"name": "Polish", "duration_minutes": 45,
		"price_options": []map[string]any{{"name": "Standard", "price_cents": 700}, {"name": "Large", "price_cents": 900}},
	})
	if svc2.status != http.StatusCreated {
		t.Fatalf("create second service: expected 201, got %d (%v)", svc2.status, svc2.body)
	}
	svc2ID := svc2.str("id")
	price2ID := priceOptionIDByName(t, svc2.body, "Standard")
	items := []map[string]string{
		{"service_id": fx.serviceID, "price_option_id": fx.priceOptionID},
		{"service_id": svc2ID, "price_option_id": price2ID},
	}
	manualPath := "/api/v1/washing-points/" + fx.washingPointID + "/queue/manual"

	t.Run("availability sizes slots for the summed duration", func(t *testing.T) {
		path := "/api/v1/washing-points/" + fx.washingPointID + "/availability?service_ids=" + fx.serviceID + "," + svc2ID + "&date=" + futureBookingDate(9)
		resp := env.do(t, http.MethodGet, path, fx.staffAccess, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		slots, _ := resp.body["items"].([]any)
		if len(slots) == 0 {
			t.Fatalf("expected slots, got %v", resp.body)
		}
		slot := slots[0].(map[string]any)
		start, _ := time.Parse(time.RFC3339, slot["start"].(string))
		end, _ := time.Parse(time.RFC3339, slot["end"].(string))
		if end.Sub(start) != 75*time.Minute {
			t.Fatalf("expected a 75 minute slot, got %v", end.Sub(start))
		}
	})

	t.Run("staff books two services: summed duration and price, items listed", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, fx.staffAccess, map[string]any{
			"items": items, "box_number": 1, "scheduled_start_at": futureBookingTime(9), "client_phone": uniquePhone(233),
		})
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
		start, _ := time.Parse(time.RFC3339, resp.str("scheduled_start_at"))
		end, _ := time.Parse(time.RFC3339, resp.str("scheduled_end_at"))
		if end.Sub(start) != 75*time.Minute {
			t.Fatalf("expected a 75 minute booking, got %v", end.Sub(start))
		}
		if resp.str("service_name") != "Quick Wash, Polish" || resp.body["price_cents"] != float64(1200) {
			t.Fatalf("unexpected totals: %v", resp.body)
		}
		lines, _ := resp.body["items"].([]any)
		if len(lines) != 2 || lines[0].(map[string]any)["service_name"] != "Quick Wash" || lines[1].(map[string]any)["price_cents"] != float64(700) {
			t.Fatalf("unexpected items: %v", resp.body["items"])
		}
	})

	t.Run("customer books two services and reads them back", func(t *testing.T) {
		carID := env.createCar(t, customerAccess, "My Car")
		resp := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
			"car_id": carID, "items": items, "box_number": 1, "scheduled_start_at": futureBookingTime(10),
		})
		if resp.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
		}
		if resp.str("service_id") != fx.serviceID || resp.str("price_option_id") != fx.priceOptionID {
			t.Fatalf("expected the legacy fields to mirror the first item, got %v", resp.body)
		}
		got := env.do(t, http.MethodGet, "/api/v1/queue/"+resp.str("id"), customerAccess, nil)
		if lines, _ := got.body["items"].([]any); got.status != http.StatusOK || len(lines) != 2 {
			t.Fatalf("expected 2 items on read-back, got %d (%v)", got.status, got.body)
		}
	})

	t.Run("a repeated service is rejected", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, manualPath, fx.staffAccess, map[string]any{
			"items": []map[string]string{items[0], items[0]}, "box_number": 1,
			"scheduled_start_at": futureBookingTime(11), "client_phone": uniquePhone(234),
		})
		errBody, _ := resp.body["error"].(map[string]any)
		if resp.status != http.StatusBadRequest || errBody["code"] != "duplicate_service" {
			t.Fatalf("expected 400 duplicate_service, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a price option used by a booking cannot be deleted", func(t *testing.T) {
		// The in-use option is the second item's, not the booking's primary one.
		resp := env.do(t, http.MethodDelete, "/api/v1/price-options/"+price2ID, fx.staffAccess, nil)
		errBody, _ := resp.body["error"].(map[string]any)
		if resp.status != http.StatusConflict || errBody["code"] != "price_option_in_use" {
			t.Fatalf("expected 409 price_option_in_use, got %d (%v)", resp.status, resp.body)
		}
	})
}

func priceOptionIDByName(t *testing.T, service map[string]any, name string) string {
	t.Helper()
	options, _ := service["price_options"].([]any)
	for _, o := range options {
		opt, _ := o.(map[string]any)
		if opt["name"] == name {
			id, _ := opt["id"].(string)
			return id
		}
	}
	t.Fatalf("no price option %q in %v", name, service)
	return ""
}
