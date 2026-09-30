package user

import (
	"context"
	"errors"
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

func (r *Repository) FindByPhone(ctx context.Context, phone string) (*User, error) {
	var u User
	err := r.db.WithContext(ctx).Where("phone_number = ?", phone).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("user_not_found", "user not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &u, nil
}

func (r *Repository) FindByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("user_not_found", "user not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &u, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := r.db.WithContext(ctx).First(&u, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("user_not_found", "user not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &u, nil
}

// FindByIDs batch-fetches users for display purposes (e.g. the staff queue
// board). Missing ids are simply absent from the result, not an error.
func (r *Repository) FindByIDs(ctx context.Context, ids []uuid.UUID) ([]User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var users []User
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return users, nil
}

// FindByWashingPointAndRole finds the auto-provisioned staff or worker
// login account for washingPointID (internal/washingpoint's credentials
// get/reset endpoints). washing_point_id+role is deliberately NOT unique:
// a customer can be promoted to staff and scoped to a point by direct-DB
// role promotion (see docs/DATA_MODEL.md) without ever getting a username,
// so this must also filter to username IS NOT NULL — a staff/worker row
// with no username isn't a login account at all, and returning one here
// would crash the caller's *User.Username dereference. created_at ASC
// makes the pick deterministic on the rare legitimate case of more than
// one (the oldest — closest to "the" provisioned account — wins) rather
// than whatever order the DB happens to return.
func (r *Repository) FindByWashingPointAndRole(ctx context.Context, washingPointID uuid.UUID, role Role) (*User, error) {
	var u User
	err := r.db.WithContext(ctx).
		Where("washing_point_id = ? AND role = ? AND username IS NOT NULL", washingPointID, role).
		Order("created_at ASC").
		First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("user_not_found", "user not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &u, nil
}

func (r *Repository) Create(ctx context.Context, u *User) error {
	if err := r.db.WithContext(ctx).Create(u).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) UpdateLastLogin(ctx context.Context, id uuid.UUID, t time.Time) error {
	err := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("last_login_at", t).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// SetCredentials sets (or overwrites) username/password_hash for a user.
// There is no API endpoint for this by design — same as role promotion,
// it's a direct-DB operation (see cmd/seed and docs/DATA_MODEL.md).
func (r *Repository) SetCredentials(ctx context.Context, id uuid.UUID, username, passwordHash string) error {
	err := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(map[string]any{
		"username":      username,
		"password_hash": passwordHash,
	}).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) UpdateName(ctx context.Context, id uuid.UUID, name string) (*User, error) {
	err := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("name", name).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return r.FindByID(ctx, id)
}
