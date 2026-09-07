// cmd_order.go — Bil24-compatible order commands: GET_ORDER_INFO and
// CREATE_ORDER_EXT. Extracted from bil24_compat.go by feature #476 so
// per-command files stay well under 700 lines. CANCEL_ORDER and
// CANCEL_RESERVATION live in cmd_order_cancel.go (feature #496, spec §7.12).
package hbil24

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// errInvalidOrderInfoID marks a GET_ORDER_INFO orderId that is neither a
// positive int64 (system_id) nor a parseable UUID (orders.id or
// checkout_sessions.id) — a protocol error (-2), distinct from a
// well-formed identifier that simply does not resolve to a row (-3).
var errInvalidOrderInfoID = errors.New("hbil24: invalid GET_ORDER_INFO orderId")

// ─────────────────────────────────────────────────────────────────────────────
// GET_ORDER_INFO — get checkout session + tickets (GetTicket)
// ─────────────────────────────────────────────────────────────────────────────

// bil24ErrorWithUserMessage constructs an error response like bil24Error but
// additionally carries a `userMessage` field duplicating the description, per
// spec §7.8: GET_ORDER_INFO error responses must include a user-facing
// message alongside the machine-readable description/resultCode so the
// legacy WordPress plugin can surface it without its own message table.
func bil24ErrorWithUserMessage(command string, code int, description string) bil24Response {
	resp := bil24Error(command, code, description)
	resp.Data = map[string]any{"userMessage": description}
	return resp
}

// handleBil24GetOrderInfo maps GET_ORDER_INFO to the platform checkout session
// and its associated tickets.
//
// Bil24 request fields used:
//   - orderId: platform checkout session UUID
//
// Response (spec §7.8 / §9.3, feature #476 W1-A2b slice 20):
//
//	{
//	  "resultCode": 0,
//	  "command": "GET_ORDER_INFO",
//	  "order": {
//	    "id":              "<uuid|int64>",
//	    "status":          "...",
//	    "sum":             <cents>,
//	    "discount":        <cents>,
//	    "charge":          <cents>,
//	    "totalSum":        <cents>,
//	    "currency":        "USD",
//	    "ticketQuantity": <int>
//	  }
//	}
//
// Note: Bil24's GET_ORDER_INFO historically did not return ticketList.
// For strict compatibility we include ticketQuantity but omit the full list.
// Clients migrated to the new platform can request the full list via
// GET /v1/checkout/{id}/tickets.
//
// Feature #505 (W1-B7b) upgraded the answer: when the order projection is
// wired (WithOrderExport) and the session has issued tickets, the `order`
// object is the FULL spec §9.3 36-key object produced by
// bil24wire.EncodeOrderHeader — the exact object the order.paid webhook
// carries, minus ticketList. The legacy body below is the fallback for an
// unwired handler (unit tests) and for sessions with nothing issued yet.
//
// Deferred to later slices (not yet implemented, intentionally omitted from
// the response so absence is honest rather than fabricated):
//   - `id` as int64 wire form: needs a compatibility_id_map KindOrder
//     migration + compatOrderID helper. Until then the field carries the
//     UUID string via TranslatePlatformID.
//   - `expiration` (RFC3339 hold expiry): needs a reservation join to read
//     reservations.expires_at — checkout_sessions itself carries only a
//     terminal ExpiredAt.
//   - `status` value mapping to Bil24 sentinels (NEW / PAID / CANCELLED /
//     REFUNDED): the wire key is renamed to "status" per spec §9.3 in this
//     slice, but the value is still checkout_sessions.state verbatim.
func (h *Handler) handleBil24GetOrderInfo(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.checkoutQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
			req.Command, ResultCodeInternalError, "order service unavailable",
		))
		return
	}

	// Feature #471 (spec §5, §7.6): auth + org-scope. GET_ORDER_INFO must
	// not leak orders from one org to another via crafted orderId values.
	channel, authed := h.authenticateCommand(r.Context(), w, req)
	if h.requireToken && !authed {
		return
	}

	// Feature #493 (W1-B1c, spec §7.8): once the orders aggregate table
	// (feature #486/#492) is wired, GET_ORDER_INFO must understand every
	// orderId shape CREATE_ORDER_EXT has ever emitted — the bigint
	// system_id, the orders.id UUID, and the legacy checkout_sessions.id
	// UUID — not just the pre-#486 checkout_sessions-only identifier space.
	// Unwired deployments/tests (see regression_158_test.go's
	// buildCompatServer) fall through unchanged to the legacy path below.
	if h.orderDeps.Q != nil {
		h.handleBil24GetOrderInfoFromOrder(w, r, req, channel, authed)
		return
	}

	orderID, err := TranslateLegacyID(req.OrderID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
			req.Command, ResultCodeInvalidRequest,
			"orderId must be a valid order identifier",
		))
		return
	}

	cs, err := h.checkoutQueries.GetCheckoutSessionByID(r.Context(), orderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
				req.Command, ResultCodeNotFound, "order not found",
			))
			return
		}
		h.logger.Error("bil24_compat: GET_ORDER_INFO: fetch checkout session failed",
			slog.String("order_id", orderID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
			req.Command, ResultCodeInternalError, "failed to retrieve order",
		))
		return
	}

	// Cross-tenant guard: once we know both the caller's org (from fid)
	// and the order's org, reject mismatches with a "not found" — the
	// caller must never learn that an order exists in another tenant.
	if authed && cs.OrgID != channel.OrgID {
		h.logger.Warn("bil24_compat: GET_ORDER_INFO: cross-tenant order access rejected",
			slog.String("order_id", orderID.String()),
			slog.String("channel_org", channel.OrgID.String()),
			slog.String("order_org", cs.OrgID.String()),
		)
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
			req.Command, ResultCodeNotFound,
			"order not found in this channel's organization",
		))
		return
	}

	// Feature #505 (W1-B7b, spec §7.8/§9.3): when the order projection is
	// wired and the session actually has issued tickets, answer with the
	// SAME order object the `order.paid` webhook carries, minus ticketList.
	// One encoder, one key set — see internal/platform/bil24wire.
	if order, ok := h.encodeOrderHeaderForWire(r.Context(), cs, channel); ok {
		writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
			"order": order,
		}))
		return
	}

	// Get ticket count if ticketQueries is available.
	ticketQuantity := 0
	if h.ticketQueries != nil {
		tickets, err := h.ticketQueries.ListTicketsByCheckoutSession(r.Context(), orderID)
		if err == nil {
			ticketQuantity = len(tickets)
		}
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"order": buildGetOrderInfoBody(cs, ticketQuantity),
	}))
}

// buildGetOrderInfoBody projects a checkout_sessions row + ticket count into
// the spec §7.8 / §9.3 `order` object body (feature #476 W1-A2b slice 20).
// Pure over the row — no DB round-trip — so the wire-shape contract can be
// unit-tested without a live pool. Kept as a package-level helper (rather
// than a *Handler method) because it is purely a value projection: it takes
// no ctx, no queries, no logger.
//
// Bil24 semantics for the financial keys (unchanged from the pre-slice
// projection):
//   - sum = checkout_sessions.subtotal (0 when nil)
//   - discount = checkout_sessions.discount (0 when nil)
//   - charge = checkout_sessions.platform_fee + checkout_sessions.provider_fee
//     (each 0 when nil)
//   - totalSum = checkout_sessions.total (0 when nil)
//   - currency omitted when nil (pre-pricing_confirmed sessions)
//
// Spec §9.3 key renames landed by this slice (§7.8 wire shape):
//   - `orderId` → `id`
//   - `state` → `status`
//   - `ticketCount` → `ticketQuantity`
//
// The outer envelope key changes from `orderInfo` to `order` at the callsite.
func buildGetOrderInfoBody(cs gen.CheckoutSessionRow, ticketQuantity int) map[string]any {
	var (
		sum      int64
		discount int64
		charge   int64
		totalSum int64
		currency string
	)
	if cs.Subtotal != nil {
		sum = *cs.Subtotal
	}
	if cs.Discount != nil {
		discount = *cs.Discount
	}
	if cs.PlatformFee != nil {
		charge += *cs.PlatformFee
	}
	if cs.ProviderFee != nil {
		charge += *cs.ProviderFee
	}
	if cs.Total != nil {
		totalSum = *cs.Total
	}
	if cs.Currency != nil {
		currency = *cs.Currency
	}

	order := map[string]any{
		// Spec §9.3: `id` is the order identifier. Wave-1 wire form is
		// int64 (compatibility_id_map KindOrder) but that migration is
		// deferred to a later slice; today we emit the UUID string via
		// TranslatePlatformID for continuity with pre-slice callers.
		"id":             TranslatePlatformID(cs.ID),
		"status":         cs.State,
		"sum":            sum,
		"discount":       discount,
		"charge":         charge,
		"totalSum":       totalSum,
		"ticketQuantity": ticketQuantity,
	}
	if currency != "" {
		order["currency"] = currency
	}
	return order
}

// handleBil24GetOrderInfoFromOrder answers GET_ORDER_INFO from the orders
// aggregate table (feature #486/#492) once orderDeps is wired. It resolves
// the orderId in any of the three shapes the gateway has ever emitted
// (system_id, orders.id, checkout_sessions.id — see resolveOrderInfoOrder),
// re-derives the linked checkout session to reuse the unchanged
// encodeOrderHeaderForWire path (feature #505) when tickets have been
// issued, and otherwise falls back to a pure orders-row projection.
func (h *Handler) handleBil24GetOrderInfoFromOrder(
	w http.ResponseWriter, r *http.Request, req bil24Request,
	channel gen.SalesChannelRow, authed bool,
) {
	order, err := h.resolveOrderInfoOrder(r.Context(), req.OrderID, channel, authed)
	if err != nil {
		switch {
		case errors.Is(err, errInvalidOrderInfoID):
			writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
				req.Command, ResultCodeInvalidRequest,
				"orderId must be a valid order identifier",
			))
		case errors.Is(err, pgx.ErrNoRows):
			writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
				req.Command, ResultCodeNotFound, "order not found",
			))
		default:
			h.logger.Error("bil24_compat: GET_ORDER_INFO: fetch order failed",
				slog.String("order_id", req.OrderID),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
				req.Command, ResultCodeInternalError, "failed to retrieve order",
			))
		}
		return
	}

	// Defense-in-depth: the checkout_session_id fallback inside
	// resolveOrderInfoOrder is unscoped, so re-check the org here too,
	// exactly mirroring the pre-#493 cross-tenant guard below.
	if authed && order.OrgID != channel.OrgID {
		h.logger.Warn("bil24_compat: GET_ORDER_INFO: cross-tenant order access rejected",
			slog.String("order_id", order.ID.String()),
			slog.String("channel_org", channel.OrgID.String()),
			slog.String("order_org", order.OrgID.String()),
		)
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(
			req.Command, ResultCodeNotFound,
			"order not found in this channel's organization",
		))
		return
	}

	ticketQuantity := 0
	if cs, cerr := h.checkoutQueries.GetCheckoutSessionByID(r.Context(), order.CheckoutSessionID); cerr == nil {
		if wireOrder, ok := h.encodeOrderHeaderForWire(r.Context(), cs, channel); ok {
			writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
				"order": wireOrder,
			}))
			return
		}
		if h.ticketQueries != nil {
			if tickets, terr := h.ticketQueries.ListTicketsByCheckoutSession(r.Context(), order.CheckoutSessionID); terr == nil {
				ticketQuantity = len(tickets)
			}
		}
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"order": buildGetOrderInfoBodyFromOrder(order, ticketQuantity),
	}))
}

// resolveOrderInfoOrder mirrors payResolveOrder (cmd_order_pay.go, feature
// #494): the gateway has, over its lifetime, emitted an orderId as a bigint
// system_id, an orders.id UUID and (pre-#486) a checkout_sessions.id UUID,
// and a caller echoing whatever we gave it must not be punished for our own
// transitional format. Unlike PAY_ORDER, GET_ORDER_INFO may be called
// unauthenticated (requireToken=false dev mode), so org-scoping is applied
// only when authed — the caller's org is not yet known otherwise.
func (h *Handler) resolveOrderInfoOrder(
	ctx context.Context, raw string, channel gen.SalesChannelRow, authed bool,
) (gen.OrderRow, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return gen.OrderRow{}, errInvalidOrderInfoID
	}

	if sysID, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return h.orderDeps.Q.GetOrderBySystemID(ctx, sysID)
	}

	id, err := uuid.Parse(raw)
	if err != nil {
		return gen.OrderRow{}, errInvalidOrderInfoID
	}

	if authed {
		order, oerr := h.orderDeps.Q.GetOrderByID(ctx, id, channel.OrgID)
		if oerr == nil {
			return order, nil
		}
		if !errors.Is(oerr, pgx.ErrNoRows) {
			return gen.OrderRow{}, oerr
		}
	}
	// Older gateway answers, and the unauthenticated (dev-mode) path, exposed
	// the checkout session id as orderId. The result is org-checked by the
	// caller (handleBil24GetOrderInfoFromOrder) once authed is known.
	return h.orderDeps.Q.GetOrderByCheckoutSession(ctx, id)
}

// buildGetOrderInfoBodyFromOrder projects an orders-table row + ticket count
// into the spec §7.8 / §9.3 `order` object body (feature #493, W1-B1c). All
// financial/currency columns on orders are NOT NULL, unlike the pre-#486
// checkout_sessions projection in buildGetOrderInfoBody, so no nil guards
// are needed here.
func buildGetOrderInfoBodyFromOrder(order gen.OrderRow, ticketQuantity int) map[string]any {
	return map[string]any{
		"id":             TranslatePlatformID(order.ID),
		"status":         order.Status,
		"sum":            order.Subtotal,
		"discount":       order.Discount,
		"charge":         order.Charge,
		"totalSum":       order.Total,
		"currency":       order.Currency,
		"ticketQuantity": ticketQuantity,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CREATE_ORDER_EXT — create a checkout session (CreateOrder) — scaffold stub
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24CreateOrderExt maps CREATE_ORDER_EXT to checkout session creation.
//
// Bil24 request fields used:
//   - actionEventId:   platform session UUID
//   - categoryPriceId: platform tier UUID
//   - quantity:        number of tickets (default 1)
//   - email:           buyer email
//
// CREATE_ORDER_EXT is NOT IMPLEMENTED in this gateway version. The command
// is recognized (not "unknown") but returns resultCode=-5 (NOT_IMPLEMENTED)
// so legacy clients get a machine-readable signal that the operation is
// unavailable. Full checkout creation requires a reservation + payment flow
// that is not exposed via the Bil24 compatibility gateway; callers MUST
// migrate to POST /v1/checkout/reservations + /v1/checkout/{id}/confirm.
//
// Returning resultCode=0 (success) from an unimplemented stub is a security
// risk because it allows the caller to believe an order was created when in
// fact no inventory hold, no payment intent, and no checkout session were
// created. This was fixed in feature #374.
//
// Feature #492 (W1-B1b) supersedes the stub: when the order dependencies are
// wired (pool + session querier + pool-bound queries), the command is served
// for real by handleBil24CreateOrderExtSession per spec §7.7. The -5 path below
// survives for deployments/tests that construct a Handler without those deps —
// answering 0 from an unwired handler would be the exact security hole #374
// closed.
//
// Response (unwired): { "resultCode": -5, "command": "CREATE_ORDER_EXT", ... }
func (h *Handler) handleBil24CreateOrderExt(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.orderDeps.wired() {
		h.handleBil24CreateOrderExtSession(w, r, req)
		return
	}
	if req.ActionEventID == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId is required",
		))
		return
	}
	if req.CategoryPriceID == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"categoryPriceId is required",
		))
		return
	}
	// Spec §4 / §7.3: Bil24 wire is int64-only for actionEventId and
	// categoryPriceId. Reject UUID strings with -2 so callers cannot slip a
	// platform UUID onto a legacy endpoint. Feature #476 W1-A2b.
	if _, err := bil24compat.ParseLegacyIntID(req.ActionEventID); err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a positive int64 session identifier",
		))
		return
	}
	if _, err := bil24compat.ParseLegacyIntID(req.CategoryPriceID); err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"categoryPriceId must be a positive int64 tier identifier",
		))
		return
	}

	h.logger.Warn("bil24_compat: CREATE_ORDER_EXT is not implemented; returning NOT_IMPLEMENTED",
		slog.String("session_id", req.ActionEventID),
		slog.String("tier_id", req.CategoryPriceID),
		slog.String("fid", req.FID),
	)

	// NOT_IMPLEMENTED: never return resultCode=0 from an unimplemented stub.
	// Real implementation: POST /v1/checkout/reservations → POST /v1/checkout/{id}/confirm.
	writeBil24JSON(w, http.StatusOK, bil24Error(
		req.Command, ResultCodeNotImplemented,
		"CREATE_ORDER_EXT is not implemented; use POST /v1/checkout/reservations to create a reservation",
	))
}
