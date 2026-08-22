// Package reqctx carries the authenticated principal through a request's
// context.Context. It's deliberately dependency-free (no auth/user imports)
// so both the auth middleware (which sets it) and any feature handler
// (which reads it) can depend on it without creating an import cycle.
package reqctx

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey int

const authUserKey ctxKey = iota

type AuthUser struct {
	ID   uuid.UUID
	Role string
	// WashingPointID scopes a staff/worker principal to the one point they
	// work at (mirrors user.User.WashingPointID, carried here via the JWT
	// so RequireAuth doesn't need a DB hit). Always nil for admin/customer.
	WashingPointID *uuid.UUID
}

func WithAuthUser(ctx context.Context, u AuthUser) context.Context {
	return context.WithValue(ctx, authUserKey, u)
}

func AuthUserFromContext(ctx context.Context) (AuthUser, bool) {
	u, ok := ctx.Value(authUserKey).(AuthUser)
	return u, ok
}

// OwnsWashingPoint reports whether u may act on washingPointID: admin
// always does (network-wide); staff/worker only if it's their own. Used by
// every staff-gated handler that reaches a specific washing point (or a
// resource belonging to one) to avoid the role check alone granting access
// across points — see docs/PLAN_WEB_APPS.md's IDOR review.
func (u AuthUser) OwnsWashingPoint(washingPointID uuid.UUID) bool {
	if u.Role == "admin" {
		return true
	}
	return u.WashingPointID != nil && *u.WashingPointID == washingPointID
}
