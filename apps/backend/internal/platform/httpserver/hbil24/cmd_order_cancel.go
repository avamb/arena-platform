// cmd_order_cancel.go — CANCEL_RESERVATION and CANCEL_ORDER (spec §7.12,
// feature #496, W1-B2c).
//
// Both commands release the hold of an UNPAID order and mark it cancelled;
// they are the buyer-abandons-checkout / operator-clears-the-cart path, not
// the refund path. A paid (or already refunded) order cannot be unwound this
// way — the site must call REFUND_TICKET (§7.13) instead — so that case
// answers 101 with the `bil24.use_refund_ticket` description. An id that
// does not resolve to any order is answered as plain success (resultCode 0):
// the legacy WordPress plugin never inspects the code on this command
// (class-bil24-orders.php:1278-1282), it just wants the HTTP round trip to
// finish so it can drop its local cart state.
//
// The two commands share one implementation: CANCEL_RESERVATION additionally
// accepts a `reservationId` (the id RESERVATION/GET_CART hand back) as an
// alternative to `orderId`; CANCEL_ORDER only ever takes `orderId`.
package hbil24

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// handleBil24CancelReservation implements CANCEL_RESERVATION (spec §7.12).
//
// Bil24 request fields used:
//   - reservationId : platform reservation id, as returned by RESERVATION
//   - orderId       : accepted as a fallback identifier, same shapes as
//     CANCEL_ORDER / PAY_ORDER
//
// Result codes:
//
//	 0 — the hold was released (or the id did not resolve to any order —
//	     spec §7.12 says the site does not check the code here)
//	-2 — neither reservationId nor orderId was supplied
//	101 — the order is already paid (or refunded); use REFUND_TICKET instead
//	-99 — the order surface is not wired on this deployment
func (h *Handler) handleBil24CancelReservation(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.ReservationID) == "" && strings.TrimSpace(req.OrderID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "reservationId or orderId is required",
		))
		return
	}
	if !h.orderDeps.wired() {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotImplemented,
			"CANCEL_RESERVATION is not available on this deployment",
		))
		return
	}
	h.handleBil24CancelWired(w, r, req, true)
}

// handleBil24CancelOrder implements CANCEL_ORDER (spec §7.12).
//
// Bil24 request fields used:
//   - orderId : bigint system_id, orders.id, or (legacy) checkout_sessions.id
//
// Result codes: same contract as CANCEL_RESERVATION, keyed on orderId only.
func (h *Handler) handleBil24CancelOrder(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.OrderID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "orderId is required",
		))
		return
	}
	if !h.orderDeps.wired() {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotImplemented,
			"CANCEL_ORDER is not available on this deployment",
		))
		return
	}
	h.handleBil24CancelWired(w, r, req, false)
}

// handleBil24CancelWired resolves the order (org-scoped to the caller's
// channel when authenticated) and cancels it via ordering.Cancel, then
// best-effort releases the reservation hold — mirroring horders.HandleCancel
// (the admin-facing equivalent), minus the gateway's own auth/localization
// wrapping.
func (h *Handler) handleBil24CancelWired(w http.ResponseWriter, r *http.Request, req bil24Request, allowReservationID bool) {
	ctx := r.Context()

	channel, authed := h.authenticateCommand(ctx, w, req)
	if h.requireToken && !authed {
		return // envelope already written
	}
	locale := gwDefaultLocale(channel)

	order, err := resolveCancelOrderRef(ctx, h.orderDeps.Q, req, channel.OrgID, allowReservationID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Spec §7.12: an id that does not resolve is still a success — the
		// legacy WP plugin never inspects the code on this command.
		writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, nil))
		return
	case err != nil:
		h.logger.Error("bil24_compat: "+req.Command+": order lookup failed",
			slog.String("fid", req.FID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeTransient, "temporary failure, please retry",
		))
		return
	}

	updated, err := ordering.Cancel(ctx, h.orderDeps.Q, ordering.CancelInput{
		OrderID: order.ID,
		OrgID:   order.OrgID,
		Actor:   orderActor(req),
		Reason:  req.Command + " via gateway fid=" + req.FID,
	})
	if err != nil {
		if errors.Is(err, ordering.ErrInvalidTransition) {
			// Already paid/refunded/manual_review: unwinding money is the
			// refund path's job, not this one's (spec §7.12).
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeUserVisible,
				h.localizeDesc(req.Locale, locale, "bil24.use_refund_ticket",
					"order has already been paid; use the refund flow instead", nil),
			))
			return
		}
		h.logger.Error("bil24_compat: "+req.Command+": cancel failed",
			slog.String("order_id", order.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeTransient, "temporary failure, please retry",
		))
		return
	}

	// A hold already released by a racing expiry/cancel is not an error: the
	// order transition above already succeeded and is what the caller asked
	// for. Only an unexpected failure gets logged.
	if _, relErr := hcheckout.ReleaseHold(ctx, h.orderDeps.Pool, h.orderDeps.Q, updated.ReservationID); relErr != nil {
		var notReleasable *hcheckout.NotReleasableError
		if !errors.As(relErr, &notReleasable) && !errors.Is(relErr, hcheckout.ErrHoldNotFound) {
			h.logger.Error("bil24_compat: "+req.Command+": release hold failed",
				slog.String("order_id", order.ID.String()),
				slog.String("error", relErr.Error()),
			)
		}
	}

	h.logger.Info("bil24_compat: "+req.Command+": order cancelled",
		slog.String("order_id", updated.ID.String()),
		slog.Int64("order_system_id", updated.SystemID),
		slog.String("fid", req.FID),
	)

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, nil))
}

// cancelOrderQuerier is the read surface resolveCancelOrderRef needs on top
// of orderRefQuerier: CANCEL_RESERVATION additionally resolves by
// reservation id.
type cancelOrderQuerier interface {
	orderRefQuerier
	GetOrderByReservationID(ctx context.Context, reservationID uuid.UUID) (gen.OrderRow, error)
}

// resolveCancelOrderRef resolves the order CANCEL_RESERVATION / CANCEL_ORDER
// should act on. reservationId (when allowed and present) takes priority
// over orderId, matching the command's own field order in spec §7.12. A
// value that fails to parse is treated the same as one that parses but does
// not resolve — both are "unknown id" (pgx.ErrNoRows), never a -2, because
// the spec's "unknown -> 0" contract does not distinguish the two.
func resolveCancelOrderRef(
	ctx context.Context,
	q cancelOrderQuerier,
	req bil24Request,
	orgID uuid.UUID,
	allowReservationID bool,
) (gen.OrderRow, error) {
	if allowReservationID {
		if raw := strings.TrimSpace(req.ReservationID); raw != "" {
			resID, err := TranslateLegacyID(raw)
			if err != nil {
				return gen.OrderRow{}, pgx.ErrNoRows
			}
			order, err := q.GetOrderByReservationID(ctx, resID)
			if err != nil {
				return gen.OrderRow{}, err
			}
			return payScopeOrder(order, orgID)
		}
	}
	if raw := strings.TrimSpace(req.OrderID); raw != "" {
		return resolveOrderRef(ctx, q, raw, orgID)
	}
	return gen.OrderRow{}, pgx.ErrNoRows
}
