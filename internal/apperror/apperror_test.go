package apperror

import (
	"errors"
	"net/http"
	"testing"
)

func TestConstructorsCarryStatusAndCode(t *testing.T) {
	cases := []struct {
		err    *Error
		status int
	}{
		{BadRequest("c", "m"), http.StatusBadRequest},
		{Unauthorized("c", "m"), http.StatusUnauthorized},
		{Forbidden("c", "m"), http.StatusForbidden},
		{NotFound("c", "m"), http.StatusNotFound},
		{Conflict("c", "m"), http.StatusConflict},
		{TooManyRequests("c", "m"), http.StatusTooManyRequests},
		{UnprocessableEntity("c", "m"), http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		if tc.err.Status != tc.status || tc.err.Code != "c" || tc.err.Error() != "m" {
			t.Errorf("status %d: got %+v", tc.status, tc.err)
		}
	}
}

func TestInternalHidesTheCauseFromTheMessage(t *testing.T) {
	cause := errors.New("db password is hunter2")
	err := Internal(cause)

	if err.Status != http.StatusInternalServerError || err.Code != "internal_error" {
		t.Fatalf("unexpected: %+v", err)
	}
	if err.Error() != "internal error" {
		t.Fatalf("the client-facing message must not leak the cause, got %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause must stay reachable through errors.Is for logging")
	}
}
