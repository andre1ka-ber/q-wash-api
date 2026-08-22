package car

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Car struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index"`
	Name      string    `gorm:"type:varchar(255);not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Car) TableName() string { return "cars" }

func (c *Car) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		c.ID = id
	}
	return nil
}
