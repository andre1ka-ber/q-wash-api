// Package box backs the cabinet app's "Боксы" tab (docs/PLAN_WEB_APPS.md
// phase 6) — individual washing bays within a point. WashingPoint.BoxesCount
// stays the capacity number the availability algorithm uses; a Box row is
// metadata (a display label) plus an open/closed flag layered on top of one
// of those numbered slots.
package box

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Box struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	WashingPointID uuid.UUID `gorm:"type:uuid;not null;index"`
	Number         int       `gorm:"not null"`
	Label          *string   `gorm:"type:varchar(255)"`
	// Create never sets this false (Manager.Create always opens a new box),
	// so the default tag is safe here — GORM's Create omits a field with a
	// default tag from its INSERT only when the Go value equals the zero
	// value, which bit schedule.WashingPointSchedule.IsOpen (see its own
	// audit note) because that one legitimately gets created as false.
	IsOpen    bool `gorm:"not null;default:true"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Box) TableName() string { return "boxes" }

func (b *Box) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		b.ID = id
	}
	return nil
}
