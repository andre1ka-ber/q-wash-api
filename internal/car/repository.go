package car

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

func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID) ([]Car, error) {
	var cars []Car
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at").Find(&cars).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return cars, nil
}

// FindByIDs batch-fetches cars for display purposes (e.g. the staff queue
// board). Missing ids are simply absent from the result, not an error.
func (r *Repository) FindByIDs(ctx context.Context, ids []uuid.UUID) ([]Car, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var cars []Car
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&cars).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return cars, nil
}

// FindOwnedByID returns the car only if it belongs to userID, so a lookup
// for someone else's car and a lookup for a nonexistent car both 404
// identically — never leaking that a car ID exists but isn't the caller's.
func (r *Repository) FindOwnedByID(ctx context.Context, id, userID uuid.UUID) (*Car, error) {
	var c Car
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("car_not_found", "car not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &c, nil
}

func (r *Repository) Create(ctx context.Context, c *Car) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, c *Car) error {
	err := r.db.WithContext(ctx).Model(&Car{}).Where("id = ?", c.ID).Update("name", c.Name).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).Delete(&Car{}, "id = ?", id).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// CountQueueUsingCar checks the queue table directly (by name, not by
// importing the queue package) for the same reason as the analogous check
// in internal/service: queue will need to import car in a later phase to
// validate bookings, so car must not import queue back.
func (r *Repository) CountQueueUsingCar(ctx context.Context, carID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("queue").Where("car_id = ?", carID).Count(&count).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return count, nil
}
