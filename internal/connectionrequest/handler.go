package connectionrequest

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/reqctx"
)

type Handler struct {
	repo    *Repository
	manager *Manager
}

func NewHandler(repo *Repository, manager *Manager) *Handler {
	return &Handler{repo: repo, manager: manager}
}

// RegisterRoutes mounts /connection-requests, admin-only throughout — this
// is the admin app's onboarding queue, not a staff-facing surface.
func (h *Handler) RegisterRoutes(r chi.Router, requireAdmin ...func(http.Handler) http.Handler) {
	r.Route("/connection-requests", func(cr chi.Router) {
		cr.With(requireAdmin...).Get("/", h.list)
		cr.With(requireAdmin...).Get("/{id}", h.get)
		cr.With(requireAdmin...).Post("/", h.create)
		cr.With(requireAdmin...).Patch("/{id}", h.updateStatus)
	})
}

type response struct {
	ID           string     `json:"id"`
	BusinessName string     `json:"business_name"`
	ContactName  string     `json:"contact_name"`
	ContactPhone string     `json:"contact_phone"`
	Address      string     `json:"address"`
	BoxesCount   int        `json:"boxes_count"`
	Note         *string    `json:"note,omitempty"`
	Status       string     `json:"status"`
	ReviewedBy   *string    `json:"reviewed_by,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func toResponse(c *ConnectionRequest) response {
	var reviewedBy *string
	if c.ReviewedBy != nil {
		s := c.ReviewedBy.String()
		reviewedBy = &s
	}
	return response{
		ID:           c.ID.String(),
		BusinessName: c.BusinessName,
		ContactName:  c.ContactName,
		ContactPhone: c.ContactPhone,
		Address:      c.Address,
		BoxesCount:   c.BoxesCount,
		Note:         c.Note,
		Status:       string(c.Status),
		ReviewedBy:   reviewedBy,
		ReviewedAt:   c.ReviewedAt,
		CreatedAt:    c.CreatedAt,
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	var status *Status
	if raw := r.URL.Query().Get("status"); raw != "" {
		st := Status(raw)
		if st != StatusNew && st != StatusApproved && st != StatusRejected {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: new, approved, rejected"))
			return
		}
		status = &st
	}

	items, err := h.repo.List(r.Context(), status)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	resp := make([]response, len(items))
	for i := range items {
		resp[i] = toResponse(&items[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": resp})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	c, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(c))
}

type createRequest struct {
	BusinessName string  `json:"business_name"`
	ContactName  string  `json:"contact_name"`
	ContactPhone string  `json:"contact_phone"`
	Address      string  `json:"address"`
	BoxesCount   int     `json:"boxes_count"`
	Note         *string `json:"note"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	businessName := strings.TrimSpace(req.BusinessName)
	contactName := strings.TrimSpace(req.ContactName)
	contactPhone := strings.TrimSpace(req.ContactPhone)
	address := strings.TrimSpace(req.Address)

	if err := validate(businessName, contactName, contactPhone, address, req.BoxesCount); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var note *string
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		if len(trimmed) > 2000 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_note", "note is too long (max 2000 chars)"))
			return
		}
		if trimmed != "" {
			note = &trimmed
		}
	}

	c := &ConnectionRequest{
		BusinessName: businessName,
		ContactName:  contactName,
		ContactPhone: contactPhone,
		Address:      address,
		BoxesCount:   req.BoxesCount,
		Note:         note,
		Status:       StatusNew,
	}
	if err := h.repo.Create(r.Context(), c); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(c))
}

type updateStatusRequest struct {
	Status string `json:"status"`
}

// updateStatus is the only write on an existing request: approve or
// reject. There's no endpoint to edit the submitted contact fields.
func (h *Handler) updateStatus(w http.ResponseWriter, r *http.Request) {
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

	var req updateStatusRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	switch Status(req.Status) {
	case StatusApproved:
		c, _, err := h.manager.Approve(r.Context(), id, authUser.ID)
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, toResponse(c))
	case StatusRejected:
		c, err := h.manager.Reject(r.Context(), id, authUser.ID)
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, toResponse(c))
	default:
		httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: approved, rejected"))
	}
}

func validate(businessName, contactName, contactPhone, address string, boxesCount int) error {
	if businessName == "" || len(businessName) > 255 {
		return apperror.BadRequest("invalid_business_name", "business_name is required (max 255 chars)")
	}
	if contactName == "" || len(contactName) > 255 {
		return apperror.BadRequest("invalid_contact_name", "contact_name is required (max 255 chars)")
	}
	if contactPhone == "" || len(contactPhone) > 32 {
		return apperror.BadRequest("invalid_contact_phone", "contact_phone is required (max 32 chars)")
	}
	if address == "" || len(address) > 500 {
		return apperror.BadRequest("invalid_address", "address is required (max 500 chars)")
	}
	if boxesCount < 1 {
		return apperror.BadRequest("invalid_boxes_count", "boxes_count must be at least 1")
	}
	return nil
}
