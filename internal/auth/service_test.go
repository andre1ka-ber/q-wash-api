package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/config"
)

func TestValidatePhoneNumber_Valid(t *testing.T) {
	cases := []string{"+15551234567", "+992901112233", "+447911123456"}
	for _, phone := range cases {
		t.Run(phone, func(t *testing.T) {
			if err := ValidatePhoneNumber(phone); err != nil {
				t.Errorf("expected %q to be valid, got %v", phone, err)
			}
		})
	}
}

func TestValidatePhoneNumber_Invalid(t *testing.T) {
	cases := []string{
		"",
		"15551234567",       // missing +
		"+0551234567",       // leading zero after +
		"+123456",           // too short (needs at least 7 digits after the +)
		"+1234567890123456", // too long
		"+1555abc4567",      // non-digit
	}
	for _, phone := range cases {
		t.Run(phone, func(t *testing.T) {
			if err := ValidatePhoneNumber(phone); err == nil {
				t.Errorf("expected %q to be invalid", phone)
			}
		})
	}
}

func TestValidatePasswordPolicy_Valid(t *testing.T) {
	cases := []string{"Password1", "Abcdefg2", "СтройныйПароль9", "aB3defgh"}
	for _, pw := range cases {
		t.Run(pw, func(t *testing.T) {
			if err := ValidatePasswordPolicy(pw); err != nil {
				t.Errorf("expected %q to be valid, got %v", pw, err)
			}
		})
	}
}

func TestValidatePasswordPolicy_Invalid(t *testing.T) {
	cases := []string{
		"",
		"Ab1",      // too short
		"Abcdefgh", // no digit
		"abcdefg1", // no uppercase
		"ABCDEFG1", // no lowercase
	}
	for _, pw := range cases {
		t.Run(pw, func(t *testing.T) {
			if err := ValidatePasswordPolicy(pw); err == nil {
				t.Errorf("expected %q to be invalid", pw)
			}
		})
	}
}

func TestGenerateOTPCode_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		code, err := generateOTPCode()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !otpCodeRegexp.MatchString(code) {
			t.Fatalf("code %q does not match the expected 6-digit format", code)
		}
		seen[code] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected repeated calls to produce varying codes, got only %d distinct value(s) across 20 calls", len(seen))
	}
}

func TestGenerateRefreshToken_NonEmptyAndUnique(t *testing.T) {
	a, err := generateRefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := generateRefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("expected non-empty tokens")
	}
	if a == b {
		t.Error("expected two calls to produce different tokens")
	}
}

func TestHashRefreshToken_DeterministicPerPepper(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, config.AuthConfig{RefreshTokenPepper: "pepper-a"})

	h1 := svc.hashRefreshToken("token-1")
	h2 := svc.hashRefreshToken("token-1")
	if h1 != h2 {
		t.Errorf("expected hashing the same token twice to be deterministic, got %q and %q", h1, h2)
	}

	h3 := svc.hashRefreshToken("token-2")
	if h1 == h3 {
		t.Error("expected different tokens to hash differently")
	}
}

func TestHashRefreshToken_DiffersByPepper(t *testing.T) {
	svcA := NewService(nil, nil, nil, nil, config.AuthConfig{RefreshTokenPepper: "pepper-a"})
	svcB := NewService(nil, nil, nil, nil, config.AuthConfig{RefreshTokenPepper: "pepper-b"})

	if svcA.hashRefreshToken("token-1") == svcB.hashRefreshToken("token-1") {
		t.Error("expected the same token to hash differently under a different pepper")
	}
}

func reviewService(maxAttempts int, cooldown time.Duration, now func() time.Time) *Service {
	svc := NewService(nil, nil, nil, nil, config.AuthConfig{
		ReviewPhone:    "+992900000000",
		ReviewOTP:      "424242",
		OTPMaxAttempts: maxAttempts,
		OTPCooldown:    cooldown,
	})
	svc.now = now
	return svc
}

func TestRequestOTP_ReviewPhoneSendsNothing(t *testing.T) {
	// nil repo and nil sender: any attempt to store or send a code would panic.
	svc := reviewService(5, time.Minute, time.Now)
	if err := svc.RequestOTP(context.Background(), "+992900000000"); err != nil {
		t.Fatalf("expected the reviewer phone to be accepted, got %v", err)
	}
}

func TestIsReviewPhone_OffByDefault(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, config.AuthConfig{})
	if svc.isReviewPhone("") || svc.isReviewPhone("+992900000000") {
		t.Fatal("expected no phone to count as reviewer when unconfigured")
	}
}

func TestIsReviewPhone_OtherPhonesUnaffected(t *testing.T) {
	svc := reviewService(5, time.Minute, time.Now)
	if svc.isReviewPhone("+992911111111") {
		t.Fatal("expected only the configured phone to count as reviewer")
	}
}

func TestVerifyReviewOTP_RightCode(t *testing.T) {
	svc := reviewService(5, time.Minute, time.Now)
	if err := svc.verifyReviewOTP("424242"); err != nil {
		t.Fatalf("expected the fixed code to be accepted, got %v", err)
	}
}

func TestVerifyReviewOTP_WrongCodeThenLockout(t *testing.T) {
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := reviewService(3, time.Minute, func() time.Time { return clock })

	for i := 0; i < 3; i++ {
		err := svc.verifyReviewOTP("000000")
		var appErr *apperror.Error
		if !errors.As(err, &appErr) || appErr.Code != "otp_invalid" {
			t.Fatalf("attempt %d: expected otp_invalid, got %v", i+1, err)
		}
	}

	err := svc.verifyReviewOTP("424242")
	var appErr *apperror.Error
	if !errors.As(err, &appErr) || appErr.Code != "otp_locked" {
		t.Fatalf("expected otp_locked even for the right code, got %v", err)
	}

	clock = clock.Add(61 * time.Second)
	if err := svc.verifyReviewOTP("424242"); err != nil {
		t.Fatalf("expected the right code to work after the lockout, got %v", err)
	}
}

func TestVerifyReviewOTP_SuccessResetsFailures(t *testing.T) {
	svc := reviewService(3, time.Minute, time.Now)
	for i := 0; i < 2; i++ {
		_ = svc.verifyReviewOTP("000000")
	}
	if err := svc.verifyReviewOTP("424242"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc.verifyReviewOTP("000000"); err == nil {
			t.Fatal("expected a wrong code to be rejected")
		}
	}
	if err := svc.verifyReviewOTP("424242"); err != nil {
		t.Fatalf("expected success to have reset the failure count, got %v", err)
	}
}
