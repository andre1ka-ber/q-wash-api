package user

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes mounts /me under r. The caller is responsible for wrapping
// r with an auth-required middleware — these handlers assume an AuthUser is
// already present in the request context.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/me", h.getMe)
	r.Patch("/me", h.updateMe)
}

type response struct {
	ID             string     `json:"id"`
	PhoneNumber    string     `json:"phone_number"`
	Name           *string    `json:"name,omitempty"`
	Role           string     `json:"role"`
	WashingPointID *string    `json:"washing_point_id,omitempty"`
	LastLoginAt    *time.Time `json:"last_login_at,omitempty"`
}

func toResponse(u *User) response {
	var washingPointID *string
	if u.WashingPointID != nil {
		id := u.WashingPointID.String()
		washingPointID = &id
	}
	return response{
		ID:             u.ID.String(),
		PhoneNumber:    u.PhoneNumber,
		Name:           u.Name,
		Role:           string(u.Role),
		WashingPointID: washingPointID,
		LastLoginAt:    u.LastLoginAt,
	}
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	u, err := h.repo.FindByID(r.Context(), authUser.ID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(u))
}

type updateMeRequest struct {
	Name *string `json:"name"`
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	var req updateMeRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_name", "name is required"))
		return
	}
	name := strings.TrimSpace(*req.Name)
	if len(name) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_name", "name is too long"))
		return
	}

	u, err := h.repo.UpdateName(r.Context(), authUser.ID, name)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(u))
}
