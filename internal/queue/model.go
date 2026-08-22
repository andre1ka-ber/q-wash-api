package queue

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Status string

const (
	StatusQueue    Status = "queue"
	StatusWaiting  Status = "waiting"
	StatusWashing  Status = "washing"
	StatusReady    Status = "ready"
	StatusCanceled Status = "canceled"
)

// Queue is a booking / queue entry. Status moves forward through
// queue -> waiting -> washing -> ready; canceled is reachable only from
// queue or waiting (see docs/DATA_MODEL.md).
type Queue struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey"`
	Status           Status    `gorm:"type:varchar(16);not null;default:queue"`
	UserID           uuid.UUID `gorm:"type:uuid;not null;index"`
	CarID            uuid.UUID `gorm:"type:uuid;not null"`
	ServiceID        uuid.UUID `gorm:"type:uuid;not null"`
	PriceOptionID    uuid.UUID `gorm:"type:uuid;not null"`
	WashingPointID   uuid.UUID `gorm:"type:uuid;not null;index"`
	BoxNumber        int       `gorm:"not null"`
	ScheduledStartAt time.Time `gorm:"not null"`
	ScheduledEndAt   time.Time `gorm:"not null"`
	Notes            *string   `gorm:"type:text"`
	CanceledAt       *time.Time
	// PausedAt is only meaningful while Status is StatusWashing — toggled
	// by the worker app's pause/resume actions (docs/PLAN_WEB_APPS.md
	// phase 7), not yet wired to any endpoint.
	PausedAt  *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Queue) TableName() string { return "queue" }

func (q *Queue) BeforeCreate(tx *gorm.DB) error {
	if q.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		q.ID = id
	}
	return nil
}
