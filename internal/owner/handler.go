package owner

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

// RegisterRoutes mounts /owners, admin-only for every method — unlike
// washing points, owner contact info isn't public.
func (h *Handler) RegisterRoutes(r chi.Router, requireAdmin ...func(http.Handler) http.Handler) {
	r.Route("/owners", func(o chi.Router) {
		o.With(requireAdmin...).Get("/", h.list)
		o.With(requireAdmin...).Get("/{id}", h.get)
		o.With(requireAdmin...).Post("/", h.create)
		o.With(requireAdmin...).Patch("/{id}", h.update)
	})
}

type response struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ContactName  *string `json:"contact_name,omitempty"`
	ContactPhone *string `json:"contact_phone,omitempty"`
	ContactEmail *string `json:"contact_email,omitempty"`
}

func toResponse(o *Owner) response {
	return response{
		ID:           o.ID.String(),
		Name:         o.Name,
		ContactName:  o.ContactName,
		ContactPhone: o.ContactPhone,
		ContactEmail: o.ContactEmail,
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	owners, err := h.repo.List(r.Context())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(owners))
	for i := range owners {
		items[i] = toResponse(&owners[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	o, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(o))
}

type createRequest struct {
	Name         string  `json:"name"`
	ContactName  *string `json:"contact_name"`
	ContactPhone *string `json:"contact_phone"`
	ContactEmail *string `json:"contact_email"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := validateName(name); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	o := &Owner{
		Name:         name,
		ContactName:  trimPtr(req.ContactName),
		ContactPhone: trimPtr(req.ContactPhone),
		ContactEmail: trimPtr(req.ContactEmail),
	}
	if err := h.repo.Create(r.Context(), o); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(o))
}

type updateRequest struct {
	Name         *string `json:"name"`
	ContactName  *string `json:"contact_name"`
	ContactPhone *string `json:"contact_phone"`
	ContactEmail *string `json:"contact_email"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	o, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var req updateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if err := validateName(name); err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		o.Name = name
	}
	if req.ContactName != nil {
		o.ContactName = trimPtr(req.ContactName)
	}
	if req.ContactPhone != nil {
		o.ContactPhone = trimPtr(req.ContactPhone)
	}
	if req.ContactEmail != nil {
		o.ContactEmail = trimPtr(req.ContactEmail)
	}

	if err := h.repo.Update(r.Context(), o); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(o))
}

func validateName(name string) error {
	if name == "" || len(name) > 255 {
		return apperror.BadRequest("invalid_name", "name is required (max 255 chars)")
	}
	return nil
}

// trimPtr trims s and returns nil instead of a pointer to an empty string,
// so an explicitly-blanked-out optional field is stored as NULL.
func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
