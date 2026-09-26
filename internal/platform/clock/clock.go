// Package clock holds the platform's single notion of "local time": every
// booking, schedule, report and notification resolves calendar days and
// wall-clock times in one fixed timezone (docs/architecture.md), so the
// location and the day-boundary helper live here instead of being redefined
// by each feature.
package clock

import (
	"time"

	_ "time/tzdata" // embed the IANA database so LoadLocation works regardless of the host's
)

// BusinessLocation is the fixed timezone washing-point open_time/close_time
// "HH:MM" strings are interpreted in. Washing points have no per-point
// timezone field yet (single-market deployment — see docs/DATA_MODEL.md);
// this app currently only serves Tajikistan (+992 numbers), which has one
// fixed UTC+5 offset with no DST, so a single constant is correct today.
var BusinessLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Dushanbe")
	if err != nil {
		panic("clock.BusinessLocation: failed to load Asia/Dushanbe: " + err.Error())
	}
	return loc
}()

// DayBounds returns [start, end) for the calendar date of day in
// BusinessLocation — midnight to the next midnight, local time.
func DayBounds(day time.Time) (time.Time, time.Time) {
	local := day.In(BusinessLocation)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, BusinessLocation)
	return start, start.AddDate(0, 0, 1)
}
