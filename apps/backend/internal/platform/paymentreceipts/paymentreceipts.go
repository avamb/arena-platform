// Package paymentreceipts archives the payment provider's own receipt of a
// paid payment (spec 51 section 2, migration 0184).
//
// A Stripe receipt link (charge.receipt_url) lives 30 days, while a dispute
// may come 120 days after the event, so when a payment succeeds the payment
// webhook queues payment.receipt_archive (EnqueueTx, on the webhook's own
// transaction) to run ArchiveDelay later: Stripe numbers a receipt only after
// it has mailed it, and a run at once would keep an unnumbered copy. The job, run by arena-worker, asks the provider's module
// for the receipt (payments.ReceiptFetcher), stores the link and the number on
// the payment intent and keeps a copy of the document in the media store
// (owner type payment_receipt). The dispute package reads it from there.
//
// The job never touches money or tickets. A provider without a receipt
// archive (no Capabilities.ProviderReceipt) is never queued.
package paymentreceipts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// JobType is the worker job that archives one payment's receipt.
const JobType = "payment.receipt_archive"

// MaxAttempts bounds the retries of one archive job. A receipt that is not
// ready yet, or whose download failed, is retried with the worker's backoff;
// a job that runs out of attempts lands in the dead-letter queue, which the
// ops watchdog reports, so a lost copy is never silent.
const MaxAttempts = 8

// ArchiveDelay is how long after the payment the job runs.
const ArchiveDelay = 15 * time.Minute

// Payload is the job's payload.
type Payload struct {
	PaymentIntentID string `json:"payment_intent_id"`
	// ConfigID is the payment configuration that verified the webhook (the
	// per-config route). The receipt is read through THAT account, not the
	// organization's preferred one. Empty on the legacy route.
	ConfigID string `json:"config_id,omitempty"`
}

// Archivable reports whether payments of this provider have a receipt
// arena can archive.
func Archivable(provider string) bool {
	e, ok := paymodules.Registry().Get(provider)
	return ok && e.Implemented() && e.Descriptor.Capabilities.ProviderReceipt
}

// EnqueueTx queues the archive of one succeeded payment on the caller's
// transaction, to run ArchiveDelay later. configID names the configuration
// that verified the payment, nil when unknown. It does nothing for a provider
// without a receipt archive.
func EnqueueTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID, provider string, configID *uuid.UUID) error {
	if !Archivable(provider) {
		return nil
	}
	p := Payload{PaymentIntentID: paymentIntentID.String()}
	if configID != nil && *configID != uuid.Nil {
		p.ConfigID = configID.String()
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, $3, 'pending', now() + $4::interval)`,
		JobType, body, MaxAttempts, fmt.Sprintf("%d seconds", int(ArchiveDelay/time.Second)))
	if err != nil {
		return fmt.Errorf("enqueue %s in tx: %w", JobType, err)
	}
	return nil
}

// ModuleSource builds an organization's payment module (the refund engine's
// hcheckout.RefundModuleSource satisfies it).
type ModuleSource interface {
	Build(ctx context.Context, orgID uuid.UUID, provider string) (payments.Module, error)
}

// Queries is the part of gen.Queries the job uses.
type Queries interface {
	GetPaymentIntentReceipt(ctx context.Context, id uuid.UUID) (gen.PaymentIntentReceiptRow, error)
	SetPaymentIntentReceipt(ctx context.Context, id uuid.UUID, url, number *string, mediaID *uuid.UUID) error
	GetPaymentReceiptMediaID(ctx context.Context, paymentIntentID, orgID uuid.UUID) (uuid.UUID, error)
}

// Store keeps a copy of a receipt and answers its media id.
type Store interface {
	SaveReceipt(ctx context.Context, orgID, paymentIntentID uuid.UUID, contentType string, body []byte) (uuid.UUID, error)
}

// Options wires the handler.
type Options struct {
	Queries Queries
	Modules ModuleSource
	// Store is nil when the deployment has no media storage: the link and
	// number are still stored, the copy is not kept.
	Store  Store
	Logger *slog.Logger
}

// NewHandler returns the worker handler of JobType.
func NewHandler(opts Options) func(ctx context.Context, payload []byte) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, raw []byte) error {
		var p Payload
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%s: bad payload: %w", JobType, err)
		}
		id, err := uuid.Parse(p.PaymentIntentID)
		if err != nil {
			return fmt.Errorf("%s: bad payment_intent_id: %w", JobType, err)
		}
		if p.ConfigID != "" {
			configID, err := uuid.Parse(p.ConfigID)
			if err != nil {
				return fmt.Errorf("%s: bad config_id: %w", JobType, err)
			}
			ctx = refunds.WithProviderConfig(ctx, configID)
		}
		return archive(ctx, opts, logger, id)
	}
}

func archive(ctx context.Context, opts Options, logger *slog.Logger, id uuid.UUID) error {
	if opts.Queries == nil || opts.Modules == nil {
		return fmt.Errorf("%s: not wired", JobType)
	}
	pi, err := opts.Queries.GetPaymentIntentReceipt(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if pi.State != "succeeded" || pi.ReceiptArchivedAt != nil {
		return nil
	}
	ref := strings.TrimSpace(deref(pi.ProviderChargeRef))
	if ref == "" {
		ref = strings.TrimSpace(deref(pi.ProviderPaymentID))
	}
	if ref == "" {
		return nil
	}
	module, err := opts.Modules.Build(ctx, pi.OrgID, pi.Provider)
	if err != nil {
		return fmt.Errorf("%s: build %s module: %w", JobType, pi.Provider, err)
	}
	fetcher, ok := module.(payments.ReceiptFetcher)
	if !ok {
		return nil
	}
	rec, err := fetcher.FetchReceipt(ctx, ref)
	switch {
	case errors.Is(err, payments.ErrReceiptNotFound):
		// The account asked does not know the payment: retrying cannot help.
		logger.Warn("payment receipt: the provider does not know the payment",
			slog.String("payment_intent_id", pi.ID.String()),
			slog.String("provider", pi.Provider),
		)
		return nil
	case errors.Is(err, payments.ErrReceiptDownload):
		// The link is kept now; the copy is tried again on the next attempt.
		// Without a media store there is no copy to wait for.
		if setErr := opts.Queries.SetPaymentIntentReceipt(ctx, pi.ID, nonEmpty(rec.URL), nonEmpty(rec.Number), nil); setErr != nil {
			return setErr
		}
		if opts.Store == nil {
			return nil
		}
		return fmt.Errorf("%s: payment intent %s: %w", JobType, id, err)
	case err != nil:
		return fmt.Errorf("%s: payment intent %s: %w", JobType, id, err)
	}
	var mediaID *uuid.UUID
	if len(rec.File) > 0 && opts.Store != nil {
		saved, saveErr := saveOnce(ctx, opts, pi, rec)
		if saveErr != nil {
			return fmt.Errorf("%s: save receipt copy: %w", JobType, saveErr)
		}
		mediaID = &saved
	}
	if err := opts.Queries.SetPaymentIntentReceipt(ctx, pi.ID, nonEmpty(rec.URL), nonEmpty(rec.Number), mediaID); err != nil {
		return err
	}
	logger.Info("payment receipt archived",
		slog.String("payment_intent_id", pi.ID.String()),
		slog.String("provider", pi.Provider),
		slog.Bool("copy_kept", mediaID != nil),
		slog.String("content_type", rec.ContentType),
	)
	return nil
}

// saveOnce reuses the copy an earlier run saved (it failed after the save,
// or a stale claim ran the job twice), so a retry never leaves an orphan.
func saveOnce(ctx context.Context, opts Options, pi gen.PaymentIntentReceiptRow, rec payments.Receipt) (uuid.UUID, error) {
	existing, err := opts.Queries.GetPaymentReceiptMediaID(ctx, pi.ID, pi.OrgID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	return opts.Store.SaveReceipt(ctx, pi.OrgID, pi.ID, rec.ContentType, rec.File)
}

// MediaStore keeps receipts in the platform media store.
type MediaStore struct{ Repo *mediastore.Repo }

// SaveReceipt implements Store: the bytes under a fresh payment_receipt key
// and the media_objects row that points at them, owned by the payment intent.
func (m MediaStore) SaveReceipt(ctx context.Context, orgID, paymentIntentID uuid.UUID, contentType string, body []byte) (uuid.UUID, error) {
	if m.Repo == nil {
		return uuid.Nil, errors.New("paymentreceipts: no media store")
	}
	key, err := mediastore.NewStorageKey(mediastore.OwnerPaymentReceipt)
	if err != nil {
		return uuid.Nil, err
	}
	sum, size, err := m.Repo.PutAndStream(ctx, key, contentType, bytes.NewReader(body))
	if err != nil {
		return uuid.Nil, err
	}
	obj, err := m.Repo.Insert(ctx, mediastore.InsertInput{
		OrgID:          &orgID,
		OwnerType:      mediastore.OwnerPaymentReceipt,
		OwnerID:        &paymentIntentID,
		StorageBackend: m.Repo.Backend(),
		StorageKey:     key,
		ContentType:    contentType,
		ByteSize:       size,
		ChecksumSHA256: sum,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return obj.ID, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
