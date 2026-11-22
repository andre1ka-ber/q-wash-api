//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"q-wash-api/internal/user"
)

// TestPlatformSettings covers the admin "Настройки" rules end to end: the
// settings API itself, and each rule acting on real bookings / approvals.
func TestPlatformSettings(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(800)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(801), user.RoleAdmin)
	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	set := func(body map[string]any) apiResponse {
		t.Helper()
		r := env.do(t, http.MethodPatch, "/api/v1/admin/settings", adminAccess, body)
		if r.status != http.StatusOK {
			t.Fatalf("set %v: expected 200, got %d (%v)", body, r.status, r.body)
		}
		return r
	}

	t.Run("defaults keep the old behavior and only admins may read or change them", func(t *testing.T) {
		got := env.do(t, http.MethodGet, "/api/v1/admin/settings", adminAccess, nil)
		if got.status != http.StatusOK {
			t.Fatalf("expected 200, got %d", got.status)
		}
		want := map[string]any{
			"booking_horizon_days": float64(7), "no_show_auto_cancel_min": float64(0), "client_cancel_cutoff_min": float64(0),
			"sms_confirmation": false, "new_point_moderation": true,
		}
		for k, v := range want {
			if got.body[k] != v {
				t.Errorf("%s: got %v, want %v", k, got.body[k], v)
			}
		}
		if r := env.do(t, http.MethodGet, "/api/v1/admin/settings", fx.staffAccess, nil); r.status != http.StatusForbidden {
			t.Errorf("staff GET: expected 403, got %d", r.status)
		}
		if r := env.do(t, http.MethodPatch, "/api/v1/admin/settings", fx.staffAccess, map[string]any{"sms_confirmation": true}); r.status != http.StatusForbidden {
			t.Errorf("staff PATCH: expected 403, got %d", r.status)
		}
		if r := env.do(t, http.MethodGet, "/api/v1/admin/settings", "", nil); r.status != http.StatusUnauthorized {
			t.Errorf("anonymous: expected 401, got %d", r.status)
		}
	})

	t.Run("PATCH validates ranges and rejects an empty body", func(t *testing.T) {
		for body, code := range map[string]string{
			`booking_horizon_days`:     "invalid_booking_horizon_days",
			`no_show_auto_cancel_min`:  "invalid_no_show_auto_cancel_min",
			`client_cancel_cutoff_min`: "invalid_client_cancel_cutoff_min",
		} {
			bad := 9999
			if body == "booking_horizon_days" {
				bad = 0
			}
			r := env.do(t, http.MethodPatch, "/api/v1/admin/settings", adminAccess, map[string]any{body: bad})
			if r.status != http.StatusBadRequest || errCode(r) != code {
				t.Errorf("%s=%d: expected 400 %s, got %d (%v)", body, bad, code, r.status, r.body)
			}
		}
		if r := env.do(t, http.MethodPatch, "/api/v1/admin/settings", adminAccess, map[string]any{}); r.status != http.StatusBadRequest || errCode(r) != "empty_update" {
			t.Errorf("empty: expected 400 empty_update, got %d (%v)", r.status, r.body)
		}
		got := set(map[string]any{"booking_horizon_days": 14})
		if got.body["booking_horizon_days"] != float64(14) || got.body["no_show_auto_cancel_min"] != float64(0) {
			t.Errorf("partial update must only change what was sent: %v", got.body)
		}
		set(map[string]any{"booking_horizon_days": 7})
	})

	customerPhone := uniquePhone(802)
	customer := env.loginAs(t, customerPhone, "")
	carID := env.createCar(t, customer, "Rule Car")
	book := func(start string) apiResponse {
		return env.do(t, http.MethodPost, "/api/v1/queue", customer, map[string]any{
			"car_id": carID, "service_id": fx.serviceID, "box_number": 1,
			"price_option_id": fx.priceOptionID, "scheduled_start_at": start,
		})
	}

	t.Run("booking horizon rejects customer bookings beyond N days", func(t *testing.T) {
		set(map[string]any{"booking_horizon_days": 1})
		if r := book(futureBookingTime(4)); r.status != http.StatusBadRequest || errCode(r) != "too_far_ahead" {
			t.Fatalf("4 days ahead with horizon 1: expected 400 too_far_ahead, got %d (%v)", r.status, r.body)
		}
		set(map[string]any{"booking_horizon_days": 7})
	})

	var bookingID string
	t.Run("a booking inside the horizon is accepted", func(t *testing.T) {
		r := book(futureBookingTime(2))
		if r.status != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%v)", r.status, r.body)
		}
		bookingID = r.str("id")
	})

	t.Run("cancel cutoff blocks the customer but not staff", func(t *testing.T) {
		// move the booking to start in 10 minutes
		start := time.Now().Add(10 * time.Minute)
		if err := env.db.Exec("UPDATE queue SET scheduled_start_at = ?, scheduled_end_at = ? WHERE id = ?", start, start.Add(30*time.Minute), bookingID).Error; err != nil {
			t.Fatalf("move booking: %v", err)
		}
		set(map[string]any{"client_cancel_cutoff_min": 30})
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+bookingID+"/cancel", customer, nil); r.status != http.StatusConflict || errCode(r) != "cancel_too_late" {
			t.Fatalf("customer inside the cutoff: expected 409 cancel_too_late, got %d (%v)", r.status, r.body)
		}
		set(map[string]any{"client_cancel_cutoff_min": 5})
		// 10 minutes ahead is outside a 5-minute cutoff
		if r := env.do(t, http.MethodPatch, "/api/v1/queue/"+bookingID+"/cancel", customer, nil); r.status != http.StatusOK {
			t.Fatalf("customer outside the cutoff: expected 200, got %d (%v)", r.status, r.body)
		}
		set(map[string]any{"client_cancel_cutoff_min": 0})
	})

	t.Run("staff cancel ignores the cutoff", func(t *testing.T) {
		r := book(futureBookingTime(2))
		if r.status != http.StatusCreated {
			t.Fatalf("book: %d (%v)", r.status, r.body)
		}
		id := r.str("id")
		start := time.Now().Add(5 * time.Minute)
		env.db.Exec("UPDATE queue SET scheduled_start_at = ?, scheduled_end_at = ? WHERE id = ?", start, start.Add(30*time.Minute), id)
		set(map[string]any{"client_cancel_cutoff_min": 60})
		if c := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/cancel", fx.staffAccess, nil); c.status != http.StatusOK {
			t.Fatalf("staff cancel: expected 200, got %d (%v)", c.status, c.body)
		}
		set(map[string]any{"client_cancel_cutoff_min": 0})
	})

	statusOf := func(id string) string {
		var s string
		if err := env.db.Raw("SELECT status FROM queue WHERE id = ?", id).Scan(&s).Error; err != nil {
			t.Fatalf("read status: %v", err)
		}
		return s
	}

	t.Run("no-show sweep marks only overdue customer bookings, and only when on", func(t *testing.T) {
		r := book(futureBookingTime(2))
		if r.status != http.StatusCreated {
			t.Fatalf("book: %d (%v)", r.status, r.body)
		}
		id := r.str("id")
		late := time.Now().Add(-20 * time.Minute)
		env.db.Exec("UPDATE queue SET scheduled_start_at = ?, scheduled_end_at = ? WHERE id = ?", late, late.Add(30*time.Minute), id)

		// off by default: nothing happens
		if err := env.jobs.NoShow.Tick(context.Background(), time.Now()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if got := statusOf(id); got != "queue" {
			t.Fatalf("sweep off: expected queue, got %s", got)
		}

		// 30-minute grace: 20 minutes late is still fine
		set(map[string]any{"no_show_auto_cancel_min": 30})
		env.jobs.NoShow.Tick(context.Background(), time.Now())
		if got := statusOf(id); got != "queue" {
			t.Fatalf("inside the grace period: expected queue, got %s", got)
		}

		set(map[string]any{"no_show_auto_cancel_min": 15})
		env.jobs.NoShow.Tick(context.Background(), time.Now())
		if got := statusOf(id); got != "no_show" {
			t.Fatalf("past the grace period: expected no_show, got %s", got)
		}
		set(map[string]any{"no_show_auto_cancel_min": 0})
	})

	t.Run("an arrived (waiting) customer is never swept", func(t *testing.T) {
		r := book(futureBookingTime(2))
		if r.status != http.StatusCreated {
			t.Fatalf("book: %d (%v)", r.status, r.body)
		}
		id := r.str("id")
		late := time.Now().Add(-45 * time.Minute)
		env.db.Exec("UPDATE queue SET scheduled_start_at = ?, scheduled_end_at = ? WHERE id = ?", late, late.Add(30*time.Minute), id)
		if s := env.do(t, http.MethodPatch, "/api/v1/queue/"+id+"/status", fx.staffAccess, map[string]string{"status": "waiting"}); s.status != http.StatusOK {
			t.Fatalf("mark arrived: %d (%v)", s.status, s.body)
		}
		set(map[string]any{"no_show_auto_cancel_min": 15})
		env.jobs.NoShow.Tick(context.Background(), time.Now())
		if got := statusOf(id); got != "waiting" {
			t.Fatalf("expected waiting, got %s", got)
		}
		set(map[string]any{"no_show_auto_cancel_min": 0})
	})

	t.Run("SMS ticket is sent once, only when the setting is on", func(t *testing.T) {
		// free the customer's active booking first
		env.db.Exec("UPDATE queue SET status = 'canceled' WHERE user_id IN (SELECT id FROM users WHERE phone_number = ?) AND status IN ('queue','waiting','pending')", customerPhone)
		before := env.sms.countTo(customerPhone)
		r := book(futureBookingTime(3))
		if r.status != http.StatusCreated {
			t.Fatalf("book: %d (%v)", r.status, r.body)
		}
		time.Sleep(300 * time.Millisecond)
		if got := env.sms.countTo(customerPhone); got != before {
			t.Fatalf("setting off: expected no SMS, got %d new", got-before)
		}
		env.do(t, http.MethodPatch, "/api/v1/queue/"+r.str("id")+"/cancel", customer, nil)

		set(map[string]any{"sms_confirmation": true})
		r = book(futureBookingTime(3))
		if r.status != http.StatusCreated {
			t.Fatalf("book: %d (%v)", r.status, r.body)
		}
		deadline := time.Now().Add(3 * time.Second)
		for env.sms.countTo(customerPhone) == before && time.Now().Before(deadline) {
			time.Sleep(30 * time.Millisecond)
		}
		if got := env.sms.countTo(customerPhone); got != before+1 {
			t.Fatalf("setting on: expected 1 SMS, got %d", got-before)
		}
		env.sms.mu.Lock()
		last := env.sms.messages[len(env.sms.messages)-1]
		env.sms.mu.Unlock()
		if last.To != customerPhone || !strings.Contains(last.Text, "запись подтверждена") {
			t.Errorf("unexpected ticket SMS: %+v", last)
		}
		set(map[string]any{"sms_confirmation": false})
	})

	t.Run("new point moderation off activates an approved application at once", func(t *testing.T) {
		approve := func(name string) string {
			apply := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", map[string]any{
				"business_name": name, "contact_name": "Owner", "contact_phone": "+992 900 11 22 33",
				"address": "Rudaki 1", "boxes_count": 2,
			})
			if apply.status != http.StatusAccepted {
				t.Fatalf("apply: %d (%v)", apply.status, apply.body)
			}
			list := env.do(t, http.MethodGet, "/api/v1/connection-requests?status=new", adminAccess, nil)
			var id string
			for _, it := range list.body["items"].([]any) {
				if m := it.(map[string]any); m["business_name"] == name {
					id = m["id"].(string)
				}
			}
			if r := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+id, adminAccess, map[string]any{"status": "approved"}); r.status != http.StatusOK {
				t.Fatalf("approve: %d (%v)", r.status, r.body)
			}
			var status string
			env.db.Raw("SELECT status FROM washing_points WHERE name = ?", name).Scan(&status)
			return status
		}
		if got := approve("Moderated Wash"); got != "pending_review" {
			t.Fatalf("moderation on: expected pending_review, got %q", got)
		}
		set(map[string]any{"new_point_moderation": false})
		if got := approve("Unmoderated Wash"); got != "active" {
			t.Fatalf("moderation off: expected active, got %q", got)
		}
		set(map[string]any{"new_point_moderation": true})
	})
}
