package notification

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Scheduler sends the time-based stage notifications (the 1h reminder and
// the "5 minutes late" nudge). There is no queue of jobs: each tick looks at
// the bookings themselves, so a booking that is canceled/started before its
// stage is due is simply never selected, and the (booking, kind) unique
// index keeps several API instances from double-sending.
type Scheduler struct {
	repo     *Repository
	manager  *Manager
	interval time.Duration
}

func NewScheduler(repo *Repository, manager *Manager, interval time.Duration) *Scheduler {
	return &Scheduler{repo: repo, manager: manager, interval: interval}
}

var timeBasedKinds = []Kind{KindReminder, KindLate}

// Tick sends everything due at now. One failing booking doesn't stop the rest.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	var errs []error
	for _, kind := range timeBasedKinds {
		due, err := s.repo.FindDue(ctx, kind, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for i := range due {
			if err := s.manager.NotifyStage(ctx, &due[i], kind); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Run ticks until ctx is canceled.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.Tick(ctx, now); err != nil {
				slog.ErrorContext(ctx, "notification scheduler tick failed", "err", err)
			}
		}
	}
}
