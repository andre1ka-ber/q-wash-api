package car

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/reqctx"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes mounts /me/cars and /cars/{id}. Every route here requires
// an authenticated user — the caller must wrap r with RequireAuth. There's
// no role gate beyond that: ownership (not role) is what's enforced, at the
// repository layer via FindOwnedByID.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/me/cars", func(cars chi.Router) {
		cars.Get("/", h.list)
		cars.Post("/", h.create)
	})
	r.Route("/cars/{id}", func(cars chi.Router) {
		cars.Patch("/", h.update)
		cars.Delete("/", h.delete)
	})
}

type response struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toResponse(c *Car) response {
	return response{ID: c.ID.String(), Name: c.Name}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	cars, err := h.repo.ListByUser(r.Context(), authUser.ID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(cars))
	for i := range cars {
		items[i] = toResponse(&cars[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createRequest struct {
	Name string `json:"name"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_name", "name is required (max 255 chars)"))
		return
	}

	c := &Car{UserID: authUser.ID, Name: name}
	if err := h.repo.Create(r.Context(), c); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(c))
}

type updateRequest struct {
	Name *string `json:"name"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	c, err := h.repo.FindOwnedByID(r.Context(), id, authUser.ID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var req updateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if req.Name == nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_name", "name is required"))
		return
	}
	name := strings.TrimSpace(*req.Name)
	if name == "" || len(name) > 255 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_name", "name is required (max 255 chars)"))
		return
	}
	c.Name = name

	if err := h.repo.Update(r.Context(), c); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(c))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if _, err := h.repo.FindOwnedByID(r.Context(), id, authUser.ID); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	inUse, err := h.repo.CountQueueUsingCar(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if inUse > 0 {
		httputil.WriteError(w, r, apperror.Conflict("car_in_use", "car is referenced by existing bookings and cannot be deleted"))
		return
	}

	if err := h.repo.Delete(r.Context(), id); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
