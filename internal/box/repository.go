package box

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

func (r *Repository) ListByWashingPoint(ctx context.Context, washingPointID uuid.UUID) ([]Box, error) {
	var boxes []Box
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ?", washingPointID).
		Order("number").
		Find(&boxes).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return boxes, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Box, error) {
	var b Box
	err := r.db.WithContext(ctx).First(&b, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("box_not_found", "box not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &b, nil
}

// FindByNumber returns the box at washingPointID/number, or nil (not an
// error) if no such row exists — callers that gate on IsOpen must treat a
// missing row as open (fail open), same as ListByWashingPoint naturally
// does for a point with incomplete/no box rows.
func (r *Repository) FindByNumber(ctx context.Context, washingPointID uuid.UUID, number int) (*Box, error) {
	var b Box
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND number = ?", washingPointID, number).
		First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &b, nil
}

// MaxNumber returns the highest existing box number for washingPointID, or
// 0 if it has none — Manager.Create uses this to auto-assign the next
// number rather than accepting one from the client, so two concurrent
// creates racing is the only way to get a duplicate (caught by the
// migration's UNIQUE (washing_point_id, number) constraint either way).
func (r *Repository) MaxNumber(ctx context.Context, washingPointID uuid.UUID) (int, error) {
	var max int
	err := r.db.WithContext(ctx).Model(&Box{}).
		Where("washing_point_id = ?", washingPointID).
		Select("COALESCE(MAX(number), 0)").
		Scan(&max).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return max, nil
}

func (r *Repository) Create(ctx context.Context, b *Box) error {
	if err := r.db.WithContext(ctx).Create(b).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// Update writes only label/is_open — the two fields ever mutated after
// creation (number is assigned once at Create and never changes).
func (r *Repository) Update(ctx context.Context, b *Box) error {
	err := r.db.WithContext(ctx).Model(&Box{}).Where("id = ?", b.ID).Updates(map[string]any{
		"label":   b.Label,
		"is_open": b.IsOpen,
	}).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).Delete(&Box{}, "id = ?", id).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}
