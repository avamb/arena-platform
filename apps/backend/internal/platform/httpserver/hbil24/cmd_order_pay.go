// Package hbil24 — PAY_ORDER (spec §7.9, feature #494, W1-B2a; rewritten for
// the payment-window contract, owner decision 2026-09-13).
//
// PAY_ORDER is the moment the WordPress shop tells us "the buyer has paid".
// The money never touches arena: WooCommerce has already charged the card and
// is reporting the fact (or is about to void the authorization — the site
// only calls PAY_ORDER after a successful capture). Two hard constraints
// follow from that plus the owner's payment-window rule:
//
//   - A buyer gets a FIXED payment window from the moment the site created
//     the order (CREATE_ORDER_EXT sets orders.expires_at =
//     reservations.expires_at = now + window + grace). Once that instant
//     passes, payment is no longer accepted — the order is expired or
//     cancelled and its inventory goes back on sale automatically. There is
//     NO MANUAL REVIEW anywhere in this file any more: every outcome
//     resolves without an operator.
//
//   - The site polls GET_TICKETS_BY_ORDER five times with 2/4/8s backoff
//     (spec §7.10). Issuing tickets asynchronously would make the first poll a
//     coin flip, so issuance runs SYNCHRONOUSLY right after the payment
//     transaction commits. The checkout.issue_tickets worker job is enqueued
//     inside that same transaction anyway, as insurance for the case where the
//     process dies between COMMIT and the synchronous call.
//
// Every PAY_ORDER call takes a row-level lock on the order FIRST
// (LockOrderForUpdate) and makes its ENTIRE decision inside that one
// transaction: concurrent PAY_ORDER calls for the same order (a WordPress
// retry storm, or several requests landing right at the payment-window
// boundary) serialize on the lock instead of racing a read-then-write status
// decision, so at most one of them ever pays, expires, or cancels the order.
//
// The write set of a successful payment is one transaction (spec §7.9 step
// 4): payment_intents (provider='manual'), checkout_sessions → completed,
// reservation → converted, promo redemption, orders → paid, customer_org_links,
// customer_identities verification.
package hbil24

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/issuejob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// Order statuses PAY_ORDER refuses outright. ordering exposes the happy-path
// vocabulary; these live only in the migration-0092 CHECK constraint (the
// refund pipeline writes the first two; nothing today writes the third) so
// they are named here rather than widening the ordering package for a
// read-only comparison.
const (
	payStatusRefunded          = "refunded"
	payStatusPartiallyRefunded = "partially_refunded"
	// payStatusManualReview is handled for LEGACY rows only: nothing in this
	// file writes it any more (owner decision 2026-09-13 — no manual
	// review), but a row parked by a pre-payment-window deployment must
	// still answer something sane and stop being retried forever.
	payStatusManualReview = "manual_review"
	// payStatusAbandoned mirrors checkout_sessions' 'abandoned' state in the
	// orders CHECK constraint. No code path writes it to orders today, but
	// PAY_ORDER enumerates every status the constraint allows rather than
	// letting an unhandled one fall through to a generic error.
	payStatusAbandoned = "abandoned"
)

// payProviderManual is payment_intents.provider for a payment collected by the
// partner shop. arena is the ledger of record for the ticket, not for the card
// transaction, so there is no PSP to name.
const payProviderManual = "manual"

// payAmountToleranceMinor is the ±1 minor unit window of spec §7.9 step 3,
// restated by spec 20 §2.4. The wire carries MAJOR units, orders.total is
// minor, so the comparison converts first (money.Minor) and then tolerates a
// single minor unit of rounding drift between the shop's cart and ours.
const payAmountToleranceMinor int64 = 1

// payCustomerLinkSource is customer_org_links.source. Migration 0091
// constrains it to ('order','import'); a gateway sale is an order.
const payCustomerLinkSource = "order"

// errPayHoldExpired is the internal sentinel raised when ReacquireHoldTx could
// not restore the order's inventory (payment-window contract case c: the
// hold's own TTL passed and somebody else's RESERVE took the seats). It
// aborts the payment transaction so the CANCEL (payCancelOrder) runs against
// an order that transaction has already rolled back.
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

// handleBil24PayOrderWired resolves the order, then hands the ENTIRE
// pay-or-expire-or-cancel decision to payLocked, which makes it atomically
// under a row lock on the order (see the package doc comment).
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

	// Resolve the order inside the caller's org. A crafted orderId belonging
	// to another tenant must be indistinguishable from a typo, so both
	// answer -3. This read is only used to find the id/org for the lock —
	// the actual decision below re-reads the row under LockOrderForUpdate,
	// so a stale value here cannot cause a wrong answer.
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

	outcome, cs, err := h.payLocked(ctx, req, order, cartHoldTTL(channel))
	if err != nil {
		h.payTransient(w, req, "payment transaction failed", err)
		return
	}

	switch outcome {
	case payOutcomePaid, payOutcomeAlreadyPaid:
		if outcome == payOutcomePaid {
			h.payIssueTickets(ctx, order, cs, settings)
		}
		writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, nil))
	case payOutcomeOrderExpired:
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, locale, "bil24.order_expired",
				"payment time for this order has expired", nil),
		))
	case payOutcomeOrderCancelled:
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, locale, "bil24.order_cancelled",
				"order has been cancelled", nil),
		))
	case payOutcomeHoldExpired:
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, locale, "bil24.hold_expired",
				"your hold has expired, please reserve again", nil),
		))
	default:
		h.payTransient(w, req, "unexpected pay outcome", fmt.Errorf("hbil24: unhandled payOutcome %d", outcome))
	}
}

// payIssueTickets runs steps 5-6 of spec §7.9: synchronous ticket issuance
// right after the payment transaction commits. Suppressing arena's own
// delivery e-mail unless the channel opts in (settings.gateway.platform_email)
// — the WordPress shop mails its own PDF, and two e-mails per buyer is a
// support incident, not a feature.
func (h *Handler) payIssueTickets(ctx context.Context, order gen.OrderRow, cs gen.CheckoutSessionRow, settings GatewaySettings) {
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
		return
	}
	h.logger.Info("bil24_compat: PAY_ORDER: order paid",
		slog.String("order_id", order.ID.String()),
		slog.Int64("order_system_id", order.SystemID),
		slog.Int("tickets", issued),
	)
}

// payResolveOrder implements spec §7.9 step 1's lookup. The spec names
// orders.system_id — the bigint wire id — and every shape the gateway has
// ever emitted (system_id, platform UUID, legacy checkout_session UUID) is
// accepted, each org-scoped before it is returned.
func (h *Handler) payResolveOrder(ctx context.Context, raw string, orgID uuid.UUID) (gen.OrderRow, error) {
	return resolveOrderRef(ctx, h.orderDeps.Q, raw, orgID)
}

// orderRefQuerier is the read surface resolveOrderRef needs. It exists so
// GET_TICKETS_BY_ORDER (feature #495) can reuse the lookup without dragging in
// PAY_ORDER's transaction starter: a read command must not require a writable
// OrderDeps bundle to answer.
type orderRefQuerier interface {
	GetOrderBySystemID(ctx context.Context, systemID int64) (gen.OrderRow, error)
	GetOrderByID(ctx context.Context, id, orgID uuid.UUID) (gen.OrderRow, error)
	GetOrderByCheckoutSession(ctx context.Context, checkoutSessionID uuid.UUID) (gen.OrderRow, error)
}

// resolveOrderRef accepts every orderId shape the gateway has ever emitted and
// returns the order only when it belongs to the caller's org.
func resolveOrderRef(ctx context.Context, q orderRefQuerier, raw string, orgID uuid.UUID) (gen.OrderRow, error) {
	raw = strings.TrimSpace(raw)

	if sysID, err := strconv.ParseInt(raw, 10, 64); err == nil {
		order, gErr := q.GetOrderBySystemID(ctx, sysID)
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

	order, err := q.GetOrderByID(ctx, id, orgID)
	if err == nil {
		return order, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.OrderRow{}, err
	}
	// Older gateway answers exposed the checkout session id as orderId.
	order, err = q.GetOrderByCheckoutSession(ctx, id)
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
// payLocked — the one atomic decision (payment-window contract)
// ─────────────────────────────────────────────────────────────────────────────

// payOutcome enumerates every wire-visible result payLocked can reach.
type payOutcome int

const (
	// payOutcomePaid — the payment just completed in THIS call; tickets must
	// be issued. resultCode 0.
	payOutcomePaid payOutcome = iota
	// payOutcomeAlreadyPaid — the order was already 'paid' before this call
	// (idempotent replay); nothing to issue again. resultCode 0.
	payOutcomeAlreadyPaid
	// payOutcomeOrderExpired — the payment window (window+grace from
	// CREATE_ORDER_EXT) has passed, whether this call is the one that just
	// expired the order or it was already expired. resultCode 101
	// bil24.order_expired.
	payOutcomeOrderExpired
	// payOutcomeOrderCancelled — cancelled/refunded/partially_refunded/
	// abandoned. resultCode 101 bil24.order_cancelled.
	payOutcomeOrderCancelled
	// payOutcomeHoldExpired — the hold died and could not be reacquired
	// (case c: sold out to someone else within the window), so the order was
	// just cancelled; or a LEGACY manual_review row from before this
	// contract. resultCode 101 bil24.hold_expired.
	payOutcomeHoldExpired
)

// payLocked is the single entry point for PAY_ORDER's whole decision. It
// takes a row-level lock on the order FIRST (LockOrderForUpdate) and decides
// everything else — pay, expire, or leave alone — from the row it reads
// UNDER that lock, never from a value read before the transaction opened.
// That is what makes 10 concurrent PAY_ORDER calls for the same order safe:
// they serialize on the lock, and only the first one to observe
// pending_payment (with a live-or-reacquirable hold, before the deadline)
// actually pays; every other call sees the row AFTER that transaction
// committed and answers consistently with whatever it became.
func (h *Handler) payLocked(
	ctx context.Context,
	req bil24Request,
	order gen.OrderRow,
	ttl time.Duration,
) (payOutcome, gen.CheckoutSessionRow, error) {
	tx, err := h.orderDeps.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, gen.CheckoutSessionRow{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txq := gen.New(tx)
	now := time.Now().UTC()
	actor := orderActor(req)

	locked, err := txq.LockOrderForUpdate(ctx, order.ID, order.OrgID)
	if err != nil {
		return 0, gen.CheckoutSessionRow{}, fmt.Errorf("lock order: %w", err)
	}

	switch locked.Status {
	case ordering.StatusPaid:
		return payOutcomeAlreadyPaid, gen.CheckoutSessionRow{}, tx.Commit(ctx)
	case ordering.StatusExpired:
		return payOutcomeOrderExpired, gen.CheckoutSessionRow{}, tx.Commit(ctx)
	case ordering.StatusCancelled, payStatusRefunded, payStatusPartiallyRefunded, payStatusAbandoned:
		return payOutcomeOrderCancelled, gen.CheckoutSessionRow{}, tx.Commit(ctx)
	case payStatusManualReview:
		// Legacy rows only (nothing writes this status any more): answer the
		// same wire result without touching anything.
		return payOutcomeHoldExpired, gen.CheckoutSessionRow{}, tx.Commit(ctx)
	case ordering.StatusPendingPayment:
		// fall through to the window/hold decision below.
	default:
		return 0, gen.CheckoutSessionRow{}, fmt.Errorf("hbil24: order %s has unexpected status %q", locked.ID, locked.Status)
	}

	// Case (d): the payment window (window+grace from CREATE_ORDER_EXT) has
	// passed and reservation.expire_sweep / order.expire_sweep have not yet
	// caught up. PAY_ORDER performs both sweeps' job itself, atomically,
	// under the same lock: expire the order, release the hold.
	if locked.ExpiresAt != nil && now.After(*locked.ExpiresAt) {
		expired, ok, err := ordering.ExpireIfStillPending(ctx, txq, ordering.ExpireIfStillPendingInput{
			OrderID: locked.ID, Actor: actor, Now: now,
		})
		if err != nil {
			return 0, gen.CheckoutSessionRow{}, err
		}
		if !ok {
			// Cannot happen while we hold the row lock and just confirmed
			// pending_payment above — stay defensive rather than silently
			// answering something that might be stale.
			return 0, gen.CheckoutSessionRow{}, fmt.Errorf("hbil24: order %s changed status under lock", locked.ID)
		}
		if err := hcheckout.ExpireHoldForOrderTx(ctx, txq, locked.ReservationID); err != nil {
			return 0, gen.CheckoutSessionRow{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, gen.CheckoutSessionRow{}, fmt.Errorf("commit: %w", err)
		}
		h.logger.Info("bil24_compat: PAY_ORDER: order expired — payment window elapsed",
			slog.String("order_id", locked.ID.String()),
			slog.Int64("order_system_id", locked.SystemID),
			slog.Time("expires_at", *locked.ExpiresAt),
		)
		_ = expired
		return payOutcomeOrderExpired, gen.CheckoutSessionRow{}, nil
	}

	// Cases (b)/(c): still inside the window — live hold pays normally, a
	// TTL'd-but-reacquirable hold is re-taken and pays, a hold genuinely
	// lost to someone else cancels the order.
	cs, err := h.payExecuteLocked(ctx, tx, txq, req, locked, ttl, now, actor)
	switch {
	case errors.Is(err, errPayHoldExpired):
		// Roll back EXPLICITLY, right now, before doing anything else: this
		// transaction is still holding the LockOrderForUpdate row lock, and
		// payCancelOrder below opens a SEPARATE connection to cancel the
		// order — it would otherwise block waiting for a lock this very
		// call is holding, on the very same goroutine that has to return
		// before the deferred Rollback ever runs. Rolling back here (the
		// deferred Rollback afterwards is then a harmless no-op) releases
		// the lock immediately, so the cancel below proceeds as a fresh,
		// separate transaction/statement, mirroring CANCEL_ORDER
		// (cmd_order_cancel.go).
		_ = tx.Rollback(ctx)
		h.payCancelOrder(ctx, req, locked, "hold_expired")
		return payOutcomeHoldExpired, gen.CheckoutSessionRow{}, nil
	case err != nil:
		return 0, gen.CheckoutSessionRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, gen.CheckoutSessionRow{}, fmt.Errorf("commit: %w", err)
	}
	return payOutcomePaid, cs, nil
}

// payCancelOrder is case (c)'s automatic resolution (owner decision
// 2026-09-13: no manual review). It mirrors CANCEL_ORDER
// (cmd_order_cancel.go handleBil24CancelWired): cancel the order aggregate
// via the ordering lifecycle (an order_event with the reason, so the audit
// trail shows exactly why), then best-effort release whatever the hold still
// holds. Both steps run outside any transaction the caller was in — that
// transaction already rolled back — and are individually idempotent, so a
// concurrent caller reaching the same conclusion for the same order cannot
// corrupt anything: Cancel is a no-op on an already-cancelled order and
// ReleaseHold tolerates an already-released one.
func (h *Handler) payCancelOrder(ctx context.Context, req bil24Request, order gen.OrderRow, reason string) {
	actor := orderActor(req)
	updated, err := ordering.Cancel(ctx, h.orderDeps.Q, ordering.CancelInput{
		OrderID: order.ID,
		OrgID:   order.OrgID,
		Actor:   actor,
		Reason:  reason,
	})
	if err != nil {
		if errors.Is(err, ordering.ErrInvalidTransition) {
			// Lost a race: a concurrent call already moved this order on
			// (paid it, expired it, cancelled it) before this cancel could
			// land. Whatever it became is already the truth the next
			// PAY_ORDER call will see — nothing more to do here.
			h.logger.Warn("bil24_compat: PAY_ORDER: order already moved on before the cancel landed",
				slog.String("order_id", order.ID.String()),
				slog.String("reason", reason),
			)
			return
		}
		h.logger.Error("bil24_compat: PAY_ORDER: could not cancel the order after a lost hold",
			slog.String("order_id", order.ID.String()),
			slog.String("error", err.Error()),
		)
		return
	}

	if _, relErr := hcheckout.ReleaseHold(ctx, h.orderDeps.Pool, h.orderDeps.Q, updated.ReservationID); relErr != nil {
		var notReleasable *hcheckout.NotReleasableError
		if !errors.As(relErr, &notReleasable) && !errors.Is(relErr, hcheckout.ErrHoldNotFound) {
			h.logger.Error("bil24_compat: PAY_ORDER: release hold after cancel failed",
				slog.String("order_id", order.ID.String()),
				slog.String("error", relErr.Error()),
			)
		}
	}

	h.logger.Warn("bil24_compat: PAY_ORDER: order cancelled — hold could not be secured",
		slog.String("order_id", updated.ID.String()),
		slog.Int64("order_system_id", updated.SystemID),
		slog.String("reservation_id", updated.ReservationID.String()),
		slog.String("reason", reason),
		slog.String("actor", actor),
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// The payment transaction body (runs inside payLocked's transaction)
// ─────────────────────────────────────────────────────────────────────────────

// payExecuteLocked runs spec §7.9 steps 2-4 inside the caller's transaction
// (already holding the order's row lock) and returns the completed checkout
// session, which the caller needs to issue tickets against. It never begins
// or commits a transaction itself — payLocked owns that.
func (h *Handler) payExecuteLocked(
	ctx context.Context,
	tx pgx.Tx,
	txq *gen.Queries,
	req bil24Request,
	order gen.OrderRow,
	ttl time.Duration,
	now time.Time,
	actor string,
) (gen.CheckoutSessionRow, error) {
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
		// Not pricing_confirmed any more. The order was not paid (we checked
		// under the lock), so this is a session another path already
		// completed or parked; carry on with the row we read rather than
		// aborting a committed payment.
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
// holds the inventory (errPayHoldExpired → the order is cancelled by the
// caller, payLocked/payCancelOrder).
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
// Units (spec 20 §2.4): req.Amount is major, orders.total is minor, so the
// reported amount is converted with money.Minor before anything is compared,
// and BOTH sides are recorded in minor units on the order event.
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
	reportedMinor := money.Minor(*req.Amount)
	delta := reportedMinor - order.Total
	if delta < 0 {
		delta = -delta
	}
	if delta <= payAmountToleranceMinor {
		return nil
	}
	h.logger.Warn("bil24_compat: PAY_ORDER: reported amount differs from the order total",
		slog.String("order_id", order.ID.String()),
		slog.Float64("reported_major", *req.Amount),
		slog.Int64("reported_minor", reportedMinor),
		slog.Int64("expected_minor", order.Total),
	)
	if _, err := txq.InsertOrderEvent(ctx, order.ID, ordering.EventAmountMismatch, actor,
		payPayload(map[string]any{
			"reported_amount":       *req.Amount,
			"reported_amount_minor": reportedMinor,
			"expected_amount_minor": order.Total,
			"order_total":           order.Total,
			"currency":              order.Currency,
			"wire_currency":         strings.TrimSpace(req.Currency),
			"tolerance_minor":       payAmountToleranceMinor,
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
	reservationID, orderID, channelID := order.ReservationID, order.ID, order.ChannelID
	if err := txq.InsertPromoCodeRedemption(
		ctx, promo.ID, nil, &reservationID, order.Discount, order.Subtotal,
		&orderID, order.CustomerID, &channelID,
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
// and the platform disagree about a transaction. It is also what makes two
// concurrent successful PAY_ORDER transactions for the SAME order mutually
// exclusive at the database level: payment_intents.provider_payment_id is a
// GLOBAL unique index, and this value is deterministic per order+method, so
// whichever transaction's InsertPaymentIntent commits first wins and the
// other gets a real 23505 — belt-and-braces alongside the LockOrderForUpdate
// serialization in payLocked.
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
