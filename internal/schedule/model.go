// Package schedule holds each washing point's per-weekday operating hours
// (docs/PLAN_WEB_APPS.md phase 5) — the source of truth
// internal/queue's availability algorithm and booking-creation validation
// consult, replacing the old flat WashingPoint.OpenTime/CloseTime columns
// for that purpose (those columns still exist for now — see the note on
// washingpoint.Handler — but are no longer read by any booking logic).
package schedule

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WashingPointSchedule is one weekday's operating window for one washing
// point. Weekday is 0=Monday..6=Sunday. OpenTime/CloseTime are nil when
// IsOpen is false; BreakStart/BreakEnd are optional even when open (one
// lunch-break window per day, at most).
type WashingPointSchedule struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	WashingPointID uuid.UUID `gorm:"type:uuid;not null;index"`
	Weekday        int       `gorm:"not null"`
	// No `default:true` gorm tag here even though the migration's column
	// does have that DB default: GORM omits a field from the INSERT column
	// list whenever its Go value equals the zero value AND the field has a
	// `default` tag (it assumes the zero value means "let the DB default
	// apply"). Since IsOpen's zero value is false, that silently turned
	// every ReplaceAll(..., {IsOpen: false, ...}) into is_open=true —
	// found via q-wash-cabinet's Hours tab actually setting a day closed.
	IsOpen     bool    `gorm:"not null"`
	OpenTime   *string `gorm:"type:varchar(5)"`
	CloseTime  *string `gorm:"type:varchar(5)"`
	BreakStart *string `gorm:"type:varchar(5)"`
	BreakEnd   *string `gorm:"type:varchar(5)"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (WashingPointSchedule) TableName() string { return "washing_point_schedules" }

func (s *WashingPointSchedule) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		s.ID = id
	}
	return nil
}
