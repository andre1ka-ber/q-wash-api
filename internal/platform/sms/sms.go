// Package sms defines the outbound SMS interfaces. Sender delivers a ready
// free-text message (booking notifications); OTPSender delivers a one-time
// code (customer login) and exists separately so a provider that requires
// pre-registered templates can receive the code as a variable instead of a
// finished string. The stub implements both and just logs, so the auth flow
// works end-to-end without a provider account; PayomSender (payom.go) is the
// real OTP implementation.
package sms

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Sender interface {
	Send(ctx context.Context, phoneNumber, message string) error
}

// OTPSender sends a one-time login code valid for ttl.
type OTPSender interface {
	SendOTP(ctx context.Context, phoneNumber, code string, ttl time.Duration) error
}

// OTPMessage is the Russian free-text OTP body, kept to one UCS-2 SMS
// segment (70 characters).
func OTPMessage(code string, ttl time.Duration) string {
	return fmt.Sprintf("QWash: код подтверждения %s. Действует %d мин.", code, int(ttl.Minutes()))
}

type StubSender struct{}

func NewStubSender() *StubSender { return &StubSender{} }

func (s *StubSender) Send(ctx context.Context, phoneNumber, message string) error {
	slog.InfoContext(ctx, "sms (stub, not actually sent)", "to", phoneNumber, "message", message)
	return nil
}

func (s *StubSender) SendOTP(ctx context.Context, phoneNumber, code string, ttl time.Duration) error {
	return s.Send(ctx, phoneNumber, OTPMessage(code, ttl))
}

// PayomConfig is the Payom.tj account configuration. See docs/links.md for
// the API reference.
type PayomConfig struct {
	// BaseURL is the account's API host as issued by Payom, with or without
	// a scheme ("https://" is assumed when missing).
	BaseURL string
	// Token is the account's JWT bearer token.
	Token string
	// SenderName is shown as the SMS sender: up to 11 Latin letters, digits
	// or dots.
	SenderName string
	// OTPTemplateID, when set, sends OTPs by Payom template (required for
	// individual accounts; the template must use a "code" variable).
	// Empty sends free text (legal entities only).
	OTPTemplateID string
}

// NewOTPSender returns the OTP sender for the configured provider: "payom"
// for Payom.tj, anything else (the default, "stub") for the logging stub.
func NewOTPSender(provider string, payom PayomConfig) (OTPSender, error) {
	switch provider {
	case "payom":
		return NewPayomSender(payom)
	case "", "stub":
		slog.Warn("SMS provider is the stub: OTP codes are only logged, never sent (set SMS_PROVIDER=payom)")
		return NewStubSender(), nil
	default:
		return nil, fmt.Errorf("unknown SMS_PROVIDER %q (want stub or payom)", provider)
	}
}
