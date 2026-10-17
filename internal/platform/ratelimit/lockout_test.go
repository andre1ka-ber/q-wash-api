package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestLockout(max int, d time.Duration, now *time.Time) *Lockout {
	l := NewLockout(max, d)
	l.now = func() time.Time { return *now }
	return l
}

func TestLockout_LocksAfterMaxFailuresThenExpires(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	l := newTestLockout(3, 15*time.Minute, &now)

	for i := 0; i < 2; i++ {
		l.Fail("alice")
		if locked, _ := l.Locked("alice"); locked {
			t.Fatalf("locked after only %d failures", i+1)
		}
	}
	l.Fail("alice")
	locked, retry := l.Locked("alice")
	if !locked || retry != 15*time.Minute {
		t.Fatalf("want locked for 15m, got locked=%v retry=%v", locked, retry)
	}
	if locked, _ := l.Locked("bob"); locked {
		t.Fatal("another key must not be locked")
	}

	now = now.Add(15 * time.Minute)
	if locked, _ := l.Locked("alice"); locked {
		t.Fatal("lock must expire after the duration")
	}
}

func TestLockout_ResetAndStaleFailuresDoNotAccumulate(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	l := newTestLockout(3, 15*time.Minute, &now)

	l.Fail("alice")
	l.Fail("alice")
	l.Reset("alice")
	l.Fail("alice")
	if locked, _ := l.Locked("alice"); locked {
		t.Fatal("a success in between must reset the count")
	}

	l.Fail("alice")
	now = now.Add(20 * time.Minute)
	l.Fail("alice") // the earlier failures are older than the window now
	if locked, _ := l.Locked("alice"); locked {
		t.Fatal("failures older than the window must not count")
	}
}

func TestLockout_NilIsDisabled(t *testing.T) {
	var l *Lockout
	l.Fail("x")
	l.Reset("x")
	if locked, _ := l.Locked("x"); locked {
		t.Fatal("nil lockout must never lock")
	}
	if NewLockout(0, time.Minute) != nil || NewLockout(3, 0) != nil {
		t.Fatal("non-positive settings must disable the lockout")
	}
}

func TestPerIP_ZeroLimitIsPassthrough(t *testing.T) {
	h := PerIP(0, time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "9.9.9.9:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d blocked with %d", i+1, rec.Code)
		}
	}
}
