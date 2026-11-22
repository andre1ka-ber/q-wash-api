// Package admin serves the q-wash-admin app's network-wide views — a
// richer washing-point list (owner name, service count) and dashboard
// aggregates — that don't belong on any single domain's handler since they
// join across washing points, owners, services and queue. Every route here
// is admin-only (see requireAdmin in internal/app/app.go); staff/worker are
// scoped to one point and use each domain's own per-point endpoints
// instead.
package admin

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/owner"
	"q-wash-api/internal/photo"
	"q-wash-api/internal/platform/clock"
	"q-wash-api/internal/platformsettings"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/service"
	"q-wash-api/internal/user"
	"q-wash-api/internal/washingpoint"
)

type Handler struct {
	wpRepo        *washingpoint.Repository
	ownerRepo     *owner.Repository
	serviceRepo   *service.Repository
	queueRepo     *queue.Repository
	photoRepo     *photo.Repository
	userRepo      *user.Repository
	analyticsRepo *AnalyticsRepository
	settings      *platformsettings.Handler
}

func NewHandler(wpRepo *washingpoint.Repository, ownerRepo *owner.Repository, serviceRepo *service.Repository, queueRepo *queue.Repository, photoRepo *photo.Repository, userRepo *user.Repository, analyticsRepo *AnalyticsRepository, settings *platformsettings.Handler) *Handler {
	return &Handler{wpRepo: wpRepo, ownerRepo: ownerRepo, serviceRepo: serviceRepo, queueRepo: queueRepo, photoRepo: photoRepo, userRepo: userRepo, analyticsRepo: analyticsRepo, settings: settings}
}

func (h *Handler) RegisterRoutes(r chi.Router, requireAdmin ...func(http.Handler) http.Handler) {
	r.Route("/admin", func(a chi.Router) {
		a.With(requireAdmin...).Get("/washing-points", h.listWashingPoints)
		a.With(requireAdmin...).Get("/stats", h.stats)
		a.With(requireAdmin...).Get("/settings", h.settings.Get)
		a.With(requireAdmin...).Patch("/settings", h.settings.Patch)
		a.With(requireAdmin...).Get("/analytics", h.analytics)
		a.With(requireAdmin...).Get("/analytics/platform", h.platformAnalytics)
	})
}

type washingPointItem struct {
	ID            string  `json:"id"`
	OwnerID       *string `json:"owner_id,omitempty"`
	OwnerName     *string `json:"owner_name,omitempty"`
	Name          string  `json:"name"`
	Address       string  `json:"address"`
	Status        string  `json:"status"`
	BoxesCount    int     `json:"boxes_count"`
	ServicesCount int64   `json:"services_count"`
	// AccountsCount is the number of staff/worker login accounts scoped to
	// the point (0-2 in practice) — the "Доступ" column in q-wash-admin.
	AccountsCount int64     `json:"accounts_count"`
	CoverURL      *string   `json:"cover_url,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// listWashingPoints is the network-wide counterpart to the public
// GET /washing-points list: it adds owner attribution and a services
// count, batch-fetched to avoid N+1 queries per point. Box counts use the
// existing WashingPoint.BoxesCount capacity number rather than a per-Box
// count — the Box entity itself doesn't exist yet (docs/PLAN_WEB_APPS.md
// phase 6).
func (h *Handler) listWashingPoints(w http.ResponseWriter, r *http.Request) {
	points, err := h.wpRepo.List(r.Context())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	owners, err := h.ownerRepo.List(r.Context())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	ownerNames := make(map[uuid.UUID]string, len(owners))
	for _, o := range owners {
		ownerNames[o.ID] = o.Name
	}

	pointIDs := make([]uuid.UUID, len(points))
	for i, wp := range points {
		pointIDs[i] = wp.ID
	}
	serviceCounts, err := h.serviceRepo.CountActiveByWashingPointIDs(r.Context(), pointIDs)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	accountCounts, err := h.userRepo.CountLoginAccountsByWashingPointIDs(r.Context(), pointIDs)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	coverURLs, err := h.photoRepo.FindCoverURLsByWashingPointIDs(r.Context(), pointIDs)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	items := make([]washingPointItem, len(points))
	for i, wp := range points {
		item := washingPointItem{
			ID:            wp.ID.String(),
			Name:          wp.Name,
			Address:       wp.Address,
			Status:        string(wp.Status),
			BoxesCount:    wp.BoxesCount,
			ServicesCount: serviceCounts[wp.ID],
			AccountsCount: accountCounts[wp.ID],
			CreatedAt:     wp.CreatedAt,
		}
		if wp.OwnerID != nil {
			id := wp.OwnerID.String()
			item.OwnerID = &id
			if name, ok := ownerNames[*wp.OwnerID]; ok {
				item.OwnerName = &name
			}
		}
		if url, ok := coverURLs[wp.ID]; ok {
			item.CoverURL = &url
		}
		items[i] = item
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type statsResponse struct {
	PointsTotal  int `json:"points_total"`
	PointsActive int `json:"points_active"`
	// PointsAdded30d counts points created in the last 30 days (the
	// "+2 за месяц" chip next to the network size).
	PointsAdded30d int `json:"points_added_30d"`
	// BookingsToday/CanceledToday count bookings whose scheduled interval
	// overlaps today (businessLocation), network-wide.
	BookingsToday int `json:"bookings_today"`
	CanceledToday int `json:"canceled_today"`
	// BookingsYesterday / AverageUtilizationYesterday are the same two
	// aggregates for the previous calendar day, so clients can show a
	// day-over-day delta without a second request.
	BookingsYesterday int `json:"bookings_yesterday"`
	// AverageUtilization is booked box-minutes today divided by available
	// box-minutes today (boxes_count * open-hours, summed over active
	// points) — a coarse proxy until phase 5/6 land per-weekday schedules
	// and per-box open/closed state, which will make this exact.
	AverageUtilization          float64 `json:"average_utilization"`
	AverageUtilizationYesterday float64 `json:"average_utilization_yesterday"`
}

// dayAggregate is one calendar day's network-wide booking numbers.
type dayAggregate struct {
	bookings    int
	canceled    int
	utilization float64
}

// aggregateDay folds a day's bookings into counts and box-minute
// utilization over the active points' available minutes. Opening hours are
// the points' current ones (history of hour changes isn't kept), so
// "yesterday" is a close approximation, not an audit figure.
func aggregateDay(points []washingpoint.WashingPoint, bookings []queue.Queue, dayStart, dayEnd time.Time) dayAggregate {
	var agg dayAggregate

	availableMinutes := 0.0
	for _, wp := range points {
		if wp.Status != washingpoint.StatusActive {
			continue
		}
		openMin, err := parseHHMM(wp.OpenTime)
		if err != nil {
			continue
		}
		closeMin, err := parseHHMM(wp.CloseTime)
		if err != nil || closeMin <= openMin {
			continue
		}
		availableMinutes += float64(wp.BoxesCount) * float64(closeMin-openMin)
	}

	bookedMinutes := 0.0
	for _, q := range bookings {
		if q.Status == queue.StatusCanceled || q.Status == queue.StatusNoShow {
			agg.canceled++
			continue
		}
		agg.bookings++

		// Overflow bookings occupy no box, so they don't count toward
		// box utilization.
		if q.IsOverflow {
			continue
		}
		start, end := q.ScheduledStartAt, q.ScheduledEndAt
		if start.Before(dayStart) {
			start = dayStart
		}
		if end.After(dayEnd) {
			end = dayEnd
		}
		if end.After(start) {
			bookedMinutes += end.Sub(start).Minutes()
		}
	}

	if availableMinutes > 0 {
		agg.utilization = bookedMinutes / availableMinutes
	}
	return agg
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	points, err := h.wpRepo.List(r.Context())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	now := time.Now()
	dayStart, dayEnd := clock.DayBounds(now)
	prevStart, prevEnd := clock.DayBounds(dayStart.AddDate(0, 0, -1))

	today, err := h.queueRepo.FindScheduledInRangeNetworkWide(r.Context(), dayStart, dayEnd)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	yesterday, err := h.queueRepo.FindScheduledInRangeNetworkWide(r.Context(), prevStart, prevEnd)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	resp := statsResponse{PointsTotal: len(points)}
	monthAgo := now.AddDate(0, 0, -30)
	for _, wp := range points {
		if wp.Status == washingpoint.StatusActive {
			resp.PointsActive++
		}
		if wp.CreatedAt.After(monthAgo) {
			resp.PointsAdded30d++
		}
	}

	t := aggregateDay(points, today, dayStart, dayEnd)
	resp.BookingsToday, resp.CanceledToday, resp.AverageUtilization = t.bookings, t.canceled, t.utilization
	y := aggregateDay(points, yesterday, prevStart, prevEnd)
	resp.BookingsYesterday, resp.AverageUtilizationYesterday = y.bookings, y.utilization

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// parseHHMM parses a WashingPoint "HH:MM" open/close string into minutes
// since midnight, matching the format combineDayAndTime (internal/queue)
// already assumes but without needing a calendar day to anchor it to.
func parseHHMM(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return t.Hour()*60 + t.Minute(), nil
}
