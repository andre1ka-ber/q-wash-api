package notification

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/platform/sms"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/user"
)

// Manager creates notification records and, when due immediately, sends
// them through the same sms.Sender interface used for OTP delivery. There
// is no background worker in this MVP: a notification scheduled for the
// future (SendAt after now) is simply created as "pending" and stays that
// way until a future phase adds a worker to sweep due notifications.
type Manager struct {
	repo      *Repository
	userRepo  *user.Repository
	queueRepo *queue.Repository
	sms       sms.Sender
}

func NewManager(repo *Repository, userRepo *user.Repository, queueRepo *queue.Repository, smsSender sms.Sender) *Manager {
	return &Manager{repo: repo, userRepo: userRepo, queueRepo: queueRepo, sms: smsSender}
}

type CreateInput struct {
	UserID  uuid.UUID
	QueueID *uuid.UUID
	Text    string
	SendAt  time.Time
	// Caller is the staff/admin principal making the request — checked
	// against the target queue entry's washing point (when QueueID is
	// given) so staff can't message a customer tied to another point's
	// booking. See docs/PLAN_WEB_APPS.md's IDOR review.
	Caller reqctx.AuthUser
}

func (m *Manager) Create(ctx context.Context, in CreateInput) (*Notification, error) {
	target, err := m.userRepo.FindByID(ctx, in.UserID)
	if err != nil {
		return nil, err
	}

	if in.QueueID != nil {
		q, err := m.queueRepo.FindByID(ctx, *in.QueueID)
		if err != nil {
			return nil, err
		}
		if q.UserID != target.ID {
			return nil, apperror.BadRequest("queue_user_mismatch", "queue_id does not belong to user_id")
		}
		if !in.Caller.OwnsWashingPoint(q.WashingPointID) {
			return nil, apperror.NotFound("queue_not_found", "queue entry not found")
		}
	}

	n := &Notification{
		UserID:  in.UserID,
		QueueID: in.QueueID,
		Status:  StatusPending,
		Channel: ChannelSMS,
		Text:    in.Text,
		SendAt:  in.SendAt,
	}
	if err := m.repo.Create(ctx, n); err != nil {
		return nil, err
	}

	if !in.SendAt.After(time.Now()) {
		m.attemptSend(ctx, target.PhoneNumber, n)
	}

	return n, nil
}

// attemptSend is best-effort: a delivery (or persistence-of-result)
// failure is logged, not returned, since the notification record already
// exists and reflects reality either way (status stays whatever the DB has).
func (m *Manager) attemptSend(ctx context.Context, phoneNumber string, n *Notification) {
	now := time.Now()
	status := StatusSent
	if err := m.sms.Send(ctx, phoneNumber, n.Text); err != nil {
		status = StatusFailed
		slog.ErrorContext(ctx, "notification sms send failed", "notification_id", n.ID, "err", err)
	}

	if err := m.repo.UpdateDeliveryResult(ctx, n.ID, status, &now); err != nil {
		slog.ErrorContext(ctx, "failed to persist notification delivery result", "notification_id", n.ID, "err", err)
		return
	}
	n.Status = status
	n.SentAt = &now
}
