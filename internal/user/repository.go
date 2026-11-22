package user

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/platform/clientinfo"
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

// CountLoginAccountsByWashingPointIDs batch-counts the staff/worker login
// accounts (username set) per washing point — the admin list's "Доступ"
// column. Points with no account are absent from the map (count 0).
func (r *Repository) CountLoginAccountsByWashingPointIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	counts := make(map[uuid.UUID]int64, len(ids))
	if len(ids) == 0 {
		return counts, nil
	}
	var rows []struct {
		WashingPointID uuid.UUID
		N              int64
	}
	err := r.db.WithContext(ctx).Model(&User{}).
		Select("washing_point_id, COUNT(*) AS n").
		Where("washing_point_id IN ? AND role IN ? AND username IS NOT NULL", ids, []Role{RoleStaff, RoleWorker}).
		Group("washing_point_id").
		Scan(&rows).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	for _, row := range rows {
		counts[row.WashingPointID] = row.N
	}
	return counts, nil
}

// RecordLogin stores one login event for u (who just authenticated) and
// remembers the client's platform/version on the user row. Call it right
// after UpdateLastLogin; the client info comes from ctx
// (platform/clientinfo's middleware).
func (r *Repository) RecordLogin(ctx context.Context, u *User, at time.Time) error {
	info := clientinfo.From(ctx)
	var version *string
	if info.Version != "" {
		version = &info.Version
	}
	id, err := uuid.NewV7()
	if err != nil {
		return apperror.Internal(err)
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO login_events (id, user_id, role, washing_point_id, platform, app_version, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, u.ID, string(u.Role), u.WashingPointID, info.Platform, version, at,
		).Error; err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", u.ID).
			Updates(map[string]any{"last_platform": info.Platform, "last_app_version": version}).Error
	})
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}
