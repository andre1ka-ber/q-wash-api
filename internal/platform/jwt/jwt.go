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

// customerRole mirrors user.RoleCustomer; this package must not import user.
const customerRole = "customer"

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
	// privilegedTTL, when positive, replaces ttl for every role except
	// customer. A token's role and washing point are trusted until it expires
	// (no DB check per request), so a shorter life bounds how long a demoted
	// or removed staff/worker/admin keeps access.
	privilegedTTL time.Duration
}

func NewManager(secret string, ttl time.Duration) *Manager {
	return &Manager{secret: []byte(secret), ttl: ttl}
}

// WithPrivilegedTTL sets the shorter access-token lifetime for non-customer
// roles; zero keeps the single ttl for everyone.
func (m *Manager) WithPrivilegedTTL(ttl time.Duration) *Manager {
	m.privilegedTTL = ttl
	return m
}

// Generate returns a signed access token and its expiry time. washingPointID
// is nil for admin/customer accounts.
func (m *Manager) Generate(userID, role string, washingPointID *string) (string, time.Time, error) {
	now := time.Now()
	ttl := m.ttl
	if role != customerRole && m.privilegedTTL > 0 {
		ttl = m.privilegedTTL
	}
	expiresAt := now.Add(ttl)

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
