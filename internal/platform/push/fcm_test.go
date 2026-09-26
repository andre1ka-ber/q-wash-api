package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFCMSendBuildsTheV1Request(t *testing.T) {
	var got fcmRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := newFCM(srv.URL, srv.Client()).Send(context.Background(), "tok-1", Message{
		Title: "Мойка началась", Body: "Pegasus", Data: map[string]string{"queue_id": "q1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Token != "tok-1" || got.Message.Notification.Title != "Мойка началась" || got.Message.Data["queue_id"] != "q1" {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestFCMSendMapsDeadTokensToErrUnregistered(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   error
	}{
		"404 unregistered": {http.StatusNotFound, `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`, ErrUnregistered},
		"detail only":      {http.StatusBadRequest, `{"error":{"details":[{"errorCode":"UNREGISTERED"}]}}`, ErrUnregistered},
		"other failure":    {http.StatusInternalServerError, `{"error":{"status":"INTERNAL"}}`, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			err := newFCM(srv.URL, srv.Client()).Send(context.Background(), "t", Message{})
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
			} else if err == nil || errors.Is(err, ErrUnregistered) {
				t.Fatalf("expected a plain error, got %v", err)
			}
		})
	}
}
