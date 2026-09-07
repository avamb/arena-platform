// Package hbil24 — PAY_ORDER (spec §7.9, feature #494, W1-B2a).
//
// PAY_ORDER is the moment the WordPress shop tells us "the buyer has paid".
// The money never touches arena: WooCommerce has already charged the card and
// is reporting the fact. Everything this command does is therefore a
// *bookkeeping* transition, and the two hard constraints follow from that:
//
//   - The money has ALREADY MOVED. Once the shop says paid, refusing the
//     payment strands the buyer with a charge and no ticket. So almost every
//     discrepancy is recorded and waved through: an `amount` that disagrees
//     with orders.total only writes order_events.amount_mismatch (spec §7.9
//     step 3), and a promo-redemption bookkeeping failure is logged, never
//     fatal. The single exception is a hold that can no longer be re-taken —
//     we cannot conjure a seat somebody else is now sitting in — which parks
//     the order in manual_review, alerts an operator and answers 101. That is
//     the ONLY 101 after payment; the site then sets bil24_ext_status=pay_failed.
//
//   - The site polls GET_TICKETS_BY_ORDER five times with 2/4/8s backoff
//     (spec §7.10). Issuing tickets asynchronously would make the first poll a
//     coin flip, so issuance runs SYNCHRONOUSLY right after the payment
//     transaction commits. The checkout.issue_tickets worker job is enqueued
//     inside that same transaction anyway, as insurance for the case where the
//     process dies between COMMIT and the synchronous call.
//
// The write set is one transaction (spec §7.9 step 4): payment_intents
// (provider='manual'), checkout_sessions → completed, reservation → converted,
// promo redemption, orders → paid, customer_org_links, customer_identities
// verification.

package hbil24

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/issuejob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// Order statuses PAY_ORDER refuses outright. ordering exposes the happy-path
// vocabulary; these two live only in the migration-0092 CHECK constraint (the
// refund pipeline writes them) so they are named here rather than widening the
// ordering package for a read-only comparison.
const (
	payStatusRefunded          = "refunded"
	payStatusPartiallyRefunded = "partially_refunded"
	// payStatusManualReview parks an order whose hold could not be re-taken.
	payStatusManualReview = "manual_review"
)

// payProviderManual is payment_intents.provider for a payment collected by the
// partner shop. arena is the ledger of record for the ticket, not for the card
// transaction, so there is no PSP to name.
const payProviderManual = "manual"

// payAmountToleranceMajor is the ±0.01 window of spec §7.9 step 3. The Bil24
// wire carries order money in the same units as orders.total (CREATE_ORDER
// answers `totalSum: orders.total` verbatim), so no conversion is involved.
const payAmountToleranceMajor = 0.01

// payCustomerLinkSource is customer_org_links.source. Migration 0091
// constrains it to ('order','import'); a gateway sale is an order.
const payCustomerLinkSource = "order"

// errPayHoldExpired is the internal sentinel raised when ReacquireHoldTx could
// not restore the order's inventory. It aborts the payment transaction so that
// the manual-review park runs in a transaction of its own — parking an order in
// the same transaction we are about to roll back would lose the park.
var errPayHoldExpired = errors.New("hbil24: order hold expired and could not be reacquired")

// ─────────────────────────────────────────────────────────────────────────────
// Entry point
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24PayOrder is the PAY_ORDER dispatch target. It validates the shape
// of the request first — a malformed request is -2 whether or not the surface
// is wired — and then self-gates to -5 when the handler was built without the
// order transaction starter or the issuance callback, matching every other
// optional surface in this package.
func (h *Handler) handleBil24PayOrder(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.OrderID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "orderId is required",
		))
		return
	}
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for PAY_ORDER",
		))
		return
	}
	if !h.orderDeps.wired() || !h.payDeps.wired() {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotImplemented,
			"PAY_ORDER is not available on this deployment",
		))
		return
	}
	h.handleBil24PayOrderWired(w, r, req)
}

// handleBil24PayOrderWired walks spec §7.9 steps 1–6.
func (h *Handler) handleBil24PayOrderWired(w http.ResponseWriter, r *http.Request, req bil24Request) {
	ctx := r.Context()

	channel, _ := h.resolveChannelByFID(ctx, req)
	settings := parseGatewaySettings(channel.Settings)
	locale := settings.DefaultLocale
	if h.requireToken && !h.validateGatewayToken(w, req, channel.Settings) {
		return
	}

	gw, ok := h.resolveGatewaySession(ctx, w, req, channel, locale)
	if !ok {
		return
	}
	if gw.ID == uuid.Nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "order service unavailable",
		))
		return
	}

	// Step 1 — resolve the order inside the caller's org. A crafted orderId
	// belonging to another tenant must be indistinguishable from a typo, so
	// both answer -3.
	order, err := h.payResolveOrder(ctx, req.OrderID, gw.OrgID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotFound, "order not found",
		))
		return
	case err != nil:
		h.payTransient(w, req, "order lookup failed", err)
		return
	}

	// Step 1 (cont.) — terminal statuses. Already paid is the idempotent
	// replay the shop performs after a timeout: answer 0 and write nothing.
	switch order.Status {
	case ordering.StatusPaid:
		writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, nil))
		return
	case ordering.StatusCancelled, payStatusRefunded, payStatusPartiallyRefunded:
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, locale, "bil24.order_cancelled",
				"order has been cancelled", nil),
		))
		return
	}

	// Steps 2–4 — one transaction.
	cs, err := h.payExecute(ctx, req, order, cartHoldTTL(channel))
	switch {
	case errors.Is(err, errPayHoldExpired):
		h.payParkManualReview(ctx, req, order)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, locale, "bil24.hold_expired",
				"your hold has expired, please reserve again", nil),
		))
		return
	case err != nil:
		h.payTransient(w, req, "payment transaction failed", err)
		return
	}

	// Steps 5–6 — synchronous issuance. The delivery e-mail is suppressed
	// unless the channel opts in: the WordPress shop mails its own PDF, and
	// two e-mails per buyer is a support incident, not a feature.
	issued, ierr := h.payDeps.IssueTickets(ctx, cs, !settings.PlatformEmail)
	if ierr != nil {
		// The payment is committed and the insurance worker job is queued, so
		// the honest answer is still 0: the site's GET_TICKETS_BY_ORDER polls
		// will pick the tickets up once the job runs.
		h.logger.Error("bil24_compat: PAY_ORDER: synchronous issuance failed, falling back to the worker job",
			slog.String("order_id", order.ID.String()),
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", ierr.Error()),
		)
	} else {
		h.logger.Info("bil24_compat: PAY_ORDER: order paid",
			slog.String("order_id", order.ID.String()),
			slog.Int64("order_system_id", order.SystemID),
			slog.Int("tickets", issued),
		)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, nil))
}

// payResolveOrder implements spec §7.9 step 1's lookup. The spec names
// orders.system_id — the bigint wire id — but CREATE_ORDER_EXT currently
// answers with the platform UUID (see cmd_order_create.go's deferred-id note),
// and a site that echoes what we gave it must not be punished for our own
// transitional format. So all three shapes the gateway has ever emitted are
// accepted, and each is org-scoped before it is returned.
func (h *Handler) payResolveOrder(ctx context.Context, raw string, orgID uuid.UUID) (gen.OrderRow, error) {
	raw = strings.TrimSpace(raw)

	if sysID, err := strconv.ParseInt(raw, 10, 64); err == nil {
		order, gErr := h.orderDeps.Q.GetOrderBySystemID(ctx, sysID)
		if gErr != nil {
			return gen.OrderRow{}, gErr
		}
		return payScopeOrder(order, orgID)
	}

	id, err := uuid.Parse(raw)
	if err != nil {
		// Not an id in any form we mint. Indistinguishable from "unknown
		// order" on the wire, and treating it as -3 rather than -2 keeps the
		// tenant-probing surface flat.
		return gen.OrderRow{}, pgx.ErrNoRows
	}

	order, err := h.orderDeps.Q.GetOrderByID(ctx, id, orgID)
	if err == nil {
		return order, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.OrderRow{}, err
	}
	// Older gateway answers exposed the checkout session id as orderId.
	order, err = h.orderDeps.Q.GetOrderByCheckoutSession(ctx, id)
	if err != nil {
		return gen.OrderRow{}, err
	}
	return payScopeOrder(order, orgID)
}

// payScopeOrder collapses "belongs to another org" into "does not exist".
func payScopeOrder(order gen.OrderRow, orgID uuid.UUID) (gen.OrderRow, error) {
	if order.OrgID != orgID {
		return gen.OrderRow{}, pgx.ErrNoRows
	}
	return order, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Steps 2–4 — the payment transaction
// ─────────────────────────────────────────────────────────────────────────────

// payExecute runs spec §7.9 steps 2–4 atomically and returns the completed
// checkout session, which step 5 needs to issue tickets against.
func (h *Handler) payExecute(
	ctx context.Context,
	req bil24Request,
	order gen.OrderRow,
	ttl time.Duration,
) (gen.CheckoutSessionRow, error) {
	tx, err := h.orderDeps.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txq := gen.New(tx)
	now := time.Now().UTC()
	actor := orderActor(req)

	cs, err := txq.GetCheckoutSessionByID(ctx, order.CheckoutSessionID)
	if err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("load checkout session: %w", err)
	}

	// Step 2 — the hold.
	if err := h.payEnsureHold(ctx, txq, order, actor, ttl, now); err != nil {
		return gen.CheckoutSessionRow{}, err
	}

	// Step 3 — amount reconciliation. Non-blocking by design.
	h.payBestEffort(ctx, tx, "amount reconciliation", order, func(sp pgx.Tx) error {
		return h.payRecordAmountMismatch(ctx, gen.New(sp), req, order, actor)
	})

	// Step 4 — the money-side writes.
	providerPaymentID := payProviderPaymentID(order, req.Method)
	intent, err := txq.InsertPaymentIntent(
		ctx, &order.CheckoutSessionID, order.OrgID,
		payProviderManual, &providerPaymentID,
		order.Total, order.Currency, "succeeded", nil, nil,
	)
	if err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("insert payment intent: %w", err)
	}

	completed, err := txq.CompleteCheckoutSession(ctx, cs.ID, intent.ID.String(), payProviderManual)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Not pricing_confirmed any more. The order was not paid (we checked),
		// so this is a session another path already completed or parked; carry
		// on with the row we read rather than aborting a committed payment.
		h.logger.Warn("bil24_compat: PAY_ORDER: checkout session was not pricing_confirmed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("state", cs.State),
		)
		completed = cs
	case err != nil:
		return gen.CheckoutSessionRow{}, fmt.Errorf("complete checkout session: %w", err)
	}

	if err := hcheckout.ConvertReservationInTx(ctx, txq, order.ReservationID); err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("convert reservation: %w", err)
	}

	h.payBestEffort(ctx, tx, "promo redemption", order, func(sp pgx.Tx) error {
		return h.payRedeemPromo(ctx, gen.New(sp), order)
	})

	if _, err := ordering.MarkPaid(ctx, txq, ordering.PaidInput{
		OrderID: order.ID,
		OrgID:   order.OrgID,
		Actor:   actor,
		Now:     now,
		Payload: map[string]any{
			"provider":            payProviderManual,
			"provider_payment_id": providerPaymentID,
			"payment_method":      strings.TrimSpace(req.Method),
			"payment_intent_id":   intent.ID.String(),
		},
	}); err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("mark order paid: %w", err)
	}
	if method := strings.TrimSpace(req.Method); method != "" {
		if err := txq.SetOrderPaymentMethod(ctx, order.ID, order.OrgID, method); err != nil {
			return gen.CheckoutSessionRow{}, fmt.Errorf("set payment method: %w", err)
		}
	}

	h.payBestEffort(ctx, tx, "customer linking", order, func(sp pgx.Tx) error {
		return h.payLinkCustomer(ctx, gen.New(sp), order, now)
	})

	// Insurance: even though issuance runs synchronously below, a crash
	// between COMMIT and that call must not cost the buyer their tickets.
	h.payBestEffort(ctx, tx, "issuance insurance job", order, func(sp pgx.Tx) error {
		return payEnqueueIssuance(ctx, sp, completed.ID)
	})

	if err := tx.Commit(ctx); err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("commit: %w", err)
	}
	return completed, nil
}

// payBestEffort runs a bookkeeping write set that must never cost the buyer a
// payment they have already made. Merely logging and swallowing the error is
// NOT enough: Postgres marks the whole transaction aborted after any failed
// statement, so every later statement dies with 25P02 and COMMIT silently
// rolls the payment back. A SAVEPOINT (pgx nested transaction) confines the
// damage to the failed section, which is then discarded and logged.
func (h *Handler) payBestEffort(
	ctx context.Context,
	tx pgx.Tx,
	label string,
	order gen.OrderRow,
	fn func(pgx.Tx) error,
) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		h.logger.Error("bil24_compat: PAY_ORDER: could not open a savepoint",
			slog.String("section", label),
			slog.String("order_id", order.ID.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := fn(sp); err != nil {
		_ = sp.Rollback(ctx)
		h.logger.Error("bil24_compat: PAY_ORDER: bookkeeping section failed and was discarded",
			slog.String("section", label),
			slog.String("order_id", order.ID.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := sp.Commit(ctx); err != nil {
		_ = sp.Rollback(ctx)
		h.logger.Error("bil24_compat: PAY_ORDER: bookkeeping section could not be released",
			slog.String("section", label),
			slog.String("order_id", order.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// payEnsureHold is spec §7.9 step 2. A live hold is left alone; a hold past its
// TTL is re-taken on the same seats/GA units via ReacquireHoldTx, which either
// succeeds (order_events.hold_reacquired) or fails because somebody else now
// holds the inventory (errPayHoldExpired → manual review).
func (h *Handler) payEnsureHold(
	ctx context.Context,
	txq *gen.Queries,
	order gen.OrderRow,
	actor string,
	ttl time.Duration,
	now time.Time,
) error {
	res, err := txq.GetReservationByID(ctx, order.ReservationID)
	if err != nil {
		return fmt.Errorf("load reservation: %w", err)
	}

	switch res.State {
	case "converted":
		// A prior PAY_ORDER attempt already consumed the hold; the seats are
		// sold and there is nothing to re-take.
		return nil
	case "draft", "active":
		if res.ExpiresAt.After(now) {
			return nil // live hold — step 4 straight away
		}
	default:
		// cancelled / expired: the inventory is provably gone.
		return errPayHoldExpired
	}

	if _, err := hcheckout.ReacquireHoldTx(ctx, txq, hcheckout.HoldMutationInput{
		ReservationID: res.ID,
		TTL:           ttl,
		Now:           now,
	}); err != nil {
		h.logger.Warn("bil24_compat: PAY_ORDER: hold reacquire failed",
			slog.String("order_id", order.ID.String()),
			slog.String("reservation_id", res.ID.String()),
			slog.String("error", err.Error()),
		)
		return errPayHoldExpired
	}

	if _, err := txq.InsertOrderEvent(ctx, order.ID, ordering.EventHoldReacquired, actor,
		payPayload(map[string]any{
			"reservation_id": res.ID.String(),
			"expired_at":     res.ExpiresAt.UTC().Format(time.RFC3339),
			"ttl_seconds":    int(ttl.Seconds()),
		}),
	); err != nil {
		return fmt.Errorf("record hold_reacquired: %w", err)
	}
	return nil
}

// payRecordAmountMismatch is spec §7.9 step 3. The shop has already taken the
// buyer's money, so a disagreement is evidence for reconciliation, never a
// refusal — hence the error is returned for the caller's SAVEPOINT to discard
// rather than aborting the payment.
//
// No unit conversion happens here: this gateway puts order money on the wire
// verbatim (CREATE_ORDER answers `totalSum: orders.total`), so the reported
// amount and orders.total are already in the same units.
func (h *Handler) payRecordAmountMismatch(
	ctx context.Context,
	txq *gen.Queries,
	req bil24Request,
	order gen.OrderRow,
	actor string,
) error {
	if req.Amount == nil {
		return nil
	}
	expected := float64(order.Total)
	if math.Abs(expected-*req.Amount) <= payAmountToleranceMajor {
		return nil
	}
	h.logger.Warn("bil24_compat: PAY_ORDER: reported amount differs from the order total",
		slog.String("order_id", order.ID.String()),
		slog.Float64("reported", *req.Amount),
		slog.Float64("expected", expected),
	)
	if _, err := txq.InsertOrderEvent(ctx, order.ID, ordering.EventAmountMismatch, actor,
		payPayload(map[string]any{
			"reported_amount":  *req.Amount,
			"expected_amount":  expected,
			"order_total":      order.Total,
			"currency":         order.Currency,
			"wire_currency":    strings.TrimSpace(req.Currency),
			"tolerance_majors": payAmountToleranceMajor,
		}),
	); err != nil {
		return fmt.Errorf("record amount_mismatch: %w", err)
	}
	return nil
}

// payRedeemPromo records the order's promo usage. Bookkeeping only: the money
// has moved, so an over-limit code is logged for an operator rather than
// rejected. Double counting is prevented upstream by the already-paid early
// return, which makes a replayed PAY_ORDER never reach this far.
func (h *Handler) payRedeemPromo(ctx context.Context, txq *gen.Queries, order gen.OrderRow) error {
	if order.PromoCodeID == nil {
		return nil
	}
	promo, err := txq.GetPromoCodeByIDForUpdate(ctx, *order.PromoCodeID)
	if err != nil {
		return fmt.Errorf("promo lookup %s: %w", order.PromoCodeID.String(), err)
	}
	if promo.MaxUses != nil {
		used, cErr := txq.CountPromoCodeRedemptions(ctx, promo.ID)
		if cErr == nil && used >= *promo.MaxUses {
			h.logger.Warn("bil24_compat: PAY_ORDER: promo code is over its max_uses; recording the redemption anyway",
				slog.String("promo_code_id", promo.ID.String()),
				slog.Int("used", int(used)),
				slog.Int("max_uses", int(*promo.MaxUses)),
			)
		}
	}
	reservationID := order.ReservationID
	if _, err := txq.InsertPromoCodeRedemption(
		ctx, promo.ID, nil, &reservationID, order.Discount, order.Subtotal,
	); err != nil {
		return fmt.Errorf("promo redemption %s: %w", promo.ID.String(), err)
	}
	return nil
}

// payLinkCustomer is the tail of spec §7.9 step 4: a paying buyer is a customer
// of the organization, and the contact details they paid with are proven real.
// Both writes are idempotent and neither is worth failing a payment over.
func (h *Handler) payLinkCustomer(ctx context.Context, txq *gen.Queries, order gen.OrderRow, now time.Time) error {
	if order.CustomerID == nil {
		return nil
	}
	// customer_org_links.source is constrained to ('order','import') by
	// migration 0091 — the channel is recorded on the order, not here.
	if err := txq.UpsertCustomerOrgLink(ctx, *order.CustomerID, order.OrgID, payCustomerLinkSource); err != nil {
		return fmt.Errorf("customer_org_links upsert %s: %w", order.CustomerID.String(), err)
	}

	wanted := payVerifiableIdentities(order)
	if len(wanted) == 0 {
		return nil
	}
	identities, err := txq.ListCustomerIdentities(ctx, *order.CustomerID)
	if err != nil {
		return fmt.Errorf("identity lookup %s: %w", order.CustomerID.String(), err)
	}
	for _, ident := range identities {
		if ident.VerifiedAt != nil {
			continue
		}
		if wanted[ident.Kind] != ident.ValueNormalized {
			continue
		}
		if err := txq.MarkCustomerIdentityVerified(ctx, ident.ID, now); err != nil {
			return fmt.Errorf("verify identity %s: %w", ident.ID.String(), err)
		}
	}
	return nil
}

// payVerifiableIdentities normalizes the order's buyer contacts the same way
// the customers resolver stored them, so a string comparison is meaningful.
// Values that will not normalize are simply dropped: an unverifiable identity
// is not an error, it just stays unverified.
func payVerifiableIdentities(order gen.OrderRow) map[string]string {
	out := make(map[string]string, 2)
	if order.BuyerEmail != nil {
		if v, err := customers.NormalizeEmail(*order.BuyerEmail); err == nil && v != "" {
			out[string(customers.KindEmail)] = v
		}
	}
	if order.BuyerPhone != nil {
		if v, err := customers.NormalizePhone(*order.BuyerPhone, ""); err == nil && v != "" {
			out[string(customers.KindPhone)] = v
		}
	}
	return out
}

// payEnqueueIssuance queues checkout.issue_tickets inside the payment
// transaction, mirroring the payment-webhook path in hcheckout.
func payEnqueueIssuance(ctx context.Context, tx pgx.Tx, checkoutSessionID uuid.UUID) error {
	payload, err := json.Marshal(issuejob.Payload{CheckoutSessionID: checkoutSessionID.String()})
	if err != nil {
		return err
	}
	const insertWorkerJobSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, $3, 'pending', now())`
	_, err = tx.Exec(ctx, insertWorkerJobSQL, issuejob.JobType, payload, 5)
	return err
}

// payProviderPaymentID is the spec §7.9 step 4 provider reference:
// wc:<external_ref>:<method>. It is what an operator greps for when the shop
// and the platform disagree about a transaction.
func payProviderPaymentID(order gen.OrderRow, method string) string {
	ref := ""
	if order.ExternalRef != nil {
		ref = strings.TrimSpace(*order.ExternalRef)
	}
	if ref == "" {
		ref = strconv.FormatInt(order.SystemID, 10)
	}
	m := strings.TrimSpace(method)
	if m == "" {
		m = "unknown"
	}
	return "wc:" + ref + ":" + m
}

// ─────────────────────────────────────────────────────────────────────────────
// The manual-review fallback
// ─────────────────────────────────────────────────────────────────────────────

// payParkManualReview is the second half of spec §7.9 step 2's failure branch.
// It runs in its own transaction because the payment transaction has already
// been rolled back, and it is deliberately best-effort at every step: the buyer
// has been charged and an operator MUST learn about it, so a failure to write
// one of these rows still produces the alert and the error log.
func (h *Handler) payParkManualReview(ctx context.Context, req bil24Request, order gen.OrderRow) {
	actor := orderActor(req)

	tx, err := h.orderDeps.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.logger.Error("bil24_compat: PAY_ORDER: could not open the manual-review transaction",
			slog.String("order_id", order.ID.String()),
			slog.String("error", err.Error()),
		)
	} else {
		defer func() { _ = tx.Rollback(ctx) }()
		txq := gen.New(tx)

		if _, uErr := txq.UpdateOrderStatus(ctx, order.ID, order.OrgID, payStatusManualReview, nil, nil); uErr != nil {
			h.logger.Error("bil24_compat: PAY_ORDER: could not park the order in manual_review",
				slog.String("order_id", order.ID.String()),
				slog.String("error", uErr.Error()),
			)
		}
		if _, mErr := txq.MarkCheckoutSessionManualReview(ctx, order.CheckoutSessionID); mErr != nil && !errors.Is(mErr, pgx.ErrNoRows) {
			h.logger.Error("bil24_compat: PAY_ORDER: could not park the checkout session in manual_review",
				slog.String("checkout_session_id", order.CheckoutSessionID.String()),
				slog.String("error", mErr.Error()),
			)
		}
		if _, eErr := txq.InsertOrderEvent(ctx, order.ID, ordering.EventHoldExpired, actor,
			payPayload(map[string]any{
				"reservation_id": order.ReservationID.String(),
				"reason":         "hold could not be reacquired at payment time",
				"external_ref":   req.OrderID,
			}),
		); eErr != nil {
			h.logger.Error("bil24_compat: PAY_ORDER: could not record hold_expired",
				slog.String("order_id", order.ID.String()),
				slog.String("error", eErr.Error()),
			)
		}
		if cErr := tx.Commit(ctx); cErr != nil {
			h.logger.Error("bil24_compat: PAY_ORDER: manual-review transaction commit failed",
				slog.String("order_id", order.ID.String()),
				slog.String("error", cErr.Error()),
			)
		}
	}

	// The operator alert. The error log is unconditional so an unwired Alert
	// callback degrades the alert rather than losing it.
	h.logger.Error("bil24_compat: PAY_ORDER: PAID ORDER PARKED IN MANUAL REVIEW — inventory could not be restored",
		slog.String("order_id", order.ID.String()),
		slog.Int64("order_system_id", order.SystemID),
		slog.String("org_id", order.OrgID.String()),
		slog.String("reservation_id", order.ReservationID.String()),
		slog.String("external_ref", req.OrderID),
		slog.String("actor", actor),
	)
	if h.payDeps.Alert != nil {
		h.payDeps.Alert(ctx, order.ID, actor, map[string]any{
			"reason":              "hold_expired",
			"order_system_id":     order.SystemID,
			"reservation_id":      order.ReservationID.String(),
			"checkout_session_id": order.CheckoutSessionID.String(),
			"external_ref":        req.OrderID,
			"actor_label":         actor,
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Small helpers
// ─────────────────────────────────────────────────────────────────────────────

// payPayload marshals an order_events payload, degrading to '{}' rather than
// failing a payment over an audit row.
func payPayload(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return json.RawMessage(`{}`)
	}
	return b
}

// payTransient reports a retryable infrastructure failure (-1). PAY_ORDER never
// answers -99 for a pre-commit failure: the shop is expected to retry, and a
// retry is safe because every write happens in one transaction.
func (h *Handler) payTransient(w http.ResponseWriter, req bil24Request, msg string, err error) {
	h.logger.Error("bil24_compat: PAY_ORDER: "+msg,
		slog.String("fid", req.FID),
		slog.String("order_id", req.OrderID),
		slog.String("error", err.Error()),
	)
	writeBil24JSON(w, http.StatusOK, bil24Error(
		req.Command, ResultCodeTransient, "temporary failure, please retry",
	))
}
