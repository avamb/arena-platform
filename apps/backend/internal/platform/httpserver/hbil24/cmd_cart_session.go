// cmd_cart_session.go — the spec §7.4 RESERVATION contract expressed over the
// *gateway session cart* (feature #484, W1-A5b).
//
// Where the pre-W1 dispatcher in cmd_cart.go created one fresh, immutable hold
// per command, this file implements the shape the WordPress plugin actually
// speaks: a single mutable reservation per (gateway session, event session),
// created on the first RESERVE and extended / shrunk afterwards through the
// feature-#483 hold mutation primitives (hcheckout.ExtendHold / ShrinkHold /
// RefreshHoldExpiry). Four request shapes are served:
//
//	{type:"RESERVE",        …, actionEventId, categoryList:[{categoryPriceId,quantity}]}
//	{type:"RESERVE",        …, actionEventId, seatList:[{seatId}]}
//	{type:"UN_RESERVE",     …, actionEventId, seatList|categoryList}
//	{type:"UN_RESERVE_ALL", …}                       // no actionEventId
//
// and all four answer with the SAME projection — the whole cart across every
// action event — because the site matches its local basket against the returned
// seatList by seatId and counts tickets by counting rows.
//
// The entry point self-gates: handleBil24Reservation only routes here when the
// whole CartDeps surface is wired (see handler.go). A Handler built without it
// keeps the pre-#484 behaviour, which is what every earlier unit test asserts.
package hbil24

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// The three RESERVATION sub-commands carried by the wire `type` field.
const (
	cartTypeReserve      = "RESERVE"
	cartTypeUnReserve    = "UN_RESERVE"
	cartTypeUnReserveAll = "UN_RESERVE_ALL"
)

// cartCtx is the resolved per-request context every session-cart branch needs:
// who is asking (the gateway session row that keys the cart), through which
// channel (fee percent, TTL override, locale), and — for the two per-event
// shapes — which event session the mutation targets.
type cartCtx struct {
	gw        gen.GatewaySessionRow
	channel   gen.SalesChannelRow
	locale    string // channel default_locale; negotiated against req.Locale
	ttl       time.Duration
	sessionID uuid.UUID // event session; uuid.Nil for UN_RESERVE_ALL
	orgID     uuid.UUID
}

// handleBil24ReservationCart is the spec §7.4 dispatcher. It performs the
// checks common to all four shapes — credential, gateway session, org scope —
// then hands off to the per-type branch. Every branch finishes by writing the
// same whole-cart response through writeCartResponse.
func (h *Handler) handleBil24ReservationCart(w http.ResponseWriter, r *http.Request, req bil24Request) {
	ctx := r.Context()

	// An absent `type` means the legacy single-shape RESERVATION, which the
	// plugin used before it grew UN_RESERVE — it adds to the cart.
	typ := strings.ToUpper(strings.TrimSpace(req.Type))
	if typ == "" {
		typ = cartTypeReserve
	}
	switch typ {
	case cartTypeReserve, cartTypeUnReserve, cartTypeUnReserveAll:
	default:
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"type must be one of RESERVE, UN_RESERVE, UN_RESERVE_ALL",
		))
		return
	}

	// Early credential gate — a missing token is -4 before any DB round-trip.
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		h.logger.Warn("bil24_compat: RESERVATION: token missing in request; rejecting",
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for RESERVATION",
		))
		return
	}

	channel, _ := h.resolveChannelByFID(ctx, req)
	locale := parseGatewaySettings(channel.Settings).DefaultLocale
	if h.requireToken && !h.validateGatewayToken(w, req, channel.Settings) {
		return
	}

	// (userId, sessionId) must still be alive — otherwise resultCode=1 and the
	// site re-runs CREATE_USER (spec §7.3 / §7.4).
	gw, ok := h.resolveGatewaySession(ctx, w, req, channel, locale)
	if !ok {
		return
	}
	if gw.ID == uuid.Nil {
		// resolveGatewaySession degraded to pass-through (no session surface):
		// there is no cart key, so the session-cart contract cannot be served.
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "reservation service unavailable",
		))
		return
	}

	cc := cartCtx{
		gw:      gw,
		channel: channel,
		locale:  locale,
		ttl:     cartHoldTTL(channel),
		orgID:   gw.OrgID,
	}

	if typ == cartTypeUnReserveAll {
		h.cartUnReserveAll(ctx, w, req, cc)
		return
	}

	// The two per-event shapes require actionEventId.
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

	hasSeats := len(req.SeatList) > 0
	hasCats := len(req.CategoryList) > 0
	if hasSeats && hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"seatList and categoryList are mutually exclusive",
		))
		return
	}
	if !hasSeats && !hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"either seatList or categoryList must be provided",
		))
		return
	}

	// Org-scope guard (spec §7.4: "сеанс не в скоупе → -3"). The event session
	// must live in the organization that minted the gateway session.
	if h.resDeps.CtxQ != nil {
		orgCtx, oerr := h.resDeps.CtxQ.GetSessionOrgContext(ctx, sessionID)
		if oerr != nil {
			if errors.Is(oerr, pgx.ErrNoRows) {
				h.writeCartNotFound(w, req, cc)
				return
			}
			h.logger.Error("bil24_compat: RESERVATION: session org lookup failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", oerr.Error()),
			)
			h.writeCartTransient(w, req, cc)
			return
		}
		if cc.orgID != uuid.Nil && orgCtx.OrgID != cc.orgID {
			h.logger.Warn("bil24_compat: RESERVATION: cross-tenant session rejected",
				slog.String("session_id", sessionID.String()),
				slog.String("session_org", orgCtx.OrgID.String()),
				slog.String("gateway_org", cc.orgID.String()),
			)
			h.writeCartNotFound(w, req, cc)
			return
		}
		cc.orgID = orgCtx.OrgID
	}

	if typ == cartTypeUnReserve {
		h.cartUnReserve(ctx, w, req, cc)
		return
	}
	h.cartReserve(ctx, w, req, cc)
}

// cartHoldTTL is the reservation lifetime for holds this channel creates:
// sales_channels.reservation_ttl_override when positive, else the platform
// default. Spec §7.4 measures cartTimeout against it.
func cartHoldTTL(channel gen.SalesChannelRow) time.Duration {
	if channel.ReservationTTLOverride != nil && *channel.ReservationTTLOverride > 0 {
		return time.Duration(*channel.ReservationTTLOverride) * time.Second
	}
	return hcheckout.DefaultReservationTTL
}

// ─────────────────────────────────────────────────────────────────────────────
// RESERVE
// ─────────────────────────────────────────────────────────────────────────────

// cartReserve adds seats or GA units to the session cart. The event session's
// line is created on first use and extended on every later call; either way the
// TTL of the WHOLE cart is pushed to now+TTL afterwards, which is the spec §7.4
// "each successful RESERVE refreshes cartTimeout" rule.
func (h *Handler) cartReserve(ctx context.Context, w http.ResponseWriter, req bil24Request, cc cartCtx) {
	pricing, perr := h.cartSessionPricing(ctx, cc.sessionID)
	if perr != nil {
		h.writeCartTransient(w, req, cc)
		return
	}

	// One cart, one currency (spec §7.4). An empty cart adopts whatever the
	// first session brings; an unknown currency on either side skips the guard
	// rather than blocking the sale.
	cartCur := h.cartCurrency(ctx, cc)
	if pricing.currency != "" && cartCur != "" && !strings.EqualFold(pricing.currency, cartCur) {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, cc.locale, "bil24.currency_mismatch",
				"cart currency does not match this session", nil),
		))
		return
	}

	seatKeys, seatByKey, gaTiers, ok := h.cartPayload(ctx, w, req, cc, pricing)
	if !ok {
		return
	}

	existing, err := h.cartDeps.Q.GetActiveGatewayCartReservation(ctx, cc.gw.ID, cc.sessionID)
	switch {
	case err == nil:
		if _, mErr := h.cartDeps.Extend(ctx, hcheckout.HoldMutationInput{
			ReservationID: existing.ID,
			SeatKeys:      seatKeys,
			GATiers:       gaTiers,
			TTL:           cc.ttl,
		}); mErr != nil {
			h.writeCartHoldError(w, req, cc, seatByKey, pricing, mErr)
			return
		}
	case errors.Is(err, pgx.ErrNoRows):
		if !h.cartCreateHold(ctx, w, req, cc, pricing, seatKeys, seatByKey, gaTiers) {
			return
		}
	default:
		h.logger.Error("bil24_compat: RESERVATION: cart lookup failed",
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("error", err.Error()),
		)
		h.writeCartTransient(w, req, cc)
		return
	}

	h.cartRefreshAll(ctx, cc)
	h.writeCartResponse(ctx, w, req, cc)
}

// cartCreateHold opens the event session's cart line through the pre-existing
// hcheckout create-hold callbacks and binds the fresh reservation to the
// gateway session so every later RESERVE / UN_RESERVE finds it. Returns false
// when a Bil24 error envelope has already been written.
func (h *Handler) cartCreateHold(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	pricing cartPricing,
	seatKeys []string,
	seatByKey map[string]gen.SessionSeatRow,
	gaTiers []hcheckout.HoldTierQuantity,
) bool {
	var reservationID uuid.UUID
	expiresAt := time.Now().UTC().Add(cc.ttl)

	switch {
	case len(seatKeys) > 0:
		if h.resDeps.SeatedReserve == nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "reservation service unavailable",
			))
			return false
		}
		res, err := h.resDeps.SeatedReserve(ctx, hcheckout.SeatedHoldInput{
			OrgID:     cc.orgID,
			ChannelID: cc.channel.ID,
			SessionID: cc.sessionID,
			SeatKeys:  seatKeys,
			ExpiresAt: expiresAt,
		})
		if err != nil {
			h.writeCartHoldError(w, req, cc, seatByKey, pricing, err)
			return false
		}
		reservationID = res.Reservation.ID
	default:
		if h.resDeps.GAReserve == nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "reservation service unavailable",
			))
			return false
		}
		items := make([]hcheckout.GAHoldItem, 0, len(gaTiers))
		for _, t := range gaTiers {
			items = append(items, hcheckout.GAHoldItem{
				TierID: t.TierID, Quantity: t.Quantity, UnitPrice: pricing.price[t.TierID],
			})
		}
		res, err := h.resDeps.GAReserve(ctx, hcheckout.GAHoldInput{
			OrgID:     cc.orgID,
			ChannelID: cc.channel.ID,
			SessionID: cc.sessionID,
			Items:     items,
			ExpiresAt: expiresAt,
		})
		if err != nil {
			h.writeCartHoldError(w, req, cc, seatByKey, pricing, err)
			return false
		}
		reservationID = res.ID
	}

	var customerID *uuid.UUID
	if cc.gw.CustomerID != uuid.Nil {
		cid := cc.gw.CustomerID
		customerID = &cid
	}
	if err := h.cartDeps.Q.BindReservationToGatewaySession(ctx, reservationID, cc.gw.ID, customerID); err != nil {
		// The hold exists but is orphaned from the cart: report a transient
		// failure so the site retries rather than silently double-holding.
		h.logger.Error("bil24_compat: RESERVATION: cart binding failed",
			slog.String("reservation_id", reservationID.String()),
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("error", err.Error()),
		)
		h.writeCartTransient(w, req, cc)
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// UN_RESERVE / UN_RESERVE_ALL
// ─────────────────────────────────────────────────────────────────────────────

// cartUnReserve removes the named seats — or N units of the named categories —
// from the event session's cart line. ShrinkHold cancels the reservation when
// the line ends up empty (spec §7.4). A session with no cart line at all is not
// an error: the site's view is already the platform's view, so the current cart
// is simply returned.
func (h *Handler) cartUnReserve(ctx context.Context, w http.ResponseWriter, req bil24Request, cc cartCtx) {
	pricing, perr := h.cartSessionPricing(ctx, cc.sessionID)
	if perr != nil {
		h.writeCartTransient(w, req, cc)
		return
	}

	seatKeys, seatByKey, gaTiers, ok := h.cartPayload(ctx, w, req, cc, pricing)
	if !ok {
		return
	}

	existing, err := h.cartDeps.Q.GetActiveGatewayCartReservation(ctx, cc.gw.ID, cc.sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeCartResponse(ctx, w, req, cc)
			return
		}
		h.logger.Error("bil24_compat: RESERVATION: cart lookup failed",
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("error", err.Error()),
		)
		h.writeCartTransient(w, req, cc)
		return
	}

	if _, mErr := h.cartDeps.Shrink(ctx, hcheckout.HoldMutationInput{
		ReservationID: existing.ID,
		SeatKeys:      seatKeys,
		GATiers:       gaTiers,
	}); mErr != nil {
		h.writeCartHoldError(w, req, cc, seatByKey, pricing, mErr)
		return
	}

	h.writeCartResponse(ctx, w, req, cc)
}

// cartUnReserveAll empties the whole cart: every open reservation of the
// gateway session is shrunk by its entire contents, which ShrinkHold turns into
// a 'cancelled' reservation. The response then carries an empty seatList and
// cartTimeout 0 (spec §7.4).
func (h *Handler) cartUnReserveAll(ctx context.Context, w http.ResponseWriter, req bil24Request, cc cartCtx) {
	rows, err := h.cartDeps.Q.ListActiveGatewayCartReservations(ctx, cc.gw.ID)
	if err != nil {
		h.logger.Error("bil24_compat: RESERVATION: cart listing failed",
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("error", err.Error()),
		)
		h.writeCartTransient(w, req, cc)
		return
	}

	for _, res := range rows {
		seatKeys, gaTiers, cErr := h.cartContents(ctx, res)
		if cErr != nil {
			h.writeCartTransient(w, req, cc)
			return
		}
		if len(seatKeys) == 0 && len(gaTiers) == 0 {
			continue
		}
		if _, mErr := h.cartDeps.Shrink(ctx, hcheckout.HoldMutationInput{
			ReservationID: res.ID,
			SeatKeys:      seatKeys,
			GATiers:       gaTiers,
		}); mErr != nil {
			// A concurrently swept or converted hold is already gone from the
			// cart; keep emptying the rest instead of failing the command.
			h.logger.Warn("bil24_compat: RESERVATION: UN_RESERVE_ALL skipped a reservation",
				slog.String("reservation_id", res.ID.String()),
				slog.String("error", mErr.Error()),
			)
		}
	}

	h.writeCartResponse(ctx, w, req, cc)
}

// cartContents reads back everything one reservation holds, in the shape
// ShrinkHold consumes: the seat_keys of its assigned seats and, for GA units
// whose seat rows carry no tier stamp, the per-tier quantities of its
// reservation_ga_items lines.
func (h *Handler) cartContents(ctx context.Context, res gen.ReservationRow) ([]string, []hcheckout.HoldTierQuantity, error) {
	items, err := h.cartDeps.Q.ListReservationGAItems(ctx, res.ID)
	if err != nil {
		h.logger.Error("bil24_compat: RESERVATION: GA line listing failed",
			slog.String("reservation_id", res.ID.String()),
			slog.String("error", err.Error()),
		)
		return nil, nil, err
	}
	if len(items) > 0 {
		tiers := make([]hcheckout.HoldTierQuantity, 0, len(items))
		for _, it := range items {
			tiers = append(tiers, hcheckout.HoldTierQuantity{TierID: it.TierID, Quantity: it.Quantity})
		}
		return nil, tiers, nil
	}

	seats, err := h.cartDeps.Q.ListReservationSeats(ctx, res.ID)
	if err != nil {
		h.logger.Error("bil24_compat: RESERVATION: seat listing failed",
			slog.String("reservation_id", res.ID.String()),
			slog.String("error", err.Error()),
		)
		return nil, nil, err
	}
	keys := make([]string, 0, len(seats))
	for _, s := range seats {
		keys = append(keys, s.SeatKey)
	}
	return keys, nil, nil
}

// cartRefreshAll pushes every open reservation of the session to now+TTL. A
// failure is logged and swallowed — the mutation that just succeeded is more
// important than the sliding window, and the next RESERVE retries it.
func (h *Handler) cartRefreshAll(ctx context.Context, cc cartCtx) {
	rows, err := h.cartDeps.Q.ListActiveGatewayCartReservations(ctx, cc.gw.ID)
	if err != nil || len(rows) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if _, err := h.cartDeps.Refresh(ctx, ids, cc.ttl); err != nil {
		h.logger.Warn("bil24_compat: RESERVATION: cart TTL refresh failed",
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}
