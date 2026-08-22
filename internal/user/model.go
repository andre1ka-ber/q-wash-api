package user

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Role string

const (
	RoleCustomer Role = "customer"
	RoleStaff    Role = "staff"
	RoleAdmin    Role = "admin"
	// RoleWorker is a shift technician, scoped to one washing point via
	// WashingPointID below. Logs in the same way staff/admin do (see
	// docs/PLAN_WEB_APPS.md) — not yet wired to any endpoint.
	RoleWorker Role = "worker"
)

type User struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey"`
	PhoneNumber string    `gorm:"type:varchar(32);uniqueIndex;not null"`
	Name        *string   `gorm:"type:varchar(255)"`
	Role        Role      `gorm:"type:varchar(16);not null;default:customer"`
	// WashingPointID scopes a staff/worker account to the one point they
	// work at; always nil for admin (network-wide) and customer.
	WashingPointID *uuid.UUID `gorm:"type:uuid;index"`
	LastLoginAt    *time.Time
	// Username/PasswordHash are only set for staff/admin accounts that log
	// in via POST /auth/login (the queue board and staff panel) instead of
	// phone+OTP. Customers leave both nil.
	Username     *string `gorm:"type:varchar(50);uniqueIndex"`
	PasswordHash *string `gorm:"type:varchar(255)"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (User) TableName() string { return "users" }

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		u.ID = id
	}
	return nil
}
