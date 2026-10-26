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

// FindActiveBookingsInRange returns non-canceled, non-no-show bookings for
// washingPointID whose interval overlaps [rangeStart, rangeEnd) — i.e.
// scheduled_start_at < rangeEnd AND scheduled_end_at > rangeStart, the
// standard half-open interval overlap test.
func (r *Repository) FindActiveBookingsInRange(ctx context.Context, washingPointID uuid.UUID, rangeStart, rangeEnd time.Time) ([]Queue, error) {
	var rows []Queue
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND status NOT IN ? AND scheduled_start_at < ? AND scheduled_end_at > ?",
			washingPointID, []Status{StatusCanceled, StatusNoShow}, rangeEnd, rangeStart).
		Order("scheduled_start_at").
		Find(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return r.attachItems(ctx, rows)
}

// FindByWashingPointAndRange returns every booking (any status, including
// canceled) for washingPointID whose scheduled_start_at falls in
// [rangeStart, rangeEnd) — the reports aggregation's raw input
// (docs/PLAN_WEB_APPS.md phase 10). Unlike FindActiveBookingsInRange this
// intentionally doesn't filter by status: the caller needs to tell
// completed (StatusReady) apart from canceled/still-in-progress itself.
func (r *Repository) FindByWashingPointAndRange(ctx context.Context, washingPointID uuid.UUID, rangeStart, rangeEnd time.Time) ([]Queue, error) {
	var rows []Queue
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND scheduled_start_at >= ? AND scheduled_start_at < ?",
			washingPointID, rangeStart, rangeEnd).
		Order("scheduled_start_at").
		Find(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return r.attachItems(ctx, rows)
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
	items, err := r.itemsFor(ctx, []uuid.UUID{q.ID})
	if err != nil {
		return nil, err
	}
	q.Items = items[q.ID]
	return &q, nil
}

// ListLiveByWashingPoint returns bookings still active in the physical
// queue (not yet ready, not canceled) for the staff-facing board.
func (r *Repository) ListLiveByWashingPoint(ctx context.Context, washingPointID uuid.UUID) ([]Queue, error) {
	return r.ListLive(ctx, &washingPointID)
}

// ListLiveByWashingPointAndDate is ListLiveByWashingPoint further scoped
// to bookings scheduled to start within [dayStart, dayEnd) — the worker
// app's "today's queue" (docs/PLAN_WEB_APPS.md phase 7). A separate method
// rather than an optional param on ListLive/ListLiveByWashingPoint: the
// network-wide board (queue.Handler.list) has no date filter in the plan
// and shouldn't gain one as a side effect of this change.
func (r *Repository) ListLiveByWashingPointAndDate(ctx context.Context, washingPointID uuid.UUID, dayStart, dayEnd time.Time) ([]Queue, error) {
	var rows []Queue
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND status IN ? AND scheduled_start_at >= ? AND scheduled_start_at < ?",
			washingPointID, []Status{StatusQueue, StatusWaiting, StatusWashing}, dayStart, dayEnd).
		Order("scheduled_start_at").
		Find(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return r.attachItems(ctx, rows)
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
	return r.attachItems(ctx, rows)
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
	rows, err = r.attachItems(ctx, rows)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// Create inserts q. A Postgres EXCLUDE constraint violation (two bookings
// racing for the same box/time despite the application-level lock) is
// translated into a friendly 409 rather than a raw DB error.
func (r *Repository) Create(ctx context.Context, q *Queue) error {
	err := r.db.WithContext(ctx).Create(q).Error
	if err == nil {
		err = r.createItems(ctx, q)
	}
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
	return r.attachItems(ctx, rows)
}

// UpdateStatus writes status (plus canceled_at: set on cancel, cleared on
// restore to queue) only — never the booking's other fields. The write only
// applies if the booking is still in status from (what the caller read and
// validated the transition against); otherwise another request changed it in
// between and this returns 409 status_changed, so e.g. a customer's cancel
// and a worker's "start washing" racing cannot resurrect a canceled booking.
// Restoring a canceled/no-show booking can collide with a newer booking in
// the same box/time (EXCLUDE) or with the customer's other active booking
// (unique index); both surface as the same friendly 409s as Create.
func (r *Repository) UpdateStatus(ctx context.Context, q *Queue, from Status) error {
	updates := map[string]any{"status": q.Status}
	switch q.Status {
	case StatusCanceled:
		updates["canceled_at"] = q.CanceledAt
	case StatusQueue:
		updates["canceled_at"] = nil
	}
	res := r.db.WithContext(ctx).Model(&Queue{}).Where("id = ? AND status = ?", q.ID, from).Updates(updates)
	err := res.Error
	if err == nil {
		if res.RowsAffected == 0 {
			return apperror.Conflict("status_changed", "the booking was changed by someone else, reload and try again")
		}
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case postgresExclusionViolation:
			return apperror.Conflict("slot_unavailable", "the box is no longer free for this time")
		case postgresUniqueViolation:
			return apperror.Conflict("active_booking_exists", "the customer already has an active booking")
		}
	}
	return apperror.Internal(err)
}

// UpdatePausedAt writes paused_at only — a map-based update so setting it
// back to nil (resume) is a real SQL NULL, not silently skipped the way a
// struct-based Updates would treat a nil/zero field. Only applies while the
// booking is still washing (409 status_changed otherwise), so a pause racing
// the booking being finished or canceled cannot leave a paused ready booking.
func (r *Repository) UpdatePausedAt(ctx context.Context, q *Queue) error {
	res := r.db.WithContext(ctx).Model(&Queue{}).Where("id = ? AND status = ?", q.ID, StatusWashing).
		Updates(map[string]any{"paused_at": q.PausedAt})
	if res.Error != nil {
		return apperror.Internal(res.Error)
	}
	if res.RowsAffected == 0 {
		return apperror.Conflict("status_changed", "the booking was changed by someone else, reload and try again")
	}
	return nil
}

// CountCreatedNearTimes is a best-effort heuristic for internal/qrcode's
// "bookings via QR" stat — it has no causal link to an actual scan (no
// scan-to-booking session exists), it just counts bookings for
// washingPointID whose CreatedAt landed within window after any one of
// times. Fetches candidate rows by a coarse time-range query, then
// matches in Go — the qr_scans event volume this filters against is small
// (tens to low hundreds of scans per code), so a second SQL round trip or
// an array-typed query isn't worth the complexity here.
func (r *Repository) CountCreatedNearTimes(ctx context.Context, washingPointID uuid.UUID, times []time.Time, window time.Duration) (int64, error) {
	if len(times) == 0 {
		return 0, nil
	}

	lo, hi := times[0], times[0].Add(window)
	for _, t := range times[1:] {
		if t.Before(lo) {
			lo = t
		}
		if end := t.Add(window); end.After(hi) {
			hi = end
		}
	}

	var rows []struct{ CreatedAt time.Time }
	err := r.db.WithContext(ctx).Model(&Queue{}).
		Select("created_at").
		Where("washing_point_id = ? AND created_at >= ? AND created_at <= ?", washingPointID, lo, hi).
		Find(&rows).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}

	var count int64
	for _, row := range rows {
		for _, t := range times {
			if !row.CreatedAt.Before(t) && !row.CreatedAt.After(t.Add(window)) {
				count++
				break
			}
		}
	}
	return count, nil
}

// createItems inserts q.Items (position order, ids assigned) after q itself,
// inside the same transaction when the Repository is bound to one.
func (r *Repository) createItems(ctx context.Context, q *Queue) error {
	for i := range q.Items {
		q.Items[i].QueueID = q.ID
		q.Items[i].Position = i
	}
	if len(q.Items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&q.Items).Error
}

// itemsFor loads the items of the given bookings in one query, keyed by
// queue id, each list in position order.
func (r *Repository) itemsFor(ctx context.Context, queueIDs []uuid.UUID) (map[uuid.UUID][]QueueItem, error) {
	byQueue := make(map[uuid.UUID][]QueueItem, len(queueIDs))
	if len(queueIDs) == 0 {
		return byQueue, nil
	}
	var items []QueueItem
	if err := r.db.WithContext(ctx).Where("queue_id IN ?", queueIDs).Order("queue_id, position").Find(&items).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	for _, it := range items {
		byQueue[it.QueueID] = append(byQueue[it.QueueID], it)
	}
	return byQueue, nil
}

// attachItems fills Items on every row and returns the same slice.
func (r *Repository) attachItems(ctx context.Context, rows []Queue) ([]Queue, error) {
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	items, err := r.itemsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Items = items[rows[i].ID]
	}
	return rows, nil
}
