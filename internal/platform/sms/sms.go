// Package sms defines the outbound SMS interface used for OTP delivery.
// The only implementation for now is a stub that logs instead of sending,
// so the auth flow works end-to-end without a real provider account; a
// real provider (Twilio, Vonage, ...) can implement Sender later without
// any change to calling code.
package sms

import (
	"context"
	"log/slog"
)

type Sender interface {
	Send(ctx context.Context, phoneNumber, message string) error
}

type StubSender struct{}

func NewStubSender() *StubSender { return &StubSender{} }

func (s *StubSender) Send(ctx context.Context, phoneNumber, message string) error {
	slog.InfoContext(ctx, "sms (stub, not actually sent)", "to", phoneNumber, "message", message)
	return nil
}
