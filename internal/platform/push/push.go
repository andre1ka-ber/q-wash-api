// Package push defines the outbound mobile push interface, mirroring
// internal/platform/sms: callers depend on Sender, the FCM implementation
// (fcm.go) or a no-op (used when Firebase isn't configured) plugs in behind
// it, and tests pass a fake.
package push

import (
	"context"
	"errors"
	"log/slog"
)

// Message is one notification for one device.
type Message struct {
	Title string
	Body  string
	// Data is delivered to the app alongside the visible notification (e.g.
	// {"type": "booking_stage", "queue_id": "..."}), so a tap can deep-link.
	Data map[string]string
}

// ErrUnregistered means the provider reports the token as no longer valid
// (app uninstalled / token rotated); the caller should forget it.
var ErrUnregistered = errors.New("push: device token is unregistered")

type Sender interface {
	Send(ctx context.Context, deviceToken string, msg Message) error
}

// Noop is used when push isn't configured: it logs instead of sending, so
// local dev and tests run without a Firebase project.
type Noop struct{}

func NewNoop() *Noop { return &Noop{} }

func (Noop) Send(ctx context.Context, deviceToken string, msg Message) error {
	slog.InfoContext(ctx, "push (not configured, not actually sent)", "title", msg.Title, "body", msg.Body)
	return nil
}

// New returns the FCM sender when both a project id and a service-account
// key are configured, and the no-op sender otherwise.
func New(ctx context.Context, projectID string, serviceAccountJSON []byte) (Sender, error) {
	if projectID == "" || len(serviceAccountJSON) == 0 {
		slog.Warn("push notifications disabled: FCM_PROJECT_ID / service account not configured")
		return NewNoop(), nil
	}
	return NewFCM(ctx, projectID, serviceAccountJSON)
}
