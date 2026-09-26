//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"q-wash-api/internal/user"
)

func TestOwners_AdminOnlyCRUD(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(330), user.RoleAdmin)
	staffAccess := env.loginAs(t, uniquePhone(331), user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(332), "")

	t.Run("every method is closed to non-admins", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/owners"},
			{http.MethodPost, "/api/v1/owners"},
			{http.MethodGet, "/api/v1/owners/00000000-0000-0000-0000-000000000000"},
			{http.MethodPatch, "/api/v1/owners/00000000-0000-0000-0000-000000000000"},
		} {
			if r := env.do(t, tc.method, tc.path, "", map[string]any{"name": "x"}); r.status != http.StatusUnauthorized {
				t.Errorf("%s %s anonymous: expected 401, got %d", tc.method, tc.path, r.status)
			}
			for who, token := range map[string]string{"staff": staffAccess, "customer": customerAccess} {
				if r := env.do(t, tc.method, tc.path, token, map[string]any{"name": "x"}); r.status != http.StatusForbidden {
					t.Errorf("%s %s %s: expected 403, got %d", tc.method, tc.path, who, r.status)
				}
			}
		}
	})

	var id string
	t.Run("create trims fields and stores blank optionals as absent", func(t *testing.T) {
		r := env.do(t, http.MethodPost, "/api/v1/owners", adminAccess, map[string]any{
			"name": "  Pegas Auto LLC ", "contact_name": " Farrukh ", "contact_phone": "   ",
		})
		if r.status != http.StatusCreated || r.str("name") != "Pegas Auto LLC" || r.str("contact_name") != "Farrukh" {
			t.Fatalf("unexpected: %d (%v)", r.status, r.body)
		}
		if _, present := r.body["contact_phone"]; present {
			t.Errorf("a blank contact_phone must be omitted, got %v", r.body["contact_phone"])
		}
		id = r.str("id")
	})

	t.Run("name is required and length-limited", func(t *testing.T) {
		for _, name := range []string{"", "   ", strings.Repeat("a", 256)} {
			if r := env.do(t, http.MethodPost, "/api/v1/owners", adminAccess, map[string]any{"name": name}); r.status != http.StatusBadRequest || errCode(r) != "invalid_name" {
				t.Errorf("name %q: expected 400 invalid_name, got %d (%v)", name, r.status, r.body)
			}
		}
	})

	t.Run("get, list and partial update", func(t *testing.T) {
		if r := env.do(t, http.MethodGet, "/api/v1/owners/"+id, adminAccess, nil); r.status != http.StatusOK || r.str("name") != "Pegas Auto LLC" {
			t.Fatalf("get: %d (%v)", r.status, r.body)
		}
		if r := env.do(t, http.MethodGet, "/api/v1/owners/00000000-0000-0000-0000-000000000000", adminAccess, nil); r.status != http.StatusNotFound {
			t.Errorf("unknown owner: expected 404, got %d", r.status)
		}
		if r := env.do(t, http.MethodGet, "/api/v1/owners/not-a-uuid", adminAccess, nil); r.status != http.StatusBadRequest {
			t.Errorf("malformed id: expected 400, got %d", r.status)
		}

		upd := env.do(t, http.MethodPatch, "/api/v1/owners/"+id, adminAccess, map[string]any{"contact_email": "a@b.tj", "contact_name": ""})
		if upd.status != http.StatusOK || upd.str("contact_email") != "a@b.tj" || upd.str("name") != "Pegas Auto LLC" {
			t.Fatalf("update: %d (%v)", upd.status, upd.body)
		}
		if _, present := upd.body["contact_name"]; present {
			t.Errorf("blanking contact_name must clear it, got %v", upd.body["contact_name"])
		}
		if r := env.do(t, http.MethodPatch, "/api/v1/owners/"+id, adminAccess, map[string]any{"name": ""}); r.status != http.StatusBadRequest {
			t.Errorf("blank name on update: expected 400, got %d", r.status)
		}

		found := false
		for _, o := range items(t, env.do(t, http.MethodGet, "/api/v1/owners", adminAccess, nil)) {
			found = found || o["id"] == id
		}
		if !found {
			t.Error("created owner must appear in the list")
		}
	})
}

func TestConnectionRequests_ListFilterAndGet(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(335), user.RoleAdmin)

	body := func(name string) map[string]any {
		return map[string]any{
			"business_name": name, "contact_name": "Jane", "contact_phone": "+15559990001",
			"address": "1 Way", "boxes_count": 1,
		}
	}
	pending := env.do(t, http.MethodPost, "/api/v1/connection-requests", adminAccess, body("Pending Co"))
	rejected := env.do(t, http.MethodPost, "/api/v1/connection-requests", adminAccess, body("Rejected Co"))
	if r := env.do(t, http.MethodPatch, "/api/v1/connection-requests/"+rejected.str("id"), adminAccess, map[string]any{"status": "rejected"}); r.status != http.StatusOK {
		t.Fatalf("reject: %d (%v)", r.status, r.body)
	}

	ids := func(status string) map[string]bool {
		path := "/api/v1/connection-requests"
		if status != "" {
			path += "?status=" + status
		}
		out := map[string]bool{}
		for _, it := range items(t, env.do(t, http.MethodGet, path, adminAccess, nil)) {
			out[it["id"].(string)] = true
		}
		return out
	}

	if got := ids("new"); !got[pending.str("id")] || got[rejected.str("id")] {
		t.Errorf("status=new must hold only the pending one, got %v", got)
	}
	if got := ids("rejected"); got[pending.str("id")] || !got[rejected.str("id")] {
		t.Errorf("status=rejected must hold only the rejected one, got %v", got)
	}
	if got := ids(""); !got[pending.str("id")] || !got[rejected.str("id")] {
		t.Errorf("no filter must list both, got %v", got)
	}
	if r := env.do(t, http.MethodGet, "/api/v1/connection-requests?status=bogus", adminAccess, nil); r.status != http.StatusBadRequest || errCode(r) != "invalid_status" {
		t.Errorf("expected 400 invalid_status, got %d (%v)", r.status, r.body)
	}
	if r := env.do(t, http.MethodGet, "/api/v1/connection-requests/"+pending.str("id"), adminAccess, nil); r.status != http.StatusOK || r.str("business_name") != "Pending Co" {
		t.Errorf("get: %d (%v)", r.status, r.body)
	}
	if r := env.do(t, http.MethodGet, "/api/v1/connection-requests/00000000-0000-0000-0000-000000000000", adminAccess, nil); r.status != http.StatusNotFound {
		t.Errorf("unknown request: expected 404, got %d", r.status)
	}
}
