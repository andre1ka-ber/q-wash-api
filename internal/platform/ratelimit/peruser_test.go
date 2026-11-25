package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"q-wash-api/internal/platform/reqctx"
)

func TestLockoutAttempt_HoldsUnderParallelCalls(t *testing.T) {
	l := NewLockout(5, 15*time.Minute)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Attempt("alice") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("expected exactly 5 attempts, got %d", allowed.Load())
	}
	if !l.Attempt("bob") {
		t.Fatal("another key must be unaffected")
	}
	l.Reset("alice")
	if !l.Attempt("alice") {
		t.Fatal("a reset key may try again")
	}
	var off *Lockout
	if !off.Attempt("alice") {
		t.Fatal("a disabled lockout allows everything")
	}
}

func TestStreamsPerUser_CapsOpenRequestsAndReleases(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 10)
	handler := StreamsPerUser(2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	request := func(id uuid.UUID) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/events", nil)
		return r.WithContext(reqctx.WithAuthUser(r.Context(), reqctx.AuthUser{ID: id}))
	}
	alice, bob := uuid.New(), uuid.New()

	var wg sync.WaitGroup
	open := func(id uuid.UUID) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), request(id))
		}()
		<-entered
	}
	open(alice)
	open(alice)

	over := httptest.NewRecorder()
	handler.ServeHTTP(over, request(alice))
	if over.Code != http.StatusTooManyRequests {
		t.Fatalf("third stream: expected 429, got %d", over.Code)
	}
	open(bob)

	close(release)
	wg.Wait()
	again := httptest.NewRecorder()
	handler.ServeHTTP(again, request(alice))
	if again.Code != http.StatusOK {
		t.Fatalf("closed streams must free their slots, got %d", again.Code)
	}
}
