package queue

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/clock"
	"q-wash-api/internal/washingpoint"
)

// reportPeriod is the cabinet "Отчёты" tab's period picker
// (docs/PLAN_WEB_APPS.md phase 10, mock: Car Wash Web Apps.dc.html's
// tabReports section).
type reportPeriod string

const (
	periodToday reportPeriod = "today"
	periodWeek  reportPeriod = "week"
	periodMonth reportPeriod = "month"
)

type reportsKPIs struct {
	RevenueCents          int64 `json:"revenue_cents"`
	RevenueDeltaPct       *int  `json:"revenue_delta_pct"`
	Cars                  int64 `json:"cars"`
	CarsDelta             *int  `json:"cars_delta"`
	AvgReceiptCents       int64 `json:"avg_receipt_cents"`
	AvgReceiptDeltaPct    *int  `json:"avg_receipt_delta_pct"`
	BoxUtilizationPct     int   `json:"box_utilization_pct"`
	BoxUtilizationDeltaPP *int  `json:"box_utilization_delta_pp"`
}

type reportsBar struct {
	Timestamp    string `json:"timestamp"`
	RevenueCents int64  `json:"revenue_cents"`
	Highlighted  bool   `json:"highlighted"`
}

type reportsServiceRow struct {
	ServiceID    string `json:"service_id"`
	Name         string `json:"name"`
	Count        int64  `json:"count"`
	RevenueCents int64  `json:"revenue_cents"`
	SharePct     int    `json:"share_pct"`
}

// Number, not box_number: this is a box row itself (same convention
// box.handler's own responses and board's per-box rows use), not a
// booking pointing at which box it's in (which is where "box_number" is
// the right name — see queue.Handler's boardItemResponse etc.).
type reportsBoxRow struct {
	Number       int    `json:"number"`
	Label        string `json:"label"`
	Cars         int64  `json:"cars"`
	RevenueCents int64  `json:"revenue_cents"`
	LoadPct      int    `json:"load_pct"`
}

type reportsResponse struct {
	Period     string              `json:"period"`
	RangeLabel string              `json:"range_label"`
	KPIs       reportsKPIs         `json:"kpis"`
	Bars       []reportsBar        `json:"bars"`
	Services   []reportsServiceRow `json:"services"`
	Boxes      []reportsBoxRow     `json:"boxes"`
}

// reports serves the cabinet "Отчёты" tab (docs/PLAN_WEB_APPS.md phase 10):
// revenue/cars/avg-receipt/box-utilization for period, each with a delta
// against the immediately-preceding period of equal length, a revenue time
// series, and per-service/per-box breakdowns. Same requireStaff +
// OwnsWashingPoint gate as board, since it's the same kind of
// Queue-driven, per-point aggregation.
func (h *Handler) reports(w http.ResponseWriter, r *http.Request) {
	washingPointID, ok := ownWashingPointID(w, r)
	if !ok {
		return
	}

	period := reportPeriod(r.URL.Query().Get("period"))
	if period == "" {
		period = periodToday
	}
	if period != periodToday && period != periodWeek && period != periodMonth {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_period", "period must be today, week, or month"))
		return
	}

	wp, err := h.wpRepo.FindByID(r.Context(), washingPointID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	resp, err := h.buildReportsResponse(r.Context(), wp, period, time.Now())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// periodBounds returns [start, end) in clock.BusinessLocation for period, ending
// at today's close (i.e. "now"'s calendar day, not literally now — same
// whole-day granularity dayBounds already uses elsewhere). week is the
// trailing 7 calendar days including today; month is month-to-date (the
// 1st of the current calendar month through today), not a full calendar
// month — matches the design mock's own period labels ("19 – 25 сентября"
// / "1 – 25 сентября").
func periodBounds(period reportPeriod, now time.Time) (time.Time, time.Time) {
	todayStart, todayEnd := clock.DayBounds(now)
	switch period {
	case periodWeek:
		return todayStart.AddDate(0, 0, -6), todayEnd
	case periodMonth:
		local := now.In(clock.BusinessLocation)
		monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, clock.BusinessLocation)
		return monthStart, todayEnd
	default:
		return todayStart, todayEnd
	}
}

// previousPeriodBounds is the immediately-preceding period of the same
// length as [start, end) — what each KPI's delta compares against.
func previousPeriodBounds(start, end time.Time) (time.Time, time.Time) {
	span := end.Sub(start)
	return start.Add(-span), start
}

var ruMonthGenitive = [...]string{
	"января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря",
}

func formatRuDate(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), ruMonthGenitive[t.Month()-1], t.Year())
}

// rangeLabel mirrors the mock's own REP_PERIODS.range strings (e.g.
// "19 – 25 сентября 2026"). end is exclusive (the next midnight), so the
// inclusive last day is end minus one day.
func rangeLabel(period reportPeriod, start, end time.Time) string {
	lastDay := end.AddDate(0, 0, -1).In(clock.BusinessLocation)
	first := start.In(clock.BusinessLocation)
	if period == periodToday {
		return formatRuDate(lastDay)
	}
	if first.Year() == lastDay.Year() && first.Month() == lastDay.Month() {
		return fmt.Sprintf("%d – %d %s %d", first.Day(), lastDay.Day(), ruMonthGenitive[lastDay.Month()-1], lastDay.Year())
	}
	return fmt.Sprintf("%s – %s", formatRuDate(first), formatRuDate(lastDay))
}

// parseHHMMMinutes parses a WashingPoint OpenTime/CloseTime "HH:MM" string
// (validated at write time by washingpoint's own timeFormatRegexp) into
// minutes since midnight.
func parseHHMMMinutes(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, err
	}
	return h*60 + m, nil
}

// openHoursMinutes is how many minutes the point is open per calendar day,
// from its open_time/close_time — an overnight close (close <= open) wraps
// past midnight. Falls back to a full 24h day if either string doesn't
// parse, rather than failing the whole report over a formatting edge case.
func openHoursMinutes(openTime, closeTime string) int {
	openMin, err1 := parseHHMMMinutes(openTime)
	closeMin, err2 := parseHHMMMinutes(closeTime)
	if err1 != nil || err2 != nil {
		return 24 * 60
	}
	if closeMin <= openMin {
		closeMin += 24 * 60
	}
	return closeMin - openMin
}

func percentInt(numerator, denominator int64) int {
	if denominator <= 0 {
		return 0
	}
	return int(float64(numerator) / float64(denominator) * 100)
}

func deltaPct(current, previous int64) *int {
	if previous <= 0 {
		return nil
	}
	d := int(float64(current-previous) / float64(previous) * 100)
	return &d
}

func deltaInt(current, previous int64) *int {
	d := int(current - previous)
	return &d
}

func deltaPP(currentPct, previousPct int) *int {
	d := currentPct - previousPct
	return &d
}

// buildReportsResponse is reports' aggregation logic, factored out from the
// HTTP handler the same way buildBoardResponse is.
func (h *Handler) buildReportsResponse(ctx context.Context, wp *washingpoint.WashingPoint, period reportPeriod, now time.Time) (reportsResponse, error) {
	start, end := periodBounds(period, now)
	prevStart, prevEnd := previousPeriodBounds(start, end)

	rows, err := h.repo.FindByWashingPointAndRange(ctx, wp.ID, start, end)
	if err != nil {
		return reportsResponse{}, err
	}
	prevRows, err := h.repo.FindByWashingPointAndRange(ctx, wp.ID, prevStart, prevEnd)
	if err != nil {
		return reportsResponse{}, err
	}

	services, err := h.serviceRepo.ListByWashingPoint(ctx, wp.ID)
	if err != nil {
		return reportsResponse{}, err
	}
	priceByOption := make(map[uuid.UUID]int64, len(services)*2)
	nameByService := make(map[uuid.UUID]string, len(services))
	for _, s := range services {
		nameByService[s.ID] = s.Name
		for _, p := range s.PriceOptions {
			priceByOption[p.ID] = int64(p.PriceCents)
		}
	}

	boxes, err := h.boxRepo.ListByWashingPoint(ctx, wp.ID)
	if err != nil {
		return reportsResponse{}, err
	}
	openBoxes := 0
	for _, b := range boxes {
		if b.IsOpen {
			openBoxes++
		}
	}

	days := int(end.Sub(start).Hours()) / 24
	availableMinutes := int64(openBoxes) * int64(openHoursMinutes(wp.OpenTime, wp.CloseTime)) * int64(days)

	current, serviceAgg, boxAgg := summarizeReportRows(rows, priceByOption)
	previous, _, _ := summarizeReportRows(prevRows, priceByOption)

	avgReceipt := safeDivInt(current.revenueCents, current.cars)
	prevAvgReceipt := safeDivInt(previous.revenueCents, previous.cars)
	currentLoadPct := percentInt(current.bookedMinutes, availableMinutes)
	previousLoadPct := percentInt(previous.bookedMinutes, availableMinutes)

	services2 := make([]reportsServiceRow, 0, len(serviceAgg))
	for id, agg := range serviceAgg {
		services2 = append(services2, reportsServiceRow{
			ServiceID:    id.String(),
			Name:         nameByService[id],
			Count:        agg.cars,
			RevenueCents: agg.revenueCents,
			SharePct:     percentInt(agg.revenueCents, current.revenueCents),
		})
	}
	sortServiceRowsByRevenueDesc(services2)

	boxRows := make([]reportsBoxRow, 0, len(boxes))
	for _, b := range boxes {
		agg := boxAgg[b.Number]
		if agg == nil {
			agg = &reportAgg{}
		}
		label := fmt.Sprintf("Бокс %d", b.Number)
		if b.Label != nil && *b.Label != "" {
			label = *b.Label
		}
		boxAvailable := int64(openHoursMinutes(wp.OpenTime, wp.CloseTime)) * int64(days)
		boxRows = append(boxRows, reportsBoxRow{
			Number:       b.Number,
			Label:        label,
			Cars:         agg.cars,
			RevenueCents: agg.revenueCents,
			LoadPct:      percentInt(agg.bookedMinutes, boxAvailable),
		})
	}

	return reportsResponse{
		Period:     string(period),
		RangeLabel: rangeLabel(period, start, end),
		KPIs: reportsKPIs{
			RevenueCents:          current.revenueCents,
			RevenueDeltaPct:       deltaPct(current.revenueCents, previous.revenueCents),
			Cars:                  current.cars,
			CarsDelta:             deltaInt(current.cars, previous.cars),
			AvgReceiptCents:       avgReceipt,
			AvgReceiptDeltaPct:    deltaPct(avgReceipt, prevAvgReceipt),
			BoxUtilizationPct:     currentLoadPct,
			BoxUtilizationDeltaPP: deltaPP(currentLoadPct, previousLoadPct),
		},
		Bars:     reportBars(period, start, end, now, rows, priceByOption),
		Services: services2,
		Boxes:    boxRows,
	}, nil
}

// reportAgg is the shape a period total, a per-service breakdown entry,
// and a per-box breakdown entry all reduce to — same three fields either
// way, so summarizeReportRows can share one accumulation loop for all
// three instead of three near-identical ones.
type reportAgg struct {
	cars          int64
	revenueCents  int64
	bookedMinutes int64
}

// summarizeReportRows reduces rows (any status) down to completed-only
// (StatusReady, the terminal "finished" state — same assumption
// isTerminal-adjacent code elsewhere already makes) totals, plus
// per-service and per-box breakdowns of the same completed set.
func summarizeReportRows(rows []Queue, priceByOption map[uuid.UUID]int64) (reportAgg, map[uuid.UUID]*reportAgg, map[int]*reportAgg) {
	total := reportAgg{}
	byService := make(map[uuid.UUID]*reportAgg)
	byBox := make(map[int]*reportAgg)

	for _, row := range rows {
		if row.Status != StatusReady {
			continue
		}
		price := priceByOption[row.PriceOptionID]
		minutes := int64(row.ScheduledEndAt.Sub(row.ScheduledStartAt).Minutes())

		total.cars++
		total.revenueCents += price
		total.bookedMinutes += minutes

		svc := byService[row.ServiceID]
		if svc == nil {
			svc = &reportAgg{}
			byService[row.ServiceID] = svc
		}
		svc.cars++
		svc.revenueCents += price

		bx := byBox[row.BoxNumber]
		if bx == nil {
			bx = &reportAgg{}
			byBox[row.BoxNumber] = bx
		}
		bx.cars++
		bx.revenueCents += price
		bx.bookedMinutes += minutes
	}

	return total, byService, byBox
}

func safeDivInt(numerator, denominator int64) int64 {
	if denominator <= 0 {
		return 0
	}
	return numerator / denominator
}

func sortServiceRowsByRevenueDesc(rows []reportsServiceRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].RevenueCents > rows[j-1].RevenueCents; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// reportBars buckets completed bookings' revenue into a time series: by
// hour (bounded to the point's open/close hours) for today, by calendar
// day otherwise. Returns raw timestamps rather than pre-formatted labels —
// the frontend already formats its own day/weekday labels client-side
// elsewhere (QrCodePage's dayLabel), same convention here.
func reportBars(period reportPeriod, start, end, now time.Time, rows []Queue, priceByOption map[uuid.UUID]int64) []reportsBar {
	var buckets []reportBucket
	if period == periodToday {
		buckets = hourlyBuckets(start)
	} else {
		buckets = dailyBuckets(start, end)
	}

	for _, row := range rows {
		if row.Status != StatusReady {
			continue
		}
		idx := bucketIndex(period, start, row.ScheduledStartAt)
		if idx < 0 || idx >= len(buckets) {
			continue
		}
		buckets[idx].revenue += priceByOption[row.PriceOptionID]
	}

	nowLocal := now.In(clock.BusinessLocation)
	bars := make([]reportsBar, len(buckets))
	for i, b := range buckets {
		highlighted := false
		if period == periodToday {
			highlighted = b.ts.Hour() == nowLocal.Hour() && sameDay(b.ts, nowLocal)
		} else {
			highlighted = sameDay(b.ts, nowLocal)
		}
		bars[i] = reportsBar{
			Timestamp:    b.ts.Format(time.RFC3339),
			RevenueCents: b.revenue,
			Highlighted:  highlighted,
		}
	}
	return bars
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.In(clock.BusinessLocation).Date()
	by, bm, bd := b.In(clock.BusinessLocation).Date()
	return ay == by && am == bm && ad == bd
}

type reportBucket struct {
	ts      time.Time
	revenue int64
}

func hourlyBuckets(dayStart time.Time) []reportBucket {
	buckets := make([]reportBucket, 24)
	for h := range 24 {
		buckets[h] = reportBucket{ts: dayStart.Add(time.Duration(h) * time.Hour)}
	}
	return buckets
}

func dailyBuckets(start, end time.Time) []reportBucket {
	days := int(end.Sub(start).Hours()) / 24
	buckets := make([]reportBucket, days)
	for i := range days {
		buckets[i] = reportBucket{ts: start.AddDate(0, 0, i)}
	}
	return buckets
}

func bucketIndex(period reportPeriod, start, t time.Time) int {
	local := t.In(clock.BusinessLocation)
	if period == periodToday {
		return local.Hour()
	}
	return int(local.Sub(start.In(clock.BusinessLocation)).Hours()) / 24
}
