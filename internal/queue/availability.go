package queue

import (
	"sort"
	"time"
)

// TimeRange is a half-open interval [Start, End).
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// AvailabilitySlotStep is the granularity at which candidate booking start
// times are generated.
const AvailabilitySlotStep = 15 * time.Minute

// ComputeAvailableSlots generates candidate [start, start+duration) windows
// every AvailabilitySlotStep from dayOpen up to dayClose, and keeps the ones
// where the number of simultaneously busy boxes never reaches boxesCount —
// i.e. there's always at least one free box for the whole candidate window.
// This is a pure function (no DB access) so it can be unit tested directly.
func ComputeAvailableSlots(dayOpen, dayClose time.Time, boxesCount int, duration time.Duration, busy []TimeRange) []TimeRange {
	if duration <= 0 || boxesCount <= 0 || !dayClose.After(dayOpen) {
		return nil
	}

	var slots []TimeRange
	for start := dayOpen; !start.Add(duration).After(dayClose); start = start.Add(AvailabilitySlotStep) {
		end := start.Add(duration)
		if maxOverlap(start, end, busy) < boxesCount {
			slots = append(slots, TimeRange{Start: start, End: end})
		}
	}
	return slots
}

// BusyBoxInterval is a booked interval tied to the specific box it
// occupies — unlike TimeRange, which only tracks aggregate occupancy.
type BusyBoxInterval struct {
	Box   int
	Start time.Time
	End   time.Time
}

// SlotWithBoxes is a candidate booking window plus which specific boxes
// are free for the whole window, so a client can let the user pick one
// instead of the server auto-assigning it.
type SlotWithBoxes struct {
	Start          time.Time
	End            time.Time
	AvailableBoxes []int
}

// ComputeAvailableSlotsWithBoxes is ComputeAvailableSlots' per-box sibling:
// same candidate-window generation, but instead of a single boxesCount
// threshold it tracks which individual box numbers are free for each
// window and drops windows where none are. A pure function for the same
// reason as ComputeAvailableSlots (unit-testable, no DB access).
func ComputeAvailableSlotsWithBoxes(dayOpen, dayClose time.Time, boxesCount int, duration time.Duration, busy []BusyBoxInterval) []SlotWithBoxes {
	if duration <= 0 || boxesCount <= 0 || !dayClose.After(dayOpen) {
		return nil
	}

	var slots []SlotWithBoxes
	for start := dayOpen; !start.Add(duration).After(dayClose); start = start.Add(AvailabilitySlotStep) {
		end := start.Add(duration)
		if free := freeBoxes(start, end, boxesCount, busy); len(free) > 0 {
			slots = append(slots, SlotWithBoxes{Start: start, End: end, AvailableBoxes: free})
		}
	}
	return slots
}

// freeBoxes returns, in ascending order, every box in 1..boxesCount not
// occupied by a busy interval overlapping [start, end). Same overlap test
// as Manager.verifyBoxAvailable, generalized to report every free box
// instead of checking just one.
func freeBoxes(start, end time.Time, boxesCount int, busy []BusyBoxInterval) []int {
	occupied := make(map[int]bool, len(busy))
	for _, b := range busy {
		if b.Start.Before(end) && b.End.After(start) {
			occupied[b.Box] = true
		}
	}
	free := make([]int, 0, boxesCount)
	for box := 1; box <= boxesCount; box++ {
		if !occupied[box] {
			free = append(free, box)
		}
	}
	return free
}

// DaySchedule is the resolved operating window(s) for one calendar day —
// real businessLocation instants derived from a schedule.WashingPointSchedule
// row (docs/PLAN_WEB_APPS.md phase 5), replacing the flat
// WashingPoint.OpenTime/CloseTime columns as the source of truth for both
// GET .../availability and Manager.CreateBooking's operating-hours check.
type DaySchedule struct {
	IsOpen     bool
	Open       time.Time
	Close      time.Time
	BreakStart *time.Time
	BreakEnd   *time.Time
}

// Windows returns the day's open sub-windows in chronological order: one
// when there's no break, two when there is (split around it, dropping
// either side the break entirely swallows), none when the day is closed
// or malformed (close <= open). A break outside [Open, Close) is clipped
// to it first so a bad row can't produce a negative-length window.
func (d DaySchedule) Windows() []TimeRange {
	if !d.IsOpen || !d.Close.After(d.Open) {
		return nil
	}
	if d.BreakStart == nil || d.BreakEnd == nil {
		return []TimeRange{{Start: d.Open, End: d.Close}}
	}

	breakStart, breakEnd := *d.BreakStart, *d.BreakEnd
	if breakStart.Before(d.Open) {
		breakStart = d.Open
	}
	if breakEnd.After(d.Close) {
		breakEnd = d.Close
	}
	if !breakEnd.After(breakStart) {
		return []TimeRange{{Start: d.Open, End: d.Close}}
	}

	var windows []TimeRange
	if breakStart.After(d.Open) {
		windows = append(windows, TimeRange{Start: d.Open, End: breakStart})
	}
	if breakEnd.Before(d.Close) {
		windows = append(windows, TimeRange{Start: breakEnd, End: d.Close})
	}
	return windows
}

// Contains reports whether [start, end) fits entirely within one of the
// day's open sub-windows — i.e. it doesn't start before opening, end
// after closing, or straddle a break. Used by Manager.CreateBooking in
// place of the old flat open_time/close_time bounds check.
func (d DaySchedule) Contains(start, end time.Time) bool {
	for _, w := range d.Windows() {
		if !start.Before(w.Start) && !end.After(w.End) {
			return true
		}
	}
	return false
}

// ComputeAvailableSlotsForDay runs ComputeAvailableSlotsWithBoxes over each
// of the day's open sub-windows and concatenates the results — already in
// chronological order, since Windows() returns them that way.
func ComputeAvailableSlotsForDay(day DaySchedule, boxesCount int, duration time.Duration, busy []BusyBoxInterval) []SlotWithBoxes {
	var all []SlotWithBoxes
	for _, w := range day.Windows() {
		all = append(all, ComputeAvailableSlotsWithBoxes(w.Start, w.End, boxesCount, duration, busy)...)
	}
	return all
}

// maxOverlap returns the maximum number of busy intervals simultaneously
// active at any instant within [start, end). It clips each busy interval to
// the candidate window and sweeps the resulting +1/-1 events; an interval
// ending exactly when another starts does not count as overlapping, which
// matches the DB's tstzrange exclusion constraint semantics.
func maxOverlap(start, end time.Time, busy []TimeRange) int {
	type event struct {
		at    time.Time
		delta int
	}

	events := make([]event, 0, len(busy)*2)
	for _, b := range busy {
		if !b.Start.Before(end) || !b.End.After(start) {
			continue // no overlap with [start, end)
		}
		s, e := b.Start, b.End
		if s.Before(start) {
			s = start
		}
		if e.After(end) {
			e = end
		}
		events = append(events, event{s, 1}, event{e, -1})
	}

	sort.Slice(events, func(i, j int) bool {
		if events[i].at.Equal(events[j].at) {
			return events[i].delta < events[j].delta // process ends before starts at the same instant
		}
		return events[i].at.Before(events[j].at)
	})

	occupied, max := 0, 0
	for _, e := range events {
		occupied += e.delta
		if occupied > max {
			max = occupied
		}
	}
	return max
}
