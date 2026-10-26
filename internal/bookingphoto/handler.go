package bookingphoto

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/photo"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/queue"
)

type Handler struct {
	repo      *Repository
	manager   *Manager
	queueRepo *queue.Repository
}

func NewHandler(repo *Repository, manager *Manager, queueRepo *queue.Repository) *Handler {
	return &Handler{repo: repo, manager: manager, queueRepo: queueRepo}
}

// RegisterRoutes mounts /queue/{id}/photos behind requireQueueOps (staff,
// worker, admin) — the same gate as the status/pause/resume actions the
// worker app already uses.
func (h *Handler) RegisterRoutes(r chi.Router, requireQueueOps ...func(http.Handler) http.Handler) {
	r.Route("/queue/{id}/photos", func(ph chi.Router) {
		ph.With(requireQueueOps...).Get("/", h.list)
		ph.With(requireQueueOps...).Post("/", h.upload)
		ph.With(requireQueueOps...).Delete("/{photoId}", h.delete)
	})
}

type response struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func toResponse(p *BookingPhoto) response {
	return response{ID: p.ID.String(), URL: p.URL}
}

func (h *Handler) findOwnedBooking(r *http.Request, authUser reqctx.AuthUser) (*queue.Queue, error) {
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		return nil, err
	}
	booking, err := h.queueRepo.FindByID(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !authUser.OwnsWashingPoint(booking.WashingPointID) {
		return nil, apperror.NotFound("queue_not_found", "queue entry not found")
	}
	return booking, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	booking, err := h.findOwnedBooking(r, authUser)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	photos, err := h.repo.ListByQueue(r.Context(), booking.ID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]response, len(photos))
	for i := range photos {
		items[i] = toResponse(&photos[i])
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	booking, err := h.findOwnedBooking(r, authUser)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if booking.Status != queue.StatusWashing {
		httputil.WriteError(w, r, apperror.Conflict("booking_not_washing", "photos can only be added while the wash is in progress"))
		return
	}

	ext, content, closeUpload, err := photo.ParseImageUpload(w, r)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	defer closeUpload()

	p, err := h.manager.Upload(r.Context(), booking.ID, ext, content)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(p))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	booking, err := h.findOwnedBooking(r, authUser)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	photoID, err := httputil.ParseUUIDParam(r, "photoId")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	p, err := h.repo.FindByID(r.Context(), photoID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if p.QueueID != booking.ID {
		httputil.WriteError(w, r, apperror.NotFound("photo_not_found", "photo not found"))
		return
	}
	if err := h.manager.Delete(r.Context(), p); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
