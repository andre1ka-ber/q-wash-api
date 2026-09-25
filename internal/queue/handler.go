package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata" // embed the IANA database so LoadLocation works regardless of the host's zoneinfo files

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/box"
	"q-wash-api/internal/car"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/eventbus"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/schedule"
	"q-wash-api/internal/service"
	"q-wash-api/internal/user"
	"q-wash-api/internal/washingpoint"
)

// businessLocation is the fixed timezone washing-point open_time/close_time
// "HH:MM" strings are interpreted in. Washing points have no per-point
// timezone field yet (single-market deployment — see docs/DATA_MODEL.md);
// this app currently only serves Tajikistan (+992 numbers), which has one
// fixed UTC+5 offset with no DST, so a single constant is correct today.
var businessLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Dushanbe")
	if err != nil {
		panic("businessLocation: failed to load Asia/Dushanbe: " + err.Error())
	}
	return loc
}()

// BusinessLocation exposes businessLocation to other packages (currently
// internal/admin's today-aggregates) that need the same fixed timezone
// without duplicating the constant.
func BusinessLocation() *time.Location {
	return businessLocation
}

type Handler struct {
	repo         *Repository
	manager      *Manager
	wpRepo       *washingpoint.Repository
	serviceRepo  *service.Repository
	userRepo     *user.Repository
	carRepo      *car.Repository
	scheduleRepo *schedule.Repository
	boxRepo      *box.Repository
	bus          *eventbus.Bus
}

func NewHandler(
	repo *Repository,
	manager *Manager,
	wpRepo *washingpoint.Repository,
	serviceRepo *service.Repository,
	userRepo *user.Repository,
	carRepo *car.Repository,
	scheduleRepo *schedule.Repository,
	boxRepo *box.Repository,
	bus *eventbus.Bus,
) *Handler {
	return &Handler{repo: repo, manager: manager, wpRepo: wpRepo, serviceRepo: serviceRepo, userRepo: userRepo, carRepo: carRepo, scheduleRepo: scheduleRepo, boxRepo: boxRepo, bus: bus}
}

// RegisterRoutes mounts availability (public), booking create/get/cancel
// (any authenticated user, ownership enforced internally), the staff-only
// network-wide board + notification-adjacent surface (requireStaff:
// staff/admin), the per-point live board/status/pause/resume/live-boxes
// surface (requireQueueOps: staff/worker/admin, docs/PLAN_WEB_APPS.md
// phase 7 — worker needs these but not the staff-only ones), and the
// lobby-display board summary (requireStaff, phase 8 — the display kiosk
// logs in as staff/admin, not a new role; worker doesn't need this one).
// Everything for a given path prefix is registered in one Route() call
// with per-method middleware via chi's With(...) — see the note in
// washingpoint.Handler.RegisterRoutes for why (chi panics if the same
// prefix is Mount()ed from two separate calls).
func (h *Handler) RegisterRoutes(r chi.Router, requireAuth func(http.Handler) http.Handler, requireStaff, requireQueueOps []func(http.Handler) http.Handler) {
	r.Route("/washing-points/{id}/availability", func(av chi.Router) {
		av.Get("/", h.availability)
	})

	r.Route("/washing-points/{id}/queue", func(board chi.Router) {
		board.With(requireQueueOps...).Get("/", h.listByWashingPoint)
	})

	r.Route("/washing-points/{id}/boxes/live", func(live chi.Router) {
		live.With(requireQueueOps...).Get("/", h.boxesLive)
	})

	r.Route("/washing-points/{id}/board", func(brd chi.Router) {
		brd.With(requireStaff...).Get("/", h.board)
		brd.With(requireStaff...).Get("/events", h.boardEvents)
	})

	r.Route("/washing-points/{id}/reports", func(rep chi.Router) {
		rep.With(requireStaff...).Get("/", h.reports)
	})

	r.Route("/queue", func(q chi.Router) {
		q.With(requireStaff...).Get("/", h.list)
		q.With(requireAuth).Post("/", h.create)
		q.With(requireAuth).Get("/{id}", h.get)
		q.With(requireAuth).Get("/{id}/events", h.events)
		q.With(requireAuth).Patch("/{id}/cancel", h.cancel)
		q.With(requireQueueOps...).Patch("/{id}/status", h.updateStatus)
		q.With(requireQueueOps...).Patch("/{id}/pause", h.pause)
		q.With(requireQueueOps...).Patch("/{id}/resume", h.resume)
	})

	r.Route("/me/queue", func(history chi.Router) {
		history.With(requireAuth).Get("/", h.history)
	})
}

type slotResponse struct {
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	AvailableBoxes []int     `json:"available_boxes"`
}

func (h *Handler) availability(w http.ResponseWriter, r *http.Request) {
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	serviceID, err := uuid.Parse(r.URL.Query().Get("service_id"))
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_service_id", "service_id is required and must be a valid uuid"))
		return
	}

	day, err := time.Parse("2006-01-02", r.URL.Query().Get("date"))
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_date", "date is required in YYYY-MM-DD format"))
		return
	}

	wp, err := h.wpRepo.FindByID(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	svc, err := h.serviceRepo.FindByID(r.Context(), serviceID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if svc.WashingPointID != washingPointID {
		httputil.WriteError(w, r, apperror.BadRequest("service_not_at_washing_point", "service does not belong to this washing point"))
		return
	}

	weekday := weekdayIndex(day.In(businessLocation))
	scheduleRow, err := h.scheduleRepo.FindByWeekday(r.Context(), washingPointID, weekday)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	daySchedule, err := resolveDaySchedule(day, scheduleRow)
	if err != nil {
		httputil.WriteError(w, r, apperror.Internal(err))
		return
	}

	windows := daySchedule.Windows()
	if len(windows) == 0 {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": []slotResponse{}})
		return
	}

	busyRows, err := h.repo.FindActiveBookingsInRange(r.Context(), washingPointID, daySchedule.Open, daySchedule.Close)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	busy := make([]BusyBoxInterval, len(busyRows))
	for i, row := range busyRows {
		busy[i] = BusyBoxInterval{Box: row.BoxNumber, Start: row.ScheduledStartAt, End: row.ScheduledEndAt}
	}

	// A closed box (docs/PLAN_WEB_APPS.md phase 6) is layered on top of the
	// sweep-line algorithm as a synthetic all-day "booking" spanning the
	// whole operating window, rather than changing ComputeAvailableSlotsForDay
	// itself — it already treats an occupied box as unavailable for any
	// candidate window overlapping the occupied interval, which is exactly
	// what "closed all day" means. Missing box rows (e.g. an older point
	// never backfilled) fail open — nothing is added for them.
	boxes, err := h.boxRepo.ListByWashingPoint(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	for _, b := range boxes {
		if !b.IsOpen {
			busy = append(busy, BusyBoxInterval{Box: b.Number, Start: daySchedule.Open, End: daySchedule.Close})
		}
	}

	duration := time.Duration(svc.DurationMinutes) * time.Minute
	slots := ComputeAvailableSlotsForDay(daySchedule, wp.BoxesCount, duration, busy)

	items := make([]slotResponse, len(slots))
	for i, s := range slots {
		items[i] = slotResponse{Start: s.Start, End: s.End, AvailableBoxes: s.AvailableBoxes}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// combineDayAndTime combines a calendar day with an "HH:MM" time-of-day
// string (as stored on WashingPoint) into an instant in businessLocation —
// e.g. "08:00" means 08:00 Asia/Dushanbe local time, not 08:00 UTC. The day
// is first converted into businessLocation before its Y/M/D are read, so a
// UTC instant near local midnight still resolves to the intended local day.
func combineDayAndTime(day time.Time, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	local := day.In(businessLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, businessLocation), nil
}

// weekdayIndex converts t to schedule.WashingPointSchedule's weekday
// convention (0=Monday..6=Sunday) — Go's time.Weekday uses 0=Sunday..
// 6=Saturday.
func weekdayIndex(t time.Time) int {
	return (int(t.Weekday()) + 6) % 7
}

// resolveDaySchedule turns row (the schedule for day's weekday, or nil if
// the washing point has none yet) into a DaySchedule anchored to real
// businessLocation instants via combineDayAndTime. A nil row, a closed
// row, or a row missing open/close times all resolve to "closed" — a
// missing schedule should never silently fall back to some assumed set of
// hours.
func resolveDaySchedule(day time.Time, row *schedule.WashingPointSchedule) (DaySchedule, error) {
	if row == nil || !row.IsOpen || row.OpenTime == nil || row.CloseTime == nil {
		return DaySchedule{IsOpen: false}, nil
	}

	open, err := combineDayAndTime(day, *row.OpenTime)
	if err != nil {
		return DaySchedule{}, err
	}
	close_, err := combineDayAndTime(day, *row.CloseTime)
	if err != nil {
		return DaySchedule{}, err
	}
	ds := DaySchedule{IsOpen: true, Open: open, Close: close_}

	if row.BreakStart != nil && row.BreakEnd != nil {
		breakStart, err := combineDayAndTime(day, *row.BreakStart)
		if err != nil {
			return DaySchedule{}, err
		}
		breakEnd, err := combineDayAndTime(day, *row.BreakEnd)
		if err != nil {
			return DaySchedule{}, err
		}
		ds.BreakStart = &breakStart
		ds.BreakEnd = &breakEnd
	}
	return ds, nil
}

type bookingResponse struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	UserID           string     `json:"user_id"`
	CarID            string     `json:"car_id"`
	ServiceID        string     `json:"service_id"`
	PriceOptionID    string     `json:"price_option_id"`
	WashingPointID   string     `json:"washing_point_id"`
	BoxNumber        int        `json:"box_number"`
	ScheduledStartAt time.Time  `json:"scheduled_start_at"`
	ScheduledEndAt   time.Time  `json:"scheduled_end_at"`
	Notes            *string    `json:"notes,omitempty"`
	CanceledAt       *time.Time `json:"canceled_at,omitempty"`
	PausedAt         *time.Time `json:"paused_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	CarsAhead        int        `json:"cars_ahead"`
}

func toBookingResponse(q *Queue) bookingResponse {
	return bookingResponse{
		ID:               q.ID.String(),
		Status:           string(q.Status),
		UserID:           q.UserID.String(),
		CarID:            q.CarID.String(),
		ServiceID:        q.ServiceID.String(),
		PriceOptionID:    q.PriceOptionID.String(),
		WashingPointID:   q.WashingPointID.String(),
		BoxNumber:        q.BoxNumber,
		ScheduledStartAt: q.ScheduledStartAt,
		ScheduledEndAt:   q.ScheduledEndAt,
		Notes:            q.Notes,
		CanceledAt:       q.CanceledAt,
		PausedAt:         q.PausedAt,
		CreatedAt:        q.CreatedAt,
	}
}

// activeStatuses are the statuses for which "cars ahead" is a meaningful,
// still-changing number; ready/canceled bookings are done moving through
// the queue so it's left at zero.
var activeStatuses = map[Status]bool{StatusQueue: true, StatusWaiting: true, StatusWashing: true}

// withPosition fills in CarsAhead on top of toBookingResponse, querying it
// only for bookings still active in the queue.
func (h *Handler) withPosition(ctx context.Context, q *Queue) (bookingResponse, error) {
	resp := toBookingResponse(q)
	if !activeStatuses[q.Status] {
		return resp, nil
	}
	ahead, err := h.repo.CountActiveAhead(ctx, q.WashingPointID, q.ScheduledStartAt)
	if err != nil {
		return bookingResponse{}, err
	}
	resp.CarsAhead = ahead
	return resp, nil
}

type createBookingRequest struct {
	CarID            string  `json:"car_id"`
	ServiceID        string  `json:"service_id"`
	PriceOptionID    string  `json:"price_option_id"`
	BoxNumber        int     `json:"box_number"`
	ScheduledStartAt string  `json:"scheduled_start_at"`
	Notes            *string `json:"notes"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	var req createBookingRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	carID, err := uuid.Parse(req.CarID)
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_car_id", "car_id must be a valid uuid"))
		return
	}
	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_service_id", "service_id must be a valid uuid"))
		return
	}
	priceOptionID, err := uuid.Parse(req.PriceOptionID)
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_price_option_id", "price_option_id must be a valid uuid"))
		return
	}
	scheduledStartAt, err := time.Parse(time.RFC3339, req.ScheduledStartAt)
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_scheduled_start_at", "scheduled_start_at must be an RFC3339 timestamp"))
		return
	}

	var notes *string
	if req.Notes != nil {
		trimmed := strings.TrimSpace(*req.Notes)
		if len(trimmed) > 2000 {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_notes", "notes is too long (max 2000 chars)"))
			return
		}
		if trimmed != "" {
			notes = &trimmed
		}
	}

	q, err := h.manager.CreateBooking(r.Context(), CreateBookingInput{
		UserID:           authUser.ID,
		CarID:            carID,
		ServiceID:        serviceID,
		PriceOptionID:    priceOptionID,
		BoxNumber:        req.BoxNumber,
		ScheduledStartAt: scheduledStartAt,
		Notes:            notes,
	})
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	resp, err := h.withPosition(r.Context(), q)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, resp)
}

// get returns a booking to its owner or to staff/admin; anyone else gets a
// 404, matching the ownership-hiding pattern used elsewhere (see car.Handler).
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
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

	q, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if q.UserID != authUser.ID && !authUser.OwnsWashingPoint(q.WashingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("queue_not_found", "queue entry not found"))
		return
	}
	resp, err := h.withPosition(r.Context(), q)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// cancel is reachable by the booking's own owner (customer) or by
// staff/worker/admin at its washing point (broadened per
// docs/PLAN_WEB_APPS.md phase 7 — the worker app's "Снять" no-show
// action) — same ownership-hiding 404 pattern as get/updateStatus above.
func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
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

	existing, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if existing.UserID != authUser.ID && !authUser.OwnsWashingPoint(existing.WashingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("queue_not_found", "queue entry not found"))
		return
	}

	q, err := h.manager.CancelBooking(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	resp, err := h.withPosition(r.Context(), q)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// pause and resume are staff/worker/admin-only (requireQueueOps), scoped
// to the booking's own washing point — same ownership check as
// updateStatus.
func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	h.togglePause(w, r, h.manager.Pause)
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	h.togglePause(w, r, h.manager.Resume)
}

func (h *Handler) togglePause(w http.ResponseWriter, r *http.Request, action func(context.Context, uuid.UUID) (*Queue, error)) {
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
	existing, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(existing.WashingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("queue_not_found", "queue entry not found"))
		return
	}

	q, err := action(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toBookingResponse(q))
}

type updateStatusRequest struct {
	Status string `json:"status"`
}

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
	existing, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if !authUser.OwnsWashingPoint(existing.WashingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("queue_not_found", "queue entry not found"))
		return
	}

	var req updateStatusRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	newStatus := Status(req.Status)
	if newStatus != StatusWaiting && newStatus != StatusWashing && newStatus != StatusReady {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_status", "status must be one of: waiting, washing, ready"))
		return
	}

	q, err := h.manager.UpdateStatus(r.Context(), id, newStatus)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toBookingResponse(q))
}

type boardItemResponse struct {
	ID                 string     `json:"id"`
	Status             string     `json:"status"`
	BoxNumber          int        `json:"box_number"`
	ScheduledStartAt   time.Time  `json:"scheduled_start_at"`
	ScheduledEndAt     time.Time  `json:"scheduled_end_at"`
	PausedAt           *time.Time `json:"paused_at,omitempty"`
	CustomerPhoneLast4 string     `json:"customer_phone_last4"`
	CarName            string     `json:"car_name,omitempty"`
}

// list is the network-wide counterpart to listByWashingPoint: admin may
// optionally scope it via ?washing_point_id=, or omit the param for every
// point at once; staff/worker are always forced to their own
// authUser.WashingPointID regardless of the query param, matching the
// OwnsWashingPoint pattern used everywhere else (see reqctx.AuthUser).
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	var washingPointID *uuid.UUID
	if authUser.Role == "admin" {
		if raw := r.URL.Query().Get("washing_point_id"); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				httputil.WriteError(w, r, apperror.BadRequest("invalid_washing_point_id", "washing_point_id must be a valid uuid"))
				return
			}
			washingPointID = &id
		}
	} else if authUser.WashingPointID != nil {
		washingPointID = authUser.WashingPointID
	} else {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": []boardItemResponse{}})
		return
	}

	rows, err := h.repo.ListLive(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	items, err := h.toBoardItems(r.Context(), rows)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// listByWashingPoint is the worker/cabinet "today's queue" board
// (docs/PLAN_WEB_APPS.md phase 7) — scoped to bookings scheduled on one
// calendar day in businessLocation, defaulting to today when ?date= is
// omitted. Before phase 7 this returned every live booking regardless of
// date; no shipped app called this endpoint yet (confirmed by searching
// q-wash-admin/q-wash-cabinet's use of q-wash-shared's API client), so
// narrowing the default here doesn't regress anything already built.
func (h *Handler) listByWashingPoint(w http.ResponseWriter, r *http.Request) {
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

	day := time.Now()
	if raw := r.URL.Query().Get("date"); raw != "" {
		day, err = time.Parse("2006-01-02", raw)
		if err != nil {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_date", "date must be in YYYY-MM-DD format"))
			return
		}
	}
	dayStart, dayEnd := dayBounds(day)

	rows, err := h.repo.ListLiveByWashingPointAndDate(r.Context(), washingPointID, dayStart, dayEnd)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	items, err := h.toBoardItems(r.Context(), rows)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// dayBounds returns [start, end) for day's calendar date in
// businessLocation — midnight to the next midnight, local time.
func dayBounds(day time.Time) (time.Time, time.Time) {
	local := day.In(businessLocation)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, businessLocation)
	return start, start.AddDate(0, 0, 1)
}

// toBoardItems enriches raw queue rows with the customer's car name and the
// last 4 digits of their phone number — enough for staff at the counter to
// identify who's up, without the board (a shared/public-facing screen)
// showing a full phone number. Users/cars are batch-fetched rather than
// queried per row.
func (h *Handler) toBoardItems(ctx context.Context, rows []Queue) ([]boardItemResponse, error) {
	userIDs := make([]uuid.UUID, 0, len(rows))
	carIDs := make([]uuid.UUID, 0, len(rows))
	seenUser := make(map[uuid.UUID]bool, len(rows))
	seenCar := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		if !seenUser[row.UserID] {
			seenUser[row.UserID] = true
			userIDs = append(userIDs, row.UserID)
		}
		if !seenCar[row.CarID] {
			seenCar[row.CarID] = true
			carIDs = append(carIDs, row.CarID)
		}
	}

	users, err := h.userRepo.FindByIDs(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	cars, err := h.carRepo.FindByIDs(ctx, carIDs)
	if err != nil {
		return nil, err
	}

	phoneByUser := make(map[uuid.UUID]string, len(users))
	for _, u := range users {
		phoneByUser[u.ID] = u.PhoneNumber
	}
	nameByCar := make(map[uuid.UUID]string, len(cars))
	for _, c := range cars {
		nameByCar[c.ID] = c.Name
	}

	items := make([]boardItemResponse, len(rows))
	for i, row := range rows {
		items[i] = boardItemResponse{
			ID:                 row.ID.String(),
			Status:             string(row.Status),
			BoxNumber:          row.BoxNumber,
			ScheduledStartAt:   row.ScheduledStartAt,
			ScheduledEndAt:     row.ScheduledEndAt,
			PausedAt:           row.PausedAt,
			CustomerPhoneLast4: lastNDigits(phoneByUser[row.UserID], 4),
			CarName:            nameByCar[row.CarID],
		}
	}
	return items, nil
}

// boxesLive is the worker app's box-cards screen (docs/PLAN_WEB_APPS.md
// phase 7): each of the point's boxes joined with whatever booking is
// currently occupying it (status=washing, "current") or, if free, the
// earliest still-upcoming one assigned to that box number ("next"). Lives
// on queue.Handler rather than box.Handler because it needs queue's
// booking-enrichment helpers (batch phone/car/service lookups) and
// queue already imports box (for the closed-box check, phase 6) — the
// reverse import would cycle. Registered as a literal path under the same
// router as box.Handler's own /boxes routes; chi resolves the static
// "live" segment ahead of box.Handler's "/{boxId}" wildcard, so the two
// don't conflict despite sharing a URL prefix.
func (h *Handler) boxesLive(w http.ResponseWriter, r *http.Request) {
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

	boxes, err := h.boxRepo.ListByWashingPoint(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	// Deliberately the unfiltered ListLiveByWashingPoint, not
	// ListLiveByWashingPointAndDate: a box currently washing a booking that
	// started yesterday evening, or a box whose next booking is tomorrow
	// morning with nothing queued today, both still need to show correctly
	// here — this endpoint answers "what's happening right now", not
	// "what's on today's calendar" (that's listByWashingPoint's job).
	rows, err := h.repo.ListLiveByWashingPoint(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	items, err := h.toLiveBoxItems(r.Context(), boxes, rows)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type liveBoxBookingResponse struct {
	ID                 string     `json:"id"`
	Status             string     `json:"status"`
	ServiceName        string     `json:"service_name,omitempty"`
	ScheduledStartAt   time.Time  `json:"scheduled_start_at"`
	ScheduledEndAt     time.Time  `json:"scheduled_end_at"`
	PausedAt           *time.Time `json:"paused_at,omitempty"`
	CustomerPhoneLast4 string     `json:"customer_phone_last4"`
	CarName            string     `json:"car_name,omitempty"`
}

type liveBoxResponse struct {
	Number  int                     `json:"number"`
	Label   *string                 `json:"label,omitempty"`
	IsOpen  bool                    `json:"is_open"`
	Current *liveBoxBookingResponse `json:"current,omitempty"`
	Next    *liveBoxBookingResponse `json:"next,omitempty"`
}

// toLiveBoxItems joins boxes with rows (already ordered by
// scheduled_start_at, queue/waiting/washing only — see ListLiveByWashingPoint)
// into one item per box: the washing row occupying its number becomes
// "current"; otherwise the first queue/waiting row for that number
// (rows' existing order makes "first seen" the earliest) becomes "next".
func (h *Handler) toLiveBoxItems(ctx context.Context, boxes []box.Box, rows []Queue) ([]liveBoxResponse, error) {
	userIDs := make([]uuid.UUID, 0, len(rows))
	carIDs := make([]uuid.UUID, 0, len(rows))
	serviceIDs := make([]uuid.UUID, 0, len(rows))
	seenUser := make(map[uuid.UUID]bool, len(rows))
	seenCar := make(map[uuid.UUID]bool, len(rows))
	seenService := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		if !seenUser[row.UserID] {
			seenUser[row.UserID] = true
			userIDs = append(userIDs, row.UserID)
		}
		if !seenCar[row.CarID] {
			seenCar[row.CarID] = true
			carIDs = append(carIDs, row.CarID)
		}
		if !seenService[row.ServiceID] {
			seenService[row.ServiceID] = true
			serviceIDs = append(serviceIDs, row.ServiceID)
		}
	}

	users, err := h.userRepo.FindByIDs(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	cars, err := h.carRepo.FindByIDs(ctx, carIDs)
	if err != nil {
		return nil, err
	}
	services, err := h.serviceRepo.FindByIDs(ctx, serviceIDs)
	if err != nil {
		return nil, err
	}

	phoneByUser := make(map[uuid.UUID]string, len(users))
	for _, u := range users {
		phoneByUser[u.ID] = u.PhoneNumber
	}
	nameByCar := make(map[uuid.UUID]string, len(cars))
	for _, c := range cars {
		nameByCar[c.ID] = c.Name
	}
	nameByService := make(map[uuid.UUID]string, len(services))
	for _, s := range services {
		nameByService[s.ID] = s.Name
	}

	enrich := func(row Queue) *liveBoxBookingResponse {
		return &liveBoxBookingResponse{
			ID:                 row.ID.String(),
			Status:             string(row.Status),
			ServiceName:        nameByService[row.ServiceID],
			ScheduledStartAt:   row.ScheduledStartAt,
			ScheduledEndAt:     row.ScheduledEndAt,
			PausedAt:           row.PausedAt,
			CustomerPhoneLast4: lastNDigits(phoneByUser[row.UserID], 4),
			CarName:            nameByCar[row.CarID],
		}
	}

	currentByBox := make(map[int]Queue, len(boxes))
	nextByBox := make(map[int]Queue, len(boxes))
	for _, row := range rows {
		if row.Status == StatusWashing {
			currentByBox[row.BoxNumber] = row
			continue
		}
		if _, ok := nextByBox[row.BoxNumber]; !ok {
			nextByBox[row.BoxNumber] = row
		}
	}

	items := make([]liveBoxResponse, len(boxes))
	for i, b := range boxes {
		item := liveBoxResponse{Number: b.Number, Label: b.Label, IsOpen: b.IsOpen}
		if cur, ok := currentByBox[b.Number]; ok {
			item.Current = enrich(cur)
		} else if next, ok := nextByBox[b.Number]; ok {
			item.Next = enrich(next)
		}
		items[i] = item
	}
	return items, nil
}

// board is the lobby-display screen's one summary endpoint
// (docs/PLAN_WEB_APPS.md phase 8): boxes joined with whatever they're
// currently washing (nil "current" = free), plus a flat, time-ordered
// waiting list across every box for the rest of today. No ticket-number
// field exists anywhere in the data model (see PLAN_WEB_APPS.md's "Open
// assumptions" — a real one would need a per-day sequence column plus a
// matching change in the customer-facing q-wash app, out of scope for
// this endpoint), so customer identity here is car name + last-4 phone
// digits, same convention as the network board/live-boxes endpoints
// above — decided with the user for this endpoint specifically, even
// though this screen faces a room of other customers, not just staff.
func (h *Handler) board(w http.ResponseWriter, r *http.Request) {
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

	resp, err := h.buildBoardResponse(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// buildBoardResponse is board's query/enrichment logic, factored out so
// boardEvents can rebuild the same snapshot on every push without
// duplicating it — the auth/ownership check stays in each caller since it
// only needs to happen once per connection, not once per snapshot.
func (h *Handler) buildBoardResponse(ctx context.Context, washingPointID uuid.UUID) (boardResponse, error) {
	boxes, err := h.boxRepo.ListByWashingPoint(ctx, washingPointID)
	if err != nil {
		return boardResponse{}, err
	}

	// Today only, businessLocation-bounded — unlike boxesLive's deliberately
	// unfiltered query, a lobby TV showing a booking from next week in its
	// waiting list would just be noise for whoever's standing in front of it.
	dayStart, dayEnd := dayBounds(time.Now())
	rows, err := h.repo.ListLiveByWashingPointAndDate(ctx, washingPointID, dayStart, dayEnd)
	if err != nil {
		return boardResponse{}, err
	}

	userIDs := make([]uuid.UUID, 0, len(rows))
	carIDs := make([]uuid.UUID, 0, len(rows))
	serviceIDs := make([]uuid.UUID, 0, len(rows))
	seenUser := make(map[uuid.UUID]bool, len(rows))
	seenCar := make(map[uuid.UUID]bool, len(rows))
	seenService := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		if !seenUser[row.UserID] {
			seenUser[row.UserID] = true
			userIDs = append(userIDs, row.UserID)
		}
		if !seenCar[row.CarID] {
			seenCar[row.CarID] = true
			carIDs = append(carIDs, row.CarID)
		}
		if !seenService[row.ServiceID] {
			seenService[row.ServiceID] = true
			serviceIDs = append(serviceIDs, row.ServiceID)
		}
	}
	users, err := h.userRepo.FindByIDs(ctx, userIDs)
	if err != nil {
		return boardResponse{}, err
	}
	cars, err := h.carRepo.FindByIDs(ctx, carIDs)
	if err != nil {
		return boardResponse{}, err
	}
	services, err := h.serviceRepo.FindByIDs(ctx, serviceIDs)
	if err != nil {
		return boardResponse{}, err
	}
	phoneByUser := make(map[uuid.UUID]string, len(users))
	for _, u := range users {
		phoneByUser[u.ID] = u.PhoneNumber
	}
	nameByCar := make(map[uuid.UUID]string, len(cars))
	for _, c := range cars {
		nameByCar[c.ID] = c.Name
	}
	nameByService := make(map[uuid.UUID]string, len(services))
	for _, s := range services {
		nameByService[s.ID] = s.Name
	}

	currentByBox := make(map[int]Queue, len(boxes))
	waitingRows := make([]Queue, 0, len(rows))
	for _, row := range rows {
		if row.Status == StatusWashing {
			currentByBox[row.BoxNumber] = row
			continue
		}
		waitingRows = append(waitingRows, row)
	}

	respBoxes := make([]boardBoxResponse, len(boxes))
	boxesActive := 0
	for i, b := range boxes {
		item := boardBoxResponse{Number: b.Number, Label: b.Label, IsOpen: b.IsOpen}
		if cur, ok := currentByBox[b.Number]; ok {
			item.Current = &boardBookingResponse{
				Status:             string(cur.Status),
				ServiceName:        nameByService[cur.ServiceID],
				ScheduledStartAt:   cur.ScheduledStartAt,
				ScheduledEndAt:     cur.ScheduledEndAt,
				PausedAt:           cur.PausedAt,
				CustomerPhoneLast4: lastNDigits(phoneByUser[cur.UserID], 4),
				CarName:            nameByCar[cur.CarID],
			}
			boxesActive++
		}
		respBoxes[i] = item
	}

	respWaiting := make([]boardWaitingItemResponse, len(waitingRows))
	for i, row := range waitingRows {
		respWaiting[i] = boardWaitingItemResponse{
			ID:                 row.ID.String(),
			Status:             string(row.Status),
			BoxNumber:          row.BoxNumber,
			ServiceName:        nameByService[row.ServiceID],
			ScheduledStartAt:   row.ScheduledStartAt,
			CustomerPhoneLast4: lastNDigits(phoneByUser[row.UserID], 4),
			CarName:            nameByCar[row.CarID],
		}
	}

	return boardResponse{
		BoxesActive: boxesActive,
		BoxesTotal:  len(boxes),
		Boxes:       respBoxes,
		Waiting:     respWaiting,
	}, nil
}

type boardBookingResponse struct {
	Status             string     `json:"status"`
	ServiceName        string     `json:"service_name,omitempty"`
	ScheduledStartAt   time.Time  `json:"scheduled_start_at"`
	ScheduledEndAt     time.Time  `json:"scheduled_end_at"`
	PausedAt           *time.Time `json:"paused_at,omitempty"`
	CustomerPhoneLast4 string     `json:"customer_phone_last4"`
	CarName            string     `json:"car_name,omitempty"`
}

type boardBoxResponse struct {
	Number  int                   `json:"number"`
	Label   *string               `json:"label,omitempty"`
	IsOpen  bool                  `json:"is_open"`
	Current *boardBookingResponse `json:"current,omitempty"`
}

type boardWaitingItemResponse struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	BoxNumber          int       `json:"box_number"`
	ServiceName        string    `json:"service_name,omitempty"`
	ScheduledStartAt   time.Time `json:"scheduled_start_at"`
	CustomerPhoneLast4 string    `json:"customer_phone_last4"`
	CarName            string    `json:"car_name,omitempty"`
}

type boardResponse struct {
	BoxesActive int                        `json:"boxes_active"`
	BoxesTotal  int                        `json:"boxes_total"`
	Boxes       []boardBoxResponse         `json:"boxes"`
	Waiting     []boardWaitingItemResponse `json:"waiting"`
}

func lastNDigits(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// history is the caller's full booking history across every status,
// most recently scheduled first, paginated.
func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return
	}

	pagination := httputil.ParsePagination(r)
	rows, total, err := h.repo.ListByUser(r.Context(), authUser.ID, pagination.Offset(), pagination.Limit())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	items := make([]bookingResponse, len(rows))
	for i := range rows {
		resp, err := h.withPosition(r.Context(), &rows[i])
		if err != nil {
			httputil.WriteError(w, r, err)
			return
		}
		items[i] = resp
	}
	httputil.WritePaginated(w, http.StatusOK, items, pagination, total)
}

// boardEvents streams the display board as text/event-stream: an initial
// snapshot immediately, then a fresh one whenever anything changes for this
// washing point (a booking created/canceled/advanced, or a box opened/
// closed), until the client disconnects. Same requireStaff gate and
// washing-point ownership check as board — the kiosk logs in as staff/admin,
// no new role, per PLAN_WEB_APPS.md phase 8's own decision.
func (h *Handler) boardEvents(w http.ResponseWriter, r *http.Request) {
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.WriteError(w, r, apperror.Internal(fmt.Errorf("streaming unsupported")))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeSnapshot := func() (stop bool) {
		resp, err := h.buildBoardResponse(r.Context(), washingPointID)
		if err != nil {
			return true
		}
		payload, err := json.Marshal(resp)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return true
		}
		flusher.Flush()
		return false
	}

	if writeSnapshot() {
		return
	}

	changed, unsubscribe := h.bus.Subscribe(washingPointID)
	defer unsubscribe()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-changed:
			if writeSnapshot() {
				return
			}
		}
	}
}

// events streams the caller's booking as text/event-stream: an initial
// snapshot immediately, then a fresh one whenever anything changes for its
// washing point (booking created/canceled/advanced), until the booking
// reaches a terminal state or the client disconnects. Ownership uses the
// same 404-not-403 pattern as get.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
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

	q, err := h.repo.FindByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if q.UserID != authUser.ID {
		httputil.WriteError(w, r, apperror.NotFound("queue_not_found", "queue entry not found"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.WriteError(w, r, apperror.Internal(fmt.Errorf("streaming unsupported")))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeSnapshot := func(q *Queue) (terminal bool) {
		resp, err := h.withPosition(r.Context(), q)
		if err != nil {
			return true
		}
		payload, err := json.Marshal(resp)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return true
		}
		flusher.Flush()
		return q.Status == StatusReady || q.Status == StatusCanceled
	}

	if writeSnapshot(q) {
		return
	}

	changed, unsubscribe := h.bus.Subscribe(q.WashingPointID)
	defer unsubscribe()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-changed:
			fresh, err := h.repo.FindByID(r.Context(), id)
			if err != nil {
				return
			}
			if writeSnapshot(fresh) {
				return
			}
		}
	}
}
