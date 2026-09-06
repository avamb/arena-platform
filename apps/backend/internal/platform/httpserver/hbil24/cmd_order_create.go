// cmd_order_create.go — spec §7.7 CREATE_ORDER_EXT over the order aggregate
// (feature #492, W1-B1b).
//
// The WordPress plugin calls CREATE_ORDER_EXT once per WooCommerce checkout
// attempt, and it may call it MANY times for the same basket: the buyer bounces
// off the payment page, edits the cart, comes back. The command therefore is
// not "insert an order" but "make the platform's open order for this event
// session say exactly what the site just told me", which the spec spells out in
// eight steps:
//
//  1. gateway session alive                      → resultCode 1 otherwise
//  2. actionEventId in this channel's org scope  → -3 otherwise
//     sales open on that session                 → 101 bil24.sales_closed
//  3. the cart exists and matches `lines`        → ordering.ReconcileLines,
//     capacity refusal                           → 101 bil24.category_sold_out
//  4. buyer resolved from fullName/phone/email   → customers.Resolve
//  5. one open order per (customer, session):    → same orderId, rewritten
//     a live hold reuses it, a dead one expires the old order and mints a new
//  6. checkout session inserted + pricing-confirmed with the channel fee and
//     the first applicable promo code
//  7. orders row pending_payment / bil24_gateway / external_ref = request
//     orderId
//  8. the site's own total / chargePercent / expectedPrice are recorded as
//     EVIDENCE in order_events.created.payload.client_reported and never used
//     as an input to our arithmetic
//
// Transaction boundary. Steps 4–7 run in one transaction opened from
// OrderDeps.Pool: buyer, checkout session and order aggregate commit together
// or not at all. Step 3 deliberately runs OUTSIDE it — the hold mutators
// (hcheckout.ExtendHold / ShrinkHold) each own their transaction, and the
// pricing and promo reads that follow are pool-bound and would not observe an
// uncommitted cart. That is not a loss of atomicity that matters: the cart is
// already a separately committed entity (every RESERVE commits on its own), and
// a reconciled-but-unordered cart is exactly the state a plain RESERVE leaves
// behind.
package hbil24

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// orderUnit is one purchasable unit of the reconciled cart, already priced.
// The order aggregate re-derives its own units from the reservation; these
// exist only to feed ComputePricingLines and the promo evaluation, which both
// need the per-unit (tier, price) pairs before any order row exists.
type orderUnit struct {
	tierID uuid.UUID
	price  int64
}

// handleBil24CreateOrderExtSession is the wired CREATE_ORDER_EXT. The stub in
// cmd_order.go routes here only when OrderDeps is complete, so a Handler built
// without the order surface keeps answering -5.
func (h *Handler) handleBil24CreateOrderExtSession(w http.ResponseWriter, r *http.Request, req bil24Request) {
	ctx := r.Context()

	// Spec §7.7: an order with no partner reference and an order with no lines
	// are both malformed requests, not business refusals.
	if strings.TrimSpace(req.OrderID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "orderId is required",
		))
		return
	}
	if len(req.Lines) == 0 {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "lines must contain at least one line",
		))
		return
	}
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for CREATE_ORDER_EXT",
		))
		return
	}

	channel, _ := h.resolveChannelByFID(ctx, req)
	locale := parseGatewaySettings(channel.Settings).DefaultLocale
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

	cc := cartCtx{gw: gw, channel: channel, locale: locale, ttl: cartHoldTTL(channel), orgID: gw.OrgID}

	if strings.TrimSpace(req.ActionEventID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "actionEventId is required",
		))
		return
	}
	sessionID, err := h.resolveActionEventID(ctx, req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}
	cc.sessionID = sessionID

	if !h.orderScopeGuard(ctx, w, req, &cc) {
		return
	}
	sess, ok := h.orderSalesOpen(ctx, w, req, cc)
	if !ok {
		return
	}

	pricing, perr := h.cartSessionPricing(ctx, cc.sessionID)
	if perr != nil {
		h.writeCartTransient(w, req, cc)
		return
	}
	tiers, ok := h.orderLineTiers(ctx, w, req, cc, pricing)
	if !ok {
		return
	}

	res, ok := h.orderEnsureCart(ctx, w, req, cc, pricing, tiers)
	if !ok {
		return
	}
	if !h.orderReconcile(ctx, w, req, cc, res, tiers) {
		return
	}

	// The reconciliation may have grown, shrunk or replaced the hold; re-read
	// it so the expiry we publish and the units we price are the committed
	// ones, then push the whole cart's TTL the way every RESERVE does.
	h.cartRefreshAll(ctx, cc)
	res, err = h.cartDeps.Q.GetActiveGatewayCartReservation(ctx, cc.gw.ID, cc.sessionID)
	if err != nil {
		h.orderTransient(w, req, cc, "cart lookup after reconcile failed", err)
		return
	}

	units, currency, uerr := h.orderUnits(ctx, res, pricing)
	if uerr != nil {
		h.orderTransient(w, req, cc, "cart pricing failed", uerr)
		return
	}
	if len(units) == 0 {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, cc.locale, "bil24.hold_expired",
				"your hold has expired, please reserve again", nil),
		))
		return
	}

	discount, promoCodeID, ok := h.orderPromoDiscount(ctx, w, req, cc, units)
	if !ok {
		return
	}

	bd := hcheckout.ComputePricingLines(
		orderPricingLines(units), discount, currency,
		hcheckout.PricingRules{PlatformFeeRate: int64(cartFeePercent(cc.channel) * 100)},
	)

	h.orderPersist(ctx, w, req, cc, sess, res, units, bd, promoCodeID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Steps 1–2 — scope and sales window
// ─────────────────────────────────────────────────────────────────────────────

// orderScopeGuard enforces spec §7.7 step 2: the addressed event session must
// belong to the organization that minted the gateway session. cc.orgID is
// upgraded to the session's org on success, which is what every later write is
// scoped by.
func (h *Handler) orderScopeGuard(ctx context.Context, w http.ResponseWriter, req bil24Request, cc *cartCtx) bool {
	if h.resDeps.CtxQ == nil {
		return true
	}
	orgCtx, err := h.resDeps.CtxQ.GetSessionOrgContext(ctx, cc.sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeCartNotFound(w, req, *cc)
			return false
		}
		h.orderTransient(w, req, *cc, "session org lookup failed", err)
		return false
	}
	if cc.orgID != uuid.Nil && orgCtx.OrgID != cc.orgID {
		h.logger.Warn("bil24_compat: CREATE_ORDER_EXT: cross-tenant session rejected",
			slog.String("session_id", cc.sessionID.String()),
			slog.String("session_org", orgCtx.OrgID.String()),
			slog.String("gateway_org", cc.orgID.String()),
		)
		h.writeCartNotFound(w, req, *cc)
		return false
	}
	cc.orgID = orgCtx.OrgID
	return true
}

// orderSalesOpen is the spec §7.7 "sales open" gate. A session that is not
// `scheduled` — draft, cancelled, completed — or whose end has already passed
// cannot take new orders, and the refusal is USER VISIBLE (101) because the
// plugin renders the description in the basket.
func (h *Handler) orderSalesOpen(ctx context.Context, w http.ResponseWriter, req bil24Request, cc cartCtx) (gen.SessionRow, bool) {
	eventID, err := h.orderDeps.SessionQ.GetSessionEventID(ctx, cc.sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeCartNotFound(w, req, cc)
			return gen.SessionRow{}, false
		}
		h.orderTransient(w, req, cc, "session lookup failed", err)
		return gen.SessionRow{}, false
	}
	sess, err := h.orderDeps.SessionQ.GetSessionByID(ctx, cc.sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeCartNotFound(w, req, cc)
			return gen.SessionRow{}, false
		}
		h.orderTransient(w, req, cc, "session lookup failed", err)
		return gen.SessionRow{}, false
	}

	if sess.Status != "scheduled" || !sess.EndAt.After(time.Now().UTC()) {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, cc.locale, "bil24.sales_closed",
				"sales are closed for this event", nil),
		))
		return gen.SessionRow{}, false
	}
	return sess, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — the cart must equal `lines`
// ─────────────────────────────────────────────────────────────────────────────

// orderLineTiers resolves the request's `lines` into per-tier quantities.
//
// The one place this differs from the RESERVATION path (cartCategoryPayload) is
// the verdict for a category that belongs to a DIFFERENT action event: there it
// is -3 "not found", here spec §7.7 makes it the user-visible
// 101 bil24.line_wrong_session, because the site can only fix it by rebuilding
// its basket and the buyer needs to be told.
func (h *Handler) orderLineTiers(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	pricing cartPricing,
) ([]hcheckout.HoldTierQuantity, bool) {
	order := make([]uuid.UUID, 0, len(req.Lines))
	qty := make(map[uuid.UUID]int32, len(req.Lines))

	for i, l := range req.Lines {
		if strings.TrimSpace(l.CategoryPriceID) == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("lines[%d].categoryPriceId is required", i),
			))
			return nil, false
		}
		tierID, err := h.resolveCategoryPriceID(ctx, l.CategoryPriceID)
		if err != nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("lines[%d].categoryPriceId must be a valid tier identifier", i),
			))
			return nil, false
		}
		if l.Quantity <= 0 {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("lines[%d].quantity must be >= 1", i),
			))
			return nil, false
		}
		if mode, known := pricing.mode[tierID]; known {
			if mode != "fixed" && mode != "free" {
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeUserVisible,
					h.localizeDesc(req.Locale, cc.locale, "bil24.pricing_mode_unsupported",
						"pricing mode is not supported by this gateway", nil),
				))
				return nil, false
			}
		} else if len(pricing.mode) > 0 {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeUserVisible,
				h.localizeDesc(req.Locale, cc.locale, "bil24.line_wrong_session",
					"this ticket category belongs to another event", nil),
			))
			return nil, false
		}
		if _, exists := qty[tierID]; !exists {
			order = append(order, tierID)
		}
		qty[tierID] += int32(l.Quantity) //nolint:gosec // validated > 0 above
	}

	out := make([]hcheckout.HoldTierQuantity, 0, len(order))
	for _, id := range order {
		out = append(out, hcheckout.HoldTierQuantity{TierID: id, Quantity: qty[id]})
	}
	return out, true
}

// orderEnsureCart implements the site's `bil24_reserve_preflight`: a
// CREATE_ORDER_EXT that arrives without a prior RESERVE (the plugin skips it
// when WooCommerce restores a saved basket) must still find a hold, so the
// cart is created from `lines` on the spot.
func (h *Handler) orderEnsureCart(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	pricing cartPricing,
	tiers []hcheckout.HoldTierQuantity,
) (gen.ReservationRow, bool) {
	res, err := h.cartDeps.Q.GetActiveGatewayCartReservation(ctx, cc.gw.ID, cc.sessionID)
	switch {
	case err == nil:
		return res, true
	case errors.Is(err, pgx.ErrNoRows):
		if !h.cartCreateHold(ctx, w, req, cc, pricing, nil, nil, tiers) {
			return gen.ReservationRow{}, false
		}
		res, err = h.cartDeps.Q.GetActiveGatewayCartReservation(ctx, cc.gw.ID, cc.sessionID)
		if err != nil {
			h.orderTransient(w, req, cc, "cart lookup after preflight failed", err)
			return gen.ReservationRow{}, false
		}
		return res, true
	default:
		h.orderTransient(w, req, cc, "cart lookup failed", err)
		return gen.ReservationRow{}, false
	}
}

// orderReconcile makes the held GA quantities equal the requested ones.
//
// Seats are left alone on purpose (spec §7.7 step 3): on the seated path the
// site owns seat selection, so a seat the request did not mention is NOT
// evidence the buyer dropped it. A reservation with no GA lines is therefore
// skipped entirely.
func (h *Handler) orderReconcile(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	res gen.ReservationRow,
	tiers []hcheckout.HoldTierQuantity,
) bool {
	seats, err := h.cartDeps.Q.ListReservationSeats(ctx, res.ID)
	if err != nil {
		h.orderTransient(w, req, cc, "cart seat listing failed", err)
		return false
	}
	items, err := h.cartDeps.Q.ListReservationGAItems(ctx, res.ID)
	if err != nil {
		h.orderTransient(w, req, cc, "cart GA listing failed", err)
		return false
	}
	// A hold whose seats carry a tier stamp is a seated hold; its GA lines are
	// AB-48 price locks, not quantities to reconcile.
	if len(items) == 0 || orderIsSeated(seats) {
		return true
	}

	lines := make([]ordering.Line, 0, len(tiers))
	for _, t := range tiers {
		lines = append(lines, ordering.Line{TierID: t.TierID, Quantity: t.Quantity})
	}

	_, rerr := ordering.ReconcileLines(ctx, h.orderDeps.Q, ordering.HoldMutators{
		Extend: func(ctx context.Context, reservationID uuid.UUID, tq []ordering.TierQuantity) error {
			_, err := h.cartDeps.Extend(ctx, hcheckout.HoldMutationInput{
				ReservationID: reservationID, GATiers: orderHoldTiers(tq), TTL: cc.ttl,
			})
			return err
		},
		Shrink: func(ctx context.Context, reservationID uuid.UUID, tq []ordering.TierQuantity) error {
			_, err := h.cartDeps.Shrink(ctx, hcheckout.HoldMutationInput{
				ReservationID: reservationID, GATiers: orderHoldTiers(tq),
			})
			return err
		},
	}, ordering.ReconcileInput{
		ReservationID: res.ID,
		Lines:         lines,
		Actor:         orderActor(req),
	})
	if rerr != nil {
		if errors.Is(rerr, ordering.ErrCapacityUnavailable) {
			h.writeCartHoldError(w, req, cc, nil, cartPricing{}, errors.Unwrap(rerr))
			return false
		}
		h.orderTransient(w, req, cc, "cart reconciliation failed", rerr)
		return false
	}
	return true
}

func orderIsSeated(seats []gen.SessionSeatRow) bool {
	for _, s := range seats {
		if s.TierID != nil {
			return true
		}
	}
	return false
}

func orderHoldTiers(tq []ordering.TierQuantity) []hcheckout.HoldTierQuantity {
	out := make([]hcheckout.HoldTierQuantity, 0, len(tq))
	for _, t := range tq {
		out = append(out, hcheckout.HoldTierQuantity{TierID: t.TierID, Quantity: t.Quantity})
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Pricing inputs
// ─────────────────────────────────────────────────────────────────────────────

// orderUnits expands the reconciled reservation of THIS event session into
// priced units. Deliberately not collectCart: the cart may span several action
// events, but one order is one session (spec §7.7), so pricing must see only
// this session's hold.
func (h *Handler) orderUnits(ctx context.Context, res gen.ReservationRow, pricing cartPricing) ([]orderUnit, string, error) {
	items, err := h.cartDeps.Q.ListReservationGAItems(ctx, res.ID)
	if err != nil {
		return nil, "", err
	}
	seats, err := h.cartDeps.Q.ListReservationSeats(ctx, res.ID)
	if err != nil {
		return nil, "", err
	}

	currency := pricing.currency
	locked := make(map[uuid.UUID]int64, len(items))
	for _, it := range items {
		locked[it.TierID] = it.UnitPrice
		if currency == "" {
			currency = it.Currency
		}
	}

	if !orderIsSeated(seats) {
		units := make([]orderUnit, 0, len(items))
		for _, it := range items {
			for n := int32(0); n < it.Quantity; n++ {
				units = append(units, orderUnit{tierID: it.TierID, price: it.UnitPrice})
			}
		}
		return units, currency, nil
	}

	units := make([]orderUnit, 0, len(seats))
	for _, s := range seats {
		if s.TierID == nil {
			continue
		}
		price, ok := locked[*s.TierID]
		if !ok {
			price = pricing.price[*s.TierID]
		}
		units = append(units, orderUnit{tierID: *s.TierID, price: price})
	}
	return units, currency, nil
}

// orderPricingLines groups the units by (tier, price) in first-seen order so
// ComputePricingLines produces a stable, per-tier breakdown.
func orderPricingLines(units []orderUnit) []hcheckout.PricingLineInput {
	type key struct {
		tier  uuid.UUID
		price int64
	}
	order := make([]key, 0, 4)
	qty := make(map[key]int32, 4)
	for _, u := range units {
		k := key{u.tierID, u.price}
		if _, seen := qty[k]; !seen {
			order = append(order, k)
		}
		qty[k]++
	}
	out := make([]hcheckout.PricingLineInput, 0, len(order))
	for _, k := range order {
		out = append(out, hcheckout.PricingLineInput{
			TierID: k.tier.String(), Quantity: qty[k], UnitPrice: k.price,
		})
	}
	return out
}

// orderPromoDiscount picks the FIRST applicable code out of the request's
// promoCodes unioned with the codes already attached to the gateway session
// (spec §7.7 step 6). A code that does not apply is silently skipped rather
// than failing the order: ADD_PROMO_CODES is where a buyer learns a code is
// invalid, and refusing checkout over it would strand the sale.
func (h *Handler) orderPromoDiscount(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	units []orderUnit,
) (int64, *uuid.UUID, bool) {
	codes := mergePromoCodes(req.PromoCodes, cc.gw.PromoCodes)
	if len(codes) == 0 || h.promoQ == nil {
		return 0, nil, true
	}

	snap := cartSnapshot{lines: make([]cartLine, 0, len(units))}
	for _, u := range units {
		snap.lines = append(snap.lines, cartLine{price: u.price, tierID: u.tierID})
		snap.sum += u.price
	}

	now := time.Now().UTC()
	for _, code := range codes {
		v, err := h.evaluatePromoCode(ctx, promoCtx{cc: cc, snap: snap}, code, now)
		if err != nil {
			h.orderTransient(w, req, cc, "promo evaluation failed", err)
			return 0, nil, false
		}
		if v.errCode != "" || v.discount <= 0 {
			continue
		}
		id := v.row.ID
		return v.discount, &id, true
	}
	return 0, nil, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Steps 4–8 — the transactional write
// ─────────────────────────────────────────────────────────────────────────────

// orderPersist runs the whole write half of the command in one transaction:
// buyer, checkout session, and the order aggregate that either reuses the
// customer's open order or replaces an expired one.
func (h *Handler) orderPersist(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	sess gen.SessionRow,
	res gen.ReservationRow,
	units []orderUnit,
	bd hcheckout.PricingBreakdown,
	promoCodeID *uuid.UUID,
) {
	tx, err := h.orderDeps.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.orderTransient(w, req, cc, "begin order tx failed", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txq := gen.New(tx)

	// Step 4. Verified flags are never touched here: Resolve only attaches
	// identities, and the gateway has not proven ownership of either channel.
	cres, err := customers.Resolve(ctx, customers.NewStoreFromQueries(txq), customers.ResolveInput{
		Email:     strings.TrimSpace(req.Email),
		Phone:     strings.TrimSpace(req.Phone),
		Name:      strings.TrimSpace(req.FullName),
		ChannelID: cc.channel.ID,
		Source:    customers.SourceLive,
	})
	if err != nil {
		h.orderInternal(w, req, "customer resolution failed", err)
		return
	}
	customerID := cres.Customer.ID

	// Step 6.
	token, err := mintOrderCheckoutToken()
	if err != nil {
		h.orderInternal(w, req, "checkout token minting failed", err)
		return
	}
	cs, err := txq.InsertCheckoutSessionWithToken(ctx, cc.orgID, cc.channel.ID, res.ID, nil, token)
	if err != nil {
		h.orderInternal(w, req, "insert checkout session failed", err)
		return
	}
	cs, err = txq.ConfirmCheckoutSession(ctx, cs.ID,
		bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total,
		bd.Currency, promoCodeID,
	)
	if err != nil {
		h.orderInternal(w, req, "confirm checkout session failed", err)
		return
	}

	in := ordering.CreateInput{
		CheckoutSessionID: cs.ID,
		EventID:           sess.EventID,
		CustomerID:        &customerID,
		Source:            ordering.SourceBil24Gateway,
		Actor:             orderActor(req),
		ExternalRef:       &req.OrderID,
		ChargePercentBP:   ordering.ChargePercentBP(cc.channel.FeePercent),
		BuyerName:         optionalString(req.FullName),
		BuyerEmail:        optionalString(req.Email),
		BuyerPhone:        optionalString(req.Phone),
		TierUnitPrices:    orderTierPrices(units),
		ClientReported:    orderClientReported(req),
	}

	order, err := h.orderWriteAggregate(ctx, txq, in, customerID, cc, res)
	if err != nil {
		h.orderInternal(w, req, "order write failed", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.orderInternal(w, req, "commit order tx failed", err)
		return
	}

	expiration := res.ExpiresAt
	if order.ExpiresAt != nil {
		expiration = *order.ExpiresAt
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"orderId":         TranslatePlatformID(order.ID),
		"externalOrderId": req.OrderID,
		"sum":             order.Subtotal,
		"discount":        order.Discount,
		"charge":          order.Charge,
		"totalSum":        order.Total,
		"currency":        order.Currency,
		"expiration":      expiration.UTC().Format(time.RFC3339),
	}))
}

// orderWriteAggregate is spec §7.7 step 5, the one-open-order rule. The
// customer may hold at most one pending_payment order per event session
// (orders_one_pending_per_customer_session_uq), so a repeat CREATE_ORDER_EXT
// either rewrites that order — same orderId, refreshed numbers, which is what
// the site depends on to avoid duplicate WooCommerce orders — or, when the hold
// behind it is gone, expires it and mints a fresh one.
func (h *Handler) orderWriteAggregate(
	ctx context.Context,
	txq *gen.Queries,
	in ordering.CreateInput,
	customerID uuid.UUID,
	cc cartCtx,
	res gen.ReservationRow,
) (gen.OrderRow, error) {
	existing, ferr := ordering.FindOpenOrder(ctx, txq, customerID, cc.sessionID)
	switch {
	case ferr == nil && existing.ReservationID == res.ID:
		out, err := ordering.UpdateOrderFromCheckout(ctx, txq, ordering.UpdateInput{
			OrderID: existing.ID, OrgID: existing.OrgID, CreateInput: in,
		})
		return out.Order, err
	case ferr == nil:
		// The open order points at a hold this cart no longer uses — it
		// expired and the buyer re-reserved. Retire it so the partial unique
		// index lets the replacement in.
		if _, err := ordering.Expire(ctx, txq, ordering.ExpireInput{
			OrderID: existing.ID, OrgID: existing.OrgID, Actor: in.Actor,
		}); err != nil {
			return gen.OrderRow{}, err
		}
	case errors.Is(ferr, ordering.ErrNoOpenOrder):
	default:
		return gen.OrderRow{}, ferr
	}

	out, err := ordering.CreateOrderFromCheckout(ctx, txq, in)
	return out.Order, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Small helpers
// ─────────────────────────────────────────────────────────────────────────────

// orderClientReported is spec §7.7 step 8. The site's own arithmetic is
// EVIDENCE, recorded verbatim in order_events.created so a later dispute can be
// settled, and never an input: every number on the order comes from
// ComputePricingLines over platform-held prices.
func orderClientReported(req bil24Request) any {
	out := map[string]any{}
	if req.Total != nil {
		out["total"] = *req.Total
	}
	if req.ChargePercent != nil {
		out["charge_percent"] = *req.ChargePercent
	}
	if req.ExpectedPrice != nil {
		out["expected_price"] = *req.ExpectedPrice
	}
	if req.Currency != "" {
		out["currency"] = req.Currency
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// orderTierPrices feeds CreateInput.TierUnitPrices so a seated order prices its
// seats the same way this handler priced them.
func orderTierPrices(units []orderUnit) map[uuid.UUID]int64 {
	out := make(map[uuid.UUID]int64, 4)
	for _, u := range units {
		out[u.tierID] = u.price
	}
	return out
}

// orderActor is the order_events actor label for gateway-initiated writes.
// Note it is NOT usable as audit_events.actor_id, which is a uuid column.
func orderActor(req bil24Request) string {
	return "gateway:" + req.FID
}

func optionalString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// mintOrderCheckoutToken generates the 64-char hex checkout_token the
// checkout_sessions unique index requires.
func mintOrderCheckoutToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// orderTransient reports a retryable infrastructure failure (-1) after logging
// the cause; orderInternal is the non-retryable -99 counterpart used once the
// command has started writing.
func (h *Handler) orderTransient(w http.ResponseWriter, req bil24Request, cc cartCtx, msg string, err error) {
	h.logger.Error("bil24_compat: CREATE_ORDER_EXT: "+msg,
		slog.String("gateway_session_id", cc.gw.ID.String()),
		slog.String("session_id", cc.sessionID.String()),
		slog.String("error", err.Error()),
	)
	h.writeCartTransient(w, req, cc)
}

func (h *Handler) orderInternal(w http.ResponseWriter, req bil24Request, msg string, err error) {
	h.logger.Error("bil24_compat: CREATE_ORDER_EXT: "+msg,
		slog.String("fid", req.FID),
		slog.String("error", err.Error()),
	)
	writeBil24JSON(w, http.StatusOK, bil24Error(
		req.Command, ResultCodeInternalError, "failed to create order",
	))
}
