// Package storage abstracts the binary backend used by the media subsystem
// (feature #285 / G-1, Wave G).
//
// Two adapters are provided:
//
//   - LocalStorage: filesystem under a configured root directory. Used in
//     development and tests where no object store is available. Files are
//     written atomically (temp + rename) so a crash mid-write cannot leave a
//     half-written object visible.
//
//   - S3Storage: any S3-compatible service (AWS S3, Cloudflare R2, MinIO).
//     Implemented directly against the S3 REST API using AWS Signature V4
//     so the binary stays free of the heavyweight AWS SDK. Only the small
//     subset of operations needed by the media pipeline (Put, Get, Stat,
//     Delete) is supported.
//
// The active adapter is selected by NewFromConfig based on Config.Backend
// (driven by the MEDIA_BACKEND env var). All adapter implementations are
// safe for concurrent use from multiple goroutines.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Backend names the available adapter kinds. The strings here MUST match the
// values accepted by the media_objects.storage_backend CHECK constraint
// (see migration 0052_media_objects.sql).
type Backend string

const (
	// BackendS3 selects the S3-compatible adapter.
	BackendS3 Backend = "s3"
	// BackendLocal selects the filesystem adapter (dev/test only).
	BackendLocal Backend = "local"
)

// Object is the metadata returned by Stat and Get. Bytes are exposed
// separately on GetResult so callers can stream large payloads.
type Object struct {
	// Key is the backend-specific locator (S3 object key or filesystem
	// relative path). Always relative — never an absolute path or a full URL.
	Key string
	// ContentType is the MIME type that was supplied at Put time.
	// Empty when the adapter cannot recover it (rare; local adapter persists
	// it in a sidecar metadata file).
	ContentType string
	// Size is the object's byte length.
	Size int64
}

// PutInput describes a single Put operation.
type PutInput struct {
	// Key is the backend-relative locator. Must not be empty, must not start
	// with "/" or contain ".." segments. Forward slashes act as path
	// separators within the storage namespace.
	Key string
	// ContentType is the MIME type to associate with the object (e.g.
	// "image/png"). May be empty; adapters do not infer it from the bytes.
	ContentType string
	// Size is the expected number of bytes to read from Body. When > 0 the
	// adapter uses it as a Content-Length hint and fails the upload if the
	// stream supplies a different number of bytes.
	Size int64
	// Body supplies the bytes to store. The adapter reads until io.EOF.
	// Body is never closed by the adapter — the caller owns its lifecycle.
	Body io.Reader
}

// GetResult is returned by Get. Callers MUST Close Body to release the
// underlying file handle / HTTP response.
type GetResult struct {
	Object
	// Body streams the stored bytes.
	Body io.ReadCloser
}

// Storage is the contract every media backend must implement.
//
// Implementations MUST:
//   - validate the supplied Key against a small set of safety rules
//     (no empty key, no leading slash, no "..") and return ErrInvalidKey
//     for any violation;
//   - return ErrNotFound when an object does not exist (Get, Stat, Delete);
//   - be safe for concurrent use from multiple goroutines.
//
// Implementations MUST NOT:
//   - return *os.PathError or other low-level errors directly — wrap them
//     in fmt.Errorf with a stable prefix so callers can grep for
//     "storage:" in logs.
type Storage interface {
	// Backend identifies which adapter kind this instance is — useful when
	// recording media_objects.storage_backend at upload time.
	Backend() Backend
	// Put writes a new object. Overwrites an existing object at the same key.
	Put(ctx context.Context, in PutInput) (Object, error)
	// Get streams an existing object's bytes plus its metadata.
	Get(ctx context.Context, key string) (*GetResult, error)
	// Stat returns metadata for an existing object without opening its body.
	Stat(ctx context.Context, key string) (Object, error)
	// Delete removes an object. Returns ErrNotFound when the key is unknown.
	Delete(ctx context.Context, key string) error
}

// Sentinel errors returned by every adapter.
var (
	// ErrNotFound is returned when Get, Stat, or Delete is called for a key
	// that does not exist in the backend.
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalidKey is returned when a caller supplies a key that violates
	// the safety rules (empty, leading slash, contains "..").
	ErrInvalidKey = errors.New("storage: invalid key")
	// ErrSizeMismatch is returned when PutInput.Size is set and the body
	// supplied a different number of bytes.
	ErrSizeMismatch = errors.New("storage: body size does not match declared size")
)

// Config holds the parameters needed to construct any backend. The fields
// for the non-selected backend are ignored.
type Config struct {
	// Backend selects which adapter to construct. Required.
	Backend Backend

	// Local backend ---------------------------------------------------------
	// LocalRoot is the filesystem directory under which the local adapter
	// stores objects. Required when Backend == BackendLocal.
	LocalRoot string

	// S3 backend ------------------------------------------------------------
	// S3Endpoint is the base URL of the S3-compatible service (e.g.
	// "https://s3.amazonaws.com", "https://<accountid>.r2.cloudflarestorage.com",
	// "http://minio:9000"). Required when Backend == BackendS3.
	S3Endpoint string
	// S3Region is the region the bucket lives in (e.g. "us-east-1" for AWS,
	// "auto" for Cloudflare R2). Required when Backend == BackendS3.
	S3Region string
	// S3Bucket is the bucket name. Required when Backend == BackendS3.
	S3Bucket string
	// S3AccessKeyID and S3SecretAccessKey are the long-lived credentials
	// used to sign requests. Required when Backend == BackendS3.
	S3AccessKeyID     string
	S3SecretAccessKey string
	// S3UsePathStyle forces path-style addressing
	// ("https://endpoint/bucket/key") instead of virtual-hosted style
	// ("https://bucket.endpoint/key"). MinIO requires path-style; AWS S3
	// accepts both; Cloudflare R2 prefers path-style. Defaults to true so
	// the adapter works with the broadest set of providers out of the box.
	S3UsePathStyle bool
}

// Validate returns nil when the configuration is sufficient to construct the
// selected backend, or a descriptive error otherwise.
func (c Config) Validate() error {
	switch c.Backend {
	case BackendLocal:
		if c.LocalRoot == "" {
			return errors.New("storage: MEDIA_LOCAL_ROOT is required when MEDIA_BACKEND=local")
		}
		return nil
	case BackendS3:
		var missing []string
		if c.S3Endpoint == "" {
			missing = append(missing, "MEDIA_S3_ENDPOINT")
		}
		if c.S3Region == "" {
			missing = append(missing, "MEDIA_S3_REGION")
		}
		if c.S3Bucket == "" {
			missing = append(missing, "MEDIA_S3_BUCKET")
		}
		if c.S3AccessKeyID == "" {
			missing = append(missing, "MEDIA_S3_ACCESS_KEY_ID")
		}
		if c.S3SecretAccessKey == "" {
			missing = append(missing, "MEDIA_S3_SECRET_ACCESS_KEY")
		}
		if len(missing) > 0 {
			return fmt.Errorf("storage: missing S3 configuration: %v", missing)
		}
		return nil
	case "":
		return errors.New("storage: MEDIA_BACKEND is required (allowed: s3|local)")
	default:
		return fmt.Errorf("storage: MEDIA_BACKEND %q is invalid (allowed: s3|local)", c.Backend)
	}
}

// NewFromConfig constructs the Storage implementation named by cfg.Backend.
// It runs cfg.Validate first so misconfigured deployments fail fast at boot.
func NewFromConfig(cfg Config) (Storage, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Backend {
	case BackendLocal:
		return NewLocalStorage(cfg.LocalRoot)
	case BackendS3:
		return NewS3Storage(S3Options{
			Endpoint:        cfg.S3Endpoint,
			Region:          cfg.S3Region,
			Bucket:          cfg.S3Bucket,
			AccessKeyID:     cfg.S3AccessKeyID,
			SecretAccessKey: cfg.S3SecretAccessKey,
			UsePathStyle:    cfg.S3UsePathStyle,
		})
	default:
		// Unreachable: Validate already filtered out unknown backends.
		return nil, fmt.Errorf("storage: unknown backend %q", cfg.Backend)
	}
}
