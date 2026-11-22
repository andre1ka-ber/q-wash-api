package queue

import (
	"context"
	"testing"
	"time"

	"q-wash-api/internal/platform/clock"
	"q-wash-api/internal/platformsettings"
)

type fakeSettings platformsettings.Settings

func (f fakeSettings) Get(context.Context) (platformsettings.Settings, error) {
	return platformsettings.Settings(f), nil
}

func managerWith(s platformsettings.Settings) *Manager {
	m := &Manager{}
	m.SetSettings(fakeSettings(s))
	return m
}

func TestCheckHorizon(t *testing.T) {
	m := managerWith(platformsettings.Settings{BookingHorizonDays: 3})
	today, _ := clock.DayBounds(time.Now())

	if err := m.checkHorizon(context.Background(), today.AddDate(0, 0, 3).Add(23*time.Hour)); err != nil {
		t.Errorf("late on day +3 is inside a 3-day horizon: %v", err)
	}
	if err := m.checkHorizon(context.Background(), today.AddDate(0, 0, 4).Add(time.Hour)); err == nil {
		t.Error("day +4 must be rejected")
	}
	if err := (&Manager{}).checkHorizon(context.Background(), today.AddDate(0, 0, 400)); err != nil {
		t.Errorf("without settings no rule applies: %v", err)
	}
}

func TestEnforceCancelCutoff(t *testing.T) {
	now := time.Now()
	m := managerWith(platformsettings.Settings{ClientCancelCutoffMin: 30})
	soon := &Queue{Status: StatusQueue, ScheduledStartAt: now.Add(20 * time.Minute)}
	later := &Queue{Status: StatusQueue, ScheduledStartAt: now.Add(2 * time.Hour)}

	if err := m.EnforceCancelCutoff(context.Background(), soon, now); err == nil {
		t.Error("20 minutes before the start is inside a 30-minute cutoff")
	}
	if err := m.EnforceCancelCutoff(context.Background(), later, now); err != nil {
		t.Errorf("2h before the start is fine: %v", err)
	}
	soon.Status = StatusWaiting // the customer already arrived
	if err := m.EnforceCancelCutoff(context.Background(), soon, now); err != nil {
		t.Errorf("an arrived customer is never blocked: %v", err)
	}
	soon.Status = StatusQueue
	if err := managerWith(platformsettings.Settings{ClientCancelCutoffMin: 0}).EnforceCancelCutoff(context.Background(), soon, now); err != nil {
		t.Errorf("0 means off: %v", err)
	}
}
