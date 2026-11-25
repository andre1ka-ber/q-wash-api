package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/clock"
)

// Activity health of a washing point, from how recently its staff or
// workers logged in.
const (
	healthOK   = "ok"   // logged in within the last 3 days
	healthWarn = "warn" // 3–7 days ago
	healthBad  = "bad"  // older than 7 days, or never
)

type platformKPIs struct {
	CustomersTotal     int `json:"customers_total"`
	CustomersNew       int `json:"customers_new"`
	CustomersNewPrev   int `json:"customers_new_prev"`
	CustomersActive    int `json:"customers_active"`
	CustomersActivePrv int `json:"customers_active_prev"`
	StaffTotal         int `json:"staff_total"`
	StaffActive        int `json:"staff_active"`
}

type washActivity struct {
	WashingPointID string     `json:"washing_point_id"`
	Name           string     `json:"name"`
	LastLoginAt    *time.Time `json:"last_login_at"`
	LastRole       *string    `json:"last_role"`
	LastPlatform   *string    `json:"last_platform"`
	Staff          int        `json:"staff"`
	Sessions7d     int        `json:"sessions_7d"`
	Bookings7d     int        `json:"bookings_7d"`
	ActiveDays7d   int        `json:"active_days_7d"`
	Health         string     `json:"health"`
}

type countBy struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type platformHealth struct {
	UnconfirmedOver15Min int `json:"unconfirmed_over_15_min"`
	InactivePoints7d     int `json:"inactive_points_7d"`
	CanceledInPeriod     int `json:"canceled_in_period"`
}

type platformResponse struct {
	Period   string         `json:"period"`
	KPIs     platformKPIs   `json:"kpis"`
	Series   []seriesBucket `json:"series"`
	Health   platformHealth `json:"health"`
	Washes   []washActivity `json:"washes"`
	Devices  []countBy      `json:"devices"`
	Versions []countBy      `json:"versions"`
	Channels []countBy      `json:"channels"`
}

func healthFor(last *time.Time, now time.Time) string {
	if last == nil {
		return healthBad
	}
	switch age := now.Sub(*last); {
	case age < 3*24*time.Hour:
		return healthOK
	case age < 7*24*time.Hour:
		return healthWarn
	default:
		return healthBad
	}
}

func (r *AnalyticsRepository) customerCounts(ctx context.Context, from, to, prevFrom, prevTo time.Time) (platformKPIs, error) {
	var k platformKPIs
	err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FILTER (WHERE true)                                    AS customers_total,
		       COUNT(*) FILTER (WHERE created_at >= ? AND created_at < ?)      AS customers_new,
		       COUNT(*) FILTER (WHERE created_at >= ? AND created_at < ?)      AS customers_new_prev
		FROM users WHERE role = 'customer'`, from, to, prevFrom, prevTo).Scan(&k).Error
	if err != nil {
		return k, apperror.Internal(err)
	}
	err = r.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT user_id) FROM queue
		WHERE status NOT IN ('canceled', 'no_show') AND scheduled_start_at >= ? AND scheduled_start_at < ?`, from, to).
		Scan(&k.CustomersActive).Error
	if err != nil {
		return k, apperror.Internal(err)
	}
	err = r.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT user_id) FROM queue
		WHERE status NOT IN ('canceled', 'no_show') AND scheduled_start_at >= ? AND scheduled_start_at < ?`, prevFrom, prevTo).
		Scan(&k.CustomersActivePrv).Error
	if err != nil {
		return k, apperror.Internal(err)
	}
	// Scanned into its own struct: gorm's Scan zeroes the destination first,
	// which would wipe the counts above.
	var staff struct{ StaffTotal, StaffActive int }
	err = r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) AS staff_total,
		       COUNT(*) FILTER (WHERE last_login_at >= ? AND last_login_at < ?) AS staff_active
		FROM users
		WHERE role IN ('staff', 'worker') AND username IS NOT NULL`, from, to).Scan(&staff).Error
	if err != nil {
		return k, apperror.Internal(err)
	}
	k.StaffTotal, k.StaffActive = staff.StaffTotal, staff.StaffActive
	return k, nil
}

// activeCustomerStarts returns (user, start) of every non-canceled booking
// in [from, to) so the handler can count distinct customers per bucket.
func (r *AnalyticsRepository) activeCustomerStarts(ctx context.Context, from, to time.Time) ([]struct {
	UserID uuid.UUID
	Start  time.Time
}, error) {
	var rows []struct {
		UserID uuid.UUID
		Start  time.Time
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT user_id, scheduled_start_at AS start FROM queue
		WHERE status NOT IN ('canceled', 'no_show') AND scheduled_start_at >= ? AND scheduled_start_at < ?`, from, to).
		Scan(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return rows, nil
}

func (r *AnalyticsRepository) washActivities(ctx context.Context, since7d, dayEnd, now time.Time) ([]washActivity, error) {
	var rows []struct {
		ID           uuid.UUID
		Name         string
		LastLoginAt  *time.Time
		Staff        int
		Sessions7d   int
		ActiveDays7d int
		Bookings7d   int
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT wp.id AS id, wp.name AS name,
		  (SELECT MAX(u.last_login_at) FROM users u
		     WHERE u.washing_point_id = wp.id AND u.role IN ('staff', 'worker')) AS last_login_at,
		  (SELECT COUNT(*) FROM users u
		     WHERE u.washing_point_id = wp.id AND u.role IN ('staff', 'worker') AND u.username IS NOT NULL) AS staff,
		  (SELECT COUNT(*) FROM login_events e
		     WHERE e.washing_point_id = wp.id AND e.role <> 'display' AND e.created_at >= ?) AS sessions7d,
		  (SELECT COUNT(DISTINCT (e.created_at AT TIME ZONE 'Asia/Dushanbe')::date) FROM login_events e
		     WHERE e.washing_point_id = wp.id AND e.role <> 'display' AND e.created_at >= ?) AS active_days7d,
		  (SELECT COUNT(*) FROM queue q
		     WHERE q.washing_point_id = wp.id AND q.status NOT IN ('canceled', 'no_show')
		       AND q.scheduled_start_at >= ? AND q.scheduled_start_at < ?) AS bookings7d
		FROM washing_points wp
		WHERE wp.status <> 'pending_review'
		ORDER BY wp.name`, since7d, since7d, since7d, dayEnd).Scan(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}

	var last []struct {
		WashingPointID uuid.UUID
		Role           string
		Platform       string
	}
	err = r.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (washing_point_id) washing_point_id, role, platform
		FROM login_events WHERE washing_point_id IS NOT NULL AND role <> 'display'
		ORDER BY washing_point_id, created_at DESC`).Scan(&last).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	lastBy := make(map[uuid.UUID]struct{ role, platform string }, len(last))
	for _, l := range last {
		lastBy[l.WashingPointID] = struct{ role, platform string }{l.Role, l.Platform}
	}

	out := make([]washActivity, len(rows))
	for i, row := range rows {
		a := washActivity{
			WashingPointID: row.ID.String(),
			Name:           row.Name,
			LastLoginAt:    row.LastLoginAt,
			Staff:          row.Staff,
			Sessions7d:     row.Sessions7d,
			Bookings7d:     row.Bookings7d,
			ActiveDays7d:   row.ActiveDays7d,
			Health:         healthFor(row.LastLoginAt, now),
		}
		if l, ok := lastBy[row.ID]; ok {
			role, platform := l.role, l.platform
			a.LastRole, a.LastPlatform = &role, &platform
		}
		out[i] = a
	}
	return out, nil
}

func (r *AnalyticsRepository) countByQuery(ctx context.Context, query string, args ...any) ([]countBy, error) {
	var rows []countBy
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	if rows == nil {
		rows = []countBy{}
	}
	return rows, nil
}

func (h *Handler) platformAnalytics(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = periodToday
	}
	now := time.Now()
	win, ok := windowFor(period, now)
	if !ok {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_period", "period must be one of today, 7, 30"))
		return
	}
	ctx := r.Context()
	repo := h.analyticsRepo

	fail := func(err error) { httputil.WriteError(w, r, err) }

	kpis, err := repo.customerCounts(ctx, win.from, win.to, win.prevFrom, win.prevTo)
	if err != nil {
		fail(err)
		return
	}

	rows, err := repo.activeCustomerStarts(ctx, win.from, win.to)
	if err != nil {
		fail(err)
		return
	}
	series := seriesFor(period, win, nil)
	seen := make([]map[uuid.UUID]struct{}, len(series))
	for _, row := range rows {
		for i := range series {
			if !row.Start.Before(series[i].Start) && row.Start.Before(series[i].End) {
				if seen[i] == nil {
					seen[i] = map[uuid.UUID]struct{}{}
				}
				seen[i][row.UserID] = struct{}{}
				break
			}
		}
	}
	for i := range series {
		series[i].Value = len(seen[i])
	}

	dayStart, dayEnd := clock.DayBounds(now)
	since7d := dayStart.AddDate(0, 0, -6)
	washes, err := repo.washActivities(ctx, since7d, dayEnd, now)
	if err != nil {
		fail(err)
		return
	}

	health := platformHealth{}
	for _, wa := range washes {
		if wa.Health == healthBad {
			health.InactivePoints7d++
		}
	}
	err = repo.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM queue WHERE status = 'pending' AND created_at < ?`, now.Add(-15*time.Minute)).
		Scan(&health.UnconfirmedOver15Min).Error
	if err != nil {
		fail(apperror.Internal(err))
		return
	}
	err = repo.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM queue
		WHERE status IN ('canceled', 'no_show') AND scheduled_start_at >= ? AND scheduled_start_at < ?`, win.from, win.to).
		Scan(&health.CanceledInPeriod).Error
	if err != nil {
		fail(apperror.Internal(err))
		return
	}

	devices, err := repo.countByQuery(ctx, `
		SELECT last_platform AS key, COUNT(*) AS count FROM users
		WHERE role = 'customer' AND last_platform IS NOT NULL
		GROUP BY last_platform ORDER BY count DESC, key`)
	if err != nil {
		fail(err)
		return
	}
	versions, err := repo.countByQuery(ctx, `
		SELECT last_app_version AS key, COUNT(*) AS count FROM users
		WHERE role = 'customer' AND last_platform IN ('android', 'ios') AND last_app_version IS NOT NULL
		GROUP BY last_app_version ORDER BY count DESC, key LIMIT 6`)
	if err != nil {
		fail(err)
		return
	}
	channels, err := repo.countByQuery(ctx, `
		SELECT source AS key, COUNT(*) AS count FROM queue
		WHERE status NOT IN ('canceled', 'no_show') AND scheduled_start_at >= ? AND scheduled_start_at < ?
		GROUP BY source ORDER BY count DESC, key`, win.from, win.to)
	if err != nil {
		fail(err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, platformResponse{
		Period: period, KPIs: kpis, Series: series, Health: health,
		Washes: washes, Devices: devices, Versions: versions, Channels: channels,
	})
}
