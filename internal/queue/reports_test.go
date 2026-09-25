package queue

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func businessDay(y int, m time.Month, d, hour, minute int) time.Time {
	return time.Date(y, m, d, hour, minute, 0, 0, businessLocation)
}

func TestPeriodBounds(t *testing.T) {
	now := businessDay(2026, 9, 25, 14, 30) // a Friday

	t.Run("today", func(t *testing.T) {
		start, end := periodBounds(periodToday, now)
		wantStart := businessDay(2026, 9, 25, 0, 0)
		wantEnd := businessDay(2026, 9, 26, 0, 0)
		if !start.Equal(wantStart) || !end.Equal(wantEnd) {
			t.Fatalf("today bounds = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
		}
	})

	t.Run("week is trailing 7 days including today", func(t *testing.T) {
		start, end := periodBounds(periodWeek, now)
		wantStart := businessDay(2026, 9, 19, 0, 0)
		wantEnd := businessDay(2026, 9, 26, 0, 0)
		if !start.Equal(wantStart) || !end.Equal(wantEnd) {
			t.Fatalf("week bounds = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
		}
		if days := int(end.Sub(start).Hours()) / 24; days != 7 {
			t.Errorf("week span = %d days, want 7", days)
		}
	})

	t.Run("month is month-to-date", func(t *testing.T) {
		start, end := periodBounds(periodMonth, now)
		wantStart := businessDay(2026, 9, 1, 0, 0)
		wantEnd := businessDay(2026, 9, 26, 0, 0)
		if !start.Equal(wantStart) || !end.Equal(wantEnd) {
			t.Fatalf("month bounds = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
		}
	})
}

func TestPreviousPeriodBounds(t *testing.T) {
	start := businessDay(2026, 9, 19, 0, 0)
	end := businessDay(2026, 9, 26, 0, 0) // 7-day week
	prevStart, prevEnd := previousPeriodBounds(start, end)
	wantPrevStart := businessDay(2026, 9, 12, 0, 0)
	if !prevStart.Equal(wantPrevStart) || !prevEnd.Equal(start) {
		t.Fatalf("previous bounds = [%v, %v), want [%v, %v)", prevStart, prevEnd, wantPrevStart, start)
	}
}

func TestRangeLabel(t *testing.T) {
	cases := []struct {
		name   string
		period reportPeriod
		start  time.Time
		end    time.Time
		want   string
	}{
		{"today", periodToday, businessDay(2026, 9, 25, 0, 0), businessDay(2026, 9, 26, 0, 0), "25 сентября 2026"},
		{"week same month", periodWeek, businessDay(2026, 9, 19, 0, 0), businessDay(2026, 9, 26, 0, 0), "19 – 25 сентября 2026"},
		{"month crossing boundary", periodMonth, businessDay(2026, 8, 28, 0, 0), businessDay(2026, 9, 3, 0, 0), "28 августа 2026 – 2 сентября 2026"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rangeLabel(c.period, c.start, c.end)
			if got != c.want {
				t.Errorf("rangeLabel() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOpenHoursMinutes(t *testing.T) {
	cases := []struct {
		name        string
		open, close string
		want        int
	}{
		{"normal day", "09:00", "21:00", 12 * 60},
		{"overnight", "20:00", "02:00", 6 * 60},
		{"unparseable falls back to 24h", "круглосуточно", "круглосуточно", 24 * 60},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := openHoursMinutes(c.open, c.close); got != c.want {
				t.Errorf("openHoursMinutes(%q, %q) = %d, want %d", c.open, c.close, got, c.want)
			}
		})
	}
}

func TestPercentAndDeltaHelpers(t *testing.T) {
	if got := percentInt(50, 200); got != 25 {
		t.Errorf("percentInt(50,200) = %d, want 25", got)
	}
	if got := percentInt(1, 0); got != 0 {
		t.Errorf("percentInt with zero denominator = %d, want 0", got)
	}

	if got := deltaPct(120, 100); got == nil || *got != 20 {
		t.Errorf("deltaPct(120,100) = %v, want 20", got)
	}
	if got := deltaPct(100, 0); got != nil {
		t.Errorf("deltaPct with no previous data should be nil, got %v", *got)
	}

	if got := deltaInt(87, 78); got == nil || *got != 9 {
		t.Errorf("deltaInt(87,78) = %v, want 9", got)
	}

	if got := deltaPP(68, 71); got == nil || *got != -3 {
		t.Errorf("deltaPP(68,71) = %v, want -3", got)
	}
}

func TestSummarizeReportRows(t *testing.T) {
	serviceA, serviceB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	priceA, priceB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	priceByOption := map[uuid.UUID]int64{priceA: 12800, priceB: 19800}

	rows := []Queue{
		{Status: StatusReady, ServiceID: serviceA, PriceOptionID: priceA, BoxNumber: 1,
			ScheduledStartAt: businessDay(2026, 9, 25, 9, 0), ScheduledEndAt: businessDay(2026, 9, 25, 9, 50)},
		{Status: StatusReady, ServiceID: serviceB, PriceOptionID: priceB, BoxNumber: 2,
			ScheduledStartAt: businessDay(2026, 9, 25, 10, 0), ScheduledEndAt: businessDay(2026, 9, 25, 11, 30)},
		// Not completed — must not count toward revenue/cars/minutes at all.
		{Status: StatusCanceled, ServiceID: serviceA, PriceOptionID: priceA, BoxNumber: 1,
			ScheduledStartAt: businessDay(2026, 9, 25, 12, 0), ScheduledEndAt: businessDay(2026, 9, 25, 12, 50)},
		{Status: StatusWashing, ServiceID: serviceA, PriceOptionID: priceA, BoxNumber: 1,
			ScheduledStartAt: businessDay(2026, 9, 25, 13, 0), ScheduledEndAt: businessDay(2026, 9, 25, 13, 50)},
	}

	total, byService, byBox := summarizeReportRows(rows, priceByOption)

	if total.cars != 2 {
		t.Errorf("total.cars = %d, want 2 (only StatusReady counts)", total.cars)
	}
	if want := int64(12800 + 19800); total.revenueCents != want {
		t.Errorf("total.revenueCents = %d, want %d", total.revenueCents, want)
	}
	if want := int64(50 + 90); total.bookedMinutes != want {
		t.Errorf("total.bookedMinutes = %d, want %d", total.bookedMinutes, want)
	}

	if byService[serviceA] == nil || byService[serviceA].cars != 1 || byService[serviceA].revenueCents != 12800 {
		t.Errorf("byService[serviceA] = %+v, want {cars:1 revenueCents:12800}", byService[serviceA])
	}
	if byService[serviceB] == nil || byService[serviceB].cars != 1 || byService[serviceB].revenueCents != 19800 {
		t.Errorf("byService[serviceB] = %+v, want {cars:1 revenueCents:19800}", byService[serviceB])
	}

	if byBox[1] == nil || byBox[1].cars != 1 || byBox[1].bookedMinutes != 50 {
		t.Errorf("byBox[1] = %+v, want {cars:1 bookedMinutes:50}", byBox[1])
	}
	if byBox[2] == nil || byBox[2].cars != 1 || byBox[2].bookedMinutes != 90 {
		t.Errorf("byBox[2] = %+v, want {cars:1 bookedMinutes:90}", byBox[2])
	}
}

func TestReportBars_Today_BucketsByHourAndHighlightsCurrent(t *testing.T) {
	now := businessDay(2026, 9, 25, 14, 30)
	start, end := periodBounds(periodToday, now)
	price := uuid.Must(uuid.NewV7())
	priceByOption := map[uuid.UUID]int64{price: 10000}

	rows := []Queue{
		{Status: StatusReady, PriceOptionID: price, ScheduledStartAt: businessDay(2026, 9, 25, 9, 15), ScheduledEndAt: businessDay(2026, 9, 25, 9, 45)},
		{Status: StatusReady, PriceOptionID: price, ScheduledStartAt: businessDay(2026, 9, 25, 14, 5), ScheduledEndAt: businessDay(2026, 9, 25, 14, 45)},
	}

	bars := reportBars(periodToday, start, end, now, rows, priceByOption)
	if len(bars) != 24 {
		t.Fatalf("len(bars) = %d, want 24 (one per hour)", len(bars))
	}
	if bars[9].RevenueCents != 10000 {
		t.Errorf("bars[9].RevenueCents = %d, want 10000", bars[9].RevenueCents)
	}
	if bars[14].RevenueCents != 10000 {
		t.Errorf("bars[14].RevenueCents = %d, want 10000", bars[14].RevenueCents)
	}
	if !bars[14].Highlighted {
		t.Errorf("bars[14] (current hour) should be highlighted")
	}
	if bars[9].Highlighted {
		t.Errorf("bars[9] should not be highlighted")
	}
	for i, b := range bars {
		if i != 9 && i != 14 && b.RevenueCents != 0 {
			t.Errorf("bars[%d].RevenueCents = %d, want 0", i, b.RevenueCents)
		}
	}
}

func TestReportBars_Week_BucketsByDayAndHighlightsToday(t *testing.T) {
	now := businessDay(2026, 9, 25, 14, 30)
	start, end := periodBounds(periodWeek, now)
	price := uuid.Must(uuid.NewV7())
	priceByOption := map[uuid.UUID]int64{price: 5000}

	rows := []Queue{
		{Status: StatusReady, PriceOptionID: price, ScheduledStartAt: businessDay(2026, 9, 19, 10, 0), ScheduledEndAt: businessDay(2026, 9, 19, 10, 30)},
		{Status: StatusReady, PriceOptionID: price, ScheduledStartAt: businessDay(2026, 9, 25, 10, 0), ScheduledEndAt: businessDay(2026, 9, 25, 10, 30)},
	}

	bars := reportBars(periodWeek, start, end, now, rows, priceByOption)
	if len(bars) != 7 {
		t.Fatalf("len(bars) = %d, want 7", len(bars))
	}
	if bars[0].RevenueCents != 5000 {
		t.Errorf("bars[0] (first day) RevenueCents = %d, want 5000", bars[0].RevenueCents)
	}
	last := bars[len(bars)-1]
	if last.RevenueCents != 5000 {
		t.Errorf("bars[last] (today) RevenueCents = %d, want 5000", last.RevenueCents)
	}
	if !last.Highlighted {
		t.Errorf("today's bar should be highlighted")
	}
	if bars[0].Highlighted {
		t.Errorf("first day's bar should not be highlighted")
	}
}
