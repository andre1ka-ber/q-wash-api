package schedule

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/washingpoint"
)

type Handler struct {
	repo    *Repository
	manager *Manager
	wpRepo  *washingpoint.Repository
}

func NewHandler(repo *Repository, manager *Manager, wpRepo *washingpoint.Repository) *Handler {
	return &Handler{repo: repo, manager: manager, wpRepo: wpRepo}
}

// RegisterRoutes mounts /washing-points/{id}/schedule: GET is public (same
// as the washing point itself and its availability); PUT (bulk-replace the
// full week) requires requireManage (typically RequireAuth +
// RequireRole(staff, admin)).
func (h *Handler) RegisterRoutes(r chi.Router, requireManage ...func(http.Handler) http.Handler) {
	r.Route("/washing-points/{id}/schedule", func(wp chi.Router) {
		wp.Get("/", h.list)
		wp.With(requireManage...).Put("/", h.replace)
	})
}

type rowResponse struct {
	Weekday    int     `json:"weekday"`
	IsOpen     bool    `json:"is_open"`
	OpenTime   *string `json:"open_time,omitempty"`
	CloseTime  *string `json:"close_time,omitempty"`
	BreakStart *string `json:"break_start,omitempty"`
	BreakEnd   *string `json:"break_end,omitempty"`
}

func toResponse(s *WashingPointSchedule) rowResponse {
	return rowResponse{
		Weekday:    s.Weekday,
		IsOpen:     s.IsOpen,
		OpenTime:   s.OpenTime,
		CloseTime:  s.CloseTime,
		BreakStart: s.BreakStart,
		BreakEnd:   s.BreakEnd,
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	rows, err := h.repo.ListByWashingPoint(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]rowResponse, len(rows))
	for i := range rows {
		items[i] = toResponse(&rows[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type rowRequest struct {
	Weekday    int     `json:"weekday"`
	IsOpen     bool    `json:"is_open"`
	OpenTime   *string `json:"open_time"`
	CloseTime  *string `json:"close_time"`
	BreakStart *string `json:"break_start"`
	BreakEnd   *string `json:"break_end"`
}

type replaceRequest struct {
	Items []rowRequest `json:"items"`
}

func (h *Handler) replace(w http.ResponseWriter, r *http.Request) {
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
	if _, err := h.wpRepo.FindByID(r.Context(), washingPointID); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(washingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("washing_point_not_found", "washing point not found"))
		return
	}

	var req replaceRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	inputs := make([]Input, len(req.Items))
	for i, it := range req.Items {
		inputs[i] = Input{
			Weekday:    it.Weekday,
			IsOpen:     it.IsOpen,
			OpenTime:   it.OpenTime,
			CloseTime:  it.CloseTime,
			BreakStart: it.BreakStart,
			BreakEnd:   it.BreakEnd,
		}
	}

	rows, err := h.manager.Replace(r.Context(), washingPointID, inputs)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]rowResponse, len(rows))
	for i := range rows {
		items[i] = toResponse(&rows[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
