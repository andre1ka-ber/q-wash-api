//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/user"
)

// Staff may only send a free-text notification about a booking at their own
// washing point; without queue_id nothing ties the recipient to the caller.
func TestNotifications_StaffMustReferenceABookingAtTheirPoint(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(110)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(111), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(112), "")
	fixture := env.setUpBookingFixture(t, adminAccess, staffPhone)

	me := env.do(t, http.MethodGet, "/api/v1/me", customerAccess, nil)
	customerID := me.str("id")
	carID := env.createCar(t, customerAccess, "Notified Car")
	booking := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
		"car_id": carID, "service_id": fixture.serviceID, "box_number": 1,
		"price_option_id": fixture.priceOptionID, "scheduled_start_at": futureBookingTime(2),
	})
	if booking.status != http.StatusCreated {
		t.Fatalf("booking: %d %v", booking.status, booking.body)
	}

	t.Run("staff without queue_id is refused", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/notifications", fixture.staffAccess, map[string]any{"user_id": customerID, "text": "hello"})
		if resp.status != http.StatusBadRequest || resp.body["error"].(map[string]any)["code"] != "queue_id_required" {
			t.Fatalf("want 400 queue_id_required, got %d %v", resp.status, resp.body)
		}
	})

	t.Run("staff with a booking at their point may notify its customer", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/notifications", fixture.staffAccess, map[string]any{"user_id": customerID, "queue_id": booking.str("id"), "text": "hello"})
		if resp.status != http.StatusCreated {
			t.Fatalf("want 201, got %d %v", resp.status, resp.body)
		}
	})

	t.Run("admin may omit queue_id", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/notifications", adminAccess, map[string]any{"user_id": customerID, "text": "hello"})
		if resp.status != http.StatusCreated {
			t.Fatalf("want 201, got %d %v", resp.status, resp.body)
		}
	})
}

// A status write only applies if the booking is still in the status the caller
// validated the transition against.
func TestQueue_StatusWriteIsCompareAndSet(t *testing.T) {
	env := newTestEnv(t)
	staffPhone := uniquePhone(113)
	env.loginAs(t, staffPhone, user.RoleStaff)
	adminAccess := env.loginAs(t, uniquePhone(114), user.RoleAdmin)
	customerAccess := env.loginAs(t, uniquePhone(115), "")
	fixture := env.setUpBookingFixture(t, adminAccess, staffPhone)
	carID := env.createCar(t, customerAccess, "CAS Car")

	booking := env.do(t, http.MethodPost, "/api/v1/queue", customerAccess, map[string]any{
		"car_id": carID, "service_id": fixture.serviceID, "box_number": 1,
		"price_option_id": fixture.priceOptionID, "scheduled_start_at": futureBookingTime(2),
	})
	if booking.status != http.StatusCreated {
		t.Fatalf("booking: %d %v", booking.status, booking.body)
	}
	id := uuid.MustParse(booking.str("id"))
	repo := queue.NewRepository(env.db)
	ctx := context.Background()

	t.Run("repository refuses a write based on a stale status", func(t *testing.T) {
		q, err := repo.FindByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		// The booking is in "queue"; pretend the caller had read "waiting".
		q.Status = queue.StatusWashing
		err = repo.UpdateStatus(ctx, q, queue.StatusWaiting)
		var appErr *apperror.Error
		if !errors.As(err, &appErr) || appErr.Code != "status_changed" {
			t.Fatalf("expected a status_changed conflict, got %v", err)
		}
		after, _ := repo.FindByID(ctx, id)
		if after.Status != queue.StatusQueue {
			t.Fatalf("status must be untouched, got %q", after.Status)
		}
	})

	t.Run("two concurrent identical transitions: one wins, the other gets 409", func(t *testing.T) {
		statusPath := "/api/v1/queue/" + id.String() + "/status"
		const attempts = 6
		var wg sync.WaitGroup
		statuses := make([]int, attempts)
		for i := range statuses {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses[i] = env.do(t, http.MethodPatch, statusPath, fixture.staffAccess, map[string]string{"status": "waiting"}).status
			}()
		}
		wg.Wait()

		ok, conflict := 0, 0
		for _, s := range statuses {
			switch s {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != attempts-1 {
			t.Fatalf("want exactly one 200 and %d 409s, got %v", attempts-1, statuses)
		}
	})

	t.Run("a canceled booking is not resurrected by a stale start", func(t *testing.T) {
		q, _ := repo.FindByID(ctx, id) // now "waiting"
		cancel := env.do(t, http.MethodPatch, "/api/v1/queue/"+id.String()+"/cancel", customerAccess, nil)
		if cancel.status != http.StatusOK {
			t.Fatalf("cancel: %d %v", cancel.status, cancel.body)
		}
		// A worker who loaded the booking as "waiting" now starts it.
		q.Status = queue.StatusWashing
		if err := repo.UpdateStatus(ctx, q, queue.StatusWaiting); err == nil {
			t.Fatal("starting a canceled booking from a stale read must fail")
		}
		after, _ := repo.FindByID(ctx, id)
		if after.Status != queue.StatusCanceled {
			t.Fatalf("booking must stay canceled, got %q", after.Status)
		}
	})
}
