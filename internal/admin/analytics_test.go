package admin

import (
	"testing"
	"time"

	"q-wash-api/internal/platform/clock"
)

func at(day, hour, min int) time.Time {
	return time.Date(2026, 11, day, hour, min, 0, 0, clock.BusinessLocation)
}

func TestWindowFor(t *testing.T) {
	now := at(20, 15, 0)

	today, ok := windowFor("today", now)
	if !ok || !today.from.Equal(at(20, 0, 0)) || !today.to.Equal(at(21, 0, 0)) || !today.prevFrom.Equal(at(19, 0, 0)) || !today.prevTo.Equal(at(20, 0, 0)) {
		t.Errorf("today window wrong: %+v", today)
	}

	week, _ := windowFor("7", now)
	if !week.from.Equal(at(14, 0, 0)) || !week.to.Equal(at(21, 0, 0)) || !week.prevFrom.Equal(at(7, 0, 0)) || !week.prevTo.Equal(at(14, 0, 0)) {
		t.Errorf("7-day window wrong: %+v", week)
	}

	month, _ := windowFor("30", now)
	if !month.from.Equal(at(20, 0, 0).AddDate(0, 0, -29)) || !month.prevTo.Equal(month.from) {
		t.Errorf("30-day window wrong: %+v", month)
	}

	if _, ok := windowFor("year", now); ok {
		t.Error("unknown period must be rejected")
	}
}

func TestSeriesFor_TodayFoldsOutOfHoursIntoEdgeBuckets(t *testing.T) {
	w, _ := windowFor("today", at(20, 12, 0))
	starts := []time.Time{at(20, 6, 30), at(20, 8, 0), at(20, 9, 59), at(20, 13, 0), at(20, 23, 0)}
	got := seriesFor("today", w, starts)
	if len(got) != 7 {
		t.Fatalf("want 7 buckets, got %d", len(got))
	}
	want := []int{3, 0, 1, 0, 0, 0, 1} // 08-10 (incl. 06:30), 12-14, 20-22 (incl. 23:00)
	for i, b := range got {
		if b.Value != want[i] {
			t.Errorf("bucket %d = %d, want %d (%+v)", i, b.Value, want[i], b)
		}
	}
}

func TestSeriesFor_SevenAndThirtyDays(t *testing.T) {
	now := at(20, 12, 0)

	w7, _ := windowFor("7", now)
	got7 := seriesFor("7", w7, []time.Time{at(14, 9, 0), at(20, 9, 0), at(20, 10, 0)})
	if len(got7) != 7 || got7[0].Value != 1 || got7[6].Value != 2 {
		t.Errorf("7-day series wrong: %+v", got7)
	}

	w30, _ := windowFor("30", now)
	got30 := seriesFor("30", w30, []time.Time{w30.from.Add(time.Hour), at(20, 9, 0), at(19, 9, 0)})
	if len(got30) != 5 || got30[0].Value != 1 || got30[4].Value != 2 {
		t.Errorf("30-day series wrong: %+v", got30)
	}
	if !got30[4].End.Equal(w30.to) {
		t.Errorf("last 30-day bucket must end at window end: %+v", got30[4])
	}
}

func TestHealthFor(t *testing.T) {
	now := at(20, 12, 0)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	cases := []struct {
		name string
		last *time.Time
		want string
	}{
		{"never logged in", nil, healthBad},
		{"an hour ago", ago(time.Hour), healthOK},
		{"two days ago", ago(48 * time.Hour), healthOK},
		{"four days ago", ago(96 * time.Hour), healthWarn},
		{"eight days ago", ago(8 * 24 * time.Hour), healthBad},
	}
	for _, c := range cases {
		if got := healthFor(c.last, now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
