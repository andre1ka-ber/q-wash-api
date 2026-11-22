package admin

import (
	"testing"
	"time"

	"q-wash-api/internal/platform/clock"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/washingpoint"
)

func TestParseHHMM_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"00:00", 0},
		{"08:00", 480},
		{"09:30", 570},
		{"23:59", 1439},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseHHMM(c.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("parseHHMM(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestParseHHMM_Invalid(t *testing.T) {
	cases := []string{"", "not-a-time", "0900", "12:99", "9:00:00"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := parseHHMM(in); err == nil {
				t.Errorf("expected an error for %q, got none", in)
			}
		})
	}
}

func TestAggregateDay_CountsAndUtilization(t *testing.T) {
	dayStart := time.Date(2026, 11, 20, 0, 0, 0, 0, clock.BusinessLocation)
	dayEnd := dayStart.AddDate(0, 0, 1)
	at := func(h int) time.Time { return dayStart.Add(time.Duration(h) * time.Hour) }

	points := []washingpoint.WashingPoint{
		{Status: washingpoint.StatusActive, BoxesCount: 2, OpenTime: "08:00", CloseTime: "18:00"}, // 1200 box-min
		{Status: washingpoint.StatusPaused, BoxesCount: 5, OpenTime: "08:00", CloseTime: "18:00"}, // ignored
	}
	bookings := []queue.Queue{
		{Status: queue.StatusWaiting, ScheduledStartAt: at(9), ScheduledEndAt: at(11)},                    // 120
		{Status: queue.StatusWaiting, ScheduledStartAt: at(12), ScheduledEndAt: at(13), IsOverflow: true}, // counted, no box time
		{Status: queue.StatusCanceled, ScheduledStartAt: at(14), ScheduledEndAt: at(15)},
		{Status: queue.StatusNoShow, ScheduledStartAt: at(15), ScheduledEndAt: at(16)},
	}

	got := aggregateDay(points, bookings, dayStart, dayEnd)
	if got.bookings != 2 || got.canceled != 2 {
		t.Errorf("bookings/canceled = %d/%d, want 2/2", got.bookings, got.canceled)
	}
	if want := 120.0 / 1200.0; got.utilization < want-1e-9 || got.utilization > want+1e-9 {
		t.Errorf("utilization = %v, want %v", got.utilization, want)
	}
}

func TestAggregateDay_NoActivePointsMeansZeroUtilization(t *testing.T) {
	dayStart := time.Date(2026, 11, 20, 0, 0, 0, 0, clock.BusinessLocation)
	got := aggregateDay(nil, nil, dayStart, dayStart.AddDate(0, 0, 1))
	if got.bookings != 0 || got.canceled != 0 || got.utilization != 0 {
		t.Errorf("got %+v, want zero value", got)
	}
}
