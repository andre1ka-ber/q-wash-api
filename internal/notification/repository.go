package notification

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"q-wash-api/internal/apperror"
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
