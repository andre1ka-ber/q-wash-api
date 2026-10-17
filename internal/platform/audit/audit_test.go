package audit

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestWarnAndInfoCarryEventIPAndAttrs(t *testing.T) {
	buf := captureLog(t)
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "203.0.113.7:4444"

	Warn(r, "auth.login_failed", "username", "alice", "code", "invalid_credentials")
	Info(r, "auth.login", "user_id", "u1")

	out := buf.String()
	for _, want := range []string{
		"level=WARN", `msg=audit`, "event=auth.login_failed", "ip=203.0.113.7", "username=alice", "code=invalid_credentials",
		"level=INFO", "event=auth.login", "user_id=u1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "4444") {
		t.Errorf("port must not be logged:\n%s", out)
	}
}

func TestMaskPhoneAndTruncate(t *testing.T) {
	if got := MaskPhone("+992900001234"); got != "***1234" {
		t.Errorf("MaskPhone = %q", got)
	}
	if got := MaskPhone("123"); got != "***" {
		t.Errorf("short phone = %q", got)
	}
	if got := Truncate("abcdef", 3); got != "abc" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("ab", 3); got != "ab" {
		t.Errorf("Truncate short = %q", got)
	}
}
