package queue

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"q-wash-api/internal/apperror"
)

// postgresExclusionViolation is the SQLSTATE for a Postgres EXCLUDE
// constraint violation — fired by queue_no_overlapping_box_bookings if two
// bookings ever race past the application-level lock (see Manager.CreateBooking).
const postgresExclusionViolation = "23P01"

// postgresUniqueViolation is the SQLSTATE fired by
// queue_one_active_booking_per_user if two of a user's own requests race
// past Manager.CreateBooking's HasActiveForUser pre-check.
const postgresUniqueViolation = "23505"

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// WithTx returns a Repository bound to tx instead of the default db, for
// use inside a Manager-controlled transaction.
func (r *Repository) WithTx(tx *gorm.DB) *Repository {
	return &Repository{db: tx}
}

// FindActiveBookingsInRange returns non-canceled bookings for
// washingPointID whose interval overlaps [rangeStart, rangeEnd) — i.e.
// scheduled_start_at < rangeEnd AND scheduled_end_at > rangeStart, the
// standard half-open interval overlap test.
func (r *Repository) FindActiveBookingsInRange(ctx context.Context, washingPointID uuid.UUID, rangeStart, rangeEnd time.Time) ([]Queue, error) {
	var rows []Queue
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND status <> ? AND scheduled_start_at < ? AND scheduled_end_at > ?",
			washingPointID, StatusCanceled, rangeEnd, rangeStart).
		Order("scheduled_start_at").
		Find(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return rows, nil
}

// CountActiveAhead returns how many other bookings at washingPointID are
// still active (queue/waiting/washing) and scheduled to start before
// startAt — the customer-facing "N cars ahead of you" count. Deliberately
// just a number: unlike ListLiveByWashingPoint (the staff board), this
// never exposes which bookings they are.
func (r *Repository) CountActiveAhead(ctx context.Context, washingPointID uuid.UUID, startAt time.Time) (int, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&Queue{}).
		Where("washing_point_id = ? AND status IN ? AND scheduled_start_at < ?",
			washingPointID, []Status{StatusQueue, StatusWaiting, StatusWashing}, startAt).
		Count(&count).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return int(count), nil
}

// HasActiveForUser reports whether userID already has a booking in
// queue/waiting/washing status — the app-level fast path for the
// one-active-booking-per-user rule that queue_one_active_booking_per_user
// (a partial unique index) enforces as the DB-level backstop.
func (r *Repository) HasActiveForUser(ctx context.Context, userID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&Queue{}).
		Where("user_id = ? AND status IN ?", userID, []Status{StatusQueue, StatusWaiting, StatusWashing}).
		Count(&count).Error
	if err != nil {
		return false, apperror.Internal(err)
	}
	return count > 0, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Queue, error) {
	var q Queue
	err := r.db.WithContext(ctx).First(&q, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("queue_not_found", "queue entry not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &q, nil
}

// FindOwnedByID returns the booking only if it belongs to userID, so a
// lookup for someone else's booking and a lookup for a nonexistent id both
// 404 identically.
func (r *Repository) FindOwnedByID(ctx context.Context, id, userID uuid.UUID) (*Queue, error) {
	var q Queue
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&q).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("queue_not_found", "queue entry not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &q, nil
}

// ListLiveByWashingPoint returns bookings still active in the physical
// queue (not yet ready, not canceled) for the staff-facing board.
func (r *Repository) ListLiveByWashingPoint(ctx context.Context, washingPointID uuid.UUID) ([]Queue, error) {
	return r.ListLive(ctx, &washingPointID)
}

// ListLive returns bookings still active in the physical queue
// (queue/waiting/washing), optionally scoped to one washing point.
// washingPointID == nil means network-wide — callers must gate this to
// admin (or a role-forced non-nil id) themselves; see queue.Handler.list.
func (r *Repository) ListLive(ctx context.Context, washingPointID *uuid.UUID) ([]Queue, error) {
	query := r.db.WithContext(ctx).Where("status IN ?", []Status{StatusQueue, StatusWaiting, StatusWashing})
	if washingPointID != nil {
		query = query.Where("washing_point_id = ?", *washingPointID)
	}
	var rows []Queue
	if err := query.Order("scheduled_start_at").Find(&rows).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return rows, nil
}

// ListByUser returns userID's bookings across all statuses (history), most
// recently scheduled first, along with the total count for pagination.
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID, offset, limit int) ([]Queue, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&Queue{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
		return nil, 0, apperror.Internal(err)
	}

	var rows []Queue
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("scheduled_start_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, 0, apperror.Internal(err)
	}
	return rows, total, nil
}

// Create inserts q. A Postgres EXCLUDE constraint violation (two bookings
// racing for the same box/time despite the application-level lock) is
// translated into a friendly 409 rather than a raw DB error.
func (r *Repository) Create(ctx context.Context, q *Queue) error {
	err := r.db.WithContext(ctx).Create(q).Error
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case postgresExclusionViolation:
			return apperror.Conflict("slot_unavailable", "no box is available for the requested time")
		case postgresUniqueViolation:
			return apperror.Conflict("active_booking_exists", "you already have an active booking")
		}
	}
	return apperror.Internal(err)
}

// FindScheduledInRangeNetworkWide returns every booking (any status, any
// washing point) whose interval overlaps [rangeStart, rangeEnd) — used by
// the admin dashboard's today aggregates (internal/admin), unlike
// FindActiveBookingsInRange which is scoped to one point and excludes
// canceled bookings.
func (r *Repository) FindScheduledInRangeNetworkWide(ctx context.Context, rangeStart, rangeEnd time.Time) ([]Queue, error) {
	var rows []Queue
	err := r.db.WithContext(ctx).
		Where("scheduled_start_at < ? AND scheduled_end_at > ?", rangeEnd, rangeStart).
		Order("scheduled_start_at").
		Find(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return rows, nil
}

// UpdateStatus writes status (and canceled_at, when set) only — never the
// booking's other fields.
func (r *Repository) UpdateStatus(ctx context.Context, q *Queue) error {
	updates := map[string]any{"status": q.Status}
	if q.Status == StatusCanceled {
		updates["canceled_at"] = q.CanceledAt
	}
	err := r.db.WithContext(ctx).Model(&Queue{}).Where("id = ?", q.ID).Updates(updates).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}
