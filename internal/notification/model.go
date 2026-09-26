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
	ChannelSMS  Channel = "sms"
	ChannelPush Channel = "push"
)

// Kind identifies a booking-stage notification so each is sent at most once
// per booking (unique on queue_id + kind). Manually created notifications
// (POST /notifications) have no kind.
type Kind string

const (
	KindReminder Kind = "reminder_1h" // ~1h before the scheduled start
	KindLate     Kind = "late_5m"     // 5 min past the start and washing hasn't begun
	KindStarted  Kind = "started"     // status -> washing
	KindFinished Kind = "finished"    // status -> ready
)

type Notification struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;index"`
	QueueID   *uuid.UUID `gorm:"type:uuid"`
	Status    Status     `gorm:"type:varchar(16);not null;default:pending"`
	Channel   Channel    `gorm:"type:varchar(16);not null;default:sms"`
	Kind      *Kind      `gorm:"type:varchar(32)"`
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
