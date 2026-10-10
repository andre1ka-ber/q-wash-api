//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

func TestConnectionRequests_PublicApply(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(340), user.RoleAdmin)

	valid := func() map[string]any {
		return map[string]any{
			"business_name": " Web Wash ", "contact_name": "Sergey", "contact_phone": "+992 900 11 22 33",
			"address": "Lenina 24", "boxes_count": 3,
		}
	}
	listNew := func() []any {
		r := env.do(t, http.MethodGet, "/api/v1/connection-requests?status=new", adminAccess, nil)
		if r.status != http.StatusOK {
			t.Fatalf("list: %d (%v)", r.status, r.body)
		}
		return r.body["items"].([]any)
	}

	t.Run("anonymous apply stores a new request without echoing it", func(t *testing.T) {
		r := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", valid())
		if r.status != http.StatusAccepted || r.str("status") != "received" {
			t.Fatalf("expected 202 received, got %d (%v)", r.status, r.body)
		}
		if _, leaked := r.body["id"]; leaked {
			t.Error("response must not expose the stored id")
		}
		items := listNew()
		if len(items) != 1 || items[0].(map[string]any)["business_name"] != "Web Wash" {
			t.Fatalf("expected one trimmed request in the admin queue, got %v", items)
		}
	})

	t.Run("same phone again is accepted but not duplicated", func(t *testing.T) {
		if r := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", valid()); r.status != http.StatusAccepted {
			t.Fatalf("expected 202, got %d (%v)", r.status, r.body)
		}
		if n := len(listNew()); n != 1 {
			t.Errorf("expected still 1 request, got %d", n)
		}
	})

	t.Run("honeypot is accepted but stores nothing", func(t *testing.T) {
		body := valid()
		body["contact_phone"] = "+992 900 99 88 77"
		body["website"] = "http://spam.example"
		if r := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", body); r.status != http.StatusAccepted {
			t.Fatalf("expected 202, got %d", r.status)
		}
		if n := len(listNew()); n != 1 {
			t.Errorf("honeypot submission must not be stored, got %d requests", n)
		}
	})

	t.Run("validation", func(t *testing.T) {
		for field, val := range map[string]any{
			"business_name": "", "contact_name": "  ", "contact_phone": "12ab", "address": "",
			"boxes_count": 0,
		} {
			body := valid()
			body[field] = val
			if r := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", body); r.status != http.StatusBadRequest {
				t.Errorf("%s=%v: expected 400, got %d (%v)", field, val, r.status, r.body)
			}
		}
		body := valid()
		body["boxes_count"] = 101
		if r := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", body); r.status != http.StatusBadRequest || errCode(r) != "invalid_boxes_count" {
			t.Errorf("boxes_count 101: expected 400 invalid_boxes_count, got %d (%v)", r.status, r.body)
		}
	})

	t.Run("admin routes stay closed to anonymous callers", func(t *testing.T) {
		if r := env.do(t, http.MethodGet, "/api/v1/connection-requests", "", nil); r.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", r.status)
		}
		if r := env.do(t, http.MethodPost, "/api/v1/connection-requests", "", valid()); r.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", r.status)
		}
	})
}

func TestConnectionRequests_PublicApplyRateLimit(t *testing.T) {
	env := newTestEnv(t)
	var last apiResponse
	for i := 0; i < 11; i++ {
		last = env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", map[string]any{
			"business_name": "Spam Wash", "contact_name": "Bot", "contact_phone": "+992 911 000 00 0" + string(rune('0'+i%10)),
			"address": "Somewhere", "boxes_count": 1,
		})
		if i < 10 && last.status != http.StatusAccepted {
			t.Fatalf("request %d: expected 202, got %d (%v)", i+1, last.status, last.body)
		}
	}
	if last.status != http.StatusTooManyRequests || errCode(last) != "too_many_requests" {
		t.Errorf("11th request: expected 429 too_many_requests, got %d (%v)", last.status, last.body)
	}
}
