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
	StatusNoShow   Status = "no_show"
)

type Source string

const (
	SourceApp    Source = "app"
	SourceQR     Source = "qr"
	SourceManual Source = "manual"
)

// Queue is a booking / queue entry. Status moves forward through
// queue -> waiting -> washing -> ready; canceled is reachable only from
// queue or waiting; no_show is likewise reachable from queue/waiting, and
// both canceled and no_show can be restored to queue by staff (see
// docs/DATA_MODEL.md).
type Queue struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey"`
	Status Status    `gorm:"type:varchar(16);not null;default:queue"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index"`
	// CarID is nil for walk-in bookings added by staff without any car data.
	CarID            *uuid.UUID `gorm:"type:uuid"`
	ServiceID        uuid.UUID  `gorm:"type:uuid;not null"`
	PriceOptionID    uuid.UUID  `gorm:"type:uuid;not null"`
	WashingPointID   uuid.UUID  `gorm:"type:uuid;not null;index"`
	BoxNumber        int        `gorm:"not null"`
	ScheduledStartAt time.Time  `gorm:"not null"`
	ScheduledEndAt   time.Time  `gorm:"not null"`
	Notes            *string    `gorm:"type:text"`
	Source           Source     `gorm:"type:varchar(8);not null;default:app"`
	CanceledAt       *time.Time
	// PausedAt is only meaningful while Status is StatusWashing — toggled
	// by the worker app's pause/resume actions (PATCH .../pause and .../resume,
	// docs/PLAN_WEB_APPS.md phase 7).
	PausedAt  *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time

	// Items are the booking's services in position order. Not a gorm
	// association: Create inserts them in the same transaction as the row,
	// and the Repository finders fill them in with one batched query
	// (see attachItems).
	Items []QueueItem `gorm:"-"`
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

// QueueItem is one service (with its chosen price option) of a booking.
// Position 0 is the primary item, mirrored into Queue.ServiceID /
// Queue.PriceOptionID for clients that only know one service.
type QueueItem struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey"`
	QueueID       uuid.UUID `gorm:"type:uuid;not null;index"`
	ServiceID     uuid.UUID `gorm:"type:uuid;not null"`
	PriceOptionID uuid.UUID `gorm:"type:uuid;not null"`
	Position      int       `gorm:"not null"`
	CreatedAt     time.Time
}

func (QueueItem) TableName() string { return "queue_items" }

func (i *QueueItem) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		i.ID = id
	}
	return nil
}
