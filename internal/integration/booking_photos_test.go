//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/user"
)

func TestBookingPhotos_WorkerUploadOwnershipAndLimits(t *testing.T) {
	env := newTestEnv(t)
	adminAccess := env.loginAs(t, uniquePhone(220), user.RoleAdmin)
	staffPhone := uniquePhone(221)
	env.loginAs(t, staffPhone, user.RoleStaff)
	customerAccess := env.loginAs(t, uniquePhone(222), "")
	otherStaffPhone := uniquePhone(223)
	env.loginAs(t, otherStaffPhone, user.RoleStaff)
	workerPhone := uniquePhone(224)
	env.loginAs(t, workerPhone, user.RoleWorker)

	fx := env.setUpBookingFixture(t, adminAccess, staffPhone)
	env.setWashingPointID(t, workerPhone, fx.washingPointID)
	workerAccess := env.reLogin(t, workerPhone)

	otherWP := env.do(t, http.MethodPost, "/api/v1/washing-points", adminAccess, map[string]any{
		"name": "Other Point", "address": "2 Test St", "latitude": 3.0, "longitude": 4.0,
	})
	if otherWP.status != http.StatusCreated {
		t.Fatalf("create other wp: expected 201, got %d (%v)", otherWP.status, otherWP.body)
	}
	env.setWashingPointID(t, otherStaffPhone, otherWP.str("id"))
	otherStaffAccess := env.reLogin(t, otherStaffPhone)

	booking := env.do(t, http.MethodPost, "/api/v1/washing-points/"+fx.washingPointID+"/queue/manual", fx.staffAccess, map[string]any{
		"service_id": fx.serviceID, "price_option_id": fx.priceOptionID, "box_number": 1,
		"scheduled_start_at": futureBookingTime(5), "car_name": "Toyota Camry", "plate": "1234 AB 01",
		"client_phone": uniquePhone(225), "client_name": "Walk In",
	})
	if booking.status != http.StatusCreated {
		t.Fatalf("create booking: expected 201, got %d (%v)", booking.status, booking.body)
	}
	bookingID := booking.str("id")
	photosPath := "/api/v1/queue/" + bookingID + "/photos"

	t.Run("unauthenticated and customers are blocked", func(t *testing.T) {
		if resp := env.uploadFile(t, photosPath, "", "photo.jpg", fakeJPEG, false); resp.status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated: expected 401, got %d (%v)", resp.status, resp.body)
		}
		if resp := env.uploadFile(t, photosPath, customerAccess, "photo.jpg", fakeJPEG, false); resp.status != http.StatusForbidden {
			t.Fatalf("customer: expected 403, got %d (%v)", resp.status, resp.body)
		}
		if resp := env.do(t, http.MethodGet, photosPath, customerAccess, nil); resp.status != http.StatusForbidden {
			t.Fatalf("customer list: expected 403, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("another point's staff gets 404 (IDOR check)", func(t *testing.T) {
		if resp := env.uploadFile(t, photosPath, otherStaffAccess, "photo.jpg", fakeJPEG, false); resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
		if resp := env.do(t, http.MethodGet, photosPath, otherStaffAccess, nil); resp.status != http.StatusNotFound {
			t.Fatalf("list: expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("upload is refused until the wash is in progress", func(t *testing.T) {
		resp := env.uploadFile(t, photosPath, workerAccess, "photo.jpg", fakeJPEG, false)
		errBody, _ := resp.body["error"].(map[string]any)
		if resp.status != http.StatusConflict || errBody["code"] != "booking_not_washing" {
			t.Fatalf("expected 409 booking_not_washing, got %d (%v)", resp.status, resp.body)
		}
	})

	statusPath := "/api/v1/queue/" + bookingID + "/status"
	for _, status := range []string{"waiting", "washing"} {
		if resp := env.do(t, http.MethodPatch, statusPath, workerAccess, map[string]any{"status": status}); resp.status != http.StatusOK {
			t.Fatalf("worker -> %s: expected 200, got %d (%v)", status, resp.status, resp.body)
		}
	}

	t.Run("non-image files are rejected", func(t *testing.T) {
		resp := env.uploadFile(t, photosPath, workerAccess, "notes.txt", []byte("just some text"), false)
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
	})

	var firstID string
	t.Run("worker uploads and lists photos", func(t *testing.T) {
		resp := env.uploadFile(t, photosPath, workerAccess, "photo.jpg", fakeJPEG, false)
		if resp.status != http.StatusCreated || resp.str("url") == "" {
			t.Fatalf("expected 201 with a url, got %d (%v)", resp.status, resp.body)
		}
		firstID = resp.str("id")

		list := env.do(t, http.MethodGet, photosPath, workerAccess, nil)
		items, _ := list.body["items"].([]any)
		if list.status != http.StatusOK || len(items) != 1 {
			t.Fatalf("expected 200 with 1 photo, got %d (%v)", list.status, list.body)
		}
	})

	t.Run("another point's staff cannot delete it", func(t *testing.T) {
		resp := env.do(t, http.MethodDelete, photosPath+"/"+firstID, otherStaffAccess, nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("expected 404, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("a booking holds at most 10 photos", func(t *testing.T) {
		for i := 1; i < 10; i++ {
			if resp := env.uploadFile(t, photosPath, workerAccess, "photo.jpg", fakeJPEG, false); resp.status != http.StatusCreated {
				t.Fatalf("upload %d: expected 201, got %d (%v)", i+1, resp.status, resp.body)
			}
		}
		resp := env.uploadFile(t, photosPath, workerAccess, "photo.jpg", fakeJPEG, false)
		errBody, _ := resp.body["error"].(map[string]any)
		if resp.status != http.StatusConflict || errBody["code"] != "photo_limit_reached" {
			t.Fatalf("expected 409 photo_limit_reached, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("worker deletes a photo", func(t *testing.T) {
		if resp := env.do(t, http.MethodDelete, photosPath+"/"+firstID, workerAccess, nil); resp.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%v)", resp.status, resp.body)
		}
		if resp := env.do(t, http.MethodDelete, photosPath+"/"+firstID, workerAccess, nil); resp.status != http.StatusNotFound {
			t.Fatalf("second delete: expected 404, got %d (%v)", resp.status, resp.body)
		}
		list := env.do(t, http.MethodGet, photosPath, workerAccess, nil)
		items, _ := list.body["items"].([]any)
		if len(items) != 9 {
			t.Fatalf("expected 9 photos left, got %d", len(items))
		}
	})
}
