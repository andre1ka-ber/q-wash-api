package box

import (
	"context"

	"github.com/google/uuid"
)

// Manager holds the one rule beyond plain CRUD: a new box's number is
// auto-assigned, never client-chosen (see Repository.MaxNumber).
type Manager struct {
	repo *Repository
}

func NewManager(repo *Repository) *Manager {
	return &Manager{repo: repo}
}

func (m *Manager) Create(ctx context.Context, washingPointID uuid.UUID, label *string) (*Box, error) {
	max, err := m.repo.MaxNumber(ctx, washingPointID)
	if err != nil {
		return nil, err
	}
	b := &Box{
		WashingPointID: washingPointID,
		Number:         max + 1,
		Label:          label,
		IsOpen:         true,
	}
	if err := m.repo.Create(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// SeedDefault provisions one open box per number 1..count for a newly
// created washing point — same reasoning as schedule.Manager.SeedDefault:
// GET .../boxes would otherwise return nothing for a brand-new point until
// someone manually added boxes via the cabinet app, even though
// boxes_count already says it has that many. Not idempotent (unlike
// schedule's delete-then-insert ReplaceAll) — callers must only invoke this
// once, right after WashingPoint.Create, never against an existing point.
func (m *Manager) SeedDefault(ctx context.Context, washingPointID uuid.UUID, count int) error {
	for n := 1; n <= count; n++ {
		b := &Box{WashingPointID: washingPointID, Number: n, IsOpen: true}
		if err := m.repo.Create(ctx, b); err != nil {
			return err
		}
	}
	return nil
}
