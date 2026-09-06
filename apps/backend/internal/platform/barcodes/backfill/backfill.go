// Package backfill implements the tickets.backfill_ean13 worker job
// (feature #503, W1-B6b; spec 08_architecture/18_bil24_compat_wave1_specification_ru.md
// §11).
//
// Feature #502 (W1-B6a) made ticket issuance mint an EAN-13 credential +
// platform barcode for every NEW ticket, but tickets that were already
// issued before that change landed (stand data, pre-#502 fixtures) have
// no ean13 row. Production has no sales yet so a real backfill is not
// needed there — but the shared dev stand does carry pre-#502 tickets,
// and any future environment that skips straight to a later migration
// head needs the same repair path. tickets.backfill_ean13 is that repair
// path: a worker job, not a one-shot CLI, so it can be triggered the same
// way any other maintenance job is (enqueue a worker_jobs row) and its
// progress is visible in the existing job-status tooling.
package backfill

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// JobType is the worker_jobs.job_type this package handles.
const JobType = "tickets.backfill_ean13"

// ean13PlatformPrefix mirrors htickets.ean13PlatformPrefix (feature #502):
// GS1 reserves 20-29 for internal/in-store use, so platform-minted codes
// can never collide with a real Bil24 barcode ("24…").
const ean13PlatformPrefix = "21"

// DefaultBatchSize caps how many tickets a single run backfills. Kept
// small and self-scheduling would be overkill here — the job is meant to
// be enqueued (once, or repeatedly by an operator/CLI) until it reports
// zero tickets remaining, which callers detect via the metrics on Run's
// return value or simply by re-running until Store returns no candidates.
const DefaultBatchSize = 500

// TicketRow is the subset of gen.TicketRow the backfill needs: identity
// plus the stable bigint used to derive the EAN-13 body.
type TicketRow struct {
	ID             uuid.UUID
	SystemTicketID int64
}

// Store is the query surface the backfill job needs. Satisfied by
// *gen.Queries; narrowed to an interface so tests can supply a fake
// without a live database.
type Store interface {
	// ListTicketsMissingEAN13 returns up to limit active tickets that do
	// not yet have an ean13 ticket_credentials row.
	ListTicketsMissingEAN13(ctx context.Context, limit int32) ([]TicketRow, error)

	// GetBarcodeAuthorityByType resolves the "platform" barcode authority
	// row's ID.
	GetBarcodeAuthorityByType(ctx context.Context, authorityType string) (uuid.UUID, error)

	// InsertTicketCredential creates (or, thanks to ON CONFLICT, replaces)
	// the ean13 credential for a ticket. Called at most once per ticket
	// per run since ListTicketsMissingEAN13 only returns tickets that
	// don't have one yet, but idempotent regardless.
	InsertTicketCredential(ctx context.Context, ticketID uuid.UUID, credType string, payload string) error

	// InsertBarcode creates the federation-table row (authority=platform)
	// backing SCAN_TICKET / /v1/scanner/* lookups for the new code.
	InsertBarcode(ctx context.Context, authorityID uuid.UUID, externalRef string, ticketID *uuid.UUID) error
}

// Options configures the handler returned by NewHandler.
type Options struct {
	// Store is the query surface. Required.
	Store Store

	// BatchSize caps tickets backfilled per run; defaults to DefaultBatchSize.
	BatchSize int32

	// Logger receives one summary line per run. nil uses slog.Default().
	Logger *slog.Logger
}

// NewHandler returns the worker handler for job_type=JobType. Its
// signature matches worker.HandlerFunc so it can be registered directly
// without this package importing worker.
func NewHandler(opts Options) func(ctx context.Context, payload []byte) error {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		backfilled, err := Run(ctx, opts.Store, opts.BatchSize)
		if err != nil {
			return err
		}
		opts.Logger.Info("tickets.backfill_ean13 run complete", "backfilled", backfilled)
		return nil
	}
}

// Run performs one backfill pass and reports how many tickets were
// backfilled. Exported so tests (and any future admin "run it now"
// action) can drive a single deterministic pass without the worker
// machinery.
//
// Idempotent by construction: ListTicketsMissingEAN13 only returns
// tickets without an ean13 credential row, so a ticket that this call (or
// a prior one) already backfilled never comes back on a later call —
// re-running Run with no remaining candidates is a fast no-op that
// backfills 0 tickets and returns no error.
func Run(ctx context.Context, store Store, batchSize int32) (int, error) {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	candidates, err := store.ListTicketsMissingEAN13(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("backfill: list tickets missing ean13: %w", err)
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	platformAuthorityID, err := store.GetBarcodeAuthorityByType(ctx, "platform")
	if err != nil {
		return 0, fmt.Errorf("backfill: get platform barcode authority: %w", err)
	}

	backfilled := 0
	for _, t := range candidates {
		if err := ctx.Err(); err != nil {
			return backfilled, err
		}

		code := ean13.Encode(ean13PlatformPrefix, t.SystemTicketID)
		if err := store.InsertTicketCredential(ctx, t.ID, "ean13", code); err != nil {
			return backfilled, fmt.Errorf("backfill: insert ean13 credential for ticket %s: %w", t.ID, err)
		}
		ticketID := t.ID
		if err := store.InsertBarcode(ctx, platformAuthorityID, code, &ticketID); err != nil {
			return backfilled, fmt.Errorf("backfill: insert ean13 barcode for ticket %s: %w", t.ID, err)
		}
		backfilled++
	}
	return backfilled, nil
}

// PGStore adapts *gen.Queries to the narrow Store interface this package
// needs, so production wiring (cmd/arena-worker) can pass the same
// *gen.Queries handle every other job type uses.
type PGStore struct {
	Q *gen.Queries
}

// NewPGStore wraps q into a Store.
func NewPGStore(q *gen.Queries) PGStore { return PGStore{Q: q} }

func (s PGStore) ListTicketsMissingEAN13(ctx context.Context, limit int32) ([]TicketRow, error) {
	rows, err := s.Q.ListTicketsMissingEAN13(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]TicketRow, len(rows))
	for i, r := range rows {
		out[i] = TicketRow{ID: r.ID, SystemTicketID: r.SystemTicketID}
	}
	return out, nil
}

func (s PGStore) GetBarcodeAuthorityByType(ctx context.Context, authorityType string) (uuid.UUID, error) {
	row, err := s.Q.GetBarcodeAuthorityByType(ctx, authorityType)
	if err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

func (s PGStore) InsertTicketCredential(ctx context.Context, ticketID uuid.UUID, credType string, payload string) error {
	_, err := s.Q.InsertTicketCredential(ctx, ticketID, credType, payload)
	return err
}

func (s PGStore) InsertBarcode(ctx context.Context, authorityID uuid.UUID, externalRef string, ticketID *uuid.UUID) error {
	_, err := s.Q.InsertBarcode(ctx, authorityID, externalRef, ticketID)
	return err
}
