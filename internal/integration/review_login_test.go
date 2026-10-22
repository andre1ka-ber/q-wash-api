//go:build integration

package integration

import (
	"net/http"
	"testing"

	"q-wash-api/internal/config"
)

const (
	reviewPhone = "+992900000000"
	reviewCode  = "424242"
)

func TestReviewLogin(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) {
		c.Auth.ReviewPhone = reviewPhone
		c.Auth.ReviewOTP = reviewCode
	})

	t.Run("request sends no SMS", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/otp/request", "", map[string]string{"phone_number": reviewPhone})
		if resp.status != http.StatusAccepted {
			t.Fatalf("expected 202, got %d (%v)", resp.status, resp.body)
		}
		if n := env.sms.countTo(reviewPhone); n != 0 {
			t.Fatalf("expected no SMS to the reviewer phone, got %d", n)
		}
	})

	t.Run("fixed code logs in as a customer", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": reviewPhone, "code": reviewCode})
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		access, _ := resp.body["access_token"].(string)
		me := env.do(t, http.MethodGet, "/api/v1/me", access, nil)
		if me.status != http.StatusOK || me.str("role") != "customer" || me.str("phone_number") != reviewPhone {
			t.Fatalf("expected the customer account for the reviewer phone, got %d (%v)", me.status, me.body)
		}
	})

	t.Run("fixed code works again (not consumed)", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": reviewPhone, "code": reviewCode})
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("wrong code is rejected", func(t *testing.T) {
		resp := env.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": reviewPhone, "code": "000000"})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
	})

	t.Run("fixed code does not work for other phones", func(t *testing.T) {
		other := "+992911111111"
		resp := env.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": other, "code": reviewCode})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%v)", resp.status, resp.body)
		}
	})
}

func TestReviewLogin_OffByDefault(t *testing.T) {
	env := newTestEnv(t)
	resp := env.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": reviewPhone, "code": reviewCode})
	if resp.status == http.StatusOK {
		t.Fatalf("expected the fixed code to be rejected when the feature is off, got 200")
	}
}
