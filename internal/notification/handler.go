package notification

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/httputil"
)

type Handler struct {
	repo    *Repository
	manager *Manager
}

func NewHandler(repo *Repository, manager *Manager) *Handler {
	return &Handler{repo: repo, manager: manager}
}

// RegisterRoutes mounts POST /notifications (staff/admin) and
// GET /me/notifications (any authenticated user, own records only).
func (h *Handler) RegisterRoutes(r chi.Router, requireAuth func(http.Handler) http.Handler, requireStaff ...func(http.Handler) http.Handler) {
	r.Route("/notifications", func(n chi.Router) {
		n.With(requireStaff...).Post("/", h.create)
	})

	r.Route("/me/notifications", func(n chi.Router) {
		n.With(requireAuth).Get("/", h.list)
	})
}

type response struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	QueueID   *string    `json:"queue_id,omitempty"`
	Status    string     `json:"status"`
	Channel   string     `json:"channel"`
	Text      string     `json:"text"`
	SendAt    time.Time  `json:"send_at"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func toResponse(n *Notification) response {
	var queueID *string
	if n.QueueID != nil {
		s := n.QueueID.String()
		queueID = &s
	}
	return response{
		ID:        n.ID.String(),
		UserID:    n.UserID.String(),
		QueueID:   queueID,
		Status:    string(n.Status),
		Channel:   string(n.Channel),
		Text:      n.Text,
		SendAt:    n.SendAt,
		SentAt:    n.SentAt,
		CreatedAt: n.CreatedAt,
	}
}

type createRequest struct {
	UserID  string  `json:"user_id"`
	QueueID *string `json:"queue_id"`
	Text    string  `json:"text"`
	SendAt  *string `json:"send_at"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	var req createRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_user_id", "user_id must be a valid uuid"))
		return
	}

	var queueID *uuid.UUID
	if req.QueueID != nil {
		id, err := uuid.Parse(*req.QueueID)
		if err != nil {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_queue_id", "queue_id must be a valid uuid"))
			return
		}
		queueID = &id
	}

	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > 2000 {
		httputil.WriteError(w, r, apperror.BadRequest("invalid_text", "text is required (max 2000 chars)"))
		return
	}

	sendAt := time.Now()
	if req.SendAt != nil {
		parsed, err := time.Parse(time.RFC3339, *req.SendAt)
		if err != nil {
			httputil.WriteError(w, r, apperror.BadRequest("invalid_send_at", "send_at must be an RFC3339 timestamp"))
			return
		}
		sendAt = parsed
	}

	n, err := h.manager.Create(r.Context(), CreateInput{
		UserID:  userID,
		QueueID: queueID,
		Text:    text,
		SendAt:  sendAt,
		Caller:  authUser,
	})
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, toResponse(n))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}

	pagination := httputil.ParsePagination(r)
	rows, total, err := h.repo.ListByUser(r.Context(), authUser.ID, pagination.Offset(), pagination.Limit())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}

	items := make([]response, len(rows))
	for i := range rows {
		items[i] = toResponse(&rows[i])
	}
	httputil.WritePaginated(w, http.StatusOK, items, pagination, total)
}
