package photo

import (
	"context"
	"io"

	"github.com/google/uuid"

	"q-wash-api/internal/platform/storage"
)

// Manager owns the two rules that go beyond plain CRUD: keeping "at most
// one cover photo per washing point" true, and coordinating the storage
// write/delete with the DB row.
type Manager struct {
	repo    *Repository
	storage storage.Storage
}

func NewManager(repo *Repository, fileStorage storage.Storage) *Manager {
	return &Manager{repo: repo, storage: fileStorage}
}

// Upload stores content via storage.Storage, then creates the DB row.
// ext must already be a verified, server-derived extension (see
// handler.go's content-type sniffing) — never taken from client input
// directly. The first photo for a washing point is auto-promoted to cover
// regardless of isCover; a later isCover:true unsets any existing cover
// first, keeping exactly one true.
func (m *Manager) Upload(ctx context.Context, washingPointID uuid.UUID, ext string, content io.Reader, isCover bool) (*WashingPointPhoto, error) {
	url, err := m.storage.Put(ctx, "photo"+ext, content)
	if err != nil {
		return nil, err
	}

	count, err := m.repo.CountByWashingPoint(ctx, washingPointID)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		isCover = true
	}
	if isCover {
		if err := m.repo.UnsetCover(ctx, washingPointID); err != nil {
			return nil, err
		}
	}

	p := &WashingPointPhoto{
		WashingPointID: washingPointID,
		URL:            url,
		IsCover:        isCover,
		SortOrder:      int(count),
	}
	if err := m.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Delete removes the DB row, then best-effort deletes the underlying file
// — a storage error there isn't fatal (an orphaned file is a cheap
// cleanup problem; a photo the UI can't remove because storage hiccuped
// is worse). If the deleted photo was the cover and others remain, the
// oldest remaining one is auto-promoted, mirroring
// service.Manager.DeletePriceOption's default-reassignment.
func (m *Manager) Delete(ctx context.Context, p *WashingPointPhoto) error {
	if err := m.repo.Delete(ctx, p.ID); err != nil {
		return err
	}
	_ = m.storage.Delete(ctx, p.URL)

	if !p.IsCover {
		return nil
	}
	next, err := m.repo.FirstExcept(ctx, p.WashingPointID, p.ID)
	if err != nil {
		return err
	}
	if next != nil {
		next.IsCover = true
		if err := m.repo.Update(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

// SetCover promotes p to cover, unsetting any existing one first.
func (m *Manager) SetCover(ctx context.Context, p *WashingPointPhoto) error {
	if err := m.repo.UnsetCover(ctx, p.WashingPointID); err != nil {
		return err
	}
	p.IsCover = true
	return m.repo.Update(ctx, p)
}
