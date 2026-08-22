package httpserver

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
)

// noListingFS wraps an http.FileSystem so opening a directory 404s unless
// it has an index.html, instead of http.FileServer's default directory
// listing. Uploaded filenames are random UUIDs, so the risk is low, but
// there's no reason to leave enumeration open.
type noListingFS struct {
	inner http.FileSystem
}

func (fs noListingFS) Open(name string) (http.File, error) {
	f, err := fs.inner.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		index := strings.TrimSuffix(name, "/") + "/index.html"
		if _, err := fs.inner.Open(index); err != nil {
			f.Close()
			return nil, os.ErrNotExist
		}
	}
	return f, nil
}

// MountStatic serves dir's contents at baseURL on r — the top-level
// router, not the /api/v1 sub-router, same as /health, /docs and
// /openapi.yaml — for uploaded photo files
// (internal/platform/storage.LocalDisk).
func MountStatic(r chi.Router, baseURL, dir string) {
	prefix := strings.TrimSuffix(baseURL, "/")
	fileServer := http.StripPrefix(prefix, http.FileServer(noListingFS{http.Dir(dir)}))
	r.Handle(prefix+"/*", fileServer)
}
