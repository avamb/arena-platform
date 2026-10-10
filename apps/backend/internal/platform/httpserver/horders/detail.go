package horders

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// EC-05 (spec 35 §5.6): the order card needs, beyond the order row, what
// every ticket carries (category, price, EAN-13, entry), whether the letter
// went out, how the payment ended and — for an unpaid order — why. This file
// assembles those blocks; HandleGet only glues them in.

// Delivery job states as delivery_jobs.status spells them (migration 0064),
// plus "none" for a ticket that has no job row at all.
const (
	deliveryNone       = "none"
	deliveryPending    = "pending"
	deliveryProcessing = "processing"
	deliverySent       = "sent"
	deliveryFailed     = "failed"
)

// unpaid_reason values (documented on OrderDetail.unpaid_reason in
// openapi.yaml). Deterministic: a function of the order status and the
// payment intent chosen by pickPaymentIntent.
const (
	reasonPaymentFailed    = "payment_failed"    // the chosen intent failed (provider code in payment)
	reasonPaymentAbandoned = "payment_abandoned" // an intent exists, never reached a verdict, order not paid
	reasonAwaitingPayment  = "awaiting_payment"  // pending_payment, the buyer may still pay
	reasonHoldExpired      = "hold_expired"      // expired/abandoned with no intent at all
	reasonCancelled        = "cancelled"
	reasonManualReview     = "manual_review" // a payment arrived that could not complete the checkout
)

// Channel kinds (OrderDetail.channel.kind): how the buyer reached the
// checkout, derived from orders.source and the channel's settings.
const (
	channelKindSite       = "site"        // a selling site through the Bil24-protocol gateway
	channelKindHostedPage = "hosted_page" // the platform's own event/promoter page
	channelKindWidget     = "widget"      // the embeddable widget on a customer's domain
)

// ticketEntry shapes one ListOrderTicketDetails row for the `tickets` array.
// The barcode is the stored EAN-13 credential; a ticket that predates the
// stored credential (feature #502) gets the legacy deterministic
// ean13.PlatformCode — the same fallback orderexport prints, so the number
// here equals the one on every export and PDF of that ticket.
func ticketEntry(r gen.OrderTicketDetailRow, currency string) map[string]any {
	barcode := ean13.PlatformCode(r.SystemTicketID)
	if r.BarcodeStr != nil && *r.BarcodeStr != "" {
		barcode = *r.BarcodeStr
	}
	m := map[string]any{
		"id":               r.TicketID.String(),
		"item_id":          r.ItemID.String(),
		"status":           r.Status,
		"holder_email":     r.HolderEmail,
		"seat_sector":      r.SeatSector,
		"seat_row":         r.SeatRow,
		"seat_number":      r.SeatNumber,
		"seat_label":       seatLabel(r.SeatSector, r.SeatRow, r.SeatNumber),
		"issued_at":        r.IssuedAt.Format(time.RFC3339),
		"cancelled_at":     timePtr(r.CancelledAt),
		"system_ticket_id": r.SystemTicketID,
		"tier_id":          r.TierID.String(),
		"tier_name":        r.TierName,
		"price":            r.Price,
		"currency":         currency,
		"barcode":          barcode,
		"used_at":          timePtr(r.UsedAt),
	}
	return m
}

// seatLabel joins the seat parts that exist ("A / 3 / 12"); nil for a
// general-admission ticket.
func seatLabel(sector, row, number *string) *string {
	var parts []string
	for _, p := range []*string{sector, row, number} {
		if p != nil && strings.TrimSpace(*p) != "" {
			parts = append(parts, strings.TrimSpace(*p))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	s := strings.Join(parts, " / ")
	return &s
}

func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// deliveryEntry shapes the per-ticket delivery block: the delivery_jobs
// status, or "none" when the ticket has no job.
func deliveryEntry(r gen.OrderTicketDetailRow) map[string]any {
	status := deliveryNone
	if r.DeliveryStatus != nil && *r.DeliveryStatus != "" {
		status = *r.DeliveryStatus
	}
	return map[string]any{
		"ticket_id":  r.TicketID.String(),
		"status":     status,
		"sent_at":    timePtr(r.DeliverySentAt),
		"last_error": r.DeliveryError,
	}
}

// deliveryState folds the tickets' delivery statuses into one word for the
// order: sent when every ticket's letter went out, pending while any is
// still queued or being sent, failed when one failed and none is pending,
// none otherwise (no tickets, no jobs, or only skipped/disabled ones).
func deliveryState(rows []gen.OrderTicketDetailRow) string {
	if len(rows) == 0 {
		return deliveryNone
	}
	allSent, anyPending, anyFailed := true, false, false
	for _, r := range rows {
		s := deliveryNone
		if r.DeliveryStatus != nil {
			s = *r.DeliveryStatus
		}
		switch s {
		case deliverySent:
		case deliveryPending, deliveryProcessing:
			allSent, anyPending = false, true
		case deliveryFailed:
			allSent, anyFailed = false, true
		default:
			allSent = false
		}
	}
	switch {
	case allSent:
		return deliverySent
	case anyPending:
		return deliveryPending
	case anyFailed:
		return deliveryFailed
	}
	return deliveryNone
}

// pickPaymentIntent chooses the intent the card speaks about. intents come
// newest first (ListPaymentIntentsByCheckout). A succeeded one wins — it is
// the payment behind a paid order; else the newest failed one — the buyer's
// last attempt that got a verdict, with the provider's code; else the
// newest of all (an open page the buyer may still finish). nil for none.
func pickPaymentIntent(intents []gen.PaymentIntentRow) *gen.PaymentIntentRow {
	for i := range intents {
		if intents[i].State == "succeeded" {
			return &intents[i]
		}
	}
	for i := range intents {
		if intents[i].State == "failed" {
			return &intents[i]
		}
	}
	if len(intents) == 0 {
		return nil
	}
	return &intents[0]
}

// paymentEntry shapes the chosen intent; nil when the order has none (a
// gateway order paid on the selling site, an invitation, a free order).
func paymentEntry(pi *gen.PaymentIntentRow) map[string]any {
	if pi == nil {
		return nil
	}
	return map[string]any{
		"id":                  pi.ID.String(),
		"provider":            pi.Provider,
		"state":               pi.State,
		"provider_payment_id": pi.ProviderPaymentID,
		"provider_charge_ref": pi.ProviderChargeRef,
		"amount":              pi.Amount,
		"currency":            pi.Currency,
		"failure_code":        pi.FailureCode,
		"failure_message":     pi.FailureMessage,
		"hosted_checkout_url": pi.HostedCheckoutURL,
		"created_at":          pi.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":          pi.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// unpaidReason derives the machine code for an order that is not paid.
// Empty for paid / partially_refunded / refunded.
func unpaidReason(status string, pi *gen.PaymentIntentRow) string {
	switch status {
	case ordering.StatusPaid, "partially_refunded", "refunded":
		return ""
	case ordering.StatusCancelled:
		return reasonCancelled
	case "manual_review":
		return reasonManualReview
	}
	if pi != nil && pi.State == "failed" {
		return reasonPaymentFailed
	}
	if status == ordering.StatusPendingPayment {
		return reasonAwaitingPayment
	}
	// expired / abandoned
	if pi != nil {
		return reasonPaymentAbandoned
	}
	return reasonHoldExpired
}

// channelSettingsShape is the slice of sales_channels.settings the kind
// needs: the hosted page flag and the two spellings of a gateway token
// (hbil24.parseGatewaySettings reads the same keys; only presence matters
// here, never the hash).
type channelSettingsShape struct {
	HostedPage *struct {
		Enabled bool `json:"enabled"`
	} `json:"hosted_page"`
	Gateway *struct {
		TokenHash string `json:"token_hash"`
	} `json:"gateway"`
	LegacyTokenHash string `json:"gateway_token_hash"`
}

// channelKind maps the order source and the channel settings onto a kind:
// a bil24_gateway order, or any order of a channel that holds a gateway
// token, is "site"; otherwise a channel with settings.hosted_page.enabled
// is "hosted_page"; everything else (public_feed / checkout_api /
// complimentary on a plain channel) is "widget".
func channelKind(source string, settings json.RawMessage) string {
	if source == "bil24_gateway" {
		return channelKindSite
	}
	var s channelSettingsShape
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &s) // malformed = no flags, same as unset
	}
	if (s.Gateway != nil && s.Gateway.TokenHash != "") || s.LegacyTokenHash != "" {
		return channelKindSite
	}
	if s.HostedPage != nil && s.HostedPage.Enabled {
		return channelKindHostedPage
	}
	return channelKindWidget
}

// channelEntry shapes the `channel` block. A soft-deleted channel keeps its
// id and the kind the source alone implies; its name is empty.
func channelEntry(order gen.OrderRow, ch *gen.SalesChannelRow) map[string]any {
	m := map[string]any{"id": order.ChannelID.String(), "name": "", "kind": channelKind(order.Source, nil)}
	if ch != nil {
		m["name"] = ch.Name
		m["kind"] = channelKind(order.Source, ch.Settings)
	}
	return m
}

// detailExtras is everything HandleGet adds on top of the order row.
type detailExtras struct {
	tickets  []map[string]any
	delivery []map[string]any
	state    string
	payment  map[string]any
	reason   string
	channel  map[string]any
}

// loadDetailExtras runs the three detail lookups. Tickets are required;
// the payment and channel lookups degrade to "unknown" with a log line so
// the card still renders when one of them fails.
func (h *Handler) loadDetailExtras(ctx context.Context, order gen.OrderRow) (detailExtras, error) {
	rows, err := h.queries.ListOrderTicketDetails(ctx, order.ID)
	if err != nil {
		return detailExtras{}, err
	}
	ex := detailExtras{
		tickets:  make([]map[string]any, 0, len(rows)),
		delivery: make([]map[string]any, 0, len(rows)),
		state:    deliveryState(rows),
	}
	for _, r := range rows {
		ex.tickets = append(ex.tickets, ticketEntry(r, order.Currency))
		ex.delivery = append(ex.delivery, deliveryEntry(r))
	}

	intents, err := h.queries.ListPaymentIntentsByCheckout(ctx, order.CheckoutSessionID)
	if err != nil {
		h.logger.Warn("horders: payment intent lookup failed",
			slog.String("order_id", order.ID.String()), slog.Any("error", err))
	}
	pi := pickPaymentIntent(intents)
	ex.payment = paymentEntry(pi)
	ex.reason = unpaidReason(order.Status, pi)

	ch, err := h.queries.GetSalesChannelByID(ctx, order.ChannelID, order.OrgID)
	switch {
	case err == nil:
		ex.channel = channelEntry(order, &ch)
	case errors.Is(err, pgx.ErrNoRows):
		ex.channel = channelEntry(order, nil)
	default:
		h.logger.Warn("horders: channel lookup failed",
			slog.String("order_id", order.ID.String()), slog.Any("error", err))
		ex.channel = channelEntry(order, nil)
	}
	return ex, nil
}

// orgOwnsSession / orgOwnsEvent back the list's session_id / event_id
// filters: a session or event of another organization answers 404 (the
// same answer as an unknown id), never an empty page that would reveal the
// id exists.
func (h *Handler) orgOwnsSession(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	row, err := h.queries.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return row.OrgID == orgID, nil
}

func (h *Handler) orgOwnsEvent(ctx context.Context, orgID, eventID uuid.UUID) (bool, error) {
	row, err := h.queries.GetEventByID(ctx, eventID, "en")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return row.OrgID == orgID, nil
}
