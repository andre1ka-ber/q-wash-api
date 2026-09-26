package queue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/box"
	"q-wash-api/internal/car"
	"q-wash-api/internal/platform/clock"
	"q-wash-api/internal/platform/eventbus"
	"q-wash-api/internal/schedule"
	"q-wash-api/internal/service"
	"q-wash-api/internal/user"
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
	notifier     StageNotifier
}

// StageNotifier is told after a booking's status changes so the customer can
// be notified (started/finished). It's an interface, set after construction,
// because the notification package depends on queue, not the other way round.
// Implementations must not block or fail the status change.
type StageNotifier interface {
	NotifyStatus(ctx context.Context, q *Queue)
}

// SetStageNotifier wires the optional notifier; without one, status changes
// notify nobody.
func (m *Manager) SetStageNotifier(n StageNotifier) { m.notifier = n }

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

// bookingPlan is everything CreateBooking and CreateManualBooking resolve
// before touching the DB inside the washing-point lock: the validated
// service/point and the operating-hours window the new booking must fit.
type bookingPlan struct {
	wp       *washingpoint.WashingPoint
	svc      *service.Service
	end      time.Time
	dayOpen  time.Time
	dayClose time.Time
}

// planBooking validates the parts of a booking request that don't depend
// on who owns it: service/price-option/washing-point relationships, box
// range/closed state, and operating hours. requireFuture is true for
// customer bookings (start must be after now) and false for staff
// walk-ins, which start "now" by definition. expectWashingPoint, when
// non-nil, additionally requires the service to belong to that point.
func (m *Manager) planBooking(ctx context.Context, serviceID, priceOptionID uuid.UUID, boxNumber int, start time.Time, requireFuture bool, expectWashingPoint *uuid.UUID) (*bookingPlan, error) {
	svc, err := m.serviceRepo.FindByID(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	if expectWashingPoint != nil && svc.WashingPointID != *expectWashingPoint {
		return nil, apperror.NotFound("service_not_found", "service not found")
	}
	if !svc.IsActive {
		return nil, apperror.BadRequest("service_inactive", "service is not currently available")
	}

	hasPriceOption := false
	for _, po := range svc.PriceOptions {
		if po.ID == priceOptionID {
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

	if boxNumber < 1 || boxNumber > wp.BoxesCount {
		return nil, apperror.BadRequest("invalid_box_number", fmt.Sprintf("box_number must be between 1 and %d", wp.BoxesCount))
	}
	requestedBox, err := m.boxRepo.FindByNumber(ctx, wp.ID, boxNumber)
	if err != nil {
		return nil, err
	}
	if requestedBox != nil && !requestedBox.IsOpen {
		return nil, apperror.Conflict("box_closed", "the requested box is currently closed")
	}

	if requireFuture && !start.After(time.Now()) {
		return nil, apperror.BadRequest("invalid_scheduled_start_at", "scheduled_start_at must be in the future")
	}

	end := start.Add(time.Duration(svc.DurationMinutes) * time.Minute)

	weekday := weekdayIndex(start.In(clock.BusinessLocation))
	scheduleRow, err := m.scheduleRepo.FindByWeekday(ctx, wp.ID, weekday)
	if err != nil {
		return nil, err
	}
	daySchedule, err := resolveDaySchedule(start, scheduleRow)
	if err != nil {
		return nil, apperror.Internal(err)
	}
	if !daySchedule.Contains(start, end) {
		return nil, apperror.BadRequest("outside_operating_hours", "requested time is outside the washing point's operating hours")
	}
	return &bookingPlan{wp: wp, svc: svc, end: end, dayOpen: daySchedule.Open, dayClose: daySchedule.Close}, nil
}

// insertLocked locks the washing point row, re-reads the day's busy
// intervals inside the lock, verifies the box is free, and inserts q —
// beforeInsert (if set) runs inside the same transaction after the
// availability check, so any rows it creates (walk-in user/car) roll back
// with the booking. That lock serializes concurrent booking attempts for
// the same washing point — without it, two transactions could both read
// "box 1 is free" before either commits and both try to claim it. The DB's
// EXCLUDE constraint (see migrations) is still there as a last-resort
// backstop if that ever happened anyway.
func (m *Manager) insertLocked(ctx context.Context, plan *bookingPlan, q *Queue, beforeInsert func(tx *gorm.DB, q *Queue) error) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked washingpoint.WashingPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", plan.wp.ID).Error; err != nil {
			return apperror.Internal(err)
		}

		txRepo := m.repo.WithTx(tx)
		busy, err := txRepo.FindActiveBookingsInRange(ctx, plan.wp.ID, plan.dayOpen, plan.dayClose)
		if err != nil {
			return err
		}
		if err := verifyBoxAvailable(q.BoxNumber, q.ScheduledStartAt, q.ScheduledEndAt, busy); err != nil {
			return err
		}
		if beforeInsert != nil {
			if err := beforeInsert(tx, q); err != nil {
				return err
			}
		}
		return txRepo.Create(ctx, q)
	})
}

// CreateBooking validates the request against the caller's own car, the
// service/price-option/washing-point relationships, and operating hours,
// then assigns a box and inserts the row inside a transaction that locks
// the washing point row for its duration (see insertLocked).
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

	plan, err := m.planBooking(ctx, in.ServiceID, in.PriceOptionID, in.BoxNumber, in.ScheduledStartAt, true, nil)
	if err != nil {
		return nil, err
	}

	q := &Queue{
		Status:           StatusQueue,
		Source:           SourceApp,
		UserID:           in.UserID,
		CarID:            &in.CarID,
		ServiceID:        in.ServiceID,
		PriceOptionID:    in.PriceOptionID,
		WashingPointID:   plan.wp.ID,
		BoxNumber:        in.BoxNumber,
		ScheduledStartAt: in.ScheduledStartAt,
		ScheduledEndAt:   plan.end,
		Notes:            in.Notes,
	}
	if err := m.insertLocked(ctx, plan, q, nil); err != nil {
		return nil, err
	}
	m.bus.Publish(plan.wp.ID)
	return q, nil
}

type CreateManualBookingInput struct {
	WashingPointID   uuid.UUID
	ServiceID        uuid.UUID
	PriceOptionID    uuid.UUID
	BoxNumber        int
	ScheduledStartAt time.Time
	CarName          string
	Plate            string
	Phone            string
	ClientName       string
}

// CreateManualBooking is the cabinet's "Добавить вручную": a staff-created
// walk-in booking. The client is found-or-created by phone as a regular
// customer user, and their car (if any) is upserted — created inside
// the booking's transaction so a lost slot race leaves nothing behind.
// Car name/plate are optional; when given, the client's car is upserted
// (matched by plate, else by name) instead of always creating a new one. An
// existing account with a non-customer role (staff/worker/admin) is
// rejected rather than booked against. Always starts in StatusQueue.
func (m *Manager) CreateManualBooking(ctx context.Context, in CreateManualBookingInput) (*Queue, error) {
	plan, err := m.planBooking(ctx, in.ServiceID, in.PriceOptionID, in.BoxNumber, in.ScheduledStartAt, false, &in.WashingPointID)
	if err != nil {
		return nil, err
	}

	q := &Queue{
		Status:           StatusQueue,
		Source:           SourceManual,
		ServiceID:        in.ServiceID,
		PriceOptionID:    in.PriceOptionID,
		WashingPointID:   plan.wp.ID,
		BoxNumber:        in.BoxNumber,
		ScheduledStartAt: in.ScheduledStartAt,
		ScheduledEndAt:   plan.end,
	}
	err = m.insertLocked(ctx, plan, q, func(tx *gorm.DB, q *Queue) error {
		userRepo := user.NewRepository(tx)
		u, err := userRepo.FindByPhone(ctx, in.Phone)
		if err != nil {
			var appErr *apperror.Error
			if !errors.As(err, &appErr) || appErr.Code != "user_not_found" {
				return err
			}
			u = &user.User{PhoneNumber: in.Phone, Role: user.RoleCustomer}
			if in.ClientName != "" {
				name := in.ClientName
				u.Name = &name
			}
			if err := userRepo.Create(ctx, u); err != nil {
				return err
			}
		} else if u.Role != user.RoleCustomer {
			return apperror.Conflict("phone_not_customer", "this phone number belongs to a staff account")
		}

		hasActive, err := m.repo.WithTx(tx).HasActiveForUser(ctx, u.ID)
		if err != nil {
			return err
		}
		if hasActive {
			return apperror.Conflict("active_booking_exists", "this client already has an active booking")
		}

		if in.CarName != "" || in.Plate != "" {
			c, err := car.NewRepository(tx).UpsertForUser(ctx, u.ID, in.CarName, in.Plate)
			if err != nil {
				return err
			}
			q.CarID = &c.ID
		}
		q.UserID = u.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.bus.Publish(plan.wp.ID)
	return q, nil
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

// staffStatusTransitions is the staff-driven state machine: forward one
// step at a time (queue -> waiting -> washing -> ready), plus no_show from
// queue/waiting, plus restoring a no_show or canceled booking to queue
// (the cabinet's "Вернуть в очередь" — re-checked against the box's
// occupancy by the DB constraint, 409 slot_unavailable if the slot has
// been taken since). Canceling itself is a separate action
// (CancelBooking), not reachable here.
var staffStatusTransitions = map[Status][]Status{
	StatusQueue:    {StatusWaiting, StatusNoShow},
	StatusWaiting:  {StatusWashing, StatusNoShow},
	StatusWashing:  {StatusReady},
	StatusNoShow:   {StatusQueue},
	StatusCanceled: {StatusQueue},
}

func (m *Manager) UpdateStatus(ctx context.Context, id uuid.UUID, newStatus Status) (*Queue, error) {
	q, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if !slices.Contains(staffStatusTransitions[q.Status], newStatus) {
		return nil, apperror.Conflict("invalid_status_transition", fmt.Sprintf("cannot transition from %q to %q", q.Status, newStatus))
	}

	q.Status = newStatus
	if newStatus == StatusQueue {
		q.CanceledAt = nil
	}
	if err := m.repo.UpdateStatus(ctx, q); err != nil {
		return nil, err
	}
	m.bus.Publish(q.WashingPointID)
	if m.notifier != nil {
		m.notifier.NotifyStatus(ctx, q)
	}
	return q, nil
}
