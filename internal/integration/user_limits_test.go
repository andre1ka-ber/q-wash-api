//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"q-wash-api/internal/config"
	"q-wash-api/internal/user"
)

func TestRateLimit_CurrentPasswordGuessing(t *testing.T) {
	env := newTestEnv(t, withRateLimit(config.RateLimitConfig{PasswordMaxFailures: 3, PasswordLockout: 15 * time.Minute}))
	admin := env.loginAs(t, uniquePhone(980), user.RoleAdmin)
	created := env.do(t, http.MethodPost, "/api/v1/washing-points", admin, map[string]any{
		"name": "Guess Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0,
	})
	account := created.body["credentials"].(map[string]any)["staff"].(map[string]any)
	password := account["password"].(string)
	login := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": account["username"], "password": password})
	staff := login.str("access_token")
	reset := "/api/v1/washing-points/" + created.str("id") + "/credentials/staff/reset"

	expect := func(method, path string, body any, status int) {
		t.Helper()
		if resp := env.do(t, method, path, staff, body); resp.status != status {
			t.Fatalf("%s %s: expected %d, got %d (%v)", method, path, status, resp.status, resp.body)
		}
	}
	wrongChange := map[string]string{"current_password": "nope", "new_password": "Another-pass-1"}
	wrongReset := map[string]string{"current_password": "nope"}

	// The two endpoints share one budget: both test the same password.
	expect(http.MethodPatch, "/api/v1/auth/password", wrongChange, http.StatusUnauthorized)
	expect(http.MethodPost, reset, wrongReset, http.StatusForbidden)
	expect(http.MethodPatch, "/api/v1/auth/password", wrongChange, http.StatusUnauthorized)
	expect(http.MethodPost, reset, map[string]string{"current_password": password}, http.StatusTooManyRequests)
	expect(http.MethodPatch, "/api/v1/auth/password", map[string]string{"current_password": password, "new_password": "Another-pass-1"}, http.StatusTooManyRequests)
}

func TestRateLimit_AvailabilityPerIP(t *testing.T) {
	env := newTestEnv(t, withRateLimit(config.RateLimitConfig{AvailabilityPerMinute: 2}))
	path := "/api/v1/washing-points/0190b3a0-0000-7000-8000-000000000000/availability"
	for i := 0; i < 2; i++ {
		if status, _ := env.rawRequest(t, http.MethodGet, path, nil, nil); status == http.StatusTooManyRequests {
			t.Fatalf("request #%d must be within the limit", i+1)
		}
	}
	if status, code := env.rawRequest(t, http.MethodGet, path, nil, nil); status != http.StatusTooManyRequests || code != "too_many_requests" {
		t.Fatalf("over the limit: got %d %s", status, code)
	}
}

func TestRateLimit_BookingsAndStreamsPerUser(t *testing.T) {
	env := newTestEnv(t, withRateLimit(config.RateLimitConfig{BookingsPerHour: 2, StreamsPerUser: 2}))
	staffPhone := uniquePhone(981)
	env.loginAs(t, staffPhone, user.RoleStaff)
	admin := env.loginAs(t, uniquePhone(982), user.RoleAdmin)
	customer := env.loginAs(t, uniquePhone(983), "")
	other := env.loginAs(t, uniquePhone(984), "")
	fx := env.setUpBookingFixture(t, admin, staffPhone)

	book := func(token string, day int) apiResponse {
		car := env.do(t, http.MethodPost, "/api/v1/me/cars", token, map[string]string{"name": "Car"})
		return env.do(t, http.MethodPost, "/api/v1/queue", token, map[string]any{
			"car_id": car.str("id"), "service_id": fx.serviceID, "price_option_id": fx.priceOptionID,
			"box_number": 1, "scheduled_start_at": futureBookingTime(day),
		})
	}

	var lastID string
	t.Run("bookings per hour, canceled ones included", func(t *testing.T) {
		for day := 1; day <= 2; day++ {
			resp := book(customer, day)
			if resp.status != http.StatusCreated {
				t.Fatalf("booking #%d: got %d (%v)", day, resp.status, resp.body)
			}
			lastID = resp.str("id")
			if c := env.do(t, http.MethodPatch, "/api/v1/queue/"+lastID+"/cancel", customer, nil); c.status != http.StatusOK {
				t.Fatalf("cancel is never limited: got %d", c.status)
			}
		}
		resp := book(customer, 3)
		errBody, _ := resp.body["error"].(map[string]any)
		if resp.status != http.StatusTooManyRequests || errBody["code"] != "too_many_requests" {
			t.Fatalf("third booking: got %d (%v)", resp.status, resp.body)
		}
		if resp := book(other, 4); resp.status != http.StatusCreated {
			t.Fatalf("another customer must be unaffected: got %d (%v)", resp.status, resp.body)
		} else {
			lastID = resp.str("id")
		}
	})

	t.Run("open event streams per user", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		open := func() int {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.baseURL+"/api/v1/queue/"+lastID+"/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+other)
			resp, err := env.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			return resp.StatusCode
		}
		for i := 0; i < 2; i++ {
			if status := open(); status != http.StatusOK {
				t.Fatalf("stream #%d: got %d", i+1, status)
			}
		}
		if status := open(); status != http.StatusTooManyRequests {
			t.Fatalf("third stream: expected 429, got %d", status)
		}
		// Cleanups close the bodies; cancelling ends the streams so the server can stop.
		cancel()
	})
}
