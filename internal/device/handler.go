package device

import (
	"net/http"
	"strings"

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

// RegisterRoutes mounts /me/devices. Every route needs an authenticated
// user (the caller wraps r with RequireAuth); there is no role gate — any
// signed-in user may register their own device.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/me/devices", func(d chi.Router) {
		d.Put("/", h.register)
		d.Delete("/{token}", h.unregister)
	})
}

type registerRequest struct {
	Token    string   `json:"token"`
	Platform Platform `json:"platform"`
}

const maxTokenLen = 4096

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	var req registerRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || len(token) > maxTokenLen {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_token", "token is required (max 4096 chars)"))
		return
	}
	if !req.Platform.Valid() {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_platform", "platform must be android or ios"))
		return
	}

	if err := h.repo.Upsert(r.Context(), authUser.ID, token, req.Platform); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) unregister(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteOwned(r.Context(), authUser.ID, chi.URLParam(r, "token")); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
