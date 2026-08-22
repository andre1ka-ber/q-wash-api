package service

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey"`
	WashingPointID  uuid.UUID `gorm:"type:uuid;not null;index"`
	Name            string    `gorm:"type:varchar(255);not null"`
	Description     *string   `gorm:"type:text"`
	DurationMinutes int       `gorm:"not null"`
	PictureURL      *string   `gorm:"type:varchar(500)"`
	IsActive        bool      `gorm:"not null;default:true"`
	CreatedAt       time.Time
	UpdatedAt       time.Time

	PriceOptions []ServicePriceOption `gorm:"foreignKey:ServiceID"`
}

func (Service) TableName() string { return "services" }

func (s *Service) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		s.ID = id
	}
	return nil
}

// ServicePriceOption is sub-type pricing for a Service (e.g. car size, or
// scope like "full body" vs "parts only"). Every service should have at
// least one, with exactly one marked IsDefault.
type ServicePriceOption struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	ServiceID  uuid.UUID `gorm:"type:uuid;not null;index"`
	Name       string    `gorm:"type:varchar(255);not null"`
	PriceCents int       `gorm:"not null"`
	IsDefault  bool      `gorm:"not null;default:false"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (ServicePriceOption) TableName() string { return "service_price_options" }

func (p *ServicePriceOption) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		p.ID = id
	}
	return nil
}
