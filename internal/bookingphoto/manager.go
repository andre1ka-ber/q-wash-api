package bookingphoto

import (
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/platform/storage"
)

const maxPhotosPerBooking = 10

type Manager struct {
	repo    *Repository
	storage storage.Storage
}

func NewManager(repo *Repository, fileStorage storage.Storage) *Manager {
	return &Manager{repo: repo, storage: fileStorage}
}

// Upload stores content, then creates the DB row. ext must already be a
// verified, server-derived extension (see photo.ParseImageUpload).
func (m *Manager) Upload(ctx context.Context, queueID uuid.UUID, ext string, content io.Reader) (_ *BookingPhoto, err error) {
	count, err := m.repo.CountByQueue(ctx, queueID)
	if err != nil {
		return nil, err
	}
	if count >= maxPhotosPerBooking {
		return nil, apperror.Conflict("photo_limit_reached", fmt.Sprintf("a booking can have at most %d photos", maxPhotosPerBooking))
	}

	url, err := m.storage.Put(ctx, "photo"+ext, content)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = m.storage.Delete(context.WithoutCancel(ctx), url)
		}
	}()

	p := &BookingPhoto{QueueID: queueID, URL: url}
	if err := m.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Delete removes the DB row, then best-effort deletes the file — same
// trade-off as photo.Manager.Delete.
func (m *Manager) Delete(ctx context.Context, p *BookingPhoto) error {
	if err := m.repo.Delete(ctx, p.ID); err != nil {
		return err
	}
	_ = m.storage.Delete(ctx, p.URL)
	return nil
}
