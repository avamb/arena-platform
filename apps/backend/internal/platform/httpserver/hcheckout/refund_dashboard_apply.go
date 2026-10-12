package hcheckout

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hscanner"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// dashboardOutcome is what dashboardApply did inside the transaction, for
// the publishing that must wait until it commits.
type dashboardOutcome struct {
	cancelled []gen.TicketRow
	// amounts is what each cancelled ticket was paid (order_items.total), so
	// a ticket's v1.ticket.refunded carries ITS money, never the whole
	// refund's.
	amounts map[uuid.UUID]int64
}

// savepoint runs fn inside a SAVEPOINT of tx: a failed statement inside fn
// is rolled back to the savepoint and reported, never left to abort the
// outer transaction (the commit would die with 25P02 and take the refund
// row with it).
func savepoint(ctx context.Context, tx pgx.Tx, fn func(sp pgx.Tx) error) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(sp); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return nil
}

// cancelCheckoutTicketsTx cancels every active ticket of the checkout
// session and releases their places, on the caller's transaction. The
// cancellation itself must succeed (an error is returned and the caller
// rolls everything back); each place release and the artifact revocation
// run behind their own SAVEPOINT, so one inconsistent seat row is logged
// and skipped (the money is already back) without poisoning the rest.
func (h *Handler) cancelCheckoutTicketsTx(ctx context.Context, tx pgx.Tx, refund gen.RefundRow, checkoutSessionID uuid.UUID) ([]gen.TicketRow, error) {
	txq := h.refundQueries.WithTx(tx)
	reason := "inbound provider refund " + refund.ID.String()
	refundID := refund.ID
	cancelled, err := txq.CancelTicketsByCheckoutSession(ctx, checkoutSessionID, reason, &refundID)
	if err != nil {
		return nil, fmt.Errorf("cancel tickets: %w", err)
	}
	rowReleases := 0
	legacyByTier := map[string]int32{}
	legacyTiers := map[string]*uuid.UUID{}
	for _, t := range cancelled {
		var outcome htickets.TicketReleaseOutcome
		if relErr := savepoint(ctx, tx, func(sp pgx.Tx) error {
			var e error
			outcome, e = htickets.ReleaseCancelledTicketInventoryTx(ctx, h.refundQueries.WithTx(sp), t)
			return e
		}); relErr != nil {
			h.logger.Error("refund_webhook: seat release failed",
				slog.String("ticket_id", t.ID.String()), slog.String("error", relErr.Error()))
			continue
		}
		if outcome.RowReleased() {
			rowReleases++
			continue
		}
		key := ""
		if t.TierID != nil {
			key = t.TierID.String()
		}
		legacyByTier[key]++
		legacyTiers[key] = t.TierID
	}
	if len(cancelled) > 0 && h.inventoryQueries != nil {
		restore := func(tier *uuid.UUID, n int32, what string) {
			if err := savepoint(ctx, tx, func(sp pgx.Tx) error {
				_, e := h.inventoryQueries.WithTx(sp).RestoreSoldCapacity(ctx, cancelled[0].SessionID, tier, n)
				return e
			}); err != nil {
				h.logger.Error("refund_webhook: "+what+" capacity restore failed",
					slog.String("checkout_session_id", checkoutSessionID.String()), slog.String("error", err.Error()))
			}
		}
		if rowReleases > 0 {
			restore(nil, int32(rowReleases), "session-level") //nolint:gosec // bounded by the order's ticket count
		}
		for key, n := range legacyByTier {
			restore(legacyTiers[key], n, "per-tier")
		}
	}
	// Revoke barcodes and credentials so refunded tickets stop scanning.
	if err := savepoint(ctx, tx, func(sp pgx.Tx) error {
		htickets.RevokeTicketArtifactsTx(ctx, h.logger, h.ticketQueries, h.ticketQueries, sp, cancelled)
		return nil
	}); err != nil {
		h.logger.Error("refund_webhook: revoking ticket artifacts failed", slog.String("error", err.Error()))
	}
	return cancelled, nil
}

// dashboardApply is the consequence of a refund made in the provider's
// dashboard, run INSIDE the transaction that recorded it: a full refund
// cancels the order's tickets and projects the order status (refunded plus
// its order_events row), a smaller one flags the tickets for review. A
// failure rolls the refund row back too, so the provider's redelivery
// starts from nothing instead of finding a recorded refund whose tickets
// are still valid.
func (h *Handler) dashboardApply(out *dashboardOutcome) refunds.DashboardApply {
	return func(ctx context.Context, tx pgx.Tx, res refunds.DashboardRefund) error {
		if res.Payment.CheckoutSessionID == nil {
			return nil
		}
		cs := *res.Payment.CheckoutSessionID
		row := gen.RefundRow{ID: res.Refund.ID, Amount: res.Refund.Amount, Currency: res.Refund.Currency}
		if !res.Full {
			if h.ticketQueries == nil {
				return nil
			}
			reason := "partial inbound refund " + row.ID.String() + " (" + strconv.FormatInt(row.Amount, 10) + " " +
				row.Currency + ") - attribute to specific tickets"
			held, err := h.ticketQueries.WithTx(tx).SetTicketsReviewHoldByCheckoutSession(ctx, cs, reason)
			if err != nil {
				return fmt.Errorf("review hold: %w", err)
			}
			h.logger.Error("refund_webhook: PARTIAL refund made in the provider dashboard - tickets placed under review hold, operator must attribute it",
				slog.String("refund_id", row.ID.String()), slog.String("checkout_session_id", cs.String()),
				slog.Int64("refund_amount", row.Amount), slog.String("currency", row.Currency), slog.Int64("tickets_on_hold", held))
			return nil
		}
		if h.refundQueries == nil {
			return fmt.Errorf("refund queries are not wired")
		}
		cancelled, err := h.cancelCheckoutTicketsTx(ctx, tx, row, cs)
		if err != nil {
			return err
		}
		out.cancelled = cancelled
		out.amounts = map[uuid.UUID]int64{}
		if len(cancelled) > 0 {
			ids := make([]uuid.UUID, 0, len(cancelled))
			for _, t := range cancelled {
				ids = append(ids, t.ID)
			}
			rows, err := tx.Query(ctx, `SELECT ticket_id, total FROM order_items WHERE ticket_id = ANY($1::uuid[])`, ids)
			if err != nil {
				return fmt.Errorf("ticket amounts: %w", err)
			}
			for rows.Next() {
				var id uuid.UUID
				var total int64
				if err := rows.Scan(&id, &total); err != nil {
					rows.Close()
					return err
				}
				out.amounts[id] = total
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		if err := h.refundEngine.ProjectOrderTx(ctx, tx, res.Refund, res.Payment); err != nil {
			return err
		}
		return h.appendDashboardRefundEvents(ctx, tx, res, *out)
	}
}

// appendDashboardRefundEvents writes the events of a full dashboard refund
// to the outbox INSIDE the transaction that cancelled the tickets: the scanner
// event for the refund and one v1.ticket.refunded per cancelled ticket. A
// slow request or a crash after the commit can then no longer lose them.
func (h *Handler) appendDashboardRefundEvents(ctx context.Context, tx pgx.Tx, res refunds.DashboardRefund, out dashboardOutcome) error {
	if res.Payment.CheckoutSessionID == nil || len(out.cancelled) == 0 {
		return nil
	}
	w := outbox.NewPGEventsWriter(nil)
	cs := res.Payment.CheckoutSessionID.String()
	refundID := res.Refund.ID.String()
	if err := w.Append(ctx, tx, outbox.Event{
		AggregateType: hscanner.ScannerAggregateType,
		AggregateID:   refundID,
		EventType:     hscanner.ScannerEventTicketRefunded,
		Payload:       hscanner.BuildTicketRefundedPayload(cs, refundID, res.Refund.Currency, res.Refund.Amount),
	}); err != nil {
		return fmt.Errorf("outbox scanner refund event: %w", err)
	}
	for _, t := range out.cancelled {
		amount, ok := out.amounts[t.ID]
		if !ok {
			// No order line for it (legacy ticket): only a single ticket can
			// safely be given the whole refund.
			if len(out.cancelled) != 1 {
				h.logger.Warn("refund_webhook: ticket has no order line; v1.ticket.refunded carries amount 0",
					slog.String("ticket_id", t.ID.String()))
			} else {
				amount = res.Refund.Amount
			}
		}
		if err := w.Append(ctx, tx, outbox.Event{
			AggregateType: hscanner.TicketAggregateType,
			AggregateID:   t.ID.String(),
			EventType:     hscanner.TicketRefundedEventType,
			Payload:       hscanner.BuildTicketRefundedV1Payload(t.ID.String(), cs, refundID, res.Refund.Currency, amount),
		}); err != nil {
			return fmt.Errorf("outbox ticket refunded event: %w", err)
		}
	}
	return nil
}
