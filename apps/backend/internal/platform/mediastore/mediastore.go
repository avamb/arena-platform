// Package mediastore is the persistence + lifecycle layer for binary media
// uploads (feature #286 / G-2, Wave G).
//
// It wraps the storage adapter (apps/backend/internal/adapters/storage)
// with a thin pgx-backed repository that records each upload in the
// media_objects table (migration 0052), and exposes a worker handler
// (media-gc) that reclaims the bytes of soft-deleted rows whose
// deleted_at timestamp is older than the retention window.
package mediastore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

// Object is the row representation returned by Repo operations. It maps
// 1:1 to the media_objects table columns; nullable foreign-key-ish
// fields (org_id, owner_id) are pointer-typed.
type Object struct {
	ID             uuid.UUID
	OrgID          *uuid.UUID
	OwnerType      string
	OwnerID        *uuid.UUID
	StorageBackend string
	StorageKey     string
	ContentType    string
	ByteSize       int64
	ChecksumSHA256 string
	Width          *int32
	Height         *int32
	CreatedAt      time.Time
	DeletedAt      *time.Time
}

// AllowedOwnerTypes is the canonical set of owner_type values accepted by
// POST /v1/media. Mirrors the CHECK constraint on media_objects.
var AllowedOwnerTypes = map[string]struct{}{
	"org_logo":     {},
	"event_poster": {},
	"artist_photo": {},
}

// Repo is the persistence + storage facade used by HTTP handlers and the
// worker GC job. A single Repo is safe for concurrent use.
type Repo struct {
	pool    *pgxpool.Pool
	storage storage.Storage
	// signingSecret signs local-backend download URLs. Empty disables
	// signing entirely (callers must then route to a fully-public path).
	signingSecret []byte
	// downloadURLBase is the absolute or relative URL prefix prepended to
	// signed local-backend download paths, e.g. "https://api.example.com"
	// or "" (relative). The signed path itself is /v1/media-files/{id}.
	downloadURLBase string
}

// Options bundles the constructor inputs.
type Options struct {
	Pool            *pgxpool.Pool
	Storage         storage.Storage
	SigningSecret   []byte
	DownloadURLBase string
}

// New returns a Repo built from opts. Both Pool and Storage are required.
func New(opts Options) (*Repo, error) {
	if opts.Pool == nil {
		return nil, errors.New("mediastore: Pool is required")
	}
	if opts.Storage == nil {
		return nil, errors.New("mediastore: Storage is required")
	}
	r := &Repo{
		pool:            opts.Pool,
		storage:         opts.Storage,
		signingSecret:   opts.SigningSecret,
		downloadURLBase: strings.TrimRight(opts.DownloadURLBase, "/"),
	}
	return r, nil
}

// Storage returns the underlying storage adapter.
func (r *Repo) Storage() storage.Storage { return r.storage }

// Backend returns the configured storage backend name (s3|local).
func (r *Repo) Backend() string { return string(r.storage.Backend()) }

// InsertInput is the payload for Insert.
type InsertInput struct {
	OrgID          *uuid.UUID
	OwnerType      string
	OwnerID        *uuid.UUID
	StorageBackend string
	StorageKey     string
	ContentType    string
	ByteSize       int64
	ChecksumSHA256 string
	Width          *int32
	Height         *int32
}

// Insert records a freshly-uploaded object's metadata and returns the row.
func (r *Repo) Insert(ctx context.Context, in InsertInput) (Object, error) {
	const q = `
		INSERT INTO media_objects (
			org_id, owner_type, owner_id, storage_backend, storage_key,
			content_type, byte_size, checksum_sha256, width, height
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, org_id, owner_type, owner_id, storage_backend, storage_key,
		          content_type, byte_size, checksum_sha256, width, height,
		          created_at, deleted_at
	`
	var obj Object
	err := r.pool.QueryRow(ctx, q,
		in.OrgID, in.OwnerType, in.OwnerID, in.StorageBackend, in.StorageKey,
		in.ContentType, in.ByteSize, in.ChecksumSHA256, in.Width, in.Height,
	).Scan(
		&obj.ID, &obj.OrgID, &obj.OwnerType, &obj.OwnerID,
		&obj.StorageBackend, &obj.StorageKey, &obj.ContentType, &obj.ByteSize,
		&obj.ChecksumSHA256, &obj.Width, &obj.Height,
		&obj.CreatedAt, &obj.DeletedAt,
	)
	if err != nil {
		return Object{}, fmt.Errorf("mediastore: insert: %w", err)
	}
	return obj, nil
}

// ErrNotFound is returned by GetByID, SoftDelete, and HardDelete when the
// requested media row does not exist (either because the ID is unknown or
// because the row was already soft-deleted on a path that only looks at
// active rows).
var ErrNotFound = errors.New("mediastore: object not found")

// GetByID fetches an ACTIVE row by ID (deleted_at IS NULL).
func (r *Repo) GetByID(ctx context.Context, id uuid.UUID) (Object, error) {
	const q = `
		SELECT id, org_id, owner_type, owner_id, storage_backend, storage_key,
		       content_type, byte_size, checksum_sha256, width, height,
		       created_at, deleted_at
		FROM   media_objects
		WHERE  id = $1
		  AND  deleted_at IS NULL
	`
	var obj Object
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&obj.ID, &obj.OrgID, &obj.OwnerType, &obj.OwnerID,
		&obj.StorageBackend, &obj.StorageKey, &obj.ContentType, &obj.ByteSize,
		&obj.ChecksumSHA256, &obj.Width, &obj.Height,
		&obj.CreatedAt, &obj.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Object{}, ErrNotFound
		}
		return Object{}, fmt.Errorf("mediastore: get: %w", err)
	}
	return obj, nil
}

// SoftDelete sets deleted_at = now() on an active row. Returns ErrNotFound
// when the row does not exist or was already soft-deleted.
func (r *Repo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE media_objects
		SET    deleted_at = now()
		WHERE  id = $1
		  AND  deleted_at IS NULL
	`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("mediastore: soft-delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GCCandidate is a row picked up by the media-gc worker job.
type GCCandidate struct {
	ID             uuid.UUID
	StorageBackend string
	StorageKey     string
	DeletedAt      time.Time
}

// ListGCCandidates returns up to `limit` rows whose deleted_at is older
// than `before`. The caller is expected to remove the underlying bytes
// from the storage backend and then call HardDelete to remove the row.
func (r *Repo) ListGCCandidates(ctx context.Context, before time.Time, limit int) ([]GCCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
		SELECT id, storage_backend, storage_key, deleted_at
		FROM   media_objects
		WHERE  deleted_at IS NOT NULL
		  AND  deleted_at < $1
		ORDER  BY deleted_at ASC
		LIMIT  $2
	`
	rows, err := r.pool.Query(ctx, q, before, limit)
	if err != nil {
		return nil, fmt.Errorf("mediastore: list gc candidates: %w", err)
	}
	defer rows.Close()
	out := make([]GCCandidate, 0, limit)
	for rows.Next() {
		var c GCCandidate
		var deletedAt *time.Time
		if err := rows.Scan(&c.ID, &c.StorageBackend, &c.StorageKey, &deletedAt); err != nil {
			return nil, fmt.Errorf("mediastore: scan gc candidate: %w", err)
		}
		if deletedAt != nil {
			c.DeletedAt = *deletedAt
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mediastore: iterate gc candidates: %w", err)
	}
	return out, nil
}

// HardDelete removes the row entirely. Intended to run AFTER the storage
// adapter has confirmed the bytes are gone.
func (r *Repo) HardDelete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM media_objects WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("mediastore: hard-delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// =====================================================================
// Signed URL helpers
// =====================================================================

// SignedURL returns a download URL for the object identified by id.
//
// For the s3 backend the URL is an S3 presigned GET URL valid for ttl.
// For the local backend the URL points at /v1/media-files/{id} with an
// HMAC signature and expiry parameter; the corresponding handler verifies
// the signature before streaming the file. Empty signingSecret disables
// HMAC and returns an unsigned relative URL — acceptable for development
// only.
func (r *Repo) SignedURL(id uuid.UUID, key string, ttl time.Duration) (string, error) {
	switch r.storage.Backend() {
	case storage.BackendS3:
		// Delegate to the S3 adapter's presigner when available.
		if presigner, ok := r.storage.(interface {
			PresignGet(key string, ttl time.Duration) (string, error)
		}); ok {
			return presigner.PresignGet(key, ttl)
		}
		// Fallback: return a relative path that the local download handler
		// can serve. This keeps the contract testable even when the
		// installed S3 adapter does not implement presigning.
		return r.localSignedURL(id, ttl), nil
	case storage.BackendLocal:
		return r.localSignedURL(id, ttl), nil
	default:
		return "", fmt.Errorf("mediastore: unknown backend %q", r.storage.Backend())
	}
}

func (r *Repo) localSignedURL(id uuid.UUID, ttl time.Duration) string {
	expires := time.Now().Add(ttl).Unix()
	path := fmt.Sprintf("/v1/media-files/%s", id.String())
	q := url.Values{}
	q.Set("expires", strconv.FormatInt(expires, 10))
	if len(r.signingSecret) > 0 {
		q.Set("sig", computeHMAC(r.signingSecret, id.String(), expires))
	}
	return r.downloadURLBase + path + "?" + q.Encode()
}

// VerifyLocalSignature validates a signed URL for the local download
// endpoint. expires is the value of the `expires` query parameter; sig is
// the value of the `sig` query parameter. When signingSecret is empty the
// signature check is skipped (development mode).
func (r *Repo) VerifyLocalSignature(id uuid.UUID, expiresStr, sig string) error {
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return errors.New("mediastore: invalid expires parameter")
	}
	if time.Now().Unix() > expires {
		return errors.New("mediastore: signed URL expired")
	}
	if len(r.signingSecret) == 0 {
		return nil
	}
	want := computeHMAC(r.signingSecret, id.String(), expires)
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return errors.New("mediastore: signature mismatch")
	}
	return nil
}

func computeHMAC(secret []byte, id string, expires int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(id))
	mac.Write([]byte{':'})
	mac.Write([]byte(strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// NewStorageKey returns a fresh, collision-resistant object key derived
// from a UUID. The key is prefixed by ownerType so an operator inspecting
// the bucket can see which surface each object belongs to.
func NewStorageKey(ownerType string) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("mediastore: new key uuid: %w", err)
	}
	// 8-byte suffix protects against accidental UUID collisions in worst-
	// case clock anomalies and adds entropy for object-store path sharding.
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("mediastore: new key entropy: %w", err)
	}
	return fmt.Sprintf("%s/%s-%s", ownerType, id.String(), hex.EncodeToString(suffix[:])), nil
}

// PutAndRead writes the body into the storage backend and returns the
// SHA-256 hex digest plus the actual byte size streamed. The caller is
// responsible for inserting the metadata row afterwards.
func (r *Repo) PutAndStream(ctx context.Context, key, contentType string, body io.Reader) (sha256Hex string, size int64, err error) {
	hasher := sha256.New()
	counter := &countingReader{r: io.TeeReader(body, hasher)}
	_, err = r.storage.Put(ctx, storage.PutInput{
		Key:         key,
		ContentType: contentType,
		Size:        0, // unknown; adapter reads until EOF
		Body:        counter,
	})
	if err != nil {
		return "", 0, fmt.Errorf("mediastore: storage put: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), counter.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
