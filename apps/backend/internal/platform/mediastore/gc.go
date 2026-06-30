package mediastore

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

// JobType is the worker job type string for the GC handler. It MUST match
// the value enqueued by the operator (or the periodic scheduler) when
// inserting a row into worker_jobs.
const JobType = "media-gc"

// DefaultRetention is the soft-delete retention window before a media
// object's bytes are reclaimed by the GC worker. Matches the feature #286
// specification ("removes objects whose deleted_at is older than 7 days").
const DefaultRetention = 7 * 24 * time.Hour

// DefaultBatchSize caps the number of rows processed per GC invocation so
// a single job does not monopolise a worker slot or a database connection.
const DefaultBatchSize = 100

// GCHandlerOptions bundles the dependencies of the media-gc worker handler.
type GCHandlerOptions struct {
	// Repo is the mediastore persistence facade.
	Repo *Repo
	// Logger receives one structured log record per GC invocation.
	Logger *slog.Logger
	// Retention overrides the default 7-day window. Zero falls back to
	// DefaultRetention.
	Retention time.Duration
	// BatchSize overrides the default per-invocation limit. Zero falls
	// back to DefaultBatchSize.
	BatchSize int
	// Now is a clock override used by tests. Nil falls back to time.Now.
	Now func() time.Time
}

// NewGCHandler returns a worker job handler that reclaims storage bytes
// for media_objects rows whose deleted_at timestamp is older than the
// retention window. A row is removed entirely from the table only after
// the storage adapter confirms its bytes were either deleted or already
// absent — this protects against partial failures producing dangling
// rows that lie about reachable bytes.
//
// The signature is compatible with the worker.Registry.Register contract
// (func(ctx, payload) error) so the caller can plug it straight into the
// existing registry in cmd/arena-worker/main.go.
func NewGCHandler(opts GCHandlerOptions) func(ctx context.Context, payload []byte) error {
	retention := opts.Retention
	if retention <= 0 {
		retention = DefaultRetention
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return func(ctx context.Context, _ []byte) error {
		if opts.Repo == nil {
			return errors.New("media-gc: repo is nil")
		}
		cutoff := now().Add(-retention)
		candidates, err := opts.Repo.ListGCCandidates(ctx, cutoff, batchSize)
		if err != nil {
			return err
		}
		var reclaimed, missing int
		for _, c := range candidates {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err := opts.Repo.Storage().Delete(ctx, c.StorageKey)
			switch {
			case err == nil:
				reclaimed++
			case errors.Is(err, storage.ErrNotFound):
				// Bytes already gone (manual cleanup, replicated
				// crash); record the row as missing and reap it so the
				// table does not accumulate phantom soft-deletes.
				missing++
			default:
				logger.Warn("media-gc: storage delete failed; keeping row for retry",
					"media_id", c.ID.String(),
					"storage_backend", c.StorageBackend,
					"storage_key", c.StorageKey,
					"error", err.Error(),
				)
				continue
			}
			if err := opts.Repo.HardDelete(ctx, c.ID); err != nil && !errors.Is(err, ErrNotFound) {
				logger.Warn("media-gc: hard delete failed",
					"media_id", c.ID.String(),
					"error", err.Error(),
				)
			}
		}
		logger.Info("media-gc: sweep complete",
			"considered", len(candidates),
			"reclaimed_bytes_rows", reclaimed,
			"missing_bytes_rows", missing,
			"cutoff", cutoff.Format(time.RFC3339),
		)
		return nil
	}
}
