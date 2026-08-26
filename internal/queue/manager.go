package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/box"
	"q-wash-api/internal/car"
	"q-wash-api/internal/platform/eventbus"
	"q-wash-api/internal/schedule"
	"q-wash-api/internal/service"
	"q-wash-api/internal/washingpoint"
)

// Manager holds the business rules for creating and transitioning
// bookings — the parts that go beyond plain CRUD: validating ownership and
// cross-references, box availability, and the forward-only status machine.
type Manager struct {
	db           *gorm.DB
	repo         *Repository
	carRepo      *car.Repository
	serviceRepo  *service.Repository
	wpRepo       *washingpoint.Repository
	scheduleRepo *schedule.Repository
	boxRepo      *box.Repository
	bus          *eventbus.Bus
}

func NewManager(db *gorm.DB, repo *Repository, carRepo *car.Repository, serviceRepo *service.Repository, wpRepo *washingpoint.Repository, scheduleRepo *schedule.Repository, boxRepo *box.Repository, bus *eventbus.Bus) *Manager {
	return &Manager{db: db, repo: repo, carRepo: carRepo, serviceRepo: serviceRepo, wpRepo: wpRepo, scheduleRepo: scheduleRepo, boxRepo: boxRepo, bus: bus}
}

type CreateBookingInput struct {
	UserID           uuid.UUID
	CarID            uuid.UUID
	ServiceID        uuid.UUID
	PriceOptionID    uuid.UUID
	BoxNumber        int
	ScheduledStartAt time.Time
	Notes            *string
}

// CreateBooking validates the request against the caller's own car, the
// service/price-option/washing-point relationships, and operating hours,
// then assigns a box and inserts the row inside a transaction that locks
// the washing point row for its duration. That lock serializes concurrent
// booking attempts for the same washing point — without it, two
// transactions could both read "box 1 is free" before either commits and
// both try to claim it. The DB's EXCLUDE constraint (see migrations) is
// still there as a last-resort backstop if that ever happened anyway.
func (m *Manager) CreateBooking(ctx context.Context, in CreateBookingInput) (*Queue, error) {
	if _, err := m.carRepo.FindOwnedByID(ctx, in.CarID, in.UserID); err != nil {
		return nil, err
	}

	// Fast path for the common case — the DB's queue_one_active_booking_per_user
	// partial unique index (see Repository.Create) is the actual source of
	// truth and catches the race this check can't (two of the user's own
	// requests landing concurrently).
	hasActive, err := m.repo.HasActiveForUser(ctx, in.UserID)
	if err != nil {
		return nil, err
	}
	if hasActive {
		return nil, apperror.Conflict("active_booking_exists", "you already have an active booking")
	}

	svc, err := m.serviceRepo.FindByID(ctx, in.ServiceID)
	if err != nil {
		return nil, err
	}
	if !svc.IsActive {
		return nil, apperror.BadRequest("service_inactive", "service is not currently available")
	}

	hasPriceOption := false
	for _, po := range svc.PriceOptions {
		if po.ID == in.PriceOptionID {
			hasPriceOption = true
			break
		}
	}
	if !hasPriceOption {
		return nil, apperror.BadRequest("invalid_price_option", "price option does not belong to this service")
	}

	wp, err := m.wpRepo.FindByID(ctx, svc.WashingPointID)
	if err != nil {
		return nil, err
	}
	if wp.Status != washingpoint.StatusActive {
		return nil, apperror.BadRequest("washing_point_inactive", "washing point is not currently available")
	}

	if in.BoxNumber < 1 || in.BoxNumber > wp.BoxesCount {
		return nil, apperror.BadRequest("invalid_box_number", fmt.Sprintf("box_number must be between 1 and %d", wp.BoxesCount))
	}
	requestedBox, err := m.boxRepo.FindByNumber(ctx, wp.ID, in.BoxNumber)
	if err != nil {
		return nil, err
	}
	if requestedBox != nil && !requestedBox.IsOpen {
		return nil, apperror.Conflict("box_closed", "the requested box is currently closed")
	}

	if !in.ScheduledStartAt.After(time.Now()) {
		return nil, apperror.BadRequest("invalid_scheduled_start_at", "scheduled_start_at must be in the future")
	}

	duration := time.Duration(svc.DurationMinutes) * time.Minute
	scheduledEndAt := in.ScheduledStartAt.Add(duration)

	weekday := weekdayIndex(in.ScheduledStartAt.In(businessLocation))
	scheduleRow, err := m.scheduleRepo.FindByWeekday(ctx, wp.ID, weekday)
	if err != nil {
		return nil, err
	}
	daySchedule, err := resolveDaySchedule(in.ScheduledStartAt, scheduleRow)
	if err != nil {
		return nil, apperror.Internal(err)
	}
	if !daySchedule.Contains(in.ScheduledStartAt, scheduledEndAt) {
		return nil, apperror.BadRequest("outside_operating_hours", "requested time is outside the washing point's operating hours")
	}
	dayOpen, dayClose := daySchedule.Open, daySchedule.Close

	var created *Queue
	txErr := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked washingpoint.WashingPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", wp.ID).Error; err != nil {
			return apperror.Internal(err)
		}

		txRepo := m.repo.WithTx(tx)
		busy, err := txRepo.FindActiveBookingsInRange(ctx, wp.ID, dayOpen, dayClose)
		if err != nil {
			return err
		}

		if err := verifyBoxAvailable(in.BoxNumber, in.ScheduledStartAt, scheduledEndAt, busy); err != nil {
			return err
		}

		q := &Queue{
			Status:           StatusQueue,
			UserID:           in.UserID,
			CarID:            in.CarID,
			ServiceID:        in.ServiceID,
			PriceOptionID:    in.PriceOptionID,
			WashingPointID:   wp.ID,
			BoxNumber:        in.BoxNumber,
			ScheduledStartAt: in.ScheduledStartAt,
			ScheduledEndAt:   scheduledEndAt,
			Notes:            in.Notes,
		}
		if err := txRepo.Create(ctx, q); err != nil {
			return err
		}
		created = q
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	m.bus.Publish(wp.ID)
	return created, nil
}

// verifyBoxAvailable rejects boxNumber if any busy interval occupying that
// same box overlaps [start, end). The box itself was already range-checked
// against wp.BoxesCount before the transaction started; this only checks
// occupancy, which needs the freshly re-read busy set from inside the
// washing-point row lock.
func verifyBoxAvailable(boxNumber int, start, end time.Time, busy []Queue) error {
	for _, b := range busy {
		if b.BoxNumber == boxNumber && b.ScheduledStartAt.Before(end) && b.ScheduledEndAt.After(start) {
			return apperror.Conflict("slot_unavailable", "the requested box is not available for the requested time")
		}
	}
	return nil
}

// CancelBooking only allowed before washing starts. Who may call this is
// decided by the caller (queue.Handler.cancel) before reaching here — the
// booking's owner, or staff/worker/admin at its washing point (broadened
// from owner-only per docs/PLAN_WEB_APPS.md phase 7, for the worker app's
// "Снять" no-show action), same layering as UpdateStatus below.
func (m *Manager) CancelBooking(ctx context.Context, id uuid.UUID) (*Queue, error) {
	q, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if q.Status != StatusQueue && q.Status != StatusWaiting {
		return nil, apperror.Conflict("cannot_cancel", "booking can only be canceled before washing starts")
	}

	now := time.Now()
	q.Status = StatusCanceled
	q.CanceledAt = &now
	if err := m.repo.UpdateStatus(ctx, q); err != nil {
		return nil, err
	}
	m.bus.Publish(q.WashingPointID)
	return q, nil
}

// Pause and Resume toggle PausedAt without moving Status — only valid
// while Status is StatusWashing (docs/PLAN_WEB_APPS.md phase 7). Ownership
// (staff/worker/admin at the booking's washing point) is checked by the
// caller, same layering as CancelBooking/UpdateStatus.
func (m *Manager) Pause(ctx context.Context, id uuid.UUID) (*Queue, error) {
	q, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if q.Status != StatusWashing {
		return nil, apperror.Conflict("cannot_pause", "booking can only be paused while washing")
	}
	if q.PausedAt != nil {
		return nil, apperror.Conflict("cannot_pause", "booking is already paused")
	}
	now := time.Now()
	q.PausedAt = &now
	if err := m.repo.UpdatePausedAt(ctx, q); err != nil {
		return nil, err
	}
	m.bus.Publish(q.WashingPointID)
	return q, nil
}

func (m *Manager) Resume(ctx context.Context, id uuid.UUID) (*Queue, error) {
	q, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if q.Status != StatusWashing {
		return nil, apperror.Conflict("cannot_resume", "booking can only be resumed while washing")
	}
	if q.PausedAt == nil {
		return nil, apperror.Conflict("cannot_resume", "booking is not paused")
	}
	q.PausedAt = nil
	if err := m.repo.UpdatePausedAt(ctx, q); err != nil {
		return nil, err
	}
	m.bus.Publish(q.WashingPointID)
	return q, nil
}

// forwardStatusTransitions is the only staff-driven state machine: one step
// forward at a time, never skipping a stage and never backward. Canceling
// is a separate, owner-only action (CancelBooking), not reachable here.
var forwardStatusTransitions = map[Status]Status{
	StatusQueue:   StatusWaiting,
	StatusWaiting: StatusWashing,
	StatusWashing: StatusReady,
}

func (m *Manager) UpdateStatus(ctx context.Context, id uuid.UUID, newStatus Status) (*Queue, error) {
	q, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	expected, ok := forwardStatusTransitions[q.Status]
	if !ok || expected != newStatus {
		return nil, apperror.Conflict("invalid_status_transition", fmt.Sprintf("cannot transition from %q to %q", q.Status, newStatus))
	}

	q.Status = newStatus
	if err := m.repo.UpdateStatus(ctx, q); err != nil {
		return nil, err
	}
	m.bus.Publish(q.WashingPointID)
	return q, nil
}
