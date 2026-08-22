package auth

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OTPCode is a short-lived one-time login code tied to a phone number.
type OTPCode struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey"`
	PhoneNumber string    `gorm:"type:varchar(32);not null;index"`
	CodeHash    string    `gorm:"type:varchar(255);not null"`
	ExpiresAt   time.Time `gorm:"not null"`
	Attempts    int       `gorm:"not null;default:0"`
	ConsumedAt  *time.Time
	CreatedAt   time.Time
}

func (OTPCode) TableName() string { return "otp_codes" }

func (o *OTPCode) BeforeCreate(tx *gorm.DB) error {
	if o.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		o.ID = id
	}
	return nil
}

// RefreshToken supports the JWT access + rotating refresh token pattern.
type RefreshToken struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"`
	TokenHash string    `gorm:"type:varchar(255);not null;uniqueIndex"`
	ExpiresAt time.Time `gorm:"not null"`
	RevokedAt *time.Time
	CreatedAt time.Time
}

func (RefreshToken) TableName() string { return "refresh_tokens" }

func (r *RefreshToken) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		r.ID = id
	}
	return nil
}
