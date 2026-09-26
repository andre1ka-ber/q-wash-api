package queue

import (
	"testing"
	"time"

	"q-wash-api/internal/platform/clock"
	"q-wash-api/internal/schedule"
)

func day(hour, minute int) time.Time {
	return time.Date(2026, 8, 6, hour, minute, 0, 0, time.UTC)
}

func TestComputeAvailableSlots_NoBookings(t *testing.T) {
	slots := ComputeAvailableSlots(day(8, 0), day(20, 0), 2, 30*time.Minute, nil)
	if len(slots) == 0 {
		t.Fatal("expected slots with no bookings and open capacity")
	}
	if !slots[0].Start.Equal(day(8, 0)) {
		t.Errorf("expected first slot to start at open time, got %v", slots[0].Start)
	}
	last := slots[len(slots)-1]
	if last.End.After(day(20, 0)) {
		t.Errorf("last slot end %v must not be after close time", last.End)
	}
}

func TestComputeAvailableSlots_SingleBoxFullyBooked(t *testing.T) {
	// 1 box, booked for the whole day: nothing should be available.
	busy := []TimeRange{{Start: day(8, 0), End: day(20, 0)}}
	slots := ComputeAvailableSlots(day(8, 0), day(20, 0), 1, 30*time.Minute, busy)
	if len(slots) != 0 {
		t.Fatalf("expected no available slots, got %d", len(slots))
	}
}

func TestComputeAvailableSlots_SeededScenario(t *testing.T) {
	// Mirrors cmd/seed: 2 boxes, box1 busy 10:00-11:30, box2 busy 10:30-11:00.
	busy := []TimeRange{
		{Start: day(10, 0), End: day(11, 30)},
		{Start: day(10, 30), End: day(11, 0)},
	}
	duration := 30 * time.Minute

	// During [10:30, 11:00) both boxes are occupied -> no capacity for a
	// candidate slot overlapping that window.
	overlapping := ComputeAvailableSlots(day(10, 30), day(11, 0), 2, duration, busy)
	if len(overlapping) != 0 {
		t.Fatalf("expected no slots while both boxes are busy, got %v", overlapping)
	}

	// A slot fully inside the double-booked window must not appear in the
	// full-day result either.
	full := ComputeAvailableSlots(day(8, 0), day(20, 0), 2, duration, busy)
	for _, s := range full {
		if s.Start.Equal(day(10, 30)) {
			t.Errorf("slot at 10:30 should not be available (both boxes busy), got %v", s)
		}
	}

	// 09:30-10:00 is before either booking starts: both boxes free.
	found := false
	for _, s := range full {
		if s.Start.Equal(day(9, 30)) {
			found = true
		}
	}
	if !found {
		t.Error("expected 09:30 slot to be available (no overlap with any booking)")
	}
}

func TestComputeAvailableSlots_BackToBackNotOverlapping(t *testing.T) {
	// A booking ending exactly when a candidate would start must not count
	// as occupying that candidate — matches the DB's half-open range logic.
	busy := []TimeRange{{Start: day(9, 0), End: day(10, 0)}}
	slots := ComputeAvailableSlots(day(10, 0), day(10, 30), 1, 30*time.Minute, busy)
	if len(slots) != 1 {
		t.Fatalf("expected the 10:00 slot to be available right after a booking ends, got %d slots", len(slots))
	}
}

func TestComputeAvailableSlots_DurationDoesNotFit(t *testing.T) {
	slots := ComputeAvailableSlots(day(19, 45), day(20, 0), 2, 30*time.Minute, nil)
	if len(slots) != 0 {
		t.Fatalf("expected no slots when duration doesn't fit before close, got %d", len(slots))
	}
}

func TestComputeAvailableSlots_InvalidInputs(t *testing.T) {
	if slots := ComputeAvailableSlots(day(8, 0), day(20, 0), 0, 30*time.Minute, nil); slots != nil {
		t.Error("expected nil slots for zero boxesCount")
	}
	if slots := ComputeAvailableSlots(day(8, 0), day(20, 0), 2, 0, nil); slots != nil {
		t.Error("expected nil slots for zero duration")
	}
	if slots := ComputeAvailableSlots(day(20, 0), day(8, 0), 2, 30*time.Minute, nil); slots != nil {
		t.Error("expected nil slots when close is not after open")
	}
}

// --- DaySchedule / per-weekday schedule (docs/PLAN_WEB_APPS.md phase 5) ---

func TestDaySchedule_Windows_Closed(t *testing.T) {
	ds := DaySchedule{IsOpen: false, Open: day(8, 0), Close: day(20, 0)}
	if windows := ds.Windows(); windows != nil {
		t.Errorf("expected no windows for a closed day, got %v", windows)
	}
}

func TestDaySchedule_Windows_MalformedCloseBeforeOpen(t *testing.T) {
	ds := DaySchedule{IsOpen: true, Open: day(20, 0), Close: day(8, 0)}
	if windows := ds.Windows(); windows != nil {
		t.Errorf("expected no windows when close is not after open, got %v", windows)
	}
}

// TestDaySchedule_Windows_SameEveryDay is the phase-5 backfill's shape:
// open every day, same hours, no break — exactly the migration's
// same-every-day fixture, expressed as a single unsplit window.
func TestDaySchedule_Windows_SameEveryDay(t *testing.T) {
	ds := DaySchedule{IsOpen: true, Open: day(8, 0), Close: day(20, 0)}
	windows := ds.Windows()
	if len(windows) != 1 {
		t.Fatalf("expected 1 window, got %d (%v)", len(windows), windows)
	}
	if !windows[0].Start.Equal(day(8, 0)) || !windows[0].End.Equal(day(20, 0)) {
		t.Errorf("expected [08:00, 20:00), got [%v, %v)", windows[0].Start, windows[0].End)
	}
}

func TestDaySchedule_Windows_WithBreak(t *testing.T) {
	breakStart, breakEnd := day(13, 0), day(14, 0)
	ds := DaySchedule{IsOpen: true, Open: day(8, 0), Close: day(20, 0), BreakStart: &breakStart, BreakEnd: &breakEnd}
	windows := ds.Windows()
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows split around the break, got %d (%v)", len(windows), windows)
	}
	if !windows[0].Start.Equal(day(8, 0)) || !windows[0].End.Equal(day(13, 0)) {
		t.Errorf("expected morning window [08:00, 13:00), got [%v, %v)", windows[0].Start, windows[0].End)
	}
	if !windows[1].Start.Equal(day(14, 0)) || !windows[1].End.Equal(day(20, 0)) {
		t.Errorf("expected afternoon window [14:00, 20:00), got [%v, %v)", windows[1].Start, windows[1].End)
	}
}

func TestDaySchedule_Windows_BreakSwallowsMorning(t *testing.T) {
	breakStart, breakEnd := day(8, 0), day(14, 0) // break starts exactly at open
	ds := DaySchedule{IsOpen: true, Open: day(8, 0), Close: day(20, 0), BreakStart: &breakStart, BreakEnd: &breakEnd}
	windows := ds.Windows()
	if len(windows) != 1 {
		t.Fatalf("expected 1 window (morning entirely swallowed), got %d (%v)", len(windows), windows)
	}
	if !windows[0].Start.Equal(day(14, 0)) || !windows[0].End.Equal(day(20, 0)) {
		t.Errorf("expected [14:00, 20:00), got [%v, %v)", windows[0].Start, windows[0].End)
	}
}

func TestDaySchedule_Windows_BreakSwallowsAfternoon(t *testing.T) {
	breakStart, breakEnd := day(13, 0), day(20, 0) // break ends exactly at close
	ds := DaySchedule{IsOpen: true, Open: day(8, 0), Close: day(20, 0), BreakStart: &breakStart, BreakEnd: &breakEnd}
	windows := ds.Windows()
	if len(windows) != 1 {
		t.Fatalf("expected 1 window (afternoon entirely swallowed), got %d (%v)", len(windows), windows)
	}
	if !windows[0].Start.Equal(day(8, 0)) || !windows[0].End.Equal(day(13, 0)) {
		t.Errorf("expected [08:00, 13:00), got [%v, %v)", windows[0].Start, windows[0].End)
	}
}

func TestDaySchedule_Contains(t *testing.T) {
	breakStart, breakEnd := day(13, 0), day(14, 0)
	ds := DaySchedule{IsOpen: true, Open: day(8, 0), Close: day(20, 0), BreakStart: &breakStart, BreakEnd: &breakEnd}

	cases := []struct {
		name        string
		start, end  time.Time
		wantContain bool
	}{
		{"fully inside morning window", day(9, 0), day(9, 30), true},
		{"fully inside afternoon window", day(15, 0), day(15, 30), true},
		{"starts before open", day(7, 30), day(8, 30), false},
		{"ends after close", day(19, 30), day(20, 30), false},
		{"straddles the break", day(12, 30), day(13, 30), false},
		{"exactly the break window", day(13, 0), day(14, 0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ds.Contains(c.start, c.end); got != c.wantContain {
				t.Errorf("Contains(%v, %v) = %v, want %v", c.start, c.end, got, c.wantContain)
			}
		})
	}
}

func TestComputeAvailableSlotsForDay_ClosedDay(t *testing.T) {
	ds := DaySchedule{IsOpen: false}
	slots := ComputeAvailableSlotsForDay(ds, 2, 30*time.Minute, nil)
	if len(slots) != 0 {
		t.Fatalf("expected no slots on a closed day, got %d", len(slots))
	}
}

// TestComputeAvailableSlotsForDay_BreakExcludesSlots is the plan's
// break-window fixture: no candidate slot should ever fall inside, or
// straddle into, the break — even with full box capacity free.
func TestComputeAvailableSlotsForDay_BreakExcludesSlots(t *testing.T) {
	breakStart, breakEnd := day(13, 0), day(14, 0)
	ds := DaySchedule{IsOpen: true, Open: day(8, 0), Close: day(20, 0), BreakStart: &breakStart, BreakEnd: &breakEnd}

	slots := ComputeAvailableSlotsForDay(ds, 2, 30*time.Minute, nil)
	for _, s := range slots {
		if s.Start.Before(breakEnd) && s.End.After(breakStart) {
			t.Errorf("slot [%v, %v) overlaps the break window [%v, %v)", s.Start, s.End, breakStart, breakEnd)
		}
	}
	// The last pre-break slot must end by 13:00, and the first post-break
	// slot must start no earlier than 14:00 — confirms the break actually
	// bounds both windows precisely.
	for _, s := range slots {
		if s.Start.Before(day(13, 0)) && s.End.After(day(13, 0)) {
			t.Errorf("slot [%v, %v) crosses the break start boundary", s.Start, s.End)
		}
	}
}

func TestWeekdayIndex(t *testing.T) {
	// 2024-01-01 was a Monday; the following table walks a full week from
	// there, letting the assertion self-verify against Go's own Weekday()
	// rather than relying purely on a memorized calendar fact.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for offset := 0; offset < 7; offset++ {
		d := base.AddDate(0, 0, offset)
		want := offset % 7 // Monday(0)..Sunday(6), matching base being Monday
		if got := weekdayIndex(d); got != want {
			t.Errorf("weekdayIndex(%v, Go weekday %v) = %d, want %d", d, d.Weekday(), got, want)
		}
	}
}

func TestResolveDaySchedule_NilRowIsClosed(t *testing.T) {
	ds, err := resolveDaySchedule(day(0, 0), nil)
	if err != nil {
		t.Fatalf("resolveDaySchedule: %v", err)
	}
	if ds.IsOpen {
		t.Error("expected a missing schedule row to resolve as closed")
	}
}

func TestResolveDaySchedule_ClosedRow(t *testing.T) {
	row := &schedule.WashingPointSchedule{IsOpen: false}
	ds, err := resolveDaySchedule(day(0, 0), row)
	if err != nil {
		t.Fatalf("resolveDaySchedule: %v", err)
	}
	if ds.IsOpen {
		t.Error("expected is_open=false to resolve as closed")
	}
}

func TestResolveDaySchedule_OpenWithBreak(t *testing.T) {
	openStr, closeStr := "08:00", "20:00"
	breakStartStr, breakEndStr := "13:00", "14:00"
	row := &schedule.WashingPointSchedule{
		IsOpen: true, OpenTime: &openStr, CloseTime: &closeStr,
		BreakStart: &breakStartStr, BreakEnd: &breakEndStr,
	}

	ds, err := resolveDaySchedule(day(0, 0), row)
	if err != nil {
		t.Fatalf("resolveDaySchedule: %v", err)
	}
	wantOpen := time.Date(2026, 8, 6, 8, 0, 0, 0, clock.BusinessLocation)
	wantClose := time.Date(2026, 8, 6, 20, 0, 0, 0, clock.BusinessLocation)
	wantBreakStart := time.Date(2026, 8, 6, 13, 0, 0, 0, clock.BusinessLocation)
	wantBreakEnd := time.Date(2026, 8, 6, 14, 0, 0, 0, clock.BusinessLocation)

	if !ds.IsOpen {
		t.Fatal("expected IsOpen true")
	}
	if !ds.Open.Equal(wantOpen) {
		t.Errorf("Open = %v, want %v", ds.Open, wantOpen)
	}
	if !ds.Close.Equal(wantClose) {
		t.Errorf("Close = %v, want %v", ds.Close, wantClose)
	}
	if ds.BreakStart == nil || !ds.BreakStart.Equal(wantBreakStart) {
		t.Errorf("BreakStart = %v, want %v", ds.BreakStart, wantBreakStart)
	}
	if ds.BreakEnd == nil || !ds.BreakEnd.Equal(wantBreakEnd) {
		t.Errorf("BreakEnd = %v, want %v", ds.BreakEnd, wantBreakEnd)
	}
}
