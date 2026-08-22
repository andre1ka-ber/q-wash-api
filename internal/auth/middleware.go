package auth

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/jwt"
	"q-wash-api/internal/platform/reqctx"
)

// RequireAuth parses and validates the Bearer access token and stores the
// resulting reqctx.AuthUser in the request context. It does not hit the
// database — validity is judged purely by JWT signature/expiry, which keeps
// every authenticated request to a single round trip on top of whatever the
// handler itself needs.
func RequireAuth(jwtManager *jwt.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || token == "" {
				httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "missing or malformed Authorization header"))
				return
			}

			claims, err := jwtManager.Parse(token)
			if err != nil {
				httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "invalid or expired access token"))
				return
			}

			userID, err := uuid.Parse(claims.UserID)
			if err != nil {
				httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "invalid access token"))
				return
			}

			var washingPointID *uuid.UUID
			if claims.WashingPointID != nil {
				parsed, err := uuid.Parse(*claims.WashingPointID)
				if err != nil {
					httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "invalid access token"))
					return
				}
				washingPointID = &parsed
			}

			ctx := reqctx.WithAuthUser(r.Context(), reqctx.AuthUser{ID: userID, Role: claims.Role, WashingPointID: washingPointID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole must be chained after RequireAuth. It rejects the request
// with 403 unless the authenticated user's role is one of allowed.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, role := range allowed {
		allowedSet[role] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authUser, ok := reqctx.AuthUserFromContext(r.Context())
			if !ok {
				httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
				return
			}
			if _, ok := allowedSet[authUser.Role]; !ok {
				httputil.WriteError(w, r, apperror.Forbidden("forbidden", "you do not have permission to perform this action"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
