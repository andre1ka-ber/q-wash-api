package sms

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func captureLog(t *testing.T, send func()) string {
	t.Helper()
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	defer slog.SetDefault(previous)
	send()
	return out.String()
}

func TestRedactedStubSender_LogsNoMessageOrFullPhone(t *testing.T) {
	logged := captureLog(t, func() {
		if err := NewRedactedStubSender().Send(context.Background(), "+992901112233", "your ticket 42"); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(logged, "+992901112233") || strings.Contains(logged, "ticket") {
		t.Fatalf("redacted stub leaked data: %s", logged)
	}
	if !strings.Contains(logged, "***2233") {
		t.Fatalf("expected a masked phone, got %s", logged)
	}
}

func TestStubSender_LogsMessageForLocalDevelopment(t *testing.T) {
	logged := captureLog(t, func() {
		_ = NewStubSender().Send(context.Background(), "+992901112233", "your ticket 42")
	})
	if !strings.Contains(logged, "your ticket 42") {
		t.Fatalf("development stub must show the message, got %s", logged)
	}
}
