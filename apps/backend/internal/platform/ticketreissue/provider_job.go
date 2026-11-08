package ticketreissue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// ModuleSource builds the payment module of an organization (the refund
// engine's own source, hcheckout.NewRefundModuleSource).
type ModuleSource interface {
	Build(ctx context.Context, orgID uuid.UUID, provider string) (payments.Module, error)
}

// ProviderJobOptions wires the provider job.
type ProviderJobOptions struct {
	DB      Querier
	Modules ModuleSource
	Logger  *slog.Logger
}

// NewProviderEmailHandler returns the worker handler of ProviderEmailJobType.
// It runs outside every transaction of the reissue: a failure never undoes
// the reissue, it is recorded on the journal row and shown on the order card.
func NewProviderEmailHandler(opts ProviderJobOptions) func(ctx context.Context, payload []byte) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, raw []byte) error {
		var p ProviderEmailPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%s: bad payload: %w", ProviderEmailJobType, err)
		}
		id, err := uuid.Parse(p.ReissueID)
		if err != nil {
			return fmt.Errorf("%s: bad reissue_id: %w", ProviderEmailJobType, err)
		}
		if opts.DB == nil || opts.Modules == nil {
			return fmt.Errorf("%s: not wired", ProviderEmailJobType)
		}
		t, err := LoadProviderEmailTarget(ctx, opts.DB, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if t.State != "queued" && t.State != "failed" {
			return nil
		}
		record := func(state, text string) error {
			return SetProviderEmailState(ctx, opts.DB, id, state, text)
		}
		module, err := opts.Modules.Build(ctx, t.OrgID, t.Provider)
		if err != nil {
			_ = record("failed", "the payment configuration could not be used")
			return fmt.Errorf("%s: build %s module: %w", ProviderEmailJobType, t.Provider, err)
		}
		updater, ok := module.(payments.ReceiptEmailUpdater)
		if !ok || t.PaymentRef == "" {
			return record("manual", "")
		}
		if err := updater.UpdateReceiptEmail(ctx, t.PaymentRef, t.Email, "arena-reissue-"+id.String()); err != nil {
			// The provider's own error text may name the address: only the
			// fact is stored, the log keeps the error.
			logger.Warn("ticket reissue: provider address update failed",
				slog.String("reissue_id", id.String()), slog.String("provider", t.Provider),
				slog.String("error", err.Error()))
			if recErr := record("failed", "the payment provider refused the new address"); recErr != nil {
				return recErr
			}
			return fmt.Errorf("%s: %s: %w", ProviderEmailJobType, t.Provider, err)
		}
		logger.Info("ticket reissue: provider address updated",
			slog.String("reissue_id", id.String()), slog.String("provider", t.Provider))
		return record("updated", "")
	}
}
