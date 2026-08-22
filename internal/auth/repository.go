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

func (r *Repository) SaveOTPCode(ctx context.Context, o *OTPCode) error {
	if err := r.db.WithContext(ctx).Save(o).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
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
