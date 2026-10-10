package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllow_BlocksAfterLimitAndResetsAfterWindow(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	l := New(2, time.Hour)
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("1.1.1.1"); !ok {
			t.Fatalf("hit %d should be allowed", i+1)
		}
	}
	ok, retry := l.Allow("1.1.1.1")
	if ok || retry != time.Hour {
		t.Fatalf("third hit: want blocked with retry 1h, got ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("2.2.2.2"); !ok {
		t.Fatal("a different key must have its own window")
	}

	now = now.Add(time.Hour)
	if ok, _ := l.Allow("1.1.1.1"); !ok {
		t.Fatal("window expired, hit should be allowed again")
	}
}

func TestMiddleware_Returns429WithRetryAfter(t *testing.T) {
	l := New(1, time.Hour)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "9.9.9.9:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(); rec.Code != http.StatusNoContent {
		t.Fatalf("first request: want 204, got %d", rec.Code)
	}
	rec := do()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: want 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 must carry Retry-After")
	}
}
