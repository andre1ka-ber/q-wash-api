package washingpoint

import (
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

type Status string

const (
	StatusActive        Status = "active"
	StatusPaused        Status = "paused"
	StatusPendingReview Status = "pending_review"
)

type WashingPoint struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey"`
	OwnerID    *uuid.UUID `gorm:"type:uuid;index"`
	Name       string     `gorm:"type:varchar(255);not null"`
	Address    string     `gorm:"type:varchar(500);not null"`
	Latitude   float64    `gorm:"not null"`
	Longitude  float64    `gorm:"not null"`
	BoxesCount int        `gorm:"not null;default:2"`
	// OpenTime/CloseTime remain the source of truth for the availability
	// algorithm until docs/PLAN_WEB_APPS.md phase 5 moves it to per-weekday
	// WashingPointSchedule rows.
	OpenTime  string `gorm:"type:varchar(5);not null;default:'08:00'"` // "HH:MM"
	CloseTime string `gorm:"type:varchar(5);not null;default:'20:00'"` // "HH:MM"
	Status    Status `gorm:"type:varchar(16);not null;default:active"`
	// Description/Amenities back the cabinet app's photos-and-description
	// tab (docs/PLAN_WEB_APPS.md phase 4) — not yet surfaced by Handler.
	Description *string        `gorm:"type:text"`
	Amenities   pq.StringArray `gorm:"type:text[]"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (WashingPoint) TableName() string { return "washing_points" }

func (w *WashingPoint) BeforeCreate(tx *gorm.DB) error {
	if w.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		w.ID = id
	}
	return nil
}
