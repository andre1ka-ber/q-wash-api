// Package jwt issues and verifies short-lived access tokens. Refresh tokens
// are deliberately NOT JWTs (see internal/auth) — they're opaque random
// strings stored hashed in the DB, so they can be individually revoked.
package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("invalid or expired token")

type Claims struct {
	UserID string `json:"uid"`
	Role   string `json:"role"`
	// WashingPointID scopes a staff/worker principal to one point (see
	// reqctx.AuthUser). Omitted for admin/customer.
	WashingPointID *string `json:"wpid,omitempty"`
	jwt.RegisteredClaims
}

type Manager struct {
	secret []byte
	ttl    time.Duration
}

func NewManager(secret string, ttl time.Duration) *Manager {
	return &Manager{secret: []byte(secret), ttl: ttl}
}

// Generate returns a signed access token and its expiry time. washingPointID
// is nil for admin/customer accounts.
func (m *Manager) Generate(userID, role string, washingPointID *string) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(m.ttl)

	claims := Claims{
		UserID:         userID,
		Role:           role,
		WashingPointID: washingPointID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

func (m *Manager) Parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return m.secret, nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
