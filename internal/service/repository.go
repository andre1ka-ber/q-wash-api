package service

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

func (r *Repository) ListByWashingPoint(ctx context.Context, washingPointID uuid.UUID) ([]Service, error) {
	var services []Service
	err := r.db.WithContext(ctx).
		Preload("PriceOptions", func(db *gorm.DB) *gorm.DB { return db.Order("created_at") }).
		Where("washing_point_id = ?", washingPointID).
		Order("name").
		Find(&services).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return services, nil
}

// CountActiveByWashingPointIDs batch-counts active services per point for
// the admin network list (internal/admin) — one grouped query instead of
// N+1 per-point lookups.
func (r *Repository) CountActiveByWashingPointIDs(ctx context.Context, washingPointIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	counts := make(map[uuid.UUID]int64, len(washingPointIDs))
	if len(washingPointIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		WashingPointID uuid.UUID
		Count          int64
	}
	err := r.db.WithContext(ctx).Model(&Service{}).
		Select("washing_point_id, count(*) as count").
		Where("washing_point_id IN ? AND is_active = true", washingPointIDs).
		Group("washing_point_id").
		Scan(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	for _, row := range rows {
		counts[row.WashingPointID] = row.Count
	}
	return counts, nil
}

// FindByIDs batch-fetches services (name only needed, but returns full
// rows for consistency with car.Repository.FindByIDs/user.Repository.
// FindByIDs) for display purposes (e.g. the worker app's live-boxes view).
// Missing ids are simply absent from the result, not an error. No
// PriceOptions preload — callers needing those already have FindByID.
func (r *Repository) FindByIDs(ctx context.Context, ids []uuid.UUID) ([]Service, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var services []Service
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&services).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return services, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Service, error) {
	var svc Service
	err := r.db.WithContext(ctx).
		Preload("PriceOptions", func(db *gorm.DB) *gorm.DB { return db.Order("created_at") }).
		First(&svc, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("service_not_found", "service not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &svc, nil
}

// Create inserts svc along with any populated PriceOptions in one go (GORM
// cascades the create to the association and fills in each option's
// ServiceID).
func (r *Repository) Create(ctx context.Context, svc *Service) error {
	if err := r.db.WithContext(ctx).Create(svc).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// Update writes only the Service's own columns via an explicit map, never
// cascading to PriceOptions — callers that also loaded PriceOptions (e.g.
// via FindByID) must not have them silently re-saved as a side effect.
func (r *Repository) Update(ctx context.Context, svc *Service) error {
	err := r.db.WithContext(ctx).Model(&Service{}).Where("id = ?", svc.ID).Updates(map[string]any{
		"name":             svc.Name,
		"description":      svc.Description,
		"duration_minutes": svc.DurationMinutes,
		"picture_url":      svc.PictureURL,
		"is_active":        svc.IsActive,
	}).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Deactivate(ctx context.Context, id uuid.UUID) error {
	err := r.db.WithContext(ctx).Model(&Service{}).Where("id = ?", id).Update("is_active", false).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) FindPriceOptionByID(ctx context.Context, id uuid.UUID) (*ServicePriceOption, error) {
	var opt ServicePriceOption
	err := r.db.WithContext(ctx).First(&opt, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("price_option_not_found", "price option not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &opt, nil
}

// FindPriceOptionsByIDs batch-fetches price options for display (e.g. the
// cabinet day queue). Missing ids are simply absent, not an error.
func (r *Repository) FindPriceOptionsByIDs(ctx context.Context, ids []uuid.UUID) ([]ServicePriceOption, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var opts []ServicePriceOption
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&opts).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return opts, nil
}

func (r *Repository) CreatePriceOption(ctx context.Context, opt *ServicePriceOption) error {
	if err := r.db.WithContext(ctx).Create(opt).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) UpdatePriceOption(ctx context.Context, opt *ServicePriceOption) error {
	err := r.db.WithContext(ctx).Model(&ServicePriceOption{}).Where("id = ?", opt.ID).Updates(map[string]any{
		"name":        opt.Name,
		"price_cents": opt.PriceCents,
		"is_default":  opt.IsDefault,
	}).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) DeletePriceOption(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).Delete(&ServicePriceOption{}, "id = ?", id).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) CountPriceOptionsByService(ctx context.Context, serviceID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&ServicePriceOption{}).Where("service_id = ?", serviceID).Count(&count).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return count, nil
}

func (r *Repository) UnsetDefaultPriceOptions(ctx context.Context, serviceID uuid.UUID) error {
	err := r.db.WithContext(ctx).Model(&ServicePriceOption{}).
		Where("service_id = ? AND is_default = true", serviceID).
		Update("is_default", false).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) FirstPriceOptionExcept(ctx context.Context, serviceID, excludeID uuid.UUID) (*ServicePriceOption, error) {
	var opt ServicePriceOption
	err := r.db.WithContext(ctx).
		Where("service_id = ? AND id <> ?", serviceID, excludeID).
		Order("created_at").
		First(&opt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &opt, nil
}

// CountQueueUsingPriceOption checks the queue table directly (by name, not
// by importing the queue package) to avoid a service<->queue import cycle:
// queue will need to import service in a later phase to validate bookings.
func (r *Repository) CountQueueUsingPriceOption(ctx context.Context, priceOptionID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("queue").Where("price_option_id = ?", priceOptionID).Count(&count).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return count, nil
}
