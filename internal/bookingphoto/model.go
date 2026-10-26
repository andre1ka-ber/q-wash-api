// Package bookingphoto backs the worker app's photo strip — photos a
// technician attaches to a single booking while its wash is in progress.
package bookingphoto

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BookingPhoto struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	QueueID   uuid.UUID `gorm:"type:uuid;not null;index"`
	URL       string    `gorm:"type:varchar(500);not null"`
	CreatedAt time.Time
}

func (BookingPhoto) TableName() string { return "booking_photos" }

func (p *BookingPhoto) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		p.ID = id
	}
	return nil
}
