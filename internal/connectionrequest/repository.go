package connectionrequest

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

// List returns requests newest-first, optionally filtered to one status.
func (r *Repository) List(ctx context.Context, status *Status) ([]ConnectionRequest, error) {
	q := r.db.WithContext(ctx).Order("created_at desc")
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	var items []ConnectionRequest
	if err := q.Find(&items).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return items, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*ConnectionRequest, error) {
	var c ConnectionRequest
	err := r.db.WithContext(ctx).First(&c, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("connection_request_not_found", "connection request not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &c, nil
}

func (r *Repository) Create(ctx context.Context, c *ConnectionRequest) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, c *ConnectionRequest) error {
	if err := r.db.WithContext(ctx).Save(c).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}
