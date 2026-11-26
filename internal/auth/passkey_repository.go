package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"q-wash-api/internal/apperror"
)

const postgresUniqueViolation = "23505"

// CreateChallenge stores a new challenge and drops the expired ones, so
// abandoned ceremonies do not pile up.
func (r *Repository) CreateChallenge(ctx context.Context, c *PasskeyChallenge, now time.Time) error {
	if err := r.db.WithContext(ctx).Where("expires_at <= ?", now).Delete(&PasskeyChallenge{}).Error; err != nil {
		return apperror.Internal(err)
	}
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// TakeChallenge deletes the challenge and returns it, or nil if there is none
// with that id and purpose. One DELETE, so a challenge can be used only once.
func (r *Repository) TakeChallenge(ctx context.Context, id uuid.UUID, purpose challengePurpose) (*PasskeyChallenge, error) {
	var taken []PasskeyChallenge
	err := r.db.WithContext(ctx).Clauses(clause.Returning{}).
		Where("id = ? AND purpose = ?", id, purpose).
		Delete(&taken).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	if len(taken) == 0 {
		return nil, nil
	}
	return &taken[0], nil
}

func (r *Repository) ListPasskeys(ctx context.Context, userID uuid.UUID) ([]Passkey, error) {
	var passkeys []Passkey
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at, id").Find(&passkeys).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return passkeys, nil
}

// FindPasskeyByCredentialID returns nil if no passkey has that credential id.
func (r *Repository) FindPasskeyByCredentialID(ctx context.Context, credentialID []byte) (*Passkey, error) {
	var p Passkey
	err := r.db.WithContext(ctx).Where("credential_id = ?", credentialID).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &p, nil
}

// CreatePasskey stores p unless its owner already has max passkeys. The count
// and the insert run under a per-user lock, so parallel requests cannot pass
// the limit.
func (r *Repository) CreatePasskey(ctx context.Context, p *Passkey, max int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "passkey:"+p.UserID.String()).Error; err != nil {
			return apperror.Internal(err)
		}
		var n int64
		if err := tx.Model(&Passkey{}).Where("user_id = ?", p.UserID).Count(&n).Error; err != nil {
			return apperror.Internal(err)
		}
		if n >= int64(max) {
			return errPasskeyLimit()
		}
		err := tx.Create(p).Error
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == postgresUniqueViolation {
			return apperror.Conflict("passkey_exists", "this passkey is already registered")
		}
		if err != nil {
			return apperror.Internal(err)
		}
		return nil
	})
}

// SavePasskeyUse stores the credential record as updated by a sign-in.
func (r *Repository) SavePasskeyUse(ctx context.Context, p *Passkey) error {
	err := r.db.WithContext(ctx).Model(p).Select("credential", "last_used_at", "updated_at").Updates(p).Error
	if err != nil {
		return apperror.Internal(err)
	}
	return nil
}

// DeletePasskey removes the user's own passkey and reports whether there was
// one.
func (r *Repository) DeletePasskey(ctx context.Context, userID, id uuid.UUID) (bool, error) {
	res := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&Passkey{})
	if res.Error != nil {
		return false, apperror.Internal(res.Error)
	}
	return res.RowsAffected == 1, nil
}
