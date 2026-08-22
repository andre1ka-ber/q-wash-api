// Package photo backs the cabinet app's "photos & description" tab
// (docs/PLAN_WEB_APPS.md phase 4) — photos attached to a washing point's
// listing.
package photo

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type WashingPointPhoto struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	WashingPointID uuid.UUID `gorm:"type:uuid;not null;index"`
	URL            string    `gorm:"type:varchar(500);not null"`
	IsCover        bool      `gorm:"not null;default:false"`
	SortOrder      int       `gorm:"not null;default:0"`
	CreatedAt      time.Time
}

func (WashingPointPhoto) TableName() string { return "washing_point_photos" }

func (p *WashingPointPhoto) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		p.ID = id
	}
	return nil
}
