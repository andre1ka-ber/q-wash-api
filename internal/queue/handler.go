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
	bus *eventbus.Bus,
) *Handler {
	return &Handler{repo: repo, manager: manager, wpRepo: wpRepo, serviceRepo: serviceRepo, userRepo: userRepo, carRepo: carRepo, scheduleRepo: scheduleRepo, bus: bus}
}

// RegisterRoutes mounts availability (public), booking create/get/cancel
// (any authenticated user, ownership enforced internally) and the staff
// board + status transition (requireStaff). Everything for a given path
// prefix is registered in one Route() call with per-method middleware via
// chi's With(...) — see the note in washingpoint.Handler.RegisterRoutes for
// why (chi panics if the same prefix is Mount()ed from two separate calls).
func (h *Handler) RegisterRoutes(r chi.Router, requireAuth func(http.Handler) http.Handler, requireStaff ...func(http.Handler) http.Handler) {
	r.Route("/washing-points/{id}/availability", func(av chi.Router) {
		av.Get("/", h.availability)
	})

	r.Route("/washing-points/{id}/queue", func(board chi.Router) {
		board.With(requireStaff...).Get("/", h.listByWashingPoint)
	})

	r.Route("/queue", func(q chi.Router) {
		q.With(requireStaff...).Get("/", h.list)
		q.With(requireAuth).Post("/", h.create)
		q.With(requireAuth).Get("/{id}", h.get)
		q.With(requireAuth).Get("/{id}/events", h.events)
		q.With(requireAuth).Patch("/{id}/cancel", h.cancel)
		q.With(requireStaff...).Patch("/{id}/status", h.updateStatus)
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

	q, err := h.manager.CancelBooking(r.Context(), id, authUser.ID)
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
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	BoxNumber          int       `json:"box_number"`
	ScheduledStartAt   time.Time `json:"scheduled_start_at"`
	ScheduledEndAt     time.Time `json:"scheduled_end_at"`
	CustomerPhoneLast4 string    `json:"customer_phone_last4"`
	CarName            string    `json:"car_name,omitempty"`
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

	rows, err := h.repo.ListLiveByWashingPoint(r.Context(), washingPointID)
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
			CustomerPhoneLast4: lastNDigits(phoneByUser[row.UserID], 4),
			CarName:            nameByCar[row.CarID],
		}
	}
	return items, nil
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
