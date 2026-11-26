package auth

import (
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Passkey is one WebAuthn credential a customer can sign in with.
type Passkey struct {
	ID           uuid.UUID           `gorm:"type:uuid;primaryKey"`
	UserID       uuid.UUID           `gorm:"type:uuid;not null;index"`
	CredentialID []byte              `gorm:"type:bytea;not null;uniqueIndex"`
	Credential   webauthn.Credential `gorm:"type:jsonb;serializer:json;not null"`
	// Platform is the client bucket that created it (see platform/clientinfo).
	Platform   string `gorm:"type:varchar(16);not null"`
	LastUsedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (Passkey) TableName() string { return "passkeys" }

func (p *Passkey) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		p.ID = id
	}
	return nil
}

type challengePurpose string

const (
	purposeRegister challengePurpose = "register"
	purposeLogin    challengePurpose = "login"
)

// PasskeyChallenge is the server side of one pending passkey ceremony.
type PasskeyChallenge struct {
	ID        uuid.UUID            `gorm:"type:uuid;primaryKey"`
	UserID    *uuid.UUID           `gorm:"type:uuid"`
	Purpose   challengePurpose     `gorm:"type:varchar(16);not null"`
	Session   webauthn.SessionData `gorm:"type:jsonb;serializer:json;not null"`
	ExpiresAt time.Time            `gorm:"not null"`
	CreatedAt time.Time
}

func (PasskeyChallenge) TableName() string { return "passkey_challenges" }

func (c *PasskeyChallenge) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		c.ID = id
	}
	return nil
}
