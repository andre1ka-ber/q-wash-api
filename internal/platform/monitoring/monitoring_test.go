package monitoring

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInitDisabledWithoutDSN(t *testing.T) {
	if err := Init("", "production"); err != nil {
		t.Fatalf("Init with no DSN should be a no-op, got: %v", err)
	}
}

func TestMiddlewarePassthroughWithoutDSN(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	wrapped := Middleware("")(next)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if !called {
		t.Fatal("passthrough middleware did not call the wrapped handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}
