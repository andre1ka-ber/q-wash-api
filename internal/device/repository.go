package device

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"q-wash-api/internal/apperror"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Upsert registers token for userID. A token already known (the same phone
// re-registering, or a different account signing in on it) is moved to the
// caller rather than duplicated.
func (r *Repository) Upsert(ctx context.Context, userID uuid.UUID, token string, platform Platform) error {
	t := &Token{UserID: userID, Token: token, Platform: platform}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token"}},
		DoUpdates: clause.AssignmentColumns([]string{"user_id", "platform", "updated_at"}),
	}).Create(t).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// DeleteOwned removes the caller's own token; deleting an unknown or
// someone else's token is a silent no-op (idempotent logout).
func (r *Repository) DeleteOwned(ctx context.Context, userID uuid.UUID, token string) error {
	if err := r.db.WithContext(ctx).Where("user_id = ? AND token = ?", userID, token).Delete(&Token{}).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// DeleteByToken drops a token the push provider reported as dead.
func (r *Repository) DeleteByToken(ctx context.Context, token string) error {
	if err := r.db.WithContext(ctx).Where("token = ?", token).Delete(&Token{}).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID) ([]Token, error) {
	var tokens []Token
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&tokens).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return tokens, nil
}
