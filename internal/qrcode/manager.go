package qrcode

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/washingpoint"
)

// bookingAttributionWindow is how long after a scan a new booking at the
// same washing point still counts toward "bookings via QR" — a rough
// proxy, not real causal tracking (see queue.Repository.
// CountCreatedNearTimes).
const bookingAttributionWindow = 30 * time.Minute

type DayCount struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

type Stats struct {
	ScansToday    int64
	Scans7d       int64
	ScansByDay    [7]DayCount
	BookingsViaQR int64
}

type Manager struct {
	repo      *Repository
	wpRepo    *washingpoint.Repository
	queueRepo *queue.Repository
}

func NewManager(repo *Repository, wpRepo *washingpoint.Repository, queueRepo *queue.Repository) *Manager {
	return &Manager{repo: repo, wpRepo: wpRepo, queueRepo: queueRepo}
}

func (m *Manager) GenerateBatch(ctx context.Context, count int, batchLabel string) ([]QRCode, error) {
	if count < 1 || count > 500 {
		return nil, apperror.BadRequest("invalid_count", "count must be between 1 and 500")
	}

	codes := make([]QRCode, count)
	for i := range codes {
		token, err := generateToken()
		if err != nil {
			return nil, apperror.Internal(err)
		}
		codes[i] = QRCode{Token: token, BatchLabel: batchLabel, Status: StatusFree}
	}
	if err := m.repo.CreateBatch(ctx, codes); err != nil {
		return nil, err
	}
	return codes, nil
}

func (m *Manager) Assign(ctx context.Context, qrCodeID, washingPointID uuid.UUID) (*QRCode, error) {
	c, err := m.repo.FindByID(ctx, qrCodeID)
	if err != nil {
		return nil, err
	}
	if c.Status == StatusDisabled {
		return nil, apperror.Conflict("qr_code_disabled", "this qr code has been disabled and can't be reassigned")
	}
	if _, err := m.wpRepo.FindByID(ctx, washingPointID); err != nil {
		return nil, err
	}

	// One code per point: free whatever was already assigned there.
	existing, err := m.repo.FindByWashingPointID(ctx, washingPointID)
	if err != nil {
		if appErr, ok := err.(*apperror.Error); !ok || appErr.Status != http.StatusNotFound {
			return nil, err
		}
	} else if existing.ID != c.ID {
		existing.Status = StatusFree
		existing.WashingPointID = nil
		existing.AssignedAt = nil
		existing.ReplacementRequestedAt = nil
		if err := m.repo.Update(ctx, existing); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	c.Status = StatusAssigned
	c.WashingPointID = &washingPointID
	c.AssignedAt = &now
	c.ReplacementRequestedAt = nil
	if err := m.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Manager) Unassign(ctx context.Context, qrCodeID uuid.UUID) (*QRCode, error) {
	c, err := m.repo.FindByID(ctx, qrCodeID)
	if err != nil {
		return nil, err
	}
	c.Status = StatusFree
	c.WashingPointID = nil
	c.AssignedAt = nil
	c.ReplacementRequestedAt = nil
	if err := m.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Disable is terminal: an assigned or free code moves to disabled and
// (if it was assigned) is released from its point — it does not return
// to the free pool, unlike Unassign.
func (m *Manager) Disable(ctx context.Context, qrCodeID uuid.UUID) (*QRCode, error) {
	c, err := m.repo.FindByID(ctx, qrCodeID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	c.Status = StatusDisabled
	c.WashingPointID = nil
	c.DisabledAt = &now
	if err := m.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Manager) RequestReplacement(ctx context.Context, qrCodeID uuid.UUID) (*QRCode, error) {
	c, err := m.repo.FindByID(ctx, qrCodeID)
	if err != nil {
		return nil, err
	}
	if c.Status != StatusAssigned {
		return nil, apperror.Conflict("qr_code_not_assigned", "only an assigned qr code can have a replacement requested")
	}
	now := time.Now()
	c.ReplacementRequestedAt = &now
	if err := m.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Manager) Stats(ctx context.Context, c *QRCode) (Stats, error) {
	loc := queue.BusinessLocation()
	now := time.Now().In(loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	sevenDaysAgo := dayStart.AddDate(0, 0, -6)

	scansToday, err := m.repo.CountScans(ctx, c.ID, dayStart)
	if err != nil {
		return Stats{}, err
	}
	scans7d, err := m.repo.CountScans(ctx, c.ID, sevenDaysAgo)
	if err != nil {
		return Stats{}, err
	}
	scanTimes, err := m.repo.ScanTimesSince(ctx, c.ID, sevenDaysAgo)
	if err != nil {
		return Stats{}, err
	}

	var byDay [7]DayCount
	for i := 0; i < 7; i++ {
		day := sevenDaysAgo.AddDate(0, 0, i)
		byDay[i] = DayCount{Date: day.Format("2006-01-02")}
	}
	for _, t := range scanTimes {
		local := t.In(loc)
		idx := int(local.Sub(sevenDaysAgo).Hours() / 24)
		if idx >= 0 && idx < 7 {
			byDay[idx].Count++
		}
	}

	var bookingsViaQR int64
	if c.WashingPointID != nil && len(scanTimes) > 0 {
		bookingsViaQR, err = m.queueRepo.CountCreatedNearTimes(ctx, *c.WashingPointID, scanTimes, bookingAttributionWindow)
		if err != nil {
			return Stats{}, err
		}
	}

	return Stats{
		ScansToday:    scansToday,
		Scans7d:       scans7d,
		ScansByDay:    byDay,
		BookingsViaQR: bookingsViaQR,
	}, nil
}
