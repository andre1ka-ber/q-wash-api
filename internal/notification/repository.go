package notification

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/queue"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, n *Notification) error {
	if err := r.db.WithContext(ctx).Create(n).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// UpdateDeliveryResult writes only status/sent_at, the two fields a send
// attempt can change.
func (r *Repository) UpdateDeliveryResult(ctx context.Context, id uuid.UUID, status Status, sentAt *time.Time) error {
	err := r.db.WithContext(ctx).Model(&Notification{}).Where("id = ?", id).Updates(map[string]any{
		"status":  status,
		"sent_at": sentAt,
	}).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// ListByUser returns userID's notifications, most recently created first,
// along with the total count for pagination.
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID, offset, limit int) ([]Notification, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&Notification{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
		return nil, 0, apperror.Internal(err)
	}

	var rows []Notification
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, 0, apperror.Internal(err)
	}
	return rows, total, nil
}

// CreateOnce inserts a booking-stage notification unless one of the same
// kind already exists for that booking (the unique index makes this safe
// across concurrent API instances). It reports whether the row was created.
func (r *Repository) CreateOnce(ctx context.Context, n *Notification) (bool, error) {
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(n)
	if res.Error != nil {
		return false, apperror.Internal(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// dueWindow says when a time-based stage applies, relative to a booking's
// scheduled start: from start+from (inclusive) until start+until.
type dueWindow struct {
	from, until time.Duration
	// needsLeadTime skips bookings created inside the from window (a reminder
	// for a booking made 10 minutes ahead would be noise).
	needsLeadTime bool
}

var dueWindows = map[Kind]dueWindow{
	// 1h before the start, until the start itself.
	KindReminder: {from: -time.Hour, until: 0, needsLeadTime: true},
	// 5 min after the start; a 2h cap so a downtime or a first deploy doesn't
	// blast people about bookings that are long over.
	KindLate: {from: 5 * time.Minute, until: 2 * time.Hour},
}

// FindDue returns still-unstarted bookings (queue/waiting) whose kind-stage
// is due at now, whose owner has a registered device, and for which that
// stage hasn't been recorded yet.
func (r *Repository) FindDue(ctx context.Context, kind Kind, now time.Time) ([]queue.Queue, error) {
	w := dueWindows[kind]
	q := r.db.WithContext(ctx).Model(&queue.Queue{}).
		Where("status IN ?", []queue.Status{queue.StatusQueue, queue.StatusWaiting}).
		Where("scheduled_start_at + make_interval(secs => ?) <= ?", w.from.Seconds(), now).
		Where("scheduled_start_at + make_interval(secs => ?) > ?", w.until.Seconds(), now).
		Where("EXISTS (SELECT 1 FROM device_tokens d WHERE d.user_id = queue.user_id)").
		Where("NOT EXISTS (SELECT 1 FROM notifications n WHERE n.queue_id = queue.id AND n.kind = ?)", string(kind))
	if w.needsLeadTime {
		q = q.Where("created_at <= scheduled_start_at + make_interval(secs => ?)", w.from.Seconds())
	}
	var rows []queue.Queue
	if err := q.Order("scheduled_start_at").Find(&rows).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return rows, nil
}
