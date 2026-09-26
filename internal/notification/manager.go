package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/device"
	"q-wash-api/internal/platform/clock"
	"q-wash-api/internal/platform/push"
	"q-wash-api/internal/platform/reqctx"
	"q-wash-api/internal/platform/sms"
	"q-wash-api/internal/queue"
	"q-wash-api/internal/user"
	"q-wash-api/internal/washingpoint"
)

// Manager creates notification records and, when due immediately, sends
// them through the same sms.Sender interface used for OTP delivery. There
// is no background worker in this MVP: a notification scheduled for the
// future (SendAt after now) is simply created as "pending" and stays that
// way until a future phase adds a worker to sweep due notifications.
//
// Booking-stage notifications (started/finished from a status change,
// reminder/late from the Scheduler) are pushed to the customer's registered
// devices via push.Sender — see NotifyStage.
type Manager struct {
	repo       *Repository
	userRepo   *user.Repository
	queueRepo  *queue.Repository
	deviceRepo *device.Repository
	wpRepo     *washingpoint.Repository
	sms        sms.Sender
	push       push.Sender
}

func NewManager(repo *Repository, userRepo *user.Repository, queueRepo *queue.Repository, deviceRepo *device.Repository, wpRepo *washingpoint.Repository, smsSender sms.Sender, pushSender push.Sender) *Manager {
	return &Manager{repo: repo, userRepo: userRepo, queueRepo: queueRepo, deviceRepo: deviceRepo, wpRepo: wpRepo, sms: smsSender, push: pushSender}
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

func stageMessage(kind Kind, pointName string, boxNumber int, start time.Time) push.Message {
	switch kind {
	case KindReminder:
		return push.Message{Title: "Скоро мойка", Body: fmt.Sprintf("Через час · %s, %s", start.In(clock.BusinessLocation).Format("15:04"), pointName)}
	case KindLate:
		return push.Message{Title: "Вас ждут", Body: fmt.Sprintf("Время записи уже началось — подъезжайте к боксу %d · %s", boxNumber, pointName)}
	case KindStarted:
		return push.Message{Title: "Мойка началась", Body: fmt.Sprintf("Бокс %d · %s", boxNumber, pointName)}
	default: // KindFinished
		return push.Message{Title: "Машина готова", Body: fmt.Sprintf("Можно забирать · %s", pointName)}
	}
}

// kindForStatus maps a booking status change to its stage notification;
// statuses without one return "".
func kindForStatus(s queue.Status) Kind {
	switch s {
	case queue.StatusWashing:
		return KindStarted
	case queue.StatusReady:
		return KindFinished
	}
	return ""
}

// NotifyStatus implements queue.StageNotifier: called after a booking's
// status changed. It never blocks or fails the status change — delivery
// runs in the background and errors are only logged.
func (m *Manager) NotifyStatus(ctx context.Context, q *queue.Queue) {
	kind := kindForStatus(q.Status)
	if kind == "" {
		return
	}
	booking := *q
	bg := context.WithoutCancel(ctx)
	go func() {
		bg, cancel := context.WithTimeout(bg, 20*time.Second)
		defer cancel()
		if err := m.NotifyStage(bg, &booking, kind); err != nil {
			slog.ErrorContext(bg, "stage notification failed", "queue_id", booking.ID, "kind", kind, "err", err)
		}
	}()
}

// NotifyStage pushes one booking-stage notification to the booking owner's
// devices, at most once per (booking, kind). A customer with no registered
// device (e.g. a walk-in who never installed the app) is silently skipped.
// Tokens the provider reports dead are removed.
func (m *Manager) NotifyStage(ctx context.Context, q *queue.Queue, kind Kind) error {
	tokens, err := m.deviceRepo.ListByUser(ctx, q.UserID)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}
	wp, err := m.wpRepo.FindByID(ctx, q.WashingPointID)
	if err != nil {
		return err
	}

	msg := stageMessage(kind, wp.Name, q.BoxNumber, q.ScheduledStartAt)
	msg.Data = map[string]string{"type": "booking_stage", "kind": string(kind), "queue_id": q.ID.String()}

	k := kind
	n := &Notification{
		UserID: q.UserID, QueueID: &q.ID, Status: StatusPending, Channel: ChannelPush,
		Kind: &k, Text: msg.Title + ": " + msg.Body, SendAt: time.Now(),
	}
	created, err := m.repo.CreateOnce(ctx, n)
	if err != nil || !created {
		return err
	}

	delivered := false
	var sendErrs []error
	for _, t := range tokens {
		switch err := m.push.Send(ctx, t.Token, msg); {
		case err == nil:
			delivered = true
		case errors.Is(err, push.ErrUnregistered):
			if derr := m.deviceRepo.DeleteByToken(ctx, t.Token); derr != nil {
				sendErrs = append(sendErrs, derr)
			}
		default:
			sendErrs = append(sendErrs, err)
		}
	}

	now := time.Now()
	status := StatusFailed
	if delivered {
		status = StatusSent
	}
	if err := m.repo.UpdateDeliveryResult(ctx, n.ID, status, &now); err != nil {
		sendErrs = append(sendErrs, err)
	}
	return errors.Join(sendErrs...)
}
