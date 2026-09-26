package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/jwt"
	"q-wash-api/internal/user"
)

type Handler struct {
	service    *Service
	jwtManager *jwt.Manager
}

func NewHandler(service *Service, jwtManager *jwt.Manager) *Handler {
	return &Handler{service: service, jwtManager: jwtManager}
}

// RegisterPublicRoutes mounts the unauthenticated auth endpoints
// (OTP request/verify, refresh) under r.
func (h *Handler) RegisterPublicRoutes(r chi.Router) {
	r.Route("/auth", func(auth chi.Router) {
		auth.Post("/otp/request", h.requestOTP)
		auth.Post("/otp/verify", h.verifyOTP)
		auth.Post("/login", h.login)
		auth.Post("/refresh", h.refresh)
	})
}

// RegisterAuthenticatedRoutes mounts endpoints that require a valid access
// token. The caller must wrap r with RequireAuth.
func (h *Handler) RegisterAuthenticatedRoutes(r chi.Router) {
	r.Post("/auth/logout", h.logout)
}

// Middleware exposes RequireAuth bound to this handler's jwt.Manager, so
// main.go doesn't need to wire the manager into route registration itself.
func (h *Handler) Middleware() func(http.Handler) http.Handler {
	return RequireAuth(h.jwtManager)
}

type otpRequestRequest struct {
	PhoneNumber string `json:"phone_number"`
}

func (h *Handler) requestOTP(w http.ResponseWriter, r *http.Request) {
	var req otpRequestRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	if err := h.service.RequestOTP(r.Context(), req.PhoneNumber); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "otp_sent"})
}

type otpVerifyRequest struct {
	PhoneNumber string `json:"phone_number"`
	Code        string `json:"code"`
}

func (h *Handler) verifyOTP(w http.ResponseWriter, r *http.Request) {
	var req otpVerifyRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	pair, err := h.service.VerifyOTP(r.Context(), req.PhoneNumber, req.Code)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toTokenPairResponse(pair))
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// login is the username/password path for staff/admin surfaces (the queue
// board, the staff panel) — customers keep using phone+OTP. Public, like
// the OTP endpoints: the credential itself is the auth.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_credentials", "username and password are required"))
		return
	}

	pair, err := h.service.LoginWithPassword(r.Context(), username, req.Password)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toTokenPairResponse(pair))
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if req.RefreshToken == "" {
		httputil.WriteError(w, r, apperror.BadRequest("refresh_token_required", "refresh_token is required"))
		return
	}

	pair, err := h.service.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toTokenPairResponse(pair))
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	var req logoutRequest
	// Body is optional for logout (revoke-all-if-omitted); ignore a missing/empty body.
	_ = httputil.DecodeJSON(r, &req)

	if err := h.service.Logout(r.Context(), authUser.ID, req.RefreshToken); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type tokenPairResponse struct {
	AccessToken           string       `json:"access_token"`
	AccessTokenExpiresAt  time.Time    `json:"access_token_expires_at"`
	RefreshToken          string       `json:"refresh_token"`
	RefreshTokenExpiresAt time.Time    `json:"refresh_token_expires_at"`
	User                  userResponse `json:"user"`
}

type userResponse struct {
	ID             string     `json:"id"`
	PhoneNumber    string     `json:"phone_number"`
	Name           *string    `json:"name,omitempty"`
	Role           string     `json:"role"`
	WashingPointID *string    `json:"washing_point_id,omitempty"`
	LastLoginAt    *time.Time `json:"last_login_at,omitempty"`
}

func toTokenPairResponse(pair *TokenPair) tokenPairResponse {
	return tokenPairResponse{
		AccessToken:           pair.AccessToken,
		AccessTokenExpiresAt:  pair.AccessTokenExpiresAt,
		RefreshToken:          pair.RefreshToken,
		RefreshTokenExpiresAt: pair.RefreshTokenExpiresAt,
		User:                  toUserResponse(pair.User),
	}
}

func toUserResponse(u *user.User) userResponse {
	var washingPointID *string
	if u.WashingPointID != nil {
		id := u.WashingPointID.String()
		washingPointID = &id
	}
	return userResponse{
		ID:             u.ID.String(),
		PhoneNumber:    u.PhoneNumber,
		Name:           u.Name,
		Role:           string(u.Role),
		WashingPointID: washingPointID,
		LastLoginAt:    u.LastLoginAt,
	}
}
