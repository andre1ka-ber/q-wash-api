package schedule

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

func (r *Repository) ListByWashingPoint(ctx context.Context, washingPointID uuid.UUID) ([]WashingPointSchedule, error) {
	var rows []WashingPointSchedule
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ?", washingPointID).
		Order("weekday").
		Find(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return rows, nil
}

// FindByWeekday returns the one row for (washingPointID, weekday), or nil
// (not an error) if the point has no row for that weekday yet — callers
// treat a missing row as "closed" rather than erroring, see
// queue.resolveDaySchedule.
func (r *Repository) FindByWeekday(ctx context.Context, washingPointID uuid.UUID, weekday int) (*WashingPointSchedule, error) {
	var s WashingPointSchedule
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND weekday = ?", washingPointID, weekday).
		First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &s, nil
}

// ReplaceAll atomically replaces every row for washingPointID with rows —
// bulk PUT semantics (docs/PLAN_WEB_APPS.md phase 5's schedule endpoint),
// not a per-weekday upsert. rows' WashingPointID and ID fields are
// overwritten so callers only need to fill in the schedule content.
func (r *Repository) ReplaceAll(ctx context.Context, washingPointID uuid.UUID, rows []WashingPointSchedule) error {
	for i := range rows {
		rows[i].ID = uuid.Nil
		rows[i].WashingPointID = washingPointID
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("washing_point_id = ?", washingPointID).Delete(&WashingPointSchedule{}).Error; err != nil {
			return err
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}
