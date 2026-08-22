package schedule

import (
	"context"
	"regexp"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
)

var timeFormatRegexp = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// Input is one weekday's requested schedule row, decoded from the PUT
// request body — see Manager.Replace.
type Input struct {
	Weekday    int
	IsOpen     bool
	OpenTime   *string
	CloseTime  *string
	BreakStart *string
	BreakEnd   *string
}

// Manager validates a full-week schedule replacement and keeps
// "exactly one row per weekday" true — plain CRUD wouldn't otherwise
// guard against duplicate/missing weekdays or malformed hours.
type Manager struct {
	repo *Repository
}

func NewManager(repo *Repository) *Manager {
	return &Manager{repo: repo}
}

// Replace validates inputs (must cover each weekday 0..6 exactly once,
// with well-formed, consistent hours) and atomically replaces
// washingPointID's full 7-day schedule.
func (m *Manager) Replace(ctx context.Context, washingPointID uuid.UUID, inputs []Input) ([]WashingPointSchedule, error) {
	if len(inputs) != 7 {
		return nil, apperror.BadRequest("invalid_schedule", "exactly 7 rows are required, one per weekday (0=Monday..6=Sunday)")
	}

	rows := make([]WashingPointSchedule, 7)
	seen := make(map[int]bool, 7)
	for _, in := range inputs {
		if in.Weekday < 0 || in.Weekday > 6 {
			return nil, apperror.BadRequest("invalid_weekday", "weekday must be between 0 (Monday) and 6 (Sunday)")
		}
		if seen[in.Weekday] {
			return nil, apperror.BadRequest("duplicate_weekday", "each weekday must appear exactly once")
		}
		seen[in.Weekday] = true

		row := WashingPointSchedule{Weekday: in.Weekday, IsOpen: in.IsOpen}
		if in.IsOpen {
			if err := validateHours(&row, in); err != nil {
				return nil, err
			}
		}
		rows[in.Weekday] = row
	}
	// len(inputs) == 7 with 7 distinct weekdays each in [0,6] necessarily
	// covers every weekday exactly once — no separate "all present" check
	// needed beyond the count + duplicate checks above.

	if err := m.repo.ReplaceAll(ctx, washingPointID, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func validateHours(row *WashingPointSchedule, in Input) error {
	if in.OpenTime == nil || in.CloseTime == nil {
		return apperror.BadRequest("invalid_hours", "open_time and close_time are required when is_open is true")
	}
	if !timeFormatRegexp.MatchString(*in.OpenTime) || !timeFormatRegexp.MatchString(*in.CloseTime) {
		return apperror.BadRequest("invalid_hours", "open_time/close_time must be in HH:MM 24h format")
	}
	if *in.CloseTime <= *in.OpenTime {
		return apperror.BadRequest("invalid_hours", "close_time must be after open_time")
	}
	row.OpenTime = in.OpenTime
	row.CloseTime = in.CloseTime

	if in.BreakStart == nil && in.BreakEnd == nil {
		return nil
	}
	if in.BreakStart == nil || in.BreakEnd == nil {
		return apperror.BadRequest("invalid_break", "break_start and break_end must both be set or both omitted")
	}
	if !timeFormatRegexp.MatchString(*in.BreakStart) || !timeFormatRegexp.MatchString(*in.BreakEnd) {
		return apperror.BadRequest("invalid_break", "break_start/break_end must be in HH:MM 24h format")
	}
	if *in.BreakEnd <= *in.BreakStart {
		return apperror.BadRequest("invalid_break", "break_end must be after break_start")
	}
	if *in.BreakStart < *in.OpenTime || *in.BreakEnd > *in.CloseTime {
		return apperror.BadRequest("invalid_break", "break must fall within open_time/close_time")
	}
	row.BreakStart = in.BreakStart
	row.BreakEnd = in.BreakEnd
	return nil
}

// SeedDefault creates the initial 7-row schedule for a newly created
// washing point — the same hours every day, always open, no break —
// matching migration 000016's backfill behavior for points that existed
// before this table did. Called from washingpoint.Handler.create and
// connectionrequest.Manager.Approve (both accept this as a small,
// locally-defined interface to avoid importing this package back into
// washingpoint, which this package already imports the other way for its
// own ownership checks).
func (m *Manager) SeedDefault(ctx context.Context, washingPointID uuid.UUID, openTime, closeTime string) error {
	rows := make([]WashingPointSchedule, 7)
	for weekday := 0; weekday < 7; weekday++ {
		rows[weekday] = WashingPointSchedule{Weekday: weekday, IsOpen: true, OpenTime: &openTime, CloseTime: &closeTime}
	}
	return m.repo.ReplaceAll(ctx, washingPointID, rows)
}
