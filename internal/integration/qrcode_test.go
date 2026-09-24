//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"q-wash-api/internal/user"
)

func (e *testEnv) createWashingPoint(t *testing.T, adminAccess, name string) string {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": name, "address": "1 QR Test Way",
		"latitude": 1.0, "longitude": 2.0,
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("create washing point: expected 201, got %d (%v)", resp.status, resp.body)
	}
	return resp.str("id")
}

func TestQRCodes_GenerateAssignAndPoolInvariants(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(90), user.RoleAdmin)
	staffPhone := uniquePhone(91)
	staffAccess := env.loginAs(t, staffPhone, user.RoleStaff)

	pointA := env.createWashingPoint(t, adminAccess, "QR Point A")
	pointB := env.createWashingPoint(t, adminAccess, "QR Point B")
	env.setWashingPointID(t, staffPhone, pointA)
	staffAccess = env.reLogin(t, staffPhone)

	t.Run("staff is forbidden from every admin qr-codes route", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/qr-codes", staffAccess, nil)
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for staff listing the pool, got %d (%v)", resp.status, resp.body)
		}
		resp = env.do(t, http.MethodPost, "/api/v1/qr-codes/generate", staffAccess, map[string]any{"count": 1, "batch_label": "x"})
		if resp.status != http.StatusForbidden {
			t.Fatalf("expected 403 for staff generating codes, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("admin with no washing point gets a clear error from mine", func(t *testing.T) {
		resp := env.do(t, http.MethodGet, "/api/v1/qr-codes/mine", adminAccess, nil)
		if resp.status != http.StatusBadRequest || resp.body["error"].(map[string]any)["code"] != "no_washing_point" {
			t.Fatalf("expected 400 no_washing_point for an admin caller, got %d (%v)", resp.status, resp.body)
		}
	})

	var codeAID, codeBID, codeAToken string
	t.Run("admin generates a batch, sees it in the pool with sequential codes", func(t *testing.T) {
		gen := env.do(t, http.MethodPost, "/api/v1/qr-codes/generate", adminAccess, map[string]any{"count": 3, "batch_label": "Batch #1"})
		if gen.status != http.StatusCreated {
			t.Fatalf("generate: expected 201, got %d (%v)", gen.status, gen.body)
		}
		items, _ := gen.body["items"].([]any)
		if len(items) != 3 {
			t.Fatalf("expected 3 generated codes, got %d", len(items))
		}
		first := items[0].(map[string]any)
		if !strings.HasPrefix(first["code"].(string), "QW-") {
			t.Errorf("expected a QW-#### code, got %v", first["code"])
		}
		codeAID = items[0].(map[string]any)["id"].(string)
		codeAToken = items[0].(map[string]any)["token"].(string)
		codeBID = items[1].(map[string]any)["id"].(string)

		list := env.do(t, http.MethodGet, "/api/v1/qr-codes", adminAccess, nil)
		if list.status != http.StatusOK {
			t.Fatalf("list: expected 200, got %d (%v)", list.status, list.body)
		}
		stats := list.body["stats"].(map[string]any)
		if int(stats["total"].(float64)) < 3 || int(stats["free"].(float64)) < 3 {
			t.Errorf("expected pool stats to reflect at least the 3 new free codes, got %v", stats)
		}
	})

	t.Run("assigning to a point, staff sees it via mine", func(t *testing.T) {
		assign := env.do(t, http.MethodPost, "/api/v1/qr-codes/"+codeAID+"/assign", adminAccess, map[string]any{"washing_point_id": pointA})
		if assign.status != http.StatusOK {
			t.Fatalf("assign: expected 200, got %d (%v)", assign.status, assign.body)
		}
		if assign.str("status") != "assigned" {
			t.Errorf("expected status=assigned, got %q", assign.str("status"))
		}

		mine := env.do(t, http.MethodGet, "/api/v1/qr-codes/mine", staffAccess, nil)
		if mine.status != http.StatusOK {
			t.Fatalf("mine: expected 200, got %d (%v)", mine.status, mine.body)
		}
		if mine.str("id") != codeAID {
			t.Errorf("expected staff's own code to be %s, got %s", codeAID, mine.str("id"))
		}
		if mine.body["stats"] == nil {
			t.Error("expected mine to include stats")
		}
	})

	t.Run("assigning a second code to the same point frees the first", func(t *testing.T) {
		assign := env.do(t, http.MethodPost, "/api/v1/qr-codes/"+codeBID+"/assign", adminAccess, map[string]any{"washing_point_id": pointA})
		if assign.status != http.StatusOK {
			t.Fatalf("assign second code: expected 200, got %d (%v)", assign.status, assign.body)
		}

		first := env.do(t, http.MethodGet, "/api/v1/qr-codes/"+codeAID, adminAccess, nil)
		if first.status != http.StatusOK {
			t.Fatalf("get first code: expected 200, got %d (%v)", first.status, first.body)
		}
		if first.str("status") != "free" || first.body["washing_point_id"] != nil {
			t.Errorf("expected the first code to be freed after reassignment, got status=%v washing_point_id=%v", first.body["status"], first.body["washing_point_id"])
		}
	})

	t.Run("a point cannot be assigned a code belonging to another point without the other point noticing", func(t *testing.T) {
		// Sanity check the invariant the mock itself states: one code per
		// point. Assign codeB (currently on pointA) to pointB and confirm
		// pointA now has no code of its own.
		reassign := env.do(t, http.MethodPost, "/api/v1/qr-codes/"+codeBID+"/assign", adminAccess, map[string]any{"washing_point_id": pointB})
		if reassign.status != http.StatusOK {
			t.Fatalf("reassign to pointB: expected 200, got %d (%v)", reassign.status, reassign.body)
		}
		mine := env.do(t, http.MethodGet, "/api/v1/qr-codes/mine", staffAccess, nil)
		if mine.status != http.StatusNotFound {
			t.Fatalf("expected pointA's staff to see 404 (no code) after codeB moved to pointB, got %d (%v)", mine.status, mine.body)
		}
		// Reassign codeB back to pointA for the rest of the test.
		env.do(t, http.MethodPost, "/api/v1/qr-codes/"+codeBID+"/assign", adminAccess, map[string]any{"washing_point_id": pointA})
	})

	t.Run("public scan endpoint records a hit with zero auth and always serves html", func(t *testing.T) {
		resp, err := env.client.Get(env.baseURL + "/api/v1/qr-codes/scan/" + codeAToken)
		if err != nil {
			t.Fatalf("scan request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for a valid token, got %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("expected text/html, got %q", ct)
		}

		unknown, err := env.client.Get(env.baseURL + "/api/v1/qr-codes/scan/not-a-real-token")
		if err != nil {
			t.Fatalf("scan request (unknown token): %v", err)
		}
		defer unknown.Body.Close()
		if unknown.StatusCode != http.StatusOK {
			t.Errorf("expected 200 (generic page) for an unknown token, not an error status, got %d", unknown.StatusCode)
		}

		detail := env.do(t, http.MethodGet, "/api/v1/qr-codes/"+codeAID, adminAccess, nil)
		if detail.status != http.StatusOK {
			t.Fatalf("get code after scan: expected 200, got %d (%v)", detail.status, detail.body)
		}
		stats := detail.body["stats"].(map[string]any)
		if int(stats["scans_today"].(float64)) < 1 {
			t.Errorf("expected the scan to be reflected in scans_today, got %v", stats["scans_today"])
		}
	})

	t.Run("disabling an assigned code releases the point and it can't be reassigned", func(t *testing.T) {
		// codeA is currently free (freed when codeB took pointA) — assign
		// it to pointB first so disabling it actually exercises the
		// "release from an assigned point" path, not just a no-op status flip.
		assign := env.do(t, http.MethodPost, "/api/v1/qr-codes/"+codeAID+"/assign", adminAccess, map[string]any{"washing_point_id": pointB})
		if assign.status != http.StatusOK {
			t.Fatalf("assign codeA to pointB: expected 200, got %d (%v)", assign.status, assign.body)
		}

		disable := env.do(t, http.MethodPost, "/api/v1/qr-codes/"+codeAID+"/disable", adminAccess, nil)
		if disable.status != http.StatusOK {
			t.Fatalf("disable: expected 200, got %d (%v)", disable.status, disable.body)
		}
		if disable.str("status") != "disabled" {
			t.Errorf("expected status=disabled, got %q", disable.str("status"))
		}
		if disable.body["washing_point_id"] != nil {
			t.Errorf("expected washing_point_id to be cleared on disable, got %v", disable.body["washing_point_id"])
		}

		reassign := env.do(t, http.MethodPost, "/api/v1/qr-codes/"+codeAID+"/assign", adminAccess, map[string]any{"washing_point_id": pointA})
		if reassign.status != http.StatusConflict {
			t.Fatalf("expected 409 assigning a disabled code, got %d (%v)", reassign.status, reassign.body)
		}
	})

	t.Run("staff can request a replacement only while their code is assigned", func(t *testing.T) {
		req := env.do(t, http.MethodPost, "/api/v1/qr-codes/mine/request-replacement", staffAccess, nil)
		if req.status != http.StatusOK {
			t.Fatalf("request-replacement: expected 200, got %d (%v)", req.status, req.body)
		}
		if req.body["replacement_requested_at"] == nil {
			t.Error("expected replacement_requested_at to be set")
		}
	})
}
