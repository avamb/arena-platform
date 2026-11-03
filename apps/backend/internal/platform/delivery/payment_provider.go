package delivery

// payment_provider.go — the payment provider a buyer letter names (spec 51
// section 2): "Seller: <organization>" and "Payment processed by Stripe".
// The organization that received the money is the seller; the provider only
// processed the payment, and the platform is neither.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
)

// providerTitle turns a registry name ("stripe") into the human name the
// provider's own descriptor gives ("Stripe"). An unknown name, "manual" and
// "none" name no provider.
func providerTitle(name string) string {
	e, ok := paymodules.Registry().Get(strings.TrimSpace(name))
	if !ok || !e.Implemented() {
		return ""
	}
	return e.Descriptor.Title
}

// ticketPaymentProvider is the provider the ticket was paid through, "" when
// none or when the lookup fails (best effort: the letter still goes).
func ticketPaymentProvider(ctx context.Context, q *gen.Queries, ticketID uuid.UUID, logger *slog.Logger) string {
	if q == nil {
		return ""
	}
	name, err := q.GetTicketPaymentProvider(ctx, ticketID)
	if err != nil {
		logger.Warn("delivery: payment provider lookup failed", slog.String("ticket_id", ticketID.String()), slog.String("error", err.Error()))
		return ""
	}
	return providerTitle(name)
}

// orderPaymentProvider is the provider the order was paid through, "" when
// none or when the lookup fails.
func orderPaymentProvider(ctx context.Context, db gen.DBTX, orderID uuid.UUID, logger *slog.Logger) string {
	if db == nil || orderID == uuid.Nil {
		return ""
	}
	name, err := gen.New(db).GetOrderPaymentProvider(ctx, orderID)
	if err != nil {
		logger.Warn("delivery: payment provider lookup failed", slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
		return ""
	}
	return providerTitle(name)
}
