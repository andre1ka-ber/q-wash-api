package qrcode

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"q-wash-api/internal/apperror"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// List returns the pool in sticker-number order (ascending seq), not
// newest-first — the pool page reads like a printed sheet, not a feed.
// Optional status filter; search-by-code/text happens in the handler
// after this returns, since the whole pool is expected to stay small
// (tens to low hundreds of rows).
func (r *Repository) List(ctx context.Context, status *QRCodeStatus) ([]QRCode, error) {
	q := r.db.WithContext(ctx).Order("seq asc")
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	var items []QRCode
	if err := q.Find(&items).Error; err != nil {
		return nil, apperror.Internal(err)
	}
	return items, nil
}

func (r *Repository) FindByID(ctx context.Context, id uuid.UUID) (*QRCode, error) {
	var c QRCode
	err := r.db.WithContext(ctx).First(&c, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("qr_code_not_found", "qr code not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &c, nil
}

func (r *Repository) FindByToken(ctx context.Context, token string) (*QRCode, error) {
	var c QRCode
	err := r.db.WithContext(ctx).First(&c, "token = ?", token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("qr_code_not_found", "qr code not found")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &c, nil
}

func (r *Repository) FindByWashingPointID(ctx context.Context, washingPointID uuid.UUID) (*QRCode, error) {
	var c QRCode
	err := r.db.WithContext(ctx).First(&c, "washing_point_id = ?", washingPointID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NotFound("qr_code_not_found", "no qr code assigned to this washing point")
	}
	if err != nil {
		return nil, apperror.Internal(err)
	}
	return &c, nil
}

// CreateBatch bulk-inserts new codes. BeforeCreate still fires per row
// (UUID), and GORM populates each row's Seq from the bigserial default
// after insert.
func (r *Repository) CreateBatch(ctx context.Context, codes []QRCode) error {
	if err := r.db.WithContext(ctx).Create(&codes).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, c *QRCode) error {
	if err := r.db.WithContext(ctx).Save(c).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) CreateScan(ctx context.Context, qrCodeID uuid.UUID) error {
	// ScannedAt is set explicitly rather than left to the column's DB
	// default: GORM only auto-populates fields it recognizes by name
	// (CreatedAt/UpdatedAt), so a bare zero-value time.Time{} here would be
	// sent to Postgres literally instead of falling through to `now()`.
	scan := &QRScan{QRCodeID: qrCodeID, ScannedAt: time.Now()}
	if err := r.db.WithContext(ctx).Create(scan).Error; err != nil {
		return apperror.Internal(err)
	}
	return nil
}

func (r *Repository) CountScans(ctx context.Context, qrCodeID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&QRScan{}).
		Where("qr_code_id = ? AND scanned_at >= ?", qrCodeID, since).
		Count(&count).Error
	if err != nil {
		return 0, apperror.Internal(err)
	}
	return count, nil
}

// ScanTimesSince returns raw scan timestamps for the 7-day chart and the
// booking-attribution heuristic (internal/queue.Repository.
// CountCreatedNearTimes needs the actual instants, not just a count).
func (r *Repository) ScanTimesSince(ctx context.Context, qrCodeID uuid.UUID, since time.Time) ([]time.Time, error) {
	var scans []QRScan
	err := r.db.WithContext(ctx).
		Where("qr_code_id = ? AND scanned_at >= ?", qrCodeID, since).
		Order("scanned_at asc").
		Find(&scans).Error
	if err != nil {
		return nil, apperror.Internal(err)
	}
	times := make([]time.Time, len(scans))
	for i, s := range scans {
		times[i] = s.ScannedAt
	}
	return times, nil
}
