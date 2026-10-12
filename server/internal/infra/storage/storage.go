// Package storage implements S3-compatible object storage and Cloudflare KV
// publication route projections. PostgreSQL owns the route reservations.
package storage

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
)

var (
	ErrAlreadyExists          = errors.New("object already exists")
	ErrInvalidKey             = errors.New("invalid object key")
	ErrInvalidUpload          = errors.New("invalid object upload")
	ErrMultipartCleanupFailed = errors.New("multipart upload cleanup failed")
	ErrNotFound               = errors.New("object not found")
	ErrRetryableConflict      = errors.New("retryable object storage conflict")
	ErrSizeMismatch           = errors.New("object size does not match declared size")
	ErrUploadCleanupFailed    = errors.New("invalid object upload cleanup failed")
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(destination []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(destination)
}

func validateObjectKey(key string) error {
	if key == "" || path.IsAbs(key) || strings.ContainsRune(key, '\x00') || strings.Contains(key, "\\") {
		return ErrInvalidKey
	}
	cleaned := path.Clean(key)
	if cleaned != key || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ErrInvalidKey
	}
	return nil
}
