package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/platform/clock"
)

// The platform rules from the admin "Настройки" screen
// (internal/platformsettings) that act on bookings.

// checkHorizon rejects a customer booking that starts after the end of the
// day that is booking_horizon_days from today (business timezone).
func (m *Manager) checkHorizon(ctx context.Context, start time.Time) error {
	s, ok, err := m.currentSettings(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	dayStart, _ := clock.DayBounds(time.Now())
	limit := dayStart.AddDate(0, 0, s.BookingHorizonDays+1)
	if !start.Before(limit) {
		return apperror.BadRequest("too_far_ahead", fmt.Sprintf("bookings can be made at most %d days ahead", s.BookingHorizonDays))
	}
	return nil
}

// EnforceCancelCutoff is called by the HTTP layer when a *customer* cancels
// their own booking: inside the last client_cancel_cutoff_min minutes before
// the scheduled start (0 = off) a pending/queued booking can no longer be
// canceled by the customer — staff, workers and admins are not limited.
func (m *Manager) EnforceCancelCutoff(ctx context.Context, q *Queue, now time.Time) error {
	if q.Status != StatusPending && q.Status != StatusQueue {
		return nil
	}
	s, ok, err := m.currentSettings(ctx)
	if err != nil {
		return err
	}
	if !ok || s.ClientCancelCutoffMin <= 0 {
		return nil
	}
	if q.ScheduledStartAt.Sub(now) < time.Duration(s.ClientCancelCutoffMin)*time.Minute {
		return apperror.Conflict("cancel_too_late", fmt.Sprintf("a booking can only be canceled at least %d minutes before it starts", s.ClientCancelCutoffMin))
	}
	return nil
}

// MarkOverdueNoShows marks as no-show every customer booking that is still
// "queue" (the customer never arrived) more than no_show_auto_cancel_min
// minutes after its scheduled start; 0 = off. It returns how many were
// marked. Staff can restore a no-show to the queue (existing transition).
func (m *Manager) MarkOverdueNoShows(ctx context.Context, now time.Time) (int, error) {
	s, ok, err := m.currentSettings(ctx)
	if err != nil || !ok || s.NoShowAutoCancelMin <= 0 {
		return 0, err
	}
	overdue, err := m.repo.FindOverdueQueued(ctx, now.Add(-time.Duration(s.NoShowAutoCancelMin)*time.Minute))
	if err != nil {
		return 0, err
	}
	marked := 0
	var errs []error
	for i := range overdue {
		q := &overdue[i]
		q.Status = StatusNoShow
		// from=queue: a customer cancel or staff "Приехал" that raced us wins.
		if err := m.repo.UpdateStatus(ctx, q, StatusQueue); err != nil {
			var ae *apperror.Error
			if errors.As(err, &ae) && ae.Code == "status_changed" {
				continue
			}
			errs = append(errs, err)
			continue
		}
		marked++
		m.bus.Publish(q.WashingPointID)
	}
	return marked, errors.Join(errs...)
}

// NoShowSweeper runs MarkOverdueNoShows on an interval.
type NoShowSweeper struct {
	manager  *Manager
	interval time.Duration
}

func NewNoShowSweeper(manager *Manager, interval time.Duration) *NoShowSweeper {
	return &NoShowSweeper{manager: manager, interval: interval}
}

// Tick is one sweep; tests call it directly.
func (s *NoShowSweeper) Tick(ctx context.Context, now time.Time) error {
	n, err := s.manager.MarkOverdueNoShows(ctx, now)
	if n > 0 {
		slog.InfoContext(ctx, "bookings auto-marked as no-show", "count", n)
	}
	return err
}

// Run ticks until ctx is canceled.
func (s *NoShowSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.Tick(ctx, now); err != nil {
				slog.ErrorContext(ctx, "no-show sweep failed", "err", err)
			}
		}
	}
}
