package bookingphoto

import (
	"context"
	"errors"

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

func (r *Repository) ListByQueue(ctx context.Context, queueID uuid.UUID) ([]BookingPhoto, error) {
	var photos []BookingPhoto
	err := r.db.WithContext(ctx).
		Where("queue_id = ?", queueID).
		Order("created_at, id").
		Find(&photos).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return photos, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*BookingPhoto, error) {
	var p BookingPhoto
	err := r.db.WithContext(ctx).First(&p, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("photo_not_found", "photo not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &p, nil
}

func (r *Repository) Create(ctx context.Context, p *BookingPhoto) error {
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).Delete(&BookingPhoto{}, "id = ?", id).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) CountByQueue(ctx context.Context, queueID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&BookingPhoto{}).Where("queue_id = ?", queueID).Count(&count).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return count, nil
}
