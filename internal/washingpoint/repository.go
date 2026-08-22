package washingpoint

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

func (r *Repository) List(ctx context.Context) ([]WashingPoint, error) {
	var points []WashingPoint
	if err := r.db.WithContext(ctx).Order("name").Find(&points).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return points, nil
}

// CountByStatus returns the network-wide point count grouped by status,
// for the admin dashboard (internal/admin) — zero entries for a status
// with no points, rather than a missing map key.
func (r *Repository) CountByStatus(ctx context.Context) (map[Status]int64, error) {
	counts := map[Status]int64{StatusActive: 0, StatusPaused: 0, StatusPendingReview: 0}
	var rows []struct {
		Status Status
		Count  int64
	}
	err := r.db.WithContext(ctx).Model(&WashingPoint{}).
		Select("status, count(*) as count").
		Group("status").
		Scan(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	for _, row := range rows {
		counts[row.Status] = row.Count
	}
	return counts, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*WashingPoint, error) {
	var wp WashingPoint
	err := r.db.WithContext(ctx).First(&wp, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("washing_point_not_found", "washing point not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &wp, nil
}

func (r *Repository) Create(ctx context.Context, wp *WashingPoint) error {
	if err := r.db.WithContext(ctx).Create(wp).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, wp *WashingPoint) error {
	if err := r.db.WithContext(ctx).Save(wp).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Deactivate(ctx context.Context, id uuid.UUID) error {
	err := r.db.WithContext(ctx).Model(&WashingPoint{}).Where("id = ?", id).Update("status", StatusPaused).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}
