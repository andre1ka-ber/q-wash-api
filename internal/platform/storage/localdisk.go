package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// LocalDisk is a dev-only Storage that writes to a local directory. The
// caller is expected to also serve that directory at baseURL (see
// internal/platform/httpserver.MountStatic, wired in internal/app) — this
// implementation has no story for a multi-instance deployment; swap in an
// S3/GCS implementation there without touching any caller of the Storage
// interface.
type LocalDisk struct {
	dir     string
	baseURL string
}

func NewLocalDisk(dir, baseURL string) (*LocalDisk, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &LocalDisk{dir: dir, baseURL: strings.TrimSuffix(baseURL, "/")}, nil
}

func (s *LocalDisk) Put(_ context.Context, filename string, content io.Reader) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	// filepath.Ext only ever looks at the final path element, so this is
	// safe even if filename somehow contained separators.
	name := id.String() + strings.ToLower(filepath.Ext(filename))

	f, err := os.Create(filepath.Join(s.dir, name))
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, content); err != nil {
		return "", err
	}
	return s.baseURL + "/" + name, nil
}

func (s *LocalDisk) Delete(_ context.Context, url string) error {
	// path.Base strips any directory component, so this can't escape dir
	// even if url were attacker-controlled (it isn't — callers only ever
	// pass back a URL this same Put returned).
	name := path.Base(url)
	if name == "." || name == "/" || strings.ContainsAny(name, `/\`) {
		return errors.New("storage: invalid url")
	}
	err := os.Remove(filepath.Join(s.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
