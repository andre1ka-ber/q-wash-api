package httputil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"q-wash-api/internal/apperror"
)

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}

	t.Run("decodes a normal body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"a"}`))
		var got body
		if err := DecodeJSON(r, &got); err != nil || got.Name != "a" {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})

	t.Run("unknown field is a 400 invalid_body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"nope":1}`))
		err := DecodeJSON(r, &body{})
		appErr, ok := err.(*apperror.Error)
		if !ok || appErr.Status != http.StatusBadRequest || appErr.Code != "invalid_body" {
			t.Fatalf("got %#v", err)
		}
	})

	t.Run("body over the cap is a 413 request_too_large", func(t *testing.T) {
		huge := `{"name":"` + strings.Repeat("a", MaxJSONBodyBytes) + `"}`
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(huge))
		err := DecodeJSON(r, &body{})
		appErr, ok := err.(*apperror.Error)
		if !ok || appErr.Status != http.StatusRequestEntityTooLarge || appErr.Code != "request_too_large" {
			t.Fatalf("got %#v", err)
		}
	})
}
