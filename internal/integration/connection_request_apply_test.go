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

	t.Run("only a phone and one name are required, details can be added later", func(t *testing.T) {
		// contact_name alone is enough; it becomes the request's business_name.
		r := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", map[string]any{
			"contact_name": " Farrukh ", "contact_phone": "+992 900 55 66 77",
		})
		if r.status != http.StatusAccepted {
			t.Fatalf("expected 202, got %d (%v)", r.status, r.body)
		}
		var minimal map[string]any
		for _, it := range listNew() {
			m := it.(map[string]any)
			if m["contact_phone"] == "+992 900 55 66 77" {
				minimal = m
			}
		}
		if minimal == nil || minimal["business_name"] != "Farrukh" || minimal["contact_name"] != "Farrukh" {
			t.Fatalf("expected the name to fill business_name, got %v", minimal)
		}
		for _, k := range []string{"address", "boxes_count"} {
			if _, present := minimal[k]; present {
				t.Errorf("%s must be omitted while unset, got %v", k, minimal[k])
			}
		}
		id := minimal["id"].(string)

		t.Run("cannot be approved until address and boxes are filled in", func(t *testing.T) {
			if r := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+id, adminAccess, map[string]any{"status": "approved"}); r.status != http.StatusConflict || errCode(r) != "connection_request_incomplete" {
				t.Fatalf("expected 409 connection_request_incomplete, got %d (%v)", r.status, r.body)
			}
		})

		t.Run("admin completes the details with PATCH, then approves", func(t *testing.T) {
			r := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+id, adminAccess, map[string]any{
				"business_name": "Farrukh Auto Wash", "address": " Rudaki 10 ", "boxes_count": 4,
			})
			if r.status != http.StatusOK || r.str("business_name") != "Farrukh Auto Wash" || r.str("address") != "Rudaki 10" || r.body["boxes_count"] != float64(4) {
				t.Fatalf("expected the edited request back, got %d (%v)", r.status, r.body)
			}
			approved := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+id, adminAccess, map[string]any{"status": "approved"})
			if approved.status != http.StatusOK || approved.str("status") != "approved" {
				t.Fatalf("expected approval, got %d (%v)", approved.status, approved.body)
			}
			if r := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+id, adminAccess, map[string]any{"address": "x"}); r.status != http.StatusConflict {
				t.Errorf("editing a reviewed request: expected 409, got %d", r.status)
			}
		})

		t.Run("empty PATCH and bad values are rejected", func(t *testing.T) {
			other := env.do(t, http.MethodPost, "/api/v1/connection-requests", adminAccess, map[string]any{
				"business_name": "Other", "contact_name": "O", "contact_phone": "+992 900 00 00 01", "address": "A", "boxes_count": 1,
			})
			oid := other.str("id")
			for _, body := range []map[string]any{{}, {"boxes_count": 0}, {"business_name": " "}, {"status": "nope"}} {
				if r := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+oid, adminAccess, body); r.status != http.StatusBadRequest {
					t.Errorf("body %v: expected 400, got %d (%v)", body, r.status, r.body)
				}
			}
		})
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"no name":        {"contact_phone": "+992 900 11 22 44"},
			"blank names":    {"business_name": " ", "contact_name": " ", "contact_phone": "+992 900 11 22 44"},
			"bad phone":      {"business_name": "X", "contact_phone": "12ab"},
			"missing phone":  {"business_name": "X"},
			"negative boxes": {"business_name": "X", "contact_phone": "+992 900 11 22 44", "boxes_count": -1},
			"too many boxes": {"business_name": "X", "contact_phone": "+992 900 11 22 44", "boxes_count": 101},
		} {
			if r := env.do(t, http.MethodPost, "/api/v1/connection-requests/apply", "", body); r.status != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d (%v)", name, r.status, r.body)
			}
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
