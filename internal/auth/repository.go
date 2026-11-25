package auth

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

func (r *Repository) CreateOTPCode(ctx context.Context, o *OTPCode) error {
	if err := r.db.WithContext(ctx).Create(o).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// LatestActiveOTPCode returns the most recent non-consumed OTP for phone,
// regardless of expiry (the caller decides what to do with an expired one).
func (r *Repository) LatestActiveOTPCode(ctx context.Context, phone string) (*OTPCode, error) {
	var o OTPCode
	err := r.db.WithContext(ctx).
		Where("phone_number = ? AND consumed_at IS NULL", phone).
		Order("created_at DESC").
		First(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &o, nil
}

// WithPhoneLock runs fn in a transaction holding an advisory lock for phone,
// so concurrent code requests for one number run one after another and each
// sees the codes issued before it.
func (r *Repository) WithPhoneLock(ctx context.Context, phone string, fn func(*Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", phone).Error; err != nil {
			return apperror.Internal(err)
		}
		return fn(NewRepository(tx))
	})
}

// CountOTPCodesSince counts the codes issued to phone after since, consumed or
// not.
func (r *Repository) CountOTPCodesSince(ctx context.Context, phone string, since time.Time) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&OTPCode{}).
		Where("phone_number = ? AND created_at > ?", phone, since).
		Count(&n).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return n, nil
}

// ClaimOTPAttempt counts one verification attempt and reports whether the code
// still had one left. The check and the increment are one UPDATE, so parallel
// guesses cannot exceed maxAttempts.
func (r *Repository) ClaimOTPAttempt(ctx context.Context, id uuid.UUID, maxAttempts int) (bool, error) {
	res := r.db.WithContext(ctx).Model(&OTPCode{}).
		Where("id = ? AND consumed_at IS NULL AND attempts < ?", id, maxAttempts).
		Update("attempts", gorm.Expr("attempts + 1"))
	if res.Error != nil {
		return false, apperror.Internal(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ConsumeOTPCode marks the code used and reports whether this call was the one
// that did it.
func (r *Repository) ConsumeOTPCode(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	res := r.db.WithContext(ctx).Model(&OTPCode{}).
		Where("id = ? AND consumed_at IS NULL", id).
		Update("consumed_at", at)
	if res.Error != nil {
		return false, apperror.Internal(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *Repository) CreateRefreshToken(ctx context.Context, t *RefreshToken) error {
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) FindActiveRefreshTokenByHash(ctx context.Context, hash string) (*RefreshToken, error) {
	var t RefreshToken
	err := r.db.WithContext(ctx).
		Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, time.Now()).
		First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &t, nil
}

// FindRefreshTokenByHash returns the token with that hash in any state
// (active, expired or revoked), or nil. Only for diagnosing reuse of a
// revoked token; authentication must use FindActiveRefreshTokenByHash.
func (r *Repository) FindRefreshTokenByHash(ctx context.Context, hash string) (*RefreshToken, error) {
	var t RefreshToken
	err := r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &t, nil
}

// RevokeRefreshTokenIfActive revokes the token and reports whether this call
// was the one that did it. The check and the write are one UPDATE, so of two
// concurrent rotations of the same token exactly one gets true.
func (r *Repository) RevokeRefreshTokenIfActive(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", time.Now())
	if res.Error != nil {
		return false, apperror.Internal(res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *Repository) RevokeRefreshToken(ctx context.Context, id uuid.UUID) error {
	err := r.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", time.Now()).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) RevokeAllRefreshTokensForUser(ctx context.Context, userID uuid.UUID) error {
	err := r.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", time.Now()).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}
