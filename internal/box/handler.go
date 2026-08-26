package box

import (
	"net/http"
	"strings"

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

// RegisterRoutes mounts /washing-points/{id}/boxes: list is public (same
// as the washing point itself), create/update/delete require requireManage
// (typically RequireAuth + RequireRole(staff, admin)) applied per-method
// via chi's With(), same pattern as photo.Handler.RegisterRoutes.
func (h *Handler) RegisterRoutes(r chi.Router, requireManage ...func(http.Handler) http.Handler) {
	r.Route("/washing-points/{id}/boxes", func(wp chi.Router) {
		wp.Get("/", h.list)
		wp.With(requireManage...).Post("/", h.create)
		wp.With(requireManage...).Patch("/{boxId}", h.update)
		wp.With(requireManage...).Delete("/{boxId}", h.delete)
	})
}

type response struct {
	ID     string  `json:"id"`
	Number int     `json:"number"`
	Label  *string `json:"label,omitempty"`
	IsOpen bool    `json:"is_open"`
}

func toResponse(b *Box) response {
	return response{ID: b.ID.String(), Number: b.Number, Label: b.Label, IsOpen: b.IsOpen}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	boxes, err := h.repo.ListByWashingPoint(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(boxes))
	for i := range boxes {
		items[i] = toResponse(&boxes[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createRequest struct {
	Label *string `json:"label"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(washingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("washing_point_not_found", "washing point not found"))
		return
	}

	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	label, err := normalizeLabel(req.Label)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	b, err := h.manager.Create(r.Context(), washingPointID, label)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(b))
}

type updateRequest struct {
	Label  *string `json:"label"`
	IsOpen *bool   `json:"is_open"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}
	b, err := h.findOwned(r, authUser)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var req updateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if req.Label != nil {
		label, err := normalizeLabel(req.Label)
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		b.Label = label
	}
	if req.IsOpen != nil {
		b.IsOpen = *req.IsOpen
	}
	if err := h.repo.Update(r.Context(), b); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(b))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}
	b, err := h.findOwned(r, authUser)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if err := h.repo.Delete(r.Context(), b.ID); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// findOwned resolves both path params ({id}, the washing point, and
// {boxId}) and verifies authUser may act on that washing point, then that
// the box actually belongs to it — same IDOR reasoning as
// photo.Handler.findOwned.
func (h *Handler) findOwned(r *http.Request, authUser reqctx.AuthUser) (*Box, error) {
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		return nil, err
	}
	boxID, err := httputil.ParseUUIDParam(r, "boxId")
	if err != nil {
		return nil, err
	}
	if !authUser.OwnsWashingPoint(washingPointID) {
		return nil, apperror.NotFound("box_not_found", "box not found")
	}
	b, err := h.repo.FindByID(r.Context(), boxID)
	if err != nil {
		return nil, err
	}
	if b.WashingPointID != washingPointID {
		return nil, apperror.NotFound("box_not_found", "box not found")
	}
	return b, nil
}

func normalizeLabel(label *string) (*string, error) {
	if label == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*label)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > 255 {
		return nil, apperror.BadRequest("invalid_label", "label must be at most 255 characters")
	}
	return &trimmed, nil
}
