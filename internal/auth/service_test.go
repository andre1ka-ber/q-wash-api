package auth

import (
	"testing"

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
