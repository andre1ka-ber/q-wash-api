// Package device stores the FCM push tokens of customers' installed apps.
package device

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
)

func (p Platform) Valid() bool { return p == PlatformAndroid || p == PlatformIOS }

type Token struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"`
	Token     string    `gorm:"type:text;not null;uniqueIndex"`
	Platform  Platform  `gorm:"type:varchar(16);not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Token) TableName() string { return "device_tokens" }

func (t *Token) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		t.ID = id
	}
	return nil
}
