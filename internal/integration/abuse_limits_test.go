//go:build integration

package integration

import (
	"net/http"
	"sync"
	"testing"

	"q-wash-api/internal/config"
	"q-wash-api/internal/user"
)

func TestLimits_NumbersThatDriveWorkAreCapped(t *testing.T) {
	env := newTestEnv(t)
	admin := env.loginAs(t, uniquePhone(900), user.RoleAdmin)

	point := func(boxes int) map[string]any {
		return map[string]any{"name": "Cap Point", "address": "1 Test St", "latitude": 1.0, "longitude": 2.0, "boxes_count": boxes}
	}
	expect := func(t *testing.T, resp apiResponse, status int) {
		t.Helper()
		if resp.status != status {
			t.Fatalf("expected %d, got %d (%v)", status, resp.status, resp.body)
		}
	}

	expect(t, env.do(t, http.MethodPost, "/api/v1/washing-points", admin, point(101)), http.StatusBadRequest)
	created := env.do(t, http.MethodPost, "/api/v1/washing-points", admin, point(100))
	expect(t, created, http.StatusCreated)
	wpID := created.str("id")

	t.Run("washing point update", func(t *testing.T) {
		expect(t, env.do(t, http.MethodPatch, "/api/v1/washing-points/"+wpID, admin, map[string]any{"boxes_count": 1_000_000_000}), http.StatusBadRequest)
	})

	t.Run("connection request create and edit", func(t *testing.T) {
		body := map[string]any{"business_name": "B", "contact_name": "C", "contact_phone": "+992900000001", "address": "A", "boxes_count": 101}
		expect(t, env.do(t, http.MethodPost, "/api/v1/connection-requests", admin, body), http.StatusBadRequest)
		body["boxes_count"] = 2
		ok := env.do(t, http.MethodPost, "/api/v1/connection-requests", admin, body)
		expect(t, ok, http.StatusCreated)
		expect(t, env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+ok.str("id"), admin, map[string]any{"boxes_count": 101}), http.StatusBadRequest)
	})

	t.Run("service minutes and price", func(t *testing.T) {
		service := func(duration, queue, price int) map[string]any {
			return map[string]any{
				"name": "Wash", "duration_minutes": duration, "queue_minutes": queue,
				"price_options": []map[string]any{{"name": "Standard", "price_cents": price}},
			}
		}
		path := "/api/v1/washing-points/" + wpID + "/services"
		expect(t, env.do(t, http.MethodPost, path, admin, service(1441, 30, 100)), http.StatusBadRequest)
		expect(t, env.do(t, http.MethodPost, path, admin, service(30, 1441, 100)), http.StatusBadRequest)
		expect(t, env.do(t, http.MethodPost, path, admin, service(30, 30, 100_000_001)), http.StatusBadRequest)
		ok := env.do(t, http.MethodPost, path, admin, service(1440, 1440, 100_000_000))
		expect(t, ok, http.StatusCreated)
		id := ok.str("id")
		expect(t, env.do(t, http.MethodPatch, "/api/v1/services/"+id, admin, map[string]any{"duration_minutes": 1441}), http.StatusBadRequest)
		expect(t, env.do(t, http.MethodPatch, "/api/v1/services/"+id, admin, map[string]any{"queue_minutes": 1441}), http.StatusBadRequest)
		expect(t, env.do(t, http.MethodPost, "/api/v1/services/"+id+"/price-options", admin, map[string]any{"name": "Big", "price_cents": 100_000_001}), http.StatusBadRequest)
		optionID := ok.body["price_options"].([]any)[0].(map[string]any)["id"].(string)
		expect(t, env.do(t, http.MethodPatch, "/api/v1/price-options/"+optionID, admin, map[string]any{"price_cents": 100_000_001}), http.StatusBadRequest)
	})
}

// parallel fires n identical requests at once and counts the answers by
// "status code".
func (e *testEnv) parallel(t *testing.T, n int, path string, body map[string]any) map[int]map[string]int {
	t.Helper()
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	got := map[int]map[string]int{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, code := e.rawRequest(t, http.MethodPost, path, nil, body)
			mu.Lock()
			defer mu.Unlock()
			if got[status] == nil {
				got[status] = map[string]int{}
			}
			got[status][code]++
		}()
	}
	close(start)
	wg.Wait()
	return got
}

func TestOTP_ParallelRequestsCannotExceedLimits(t *testing.T) {
	env := newTestEnv(t)
	const request, verify = "/api/v1/auth/otp/request", "/api/v1/auth/otp/verify"

	t.Run("one code is issued under the cooldown", func(t *testing.T) {
		phone := uniquePhone(910)
		got := env.parallel(t, 10, request, map[string]any{"phone_number": phone})
		if got[http.StatusAccepted][""] != 1 || got[http.StatusTooManyRequests]["otp_cooldown"] != 9 {
			t.Fatalf("expected 1 accepted and 9 otp_cooldown, got %v", got)
		}
	})

	t.Run("wrong guesses stop at the attempt limit", func(t *testing.T) {
		phone := uniquePhone(911)
		env.do(t, http.MethodPost, request, "", map[string]string{"phone_number": phone})
		wrong := "000000"
		if env.sms.lastCodeFor(t, phone) == wrong {
			wrong = "111111"
		}
		got := env.parallel(t, 30, verify, map[string]any{"phone_number": phone, "code": wrong})
		if got[http.StatusBadRequest]["otp_invalid"] != 5 || got[http.StatusTooManyRequests]["otp_locked"] != 25 {
			t.Fatalf("expected 5 compared guesses and 25 locked, got %v", got)
		}
		resp := env.do(t, http.MethodPost, verify, "", map[string]string{"phone_number": phone, "code": env.sms.lastCodeFor(t, phone)})
		if resp.status != http.StatusTooManyRequests {
			t.Fatalf("a locked code must refuse the right guess, got %d", resp.status)
		}
	})

	t.Run("a correct code works exactly once", func(t *testing.T) {
		phone := uniquePhone(912)
		env.do(t, http.MethodPost, request, "", map[string]string{"phone_number": phone})
		got := env.parallel(t, 5, verify, map[string]any{"phone_number": phone, "code": env.sms.lastCodeFor(t, phone)})
		if got[http.StatusOK][""] != 1 {
			t.Fatalf("expected exactly one login, got %v", got)
		}
	})
}

func TestOTP_PerPhoneAndGlobalCaps(t *testing.T) {
	const request = "/api/v1/auth/otp/request"
	send := func(t *testing.T, env *testEnv, phone string) (int, string) {
		t.Helper()
		return env.rawRequest(t, http.MethodPost, request, nil, map[string]any{"phone_number": phone})
	}

	for name, configure := range map[string]func(*config.Config){
		"hourly": func(c *config.Config) { c.Auth.OTPCooldown = 0; c.Auth.OTPMaxPerPhoneHour = 3 },
		"daily":  func(c *config.Config) { c.Auth.OTPCooldown = 0; c.Auth.OTPMaxPerPhoneDay = 3 },
	} {
		t.Run(name+" cap per phone", func(t *testing.T) {
			env := newTestEnv(t, configure)
			phone := uniquePhone(920)
			for i := 0; i < 3; i++ {
				if status, code := send(t, env, phone); status != http.StatusAccepted {
					t.Fatalf("request #%d: got %d %s", i+1, status, code)
				}
			}
			if status, code := send(t, env, phone); status != http.StatusTooManyRequests || code != "otp_cooldown" {
				t.Fatalf("over the cap: got %d %s", status, code)
			}
			if status, _ := send(t, env, uniquePhone(921)); status != http.StatusAccepted {
				t.Fatalf("another phone must be unaffected, got %d", status)
			}
		})
	}

	t.Run("global budget across phones", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) { c.Auth.OTPMaxPerDay = 2 })
		for i := int64(0); i < 2; i++ {
			if status, _ := send(t, env, uniquePhone(930+i)); status != http.StatusAccepted {
				t.Fatalf("within budget: got %d", status)
			}
		}
		if status, code := send(t, env, uniquePhone(939)); status != http.StatusTooManyRequests || code != "otp_cooldown" {
			t.Fatalf("over budget: got %d %s", status, code)
		}
	})
}
