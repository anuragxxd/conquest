// Package objstore defines the object storage interface used by the platform.
//
// The first implementation backs MinIO/S3 using the AWS SDK. This interface
// exists so that staging logic (filer) and the control plane don't couple
// directly to a specific SDK.
package objstore

import (
	"context"
	"io"
)

// Client is a minimal object storage interface.
type Client interface {
	// Get downloads an object and returns a reader. Caller must close the reader.
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, error)

	// Put uploads data to the given bucket/key.
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error

	// Delete removes an object. No error if the object does not exist.
	Delete(ctx context.Context, bucket, key string) error

	// Exists checks if an object exists.
	Exists(ctx context.Context, bucket, key string) (bool, error)

	// ListPrefix returns keys matching a prefix.
	ListPrefix(ctx context.Context, bucket, prefix string) ([]string, error)
}
