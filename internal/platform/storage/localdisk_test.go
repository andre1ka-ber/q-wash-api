package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalDisk_PutAndDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalDisk(dir, "/uploads")
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}

	url, err := s.Put(context.Background(), "photo.jpg", strings.NewReader("fake-jpeg-bytes"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(url, "/uploads/") || !strings.HasSuffix(url, ".jpg") {
		t.Fatalf("expected a /uploads/*.jpg url, got %q", url)
	}

	name := strings.TrimPrefix(url, "/uploads/")
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("expected the file to exist on disk: %v", err)
	}
	if string(content) != "fake-jpeg-bytes" {
		t.Errorf("expected stored content to match what was written, got %q", content)
	}

	if err := s.Delete(context.Background(), url); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("expected the file to be gone after Delete, stat err = %v", err)
	}
}

func TestLocalDisk_DeleteMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalDisk(dir, "/uploads")
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	if err := s.Delete(context.Background(), "/uploads/never-existed.jpg"); err != nil {
		t.Errorf("expected deleting an already-missing file to be a no-op, got %v", err)
	}
}

func TestLocalDisk_DeleteRejectsTraversalAttempt(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalDisk(dir, "/uploads")
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(outside, []byte("do not delete me"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	// path.Base strips any leading directory components regardless, so
	// this can never resolve outside dir — asserted here so a future
	// change to that stripping logic gets caught.
	if err := s.Delete(context.Background(), "/uploads/../../../"+outside); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("expected the file outside dir to be untouched, stat err = %v", err)
	}
}
