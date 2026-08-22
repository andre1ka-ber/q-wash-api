// Package owner holds the businesses that own washing points. Kept
// separate from user: an owner doesn't necessarily have their own login
// (staff log in per-point instead), but admin needs somewhere to hang
// contact info and group points by owner regardless. See
// docs/PLAN_WEB_APPS.md.
package owner

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Owner struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name         string    `gorm:"type:varchar(255);not null"`
	ContactName  *string   `gorm:"type:varchar(255)"`
	ContactPhone *string   `gorm:"type:varchar(32)"`
	ContactEmail *string   `gorm:"type:varchar(255)"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Owner) TableName() string { return "owners" }

func (o *Owner) BeforeCreate(tx *gorm.DB) error {
	if o.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		o.ID = id
	}
	return nil
}
