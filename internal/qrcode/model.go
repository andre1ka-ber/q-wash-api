// Package qrcode backs the admin app's QR-code pool: batches of codes
// generated ahead of printing, assigned one-per-washing-point, and scanned
// by customers at the physical location. See docs/DATA_MODEL.md's QrCode
// and QrScan entities, and q-wash-api/docs/API.md's "QR codes" section.
package qrcode

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type QRCodeStatus string

const (
	StatusFree     QRCodeStatus = "free"
	StatusAssigned QRCodeStatus = "assigned"
	StatusDisabled QRCodeStatus = "disabled"
)

// QRCode is one sticker in the pool. Code() derives the human-facing
// number (QW-0031) from Seq at read time — it's never stored redundantly,
// so there's one source of truth for what's printed on the sticker. Token
// is the opaque, unguessable part of the public scan URL, deliberately
// unrelated to Seq so a scanner can't enumerate codes by incrementing a
// number.
type QRCode struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey"`
	// autoIncrement tells GORM this column is DB-managed (the bigserial
	// default) so a zero-value Seq is omitted from INSERT instead of being
	// sent literally as 0 for every row in a batch create — without this,
	// GenerateBatch's multi-row insert violates the seq unique constraint
	// on the second row onward.
	Seq                    int64        `gorm:"column:seq;autoIncrement"`
	Token                  string       `gorm:"type:varchar(64);not null"`
	BatchLabel             string       `gorm:"type:varchar(255);not null"`
	Status                 QRCodeStatus `gorm:"type:varchar(16);not null;default:free"`
	WashingPointID         *uuid.UUID   `gorm:"type:uuid"`
	AssignedAt             *time.Time
	DisabledAt             *time.Time
	ReplacementRequestedAt *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (QRCode) TableName() string { return "qr_codes" }

func (c *QRCode) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		c.ID = id
	}
	return nil
}

// Code is the human-facing sticker number, e.g. "QW-0031".
func (c QRCode) Code() string {
	return fmt.Sprintf("QW-%04d", c.Seq)
}

// ParseCode extracts the sequence number from a typed sticker code like
// "QW-0031" or "qw-31" (prefix optional, case-insensitive, whitespace
// trimmed) — used by the "assign by typed code number" flow.
func ParseCode(code string) (int64, error) {
	trimmed := strings.TrimSpace(code)
	trimmed = strings.TrimPrefix(strings.ToUpper(trimmed), "QW-")
	trimmed = strings.TrimPrefix(trimmed, "QW")
	trimmed = strings.TrimLeft(trimmed, "- ")
	seq, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid qr code %q: %w", code, err)
	}
	return seq, nil
}

// QRScan is one recorded hit of a code's public scan URL — the raw event
// log behind scan-count stats and the booking-attribution heuristic (see
// Manager.Stats).
type QRScan struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	QRCodeID  uuid.UUID `gorm:"type:uuid;not null;index"`
	ScannedAt time.Time
}

func (QRScan) TableName() string { return "qr_scans" }

func (s *QRScan) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		s.ID = id
	}
	return nil
}

// generateToken mirrors internal/auth's generateRefreshToken technique
// (crypto/rand -> base64.RawURLEncoding) — unexported there, so
// reimplemented locally rather than crossing package boundaries for it.
func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
