package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSecurityHeaders(t *testing.T) {
	for _, production := range []bool{false, true} {
		r, _ := NewRouter(nil, []string{"*"}, "", false, production)
		// A 404 gets them too: the middleware wraps the whole router.
		rec := get(t, r, "/no-such-route")

		for k, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
		} {
			if got := rec.Header().Get(k); got != want {
				t.Errorf("production=%v: %s = %q, want %q", production, k, got, want)
			}
		}
		hsts := rec.Header().Get("Strict-Transport-Security")
		if production && hsts == "" {
			t.Error("production must send Strict-Transport-Security")
		}
		if !production && hsts != "" {
			t.Errorf("non-production must not send HSTS, got %q", hsts)
		}
	}
}

func TestDocsPageOnlyOutsideProduction(t *testing.T) {
	dev, _ := NewRouter(nil, []string{"*"}, "", false, false)
	if rec := get(t, dev, "/docs"); rec.Code != http.StatusOK {
		t.Errorf("dev /docs = %d, want 200", rec.Code)
	}

	prod, _ := NewRouter(nil, []string{"*"}, "", false, true)
	if rec := get(t, prod, "/docs"); rec.Code != http.StatusNotFound {
		t.Errorf("production /docs = %d, want 404", rec.Code)
	}
	// The machine-readable spec stays available in production.
	if rec := get(t, prod, "/openapi.yaml"); rec.Code != http.StatusOK {
		t.Errorf("production /openapi.yaml = %d, want 200", rec.Code)
	}
}

func TestUploadsGetLockedDownCSP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.png"), []byte("not really a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := NewRouter(nil, []string{"*"}, "", false, false)
	MountStatic(r, "/uploads", dir)

	rec := get(t, r, "/uploads/a.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Errorf("CSP = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff = %q", got)
	}
}
