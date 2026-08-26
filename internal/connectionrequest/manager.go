package connectionrequest

import (
	"context"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/owner"
	"q-wash-api/internal/washingpoint"
)

// Manager holds the one real business rule beyond plain CRUD: approving a
// request creates an Owner (reusing one whose contact phone matches, if
// any) and a WashingPoint left in status=pending_review — deliberately not
// active, since the point still needs services/boxes/hours set up before
// it's genuinely bookable (decided with the user, see
// docs/PLAN_WEB_APPS.md). Latitude/longitude aren't collected on the
// request itself — that's the separate admin "+ New point" wizard flow,
// which sets them directly on creation — so an approved point starts with
// placeholder 0,0 coordinates that must be corrected via
// PATCH /washing-points/{id} before it can go live.
type Manager struct {
	repo           *Repository
	ownerRepo      *owner.Repository
	wpRepo         *washingpoint.Repository
	scheduleSeeder ScheduleSeeder
	boxSeeder      BoxSeeder
}

// ScheduleSeeder provisions a newly created washing point's initial
// per-weekday schedule (docs/PLAN_WEB_APPS.md phase 5). Locally defined
// for the same import-direction reason as washingpoint.ScheduleSeeder;
// satisfied structurally by *schedule.Manager.
type ScheduleSeeder interface {
	SeedDefault(ctx context.Context, washingPointID uuid.UUID, openTime, closeTime string) error
}

// BoxSeeder provisions a newly created washing point's initial boxes
// (docs/PLAN_WEB_APPS.md phase 6). Locally defined for the same
// import-direction reason as washingpoint.BoxSeeder; satisfied
// structurally by *box.Manager.
type BoxSeeder interface {
	SeedDefault(ctx context.Context, washingPointID uuid.UUID, count int) error
}

func NewManager(repo *Repository, ownerRepo *owner.Repository, wpRepo *washingpoint.Repository, scheduleSeeder ScheduleSeeder, boxSeeder BoxSeeder) *Manager {
	return &Manager{repo: repo, ownerRepo: ownerRepo, wpRepo: wpRepo, scheduleSeeder: scheduleSeeder, boxSeeder: boxSeeder}
}

func (m *Manager) Approve(ctx context.Context, id, reviewerID uuid.UUID) (*ConnectionRequest, *washingpoint.WashingPoint, error) {
	cr, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if cr.Status != StatusNew {
		return nil, nil, apperror.Conflict("connection_request_already_reviewed", "connection request has already been reviewed")
	}

	own, err := m.ownerRepo.FindByPhone(ctx, cr.ContactPhone)
	if err != nil {
		return nil, nil, err
	}
	if own == nil {
		contactName := cr.ContactName
		contactPhone := cr.ContactPhone
		own = &owner.Owner{Name: cr.BusinessName, ContactName: &contactName, ContactPhone: &contactPhone}
		if err := m.ownerRepo.Create(ctx, own); err != nil {
			return nil, nil, err
		}
	}

	wp := &washingpoint.WashingPoint{
		OwnerID:    &own.ID,
		Name:       cr.BusinessName,
		Address:    cr.Address,
		BoxesCount: cr.BoxesCount,
		OpenTime:   "08:00",
		CloseTime:  "20:00",
		Status:     washingpoint.StatusPendingReview,
	}
	if err := m.wpRepo.Create(ctx, wp); err != nil {
		return nil, nil, err
	}
	if err := m.scheduleSeeder.SeedDefault(ctx, wp.ID, wp.OpenTime, wp.CloseTime); err != nil {
		return nil, nil, apperror.Internal(err)
	}
	if err := m.boxSeeder.SeedDefault(ctx, wp.ID, wp.BoxesCount); err != nil {
		return nil, nil, apperror.Internal(err)
	}

	now := time.Now()
	cr.Status = StatusApproved
	cr.ReviewedBy = &reviewerID
	cr.ReviewedAt = &now
	if err := m.repo.Update(ctx, cr); err != nil {
		return nil, nil, err
	}
	return cr, wp, nil
}

func (m *Manager) Reject(ctx context.Context, id, reviewerID uuid.UUID) (*ConnectionRequest, error) {
	cr, err := m.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if cr.Status != StatusNew {
		return nil, apperror.Conflict("connection_request_already_reviewed", "connection request has already been reviewed")
	}

	now := time.Now()
	cr.Status = StatusRejected
	cr.ReviewedBy = &reviewerID
	cr.ReviewedAt = &now
	if err := m.repo.Update(ctx, cr); err != nil {
		return nil, err
	}
	return cr, nil
}
