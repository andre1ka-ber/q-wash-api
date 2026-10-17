package jwt

import (
	"testing"
	"time"
)

func TestGenerate_TTLByRole(t *testing.T) {
	m := NewManager("secret", 15*time.Minute).WithPrivilegedTTL(5 * time.Minute)

	for _, tc := range []struct {
		role string
		want time.Duration
	}{
		{"customer", 15 * time.Minute},
		{"staff", 5 * time.Minute},
		{"worker", 5 * time.Minute},
		{"admin", 5 * time.Minute},
	} {
		t.Run(tc.role, func(t *testing.T) {
			before := time.Now()
			token, expiresAt, err := m.Generate("user-id", tc.role, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := expiresAt.Sub(before); got < tc.want || got > tc.want+time.Second {
				t.Fatalf("lifetime = %v, want about %v", got, tc.want)
			}
			claims, err := m.Parse(token)
			if err != nil || claims.Role != tc.role {
				t.Fatalf("parse: %+v err=%v", claims, err)
			}
			if exp := claims.ExpiresAt.Time; exp.Sub(before) > tc.want+time.Second {
				t.Fatalf("exp claim %v is later than the reported expiry", exp)
			}
		})
	}
}

func TestGenerate_NoPrivilegedTTLMeansOneLifetime(t *testing.T) {
	m := NewManager("secret", 15*time.Minute)
	before := time.Now()
	_, expiresAt, err := m.Generate("user-id", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := expiresAt.Sub(before); got < 15*time.Minute || got > 15*time.Minute+time.Second {
		t.Fatalf("lifetime = %v, want about 15m", got)
	}
}
