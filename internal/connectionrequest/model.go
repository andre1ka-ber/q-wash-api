// Package connectionrequest backs the admin app's onboarding queue — a
// prospective owner applying to join the network. Approving one creates an
// Owner (reusing one with a matching contact phone, if any) and a
// WashingPoint left in status=pending_review — see Manager.Approve and
// docs/PLAN_WEB_APPS.md.
package connectionrequest

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Status string

const (
	StatusNew      Status = "new"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

type ConnectionRequest struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey"`
	BusinessName string     `gorm:"type:varchar(255);not null"`
	ContactName  string     `gorm:"type:varchar(255);not null"`
	ContactPhone string     `gorm:"type:varchar(32);not null"`
	Address      string     `gorm:"type:varchar(500);not null"`
	BoxesCount   int        `gorm:"not null"`
	Note         *string    `gorm:"type:text"`
	Status       Status     `gorm:"type:varchar(16);not null;default:new"`
	ReviewedBy   *uuid.UUID `gorm:"type:uuid"`
	ReviewedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (ConnectionRequest) TableName() string { return "connection_requests" }

func (c *ConnectionRequest) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		c.ID = id
	}
	return nil
}
