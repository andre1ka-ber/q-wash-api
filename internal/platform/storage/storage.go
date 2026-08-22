// Package storage defines the interface used to persist uploaded files
// (currently just washing-point photos, docs/PLAN_WEB_APPS.md phase 4) and
// a local-disk implementation for development — mirrors the sms.Sender
// pattern: a small interface a real provider (S3, GCS, ...) can implement
// later with no change to any caller.
package storage

import (
	"context"
	"io"
)

type Storage interface {
	// Put stores content and returns a URL the file can later be fetched
	// from. filename is used only to derive a stored extension — callers
	// should pass a server-verified extension (see internal/photo's
	// content-type sniffing), never a client-supplied one, since a
	// mismatched extension changes what Content-Type a static file server
	// later serves the bytes back as.
	Put(ctx context.Context, filename string, content io.Reader) (url string, err error)
	// Delete removes a previously stored file, identified by the URL Put
	// returned. Deleting an already-missing file is not an error.
	Delete(ctx context.Context, url string) error
}
