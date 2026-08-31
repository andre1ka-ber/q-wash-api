package schedule

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
)

func errCode(t *testing.T, err error) string {
	t.Helper()
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected an *apperror.Error, got %T: %v", err, err)
	}
	return appErr.Code
}

func strPtr(s string) *string { return &s }

func TestValidateHours_MissingOpenOrCloseTime(t *testing.T) {
	cases := []struct {
		name      string
		openTime  *string
		closeTime *string
	}{
		{"both nil", nil, nil},
		{"open nil", nil, strPtr("09:00")},
		{"close nil", strPtr("09:00"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var row WashingPointSchedule
			err := validateHours(&row, Input{OpenTime: c.openTime, CloseTime: c.closeTime})
			if got := errCode(t, err); got != "invalid_hours" {
				t.Errorf("expected invalid_hours, got %q", got)
			}
		})
	}
}

func TestValidateHours_MalformedTimeFormat(t *testing.T) {
	cases := []struct {
		name                string
		openTime, closeTime string
	}{
		{"single digit hour", "9:00", "20:00"},
		{"hour out of range", "24:00", "20:00"},
		{"minute out of range", "09:60", "20:00"},
		{"no colon", "0900", "2000"},
		{"garbage", "not-a-time", "20:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var row WashingPointSchedule
			err := validateHours(&row, Input{OpenTime: strPtr(c.openTime), CloseTime: strPtr(c.closeTime)})
			if got := errCode(t, err); got != "invalid_hours" {
				t.Errorf("expected invalid_hours, got %q", got)
			}
		})
	}
}

func TestValidateHours_CloseNotAfterOpen(t *testing.T) {
	cases := []struct {
		name                string
		openTime, closeTime string
	}{
		{"close before open", "20:00", "09:00"},
		{"close equals open", "09:00", "09:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var row WashingPointSchedule
			err := validateHours(&row, Input{OpenTime: strPtr(c.openTime), CloseTime: strPtr(c.closeTime)})
			if got := errCode(t, err); got != "invalid_hours" {
				t.Errorf("expected invalid_hours, got %q", got)
			}
		})
	}
}

func TestValidateHours_NoBreakIsValid(t *testing.T) {
	var row WashingPointSchedule
	err := validateHours(&row, Input{OpenTime: strPtr("09:00"), CloseTime: strPtr("20:00")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if row.BreakStart != nil || row.BreakEnd != nil {
		t.Errorf("expected no break to be set, got start=%v end=%v", row.BreakStart, row.BreakEnd)
	}
	if row.OpenTime == nil || *row.OpenTime != "09:00" || row.CloseTime == nil || *row.CloseTime != "20:00" {
		t.Errorf("expected open/close to be recorded on the row, got open=%v close=%v", row.OpenTime, row.CloseTime)
	}
}

func TestValidateHours_OnlyOneBreakFieldSet(t *testing.T) {
	cases := []struct {
		name       string
		breakStart *string
		breakEnd   *string
	}{
		{"start only", strPtr("13:00"), nil},
		{"end only", nil, strPtr("14:00")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var row WashingPointSchedule
			err := validateHours(&row, Input{
				OpenTime: strPtr("09:00"), CloseTime: strPtr("20:00"),
				BreakStart: c.breakStart, BreakEnd: c.breakEnd,
			})
			if got := errCode(t, err); got != "invalid_break" {
				t.Errorf("expected invalid_break, got %q", got)
			}
		})
	}
}

func TestValidateHours_MalformedBreakFormat(t *testing.T) {
	var row WashingPointSchedule
	err := validateHours(&row, Input{
		OpenTime: strPtr("09:00"), CloseTime: strPtr("20:00"),
		BreakStart: strPtr("13:00"), BreakEnd: strPtr("not-a-time"),
	})
	if got := errCode(t, err); got != "invalid_break" {
		t.Errorf("expected invalid_break, got %q", got)
	}
}

func TestValidateHours_BreakEndNotAfterBreakStart(t *testing.T) {
	var row WashingPointSchedule
	err := validateHours(&row, Input{
		OpenTime: strPtr("09:00"), CloseTime: strPtr("20:00"),
		BreakStart: strPtr("14:00"), BreakEnd: strPtr("13:00"),
	})
	if got := errCode(t, err); got != "invalid_break" {
		t.Errorf("expected invalid_break, got %q", got)
	}
}

func TestValidateHours_BreakOutsideOpenCloseRange(t *testing.T) {
	cases := []struct {
		name       string
		breakStart string
		breakEnd   string
	}{
		{"starts before open", "08:00", "10:00"},
		{"ends after close", "19:00", "21:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var row WashingPointSchedule
			err := validateHours(&row, Input{
				OpenTime: strPtr("09:00"), CloseTime: strPtr("20:00"),
				BreakStart: strPtr(c.breakStart), BreakEnd: strPtr(c.breakEnd),
			})
			if got := errCode(t, err); got != "invalid_break" {
				t.Errorf("expected invalid_break, got %q", got)
			}
		})
	}
}

func TestValidateHours_ValidBreakIsRecorded(t *testing.T) {
	var row WashingPointSchedule
	err := validateHours(&row, Input{
		OpenTime: strPtr("09:00"), CloseTime: strPtr("20:00"),
		BreakStart: strPtr("13:00"), BreakEnd: strPtr("14:00"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if row.BreakStart == nil || *row.BreakStart != "13:00" || row.BreakEnd == nil || *row.BreakEnd != "14:00" {
		t.Errorf("expected break to be recorded, got start=%v end=%v", row.BreakStart, row.BreakEnd)
	}
}

func mkInput(weekday int, isOpen bool) Input {
	if !isOpen {
		return Input{Weekday: weekday, IsOpen: false}
	}
	return Input{Weekday: weekday, IsOpen: true, OpenTime: strPtr("09:00"), CloseTime: strPtr("20:00")}
}

func TestReplace_WrongRowCount(t *testing.T) {
	m := &Manager{}
	_, err := m.Replace(context.Background(), uuid.Nil, []Input{mkInput(0, true)})
	if got := errCode(t, err); got != "invalid_schedule" {
		t.Errorf("expected invalid_schedule, got %q", got)
	}
}

func TestReplace_WeekdayOutOfRange(t *testing.T) {
	inputs := make([]Input, 7)
	for i := 0; i < 7; i++ {
		inputs[i] = mkInput(i, false)
	}
	inputs[0].Weekday = 7 // valid range is 0..6

	m := &Manager{}
	_, err := m.Replace(context.Background(), uuid.Nil, inputs)
	if got := errCode(t, err); got != "invalid_weekday" {
		t.Errorf("expected invalid_weekday, got %q", got)
	}
}

func TestReplace_DuplicateWeekday(t *testing.T) {
	inputs := make([]Input, 7)
	for i := 0; i < 7; i++ {
		inputs[i] = mkInput(i, false)
	}
	inputs[1].Weekday = 0 // duplicates inputs[0]'s weekday

	m := &Manager{}
	_, err := m.Replace(context.Background(), uuid.Nil, inputs)
	if got := errCode(t, err); got != "duplicate_weekday" {
		t.Errorf("expected duplicate_weekday, got %q", got)
	}
}
