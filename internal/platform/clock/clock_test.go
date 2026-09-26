package clock

import (
	"testing"
	"time"
)

func TestBusinessLocationIsDushanbeUTCPlus5(t *testing.T) {
	_, offset := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).In(BusinessLocation).Zone()
	if offset != 5*60*60 {
		t.Fatalf("offset = %ds, want +5h", offset)
	}
	// no DST: same offset in summer
	_, summer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(BusinessLocation).Zone()
	if summer != offset {
		t.Fatalf("summer offset %d differs from winter %d", summer, offset)
	}
}

func TestDayBoundsUseTheLocalCalendarDay(t *testing.T) {
	// 21:30Z on the 26th is already 02:30 on the 27th in UTC+5.
	start, end := DayBounds(time.Date(2026, 9, 26, 21, 30, 0, 0, time.UTC))

	if got := start.Format(time.RFC3339); got != "2026-09-27T00:00:00+05:00" {
		t.Errorf("start = %s", got)
	}
	if got := end.Format(time.RFC3339); got != "2026-09-28T00:00:00+05:00" {
		t.Errorf("end = %s", got)
	}
	if end.Sub(start) != 24*time.Hour {
		t.Errorf("a day here is 24h (no DST), got %s", end.Sub(start))
	}
}
