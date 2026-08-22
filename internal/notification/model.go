package notification

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusSent    Status = "sent"
	StatusFailed  Status = "failed"
)

type Channel string

const (
	ChannelSMS Channel = "sms"
)

type Notification struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;index"`
	QueueID   *uuid.UUID `gorm:"type:uuid"`
	Status    Status     `gorm:"type:varchar(16);not null;default:pending"`
	Channel   Channel    `gorm:"type:varchar(16);not null;default:sms"`
	Text      string     `gorm:"type:text;not null"`
	SendAt    time.Time  `gorm:"not null"`
	SentAt    *time.Time
	CreatedAt time.Time
}

func (Notification) TableName() string { return "notifications" }

func (n *Notification) BeforeCreate(tx *gorm.DB) error {
	if n.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		n.ID = id
	}
	return nil
}
