package owner

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

func (r *Repository) List(ctx context.Context) ([]Owner, error) {
	var owners []Owner
	if err := r.db.WithContext(ctx).Order("name").Find(&owners).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return owners, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*Owner, error) {
	var o Owner
	err := r.db.WithContext(ctx).First(&o, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("owner_not_found", "owner not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &o, nil
}

// FindByPhone returns the owner with the given contact phone, or nil (not
// an error) if none matches — used by connectionrequest.Manager.Approve to
// decide whether to reuse an existing owner or create a new one.
func (r *Repository) FindByPhone(ctx context.Context, phone string) (*Owner, error) {
	var o Owner
	err := r.db.WithContext(ctx).First(&o, "contact_phone = ?", phone).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &o, nil
}

func (r *Repository) Create(ctx context.Context, o *Owner) error {
	if err := r.db.WithContext(ctx).Create(o).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, o *Owner) error {
	if err := r.db.WithContext(ctx).Save(o).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}
