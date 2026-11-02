// Package paymentreceipts archives the payment provider's own receipt of a
// paid payment (spec 51 section 2, migration 0184).
//
// A Stripe receipt link (charge.receipt_url) lives 30 days, while a dispute
// may come 120 days after the event, so right after a payment succeeds the
// payment webhook queues payment.receipt_archive (EnqueueTx, on the webhook's
// own transaction). The job, run by arena-worker, asks the provider's module
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobType is the worker job that archives one payment's receipt.
const JobType = "payment.receipt_archive"

// MaxAttempts bounds the retries of one archive job. A receipt that is not
// ready yet (the charge is still being created) is retried with the worker's
// backoff; after that the link simply stays unarchived.
const MaxAttempts = 8

// Payload is the job's payload.
type Payload struct {
	PaymentIntentID string `json:"payment_intent_id"`
}

// Archivable reports whether payments of this provider have a receipt
// arena can archive.
func Archivable(provider string) bool {
	e, ok := paymodules.Registry().Get(provider)
	return ok && e.Implemented() && e.Descriptor.Capabilities.ProviderReceipt
}

// EnqueueTx queues the archive of one succeeded payment on the caller's
// transaction. It does nothing for a provider without a receipt archive.
func EnqueueTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID, provider string) error {
	if !Archivable(provider) {
		return nil
	}
	_, err := worker.EnqueueInTx(ctx, tx, JobType, Payload{PaymentIntentID: paymentIntentID.String()}, MaxAttempts)
	return err
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
	if err != nil {
		return fmt.Errorf("%s: payment intent %s: %w", JobType, id, err)
	}
	var mediaID *uuid.UUID
	if len(rec.File) > 0 && opts.Store != nil {
		saved, saveErr := opts.Store.SaveReceipt(ctx, pi.OrgID, pi.ID, rec.ContentType, rec.File)
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
