package queue

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/car"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/service"
	"q-wash-api/internal/user"
)

// ownWashingPointID is the shared preamble of every per-point staff
// endpoint: authenticated caller, {id} path param, and the caller's
// washing-point scope (404, not 403, for another point — the
// OwnsWashingPoint convention). On failure it writes the error response
// and returns ok=false.
func ownWashingPointID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	authUser, ok := reqctx.AuthUserFromContext(r.Context())
	if !ok {
		httputil.WriteError(w, r, apperror.Unauthorized("unauthenticated", "authentication required"))
		return uuid.Nil, false
	}
	washingPointID, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return uuid.Nil, false
	}
	if !authUser.OwnsWashingPoint(washingPointID) {
		httputil.WriteError(w, r, apperror.NotFound("washing_point_not_found", "washing point not found"))
		return uuid.Nil, false
	}
	return washingPointID, true
}

// dayFromQuery reads the optional ?date=YYYY-MM-DD param as a calendar day
// in businessLocation, defaulting to now (today) when omitted.
func dayFromQuery(r *http.Request) (time.Time, error) {
	raw := r.URL.Query().Get("date")
	if raw == "" {
		return time.Now(), nil
	}
	day, err := time.ParseInLocation("2006-01-02", raw, businessLocation)
	if err != nil {
		return time.Time{}, apperror.BadRequest("invalid_date", "date must be in YYYY-MM-DD format")
	}
	return day, nil
}

// rowRefs holds the users/cars (and optionally services) referenced by a
// batch of queue rows, fetched once instead of per row. Missing ids just
// yield zero values from the accessors.
type rowRefs struct {
	users    map[uuid.UUID]user.User
	cars     map[uuid.UUID]car.Car
	services map[uuid.UUID]service.Service
}

func (r *rowRefs) phone(id uuid.UUID) string       { return r.users[id].PhoneNumber }
func (r *rowRefs) carName(id uuid.UUID) string     { return r.cars[id].Name }
func (r *rowRefs) serviceName(id uuid.UUID) string { return r.services[id].Name }

// loadRefs batch-fetches the users and cars for rows, plus their services
// when withServices is set (the public-facing board/board-items shapes
// don't need service names, so they skip that query).
func (h *Handler) loadRefs(ctx context.Context, rows []Queue, withServices bool) (*rowRefs, error) {
	userIDs := uniqueIDs(rows, func(q Queue) uuid.UUID { return q.UserID })
	carIDs := uniqueIDs(rows, func(q Queue) uuid.UUID { return q.CarID })

	users, err := h.userRepo.FindByIDs(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	cars, err := h.carRepo.FindByIDs(ctx, carIDs)
	if err != nil {
		return nil, err
	}
	refs := &rowRefs{
		users: make(map[uuid.UUID]user.User, len(users)),
		cars:  make(map[uuid.UUID]car.Car, len(cars)),
	}
	for _, u := range users {
		refs.users[u.ID] = u
	}
	for _, c := range cars {
		refs.cars[c.ID] = c
	}
	if withServices {
		services, err := h.serviceRepo.FindByIDs(ctx, uniqueIDs(rows, func(q Queue) uuid.UUID { return q.ServiceID }))
		if err != nil {
			return nil, err
		}
		refs.services = make(map[uuid.UUID]service.Service, len(services))
		for _, s := range services {
			refs.services[s.ID] = s
		}
	}
	return refs, nil
}

func uniqueIDs(rows []Queue, pick func(Queue) uuid.UUID) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		if id := pick(row); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func parseUUIDField(raw, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperror.BadRequest("invalid_"+field, field+" must be a valid uuid")
	}
	return id, nil
}

func parseScheduledStart(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, apperror.BadRequest("invalid_scheduled_start_at", "scheduled_start_at must be an RFC3339 timestamp")
	}
	return t, nil
}
