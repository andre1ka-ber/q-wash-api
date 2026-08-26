package washingpoint

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/reqctx"
)

var timeFormatRegexp = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ScheduleSeeder provisions a newly created washing point's initial
// per-weekday schedule (docs/PLAN_WEB_APPS.md phase 5) so it's bookable
// immediately, without importing internal/schedule here — that package
// already imports this one for its own ownership checks, and Go doesn't
// allow the reverse. Satisfied structurally by *schedule.Manager.
type ScheduleSeeder interface {
	SeedDefault(ctx context.Context, washingPointID uuid.UUID, openTime, closeTime string) error
}

// BoxSeeder provisions a newly created washing point's initial boxes
// (docs/PLAN_WEB_APPS.md phase 6), same reasoning and same
// can't-import-internal/box-here constraint as ScheduleSeeder. Satisfied
// structurally by *box.Manager.
type BoxSeeder interface {
	SeedDefault(ctx context.Context, washingPointID uuid.UUID, count int) error
}

type Handler struct {
	repo           *Repository
	scheduleSeeder ScheduleSeeder
	boxSeeder      BoxSeeder
}

func NewHandler(repo *Repository, scheduleSeeder ScheduleSeeder, boxSeeder BoxSeeder) *Handler {
	return &Handler{repo: repo, scheduleSeeder: scheduleSeeder, boxSeeder: boxSeeder}
}

// RegisterRoutes mounts /washing-points: reads are public, writes require
// requireManage (typically RequireAuth + RequireRole(staff, admin)) applied
// per-method via chi's With(), since chi doesn't allow mounting the same
// path prefix from two separate route groups.
func (h *Handler) RegisterRoutes(r chi.Router, requireManage ...func(http.Handler) http.Handler) {
	r.Route("/washing-points", func(wp chi.Router) {
		wp.Get("/", h.list)
		wp.Get("/{id}", h.get)

		wp.With(requireManage...).Post("/", h.create)
		wp.With(requireManage...).Patch("/{id}", h.update)
		wp.With(requireManage...).Delete("/{id}", h.deactivate)
	})
}

type response struct {
	ID          string   `json:"id"`
	OwnerID     *string  `json:"owner_id,omitempty"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	BoxesCount  int      `json:"boxes_count"`
	OpenTime    string   `json:"open_time"`
	CloseTime   string   `json:"close_time"`
	Status      string   `json:"status"`
	Description *string  `json:"description,omitempty"`
	Amenities   []string `json:"amenities,omitempty"`
}

func toResponse(wp *WashingPoint) response {
	var ownerID *string
	if wp.OwnerID != nil {
		s := wp.OwnerID.String()
		ownerID = &s
	}
	return response{
		ID:          wp.ID.String(),
		OwnerID:     ownerID,
		Name:        wp.Name,
		Address:     wp.Address,
		Latitude:    wp.Latitude,
		Longitude:   wp.Longitude,
		BoxesCount:  wp.BoxesCount,
		OpenTime:    wp.OpenTime,
		CloseTime:   wp.CloseTime,
		Status:      string(wp.Status),
		Description: wp.Description,
		Amenities:   []string(wp.Amenities),
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	points, err := h.repo.List(r.Context())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(points))
	for i, wp := range points {
		items[i] = toResponse(&wp)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	wp, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(wp))
}

type createRequest struct {
	OwnerID     *string  `json:"owner_id"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	BoxesCount  *int     `json:"boxes_count"`
	OpenTime    *string  `json:"open_time"`
	CloseTime   *string  `json:"close_time"`
	Status      *string  `json:"status"`
	Description *string  `json:"description"`
	Amenities   []string `json:"amenities"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}
	// Directly creating a point is admin-only — staff (who are themselves
	// scoped to one existing point) go through the connection-request
	// onboarding flow (connectionrequest.Manager.Approve) instead, which
	// also creates the Owner and leaves the point pending_review.
	if authUser.Role != "admin" {
		httputil.WriteError(w, r, apperror.Forbidden("forbidden", "only admin can create a washing point directly; use the connection-request onboarding flow"))
		return
	}

	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	address := strings.TrimSpace(req.Address)
	boxesCount := 2
	if req.BoxesCount != nil {
		boxesCount = *req.BoxesCount
	}
	openTime := "08:00"
	if req.OpenTime != nil {
		openTime = *req.OpenTime
	}
	closeTime := "20:00"
	if req.CloseTime != nil {
		closeTime = *req.CloseTime
	}

	if err := validate(name, address, req.Latitude, req.Longitude, boxesCount, openTime, closeTime); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	var ownerID *uuid.UUID
	if req.OwnerID != nil {
		parsed, err := uuid.Parse(*req.OwnerID)
		if err != nil {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_owner_id", "owner_id must be a valid uuid"))
			return
		}
		ownerID = &parsed
	}

	status := StatusActive
	if req.Status != nil {
		if !isValidStatus(Status(*req.Status)) {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: active, paused, pending_review"))
			return
		}
		status = Status(*req.Status)
	}

	wp := &WashingPoint{
		OwnerID:     ownerID,
		Name:        name,
		Address:     address,
		Latitude:    req.Latitude,
		Longitude:   req.Longitude,
		BoxesCount:  boxesCount,
		OpenTime:    openTime,
		CloseTime:   closeTime,
		Status:      status,
		Description: req.Description,
		Amenities:   req.Amenities,
	}
	if err := h.repo.Create(r.Context(), wp); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	// Seed a default 7-day schedule (same hours every day, matching the
	// legacy open_time/close_time defaults above) so the point is bookable
	// immediately — GET .../availability treats a washing point with no
	// schedule rows as closed every day, see queue.resolveDaySchedule.
	if err := h.scheduleSeeder.SeedDefault(r.Context(), wp.ID, openTime, closeTime); err != nil {
		httputil.WriteError(w, r, apperror.Internal(err))
		return
	}
	// Same reasoning as the schedule seed above: GET .../boxes should
	// return boxes_count boxes immediately, not zero until someone adds
	// them by hand through the cabinet app.
	if err := h.boxSeeder.SeedDefault(r.Context(), wp.ID, boxesCount); err != nil {
		httputil.WriteError(w, r, apperror.Internal(err))
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(wp))
}

type updateRequest struct {
	OwnerID     *string   `json:"owner_id"`
	Name        *string   `json:"name"`
	Address     *string   `json:"address"`
	Latitude    *float64  `json:"latitude"`
	Longitude   *float64  `json:"longitude"`
	BoxesCount  *int      `json:"boxes_count"`
	OpenTime    *string   `json:"open_time"`
	CloseTime   *string   `json:"close_time"`
	Status      *string   `json:"status"`
	Description *string   `json:"description"`
	Amenities   *[]string `json:"amenities"`
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
	wp, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(wp.ID) {
		httputil.WriteError(w, r, apperror.NotFound("washing_point_not_found", "washing point not found"))
		return
	}

	var req updateRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	if req.Name != nil {
		wp.Name = strings.TrimSpace(*req.Name)
	}
	if req.Address != nil {
		wp.Address = strings.TrimSpace(*req.Address)
	}
	if req.Latitude != nil {
		wp.Latitude = *req.Latitude
	}
	if req.Longitude != nil {
		wp.Longitude = *req.Longitude
	}
	if req.BoxesCount != nil {
		wp.BoxesCount = *req.BoxesCount
	}
	if req.OpenTime != nil {
		wp.OpenTime = *req.OpenTime
	}
	if req.CloseTime != nil {
		wp.CloseTime = *req.CloseTime
	}
	if req.Status != nil {
		if !isValidStatus(Status(*req.Status)) {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: active, paused, pending_review"))
			return
		}
		wp.Status = Status(*req.Status)
	}
	if req.OwnerID != nil {
		if *req.OwnerID == "" {
			wp.OwnerID = nil
		} else {
			parsed, err := uuid.Parse(*req.OwnerID)
			if err != nil {
				httputil.WriteError(w, r, apperror.BadRequest("invalid_owner_id", "owner_id must be a valid uuid"))
				return
			}
			wp.OwnerID = &parsed
		}
	}
	if req.Description != nil {
		wp.Description = req.Description
	}
	if req.Amenities != nil {
		wp.Amenities = *req.Amenities
	}

	if err := validate(wp.Name, wp.Address, wp.Latitude, wp.Longitude, wp.BoxesCount, wp.OpenTime, wp.CloseTime); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	if err := h.repo.Update(r.Context(), wp); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toResponse(wp))
}

func (h *Handler) deactivate(w http.ResponseWriter, r *http.Request) {
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
	wp, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(wp.ID) {
		httputil.WriteError(w, r, apperror.NotFound("washing_point_not_found", "washing point not found"))
		return
	}
	if err := h.repo.Deactivate(r.Context(), id); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func isValidStatus(s Status) bool {
	return s == StatusActive || s == StatusPaused || s == StatusPendingReview
}

func validate(name, address string, lat, lon float64, boxesCount int, openTime, closeTime string) error {
	if name == "" || len(name) > 255 {
		return apperror.BadRequest("invalid_name", "name is required (max 255 chars)")
	}
	if address == "" || len(address) > 500 {
		return apperror.BadRequest("invalid_address", "address is required (max 500 chars)")
	}
	if lat < -90 || lat > 90 {
		return apperror.BadRequest("invalid_latitude", "latitude must be between -90 and 90")
	}
	if lon < -180 || lon > 180 {
		return apperror.BadRequest("invalid_longitude", "longitude must be between -180 and 180")
	}
	if boxesCount < 1 {
		return apperror.BadRequest("invalid_boxes_count", "boxes_count must be at least 1")
	}
	if !timeFormatRegexp.MatchString(openTime) {
		return apperror.BadRequest("invalid_open_time", "open_time must be in HH:MM 24h format")
	}
	if !timeFormatRegexp.MatchString(closeTime) {
		return apperror.BadRequest("invalid_close_time", "close_time must be in HH:MM 24h format")
	}
	if closeTime <= openTime {
		return apperror.BadRequest("invalid_hours", "close_time must be after open_time")
	}
	return nil
}
