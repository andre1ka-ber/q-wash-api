package photo

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

func (r *Repository) ListByWashingPoint(ctx context.Context, washingPointID uuid.UUID) ([]WashingPointPhoto, error) {
	var photos []WashingPointPhoto
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ?", washingPointID).
		Order("sort_order, created_at").
		Find(&photos).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return photos, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*WashingPointPhoto, error) {
	var p WashingPointPhoto
	err := r.db.WithContext(ctx).First(&p, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("photo_not_found", "photo not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &p, nil
}

func (r *Repository) Create(ctx context.Context, p *WashingPointPhoto) error {
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// Update writes only sort_order/is_cover — the two fields ever mutated
// after creation.
func (r *Repository) Update(ctx context.Context, p *WashingPointPhoto) error {
	err := r.db.WithContext(ctx).Model(&WashingPointPhoto{}).Where("id = ?", p.ID).Updates(map[string]any{
		"is_cover":   p.IsCover,
		"sort_order": p.SortOrder,
	}).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).Delete(&WashingPointPhoto{}, "id = ?", id).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// UnsetCover clears is_cover for every photo at washingPointID — called
// before setting a new one so exactly one stays true, same pattern as
// service.Repository.UnsetDefaultPriceOptions.
func (r *Repository) UnsetCover(ctx context.Context, washingPointID uuid.UUID) error {
	err := r.db.WithContext(ctx).Model(&WashingPointPhoto{}).
		Where("washing_point_id = ? AND is_cover = true", washingPointID).
		Update("is_cover", false).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// CountByWashingPoint is used to decide whether a newly uploaded photo is
// the first for its point (auto-promoted to cover) and to compute its
// sort_order.
func (r *Repository) CountByWashingPoint(ctx context.Context, washingPointID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&WashingPointPhoto{}).Where("washing_point_id = ?", washingPointID).Count(&count).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return count, nil
}

// FirstExcept returns the oldest remaining photo for washingPointID other
// than excludeID, or nil if none — used to auto-promote a new cover when
// the current one is deleted, same pattern as
// service.Repository.FirstPriceOptionExcept.
func (r *Repository) FirstExcept(ctx context.Context, washingPointID, excludeID uuid.UUID) (*WashingPointPhoto, error) {
	var p WashingPointPhoto
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND id <> ?", washingPointID, excludeID).
		Order("sort_order, created_at").
		First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &p, nil
}
