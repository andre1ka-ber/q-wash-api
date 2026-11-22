package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/clock"
)

// Periods accepted by GET /admin/analytics?period=.
const (
	periodToday = "today"
	period7     = "7"
	period30    = "30"
)

// AnalyticsRepository runs the aggregate SQL behind the admin "Аналитика →
// Операции" view. Everything is read-only and network-wide.
type AnalyticsRepository struct {
	db *gorm.DB
}

func NewAnalyticsRepository(db *gorm.DB) *AnalyticsRepository {
	return &AnalyticsRepository{db: db}
}

// periodTotals are the headline numbers for one window.
type periodTotals struct {
	Bookings     int     `json:"bookings"`
	Canceled     int     `json:"canceled"`
	RevenueCents int64   `json:"revenue_cents"`
	NewCustomers int     `json:"new_customers"`
	CancelShare  float64 `json:"cancel_share"`
}

type seriesBucket struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Value int       `json:"value"`
}

type topPoint struct {
	WashingPointID string `json:"washing_point_id"`
	Name           string `json:"name"`
	RevenueCents   int64  `json:"revenue_cents"`
}

// totals counts bookings that start inside [from, to). Canceled and
// no-show bookings are counted apart (like /admin/stats does); revenue is
// the price of every service on bookings that reached "ready", at the
// price option's current price (bookings keep no price snapshot).
func (r *AnalyticsRepository) totals(ctx context.Context, from, to time.Time) (periodTotals, error) {
	var t periodTotals

	var counts struct {
		Bookings int
		Canceled int
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FILTER (WHERE status NOT IN ('canceled', 'no_show')) AS bookings,
		       COUNT(*) FILTER (WHERE status IN ('canceled', 'no_show'))     AS canceled
		FROM queue
		WHERE scheduled_start_at >= ? AND scheduled_start_at < ?`, from, to).Scan(&counts).Error
	if err != nil {
		return t, apperror.Internal(err)
	}
	t.Bookings, t.Canceled = counts.Bookings, counts.Canceled
	if all := counts.Bookings + counts.Canceled; all > 0 {
		t.CancelShare = float64(counts.Canceled) / float64(all)
	}

	err = r.db.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(spo.price_cents), 0)
		FROM queue q
		JOIN queue_items qi ON qi.queue_id = q.id
		JOIN service_price_options spo ON spo.id = qi.price_option_id
		WHERE q.status = 'ready' AND q.scheduled_start_at >= ? AND q.scheduled_start_at < ?`, from, to).
		Scan(&t.RevenueCents).Error
	if err != nil {
		return t, apperror.Internal(err)
	}

	err = r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM users
		WHERE role = 'customer' AND created_at >= ? AND created_at < ?`, from, to).
		Scan(&t.NewCustomers).Error
	if err != nil {
		return t, apperror.Internal(err)
	}
	return t, nil
}

// bookingStarts returns the start time of every non-canceled booking in
// [from, to); the handler buckets them (hour/day/week) in Go so the
// bucket edges follow the business timezone.
func (r *AnalyticsRepository) bookingStarts(ctx context.Context, from, to time.Time) ([]time.Time, error) {
	var starts []time.Time
	err := r.db.WithContext(ctx).Raw(`
		SELECT scheduled_start_at FROM queue
		WHERE status NOT IN ('canceled', 'no_show') AND scheduled_start_at >= ? AND scheduled_start_at < ?`, from, to).
		Scan(&starts).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return starts, nil
}

func (r *AnalyticsRepository) topPoints(ctx context.Context, from, to time.Time, limit int) ([]topPoint, error) {
	var rows []struct {
		ID           uuid.UUID
		Name         string
		RevenueCents int64
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT wp.id AS id, wp.name AS name, SUM(spo.price_cents) AS revenue_cents
		FROM queue q
		JOIN queue_items qi ON qi.queue_id = q.id
		JOIN service_price_options spo ON spo.id = qi.price_option_id
		JOIN washing_points wp ON wp.id = q.washing_point_id
		WHERE q.status = 'ready' AND q.scheduled_start_at >= ? AND q.scheduled_start_at < ?
		GROUP BY wp.id, wp.name
		ORDER BY revenue_cents DESC, wp.name
		LIMIT ?`, from, to, limit).Scan(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	out := make([]topPoint, len(rows))
	for i, row := range rows {
		out[i] = topPoint{WashingPointID: row.ID.String(), Name: row.Name, RevenueCents: row.RevenueCents}
	}
	return out, nil
}

// analyticsWindow is the current window and the one right before it, same
// length, for the delta chips.
type analyticsWindow struct {
	from, to         time.Time
	prevFrom, prevTo time.Time
}

// windowFor resolves a period to calendar-day windows in the business
// timezone. "7" and "30" end with today (inclusive).
func windowFor(period string, now time.Time) (analyticsWindow, bool) {
	dayStart, dayEnd := clock.DayBounds(now)
	days := 0
	switch period {
	case periodToday:
		days = 1
	case period7:
		days = 7
	case period30:
		days = 30
	default:
		return analyticsWindow{}, false
	}
	from := dayStart.AddDate(0, 0, -(days - 1))
	return analyticsWindow{from: from, to: dayEnd, prevFrom: from.AddDate(0, 0, -days), prevTo: from}, true
}

// seriesFor buckets bookings for the chart: today = 2-hour buckets from
// 08:00 to 22:00 (earlier/later bookings fold into the first/last bucket),
// 7 days = one bucket per day, 30 days = four 7-day weeks then the last
// two days.
func seriesFor(period string, w analyticsWindow, starts []time.Time) []seriesBucket {
	var buckets []seriesBucket
	switch period {
	case periodToday:
		for h := 8; h < 22; h += 2 {
			buckets = append(buckets, seriesBucket{
				Start: w.from.Add(time.Duration(h) * time.Hour),
				End:   w.from.Add(time.Duration(h+2) * time.Hour),
			})
		}
		buckets[0].Start = w.from
		buckets[len(buckets)-1].End = w.to
	case period7:
		for d := 0; d < 7; d++ {
			buckets = append(buckets, seriesBucket{Start: w.from.AddDate(0, 0, d), End: w.from.AddDate(0, 0, d+1)})
		}
	default: // period30
		for k := 0; k < 4; k++ {
			buckets = append(buckets, seriesBucket{Start: w.from.AddDate(0, 0, 7*k), End: w.from.AddDate(0, 0, 7*(k+1))})
		}
		buckets = append(buckets, seriesBucket{Start: w.from.AddDate(0, 0, 28), End: w.to})
	}
	for _, s := range starts {
		for i := range buckets {
			if !s.Before(buckets[i].Start) && s.Before(buckets[i].End) {
				buckets[i].Value++
				break
			}
		}
	}
	return buckets
}

type analyticsResponse struct {
	Period    string         `json:"period"`
	From      time.Time      `json:"from"`
	To        time.Time      `json:"to"`
	Current   periodTotals   `json:"current"`
	Previous  periodTotals   `json:"previous"`
	Series    []seriesBucket `json:"series"`
	TopPoints []topPoint     `json:"top_points"`
}

func (h *Handler) analytics(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = periodToday
	}
	win, ok := windowFor(period, time.Now())
	if !ok {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_period", "period must be one of today, 7, 30"))
		return
	}

	ctx := r.Context()
	cur, err := h.analyticsRepo.totals(ctx, win.from, win.to)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	prev, err := h.analyticsRepo.totals(ctx, win.prevFrom, win.prevTo)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	starts, err := h.analyticsRepo.bookingStarts(ctx, win.from, win.to)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	top, err := h.analyticsRepo.topPoints(ctx, win.from, win.to, 5)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, analyticsResponse{
		Period:    period,
		From:      win.from,
		To:        win.to,
		Current:   cur,
		Previous:  prev,
		Series:    seriesFor(period, win, starts),
		TopPoints: top,
	})
}
