// cmd_cart_view.go — the read side of the spec §7.4 session cart (feature
// #484): request-payload translation, tier pricing lookups, the whole-cart
// response projection and the localized error envelopes. The mutating
// dispatcher lives in cmd_cart_session.go; the two files are split so neither
// exceeds the 700-line per-file rule of feature #476.
//
// Every one of the four RESERVATION shapes answers with the SAME projection:
//
//	{ "resultCode": 0, "description": "OK", "command": "RESERVATION",
//	  "cartTimeout": <int seconds>, "currency": "CZK",
//	  "sum": …, "discount": …, "charge": …, "totalSum": …,
//	  "seatList": [ {seatId, actionEventId, categoryPriceId,
//	                 tariffPlanId, price, discount}, … ] }
//
// The seatList covers the whole cart across every action event, and a general
// admission unit appears as its own row carrying the system_seat_id of the
// AB-51 materialised seat, because the WordPress plugin counts tickets by
// counting rows.
package hbil24

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// ─────────────────────────────────────────────────────────────────────────────
// Pricing snapshot
// ─────────────────────────────────────────────────────────────────────────────

// cartPricing is the per-event-session tier snapshot the cart needs: the AB-48
// effective unit price, the display name (for the sold-out message), the
// pricing mode (pwyw is refused by the gateway) and the session currency.
type cartPricing struct {
	price    map[uuid.UUID]int64
	name     map[uuid.UUID]string
	mode     map[uuid.UUID]string
	currency string
}

// newCartPricing returns an empty, non-nil snapshot.
func newCartPricing() cartPricing {
	return cartPricing{
		price: map[uuid.UUID]int64{},
		name:  map[uuid.UUID]string{},
		mode:  map[uuid.UUID]string{},
	}
}

// cartSessionPricing loads the tier snapshot of one event session and resolves
// scheduled prices through the ONE AB-48 resolver. A missing tier surface (unit
// tests that omit it) yields an empty snapshot rather than an error; a failing
// price-window lookup degrades to base prices, matching GET_SEAT_LIST.
func (h *Handler) cartSessionPricing(ctx context.Context, sessionID uuid.UUID) (cartPricing, error) {
	p := newCartPricing()
	if h.resDeps.TierQ == nil || sessionID == uuid.Nil {
		return p, nil
	}
	tiers, err := h.resDeps.TierQ.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("bil24_compat: RESERVATION: tier snapshot failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		return p, err
	}
	eff, effErr := priceresolve.ForTiers(ctx, h.resDeps.TierQ, tiers, time.Now().UTC())
	if effErr != nil {
		h.logger.Warn("bil24_compat: RESERVATION: price window lookup failed; using base prices",
			slog.String("error", effErr.Error()))
		eff = nil
	}
	for _, t := range tiers {
		amount := t.PriceAmount
		if e, ok := eff[t.ID]; ok {
			amount = e.Amount
		}
		if t.PricingMode == "free" {
			amount = 0
		}
		p.price[t.ID] = amount
		p.name[t.ID] = t.Name
		p.mode[t.ID] = t.PricingMode
		if p.currency == "" {
			p.currency = t.Currency
		}
	}
	return p, nil
}

// cartCurrency reports the currency the cart is already denominated in, or ""
// when the cart is empty. Spec §7.4 allows exactly one currency per cart; the
// caller compares this against the currency of the session being added.
func (h *Handler) cartCurrency(ctx context.Context, cc cartCtx) string {
	rows, err := h.cartDeps.Q.ListActiveGatewayCartReservations(ctx, cc.gw.ID)
	if err != nil {
		return ""
	}
	for _, res := range rows {
		if items, iErr := h.cartDeps.Q.ListReservationGAItems(ctx, res.ID); iErr == nil {
			for _, it := range items {
				if it.Currency != "" {
					return it.Currency
				}
			}
		}
		if p, pErr := h.cartSessionPricing(ctx, res.SessionID); pErr == nil && p.currency != "" {
			return p.currency
		}
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Request payload translation
// ─────────────────────────────────────────────────────────────────────────────

// cartPayload turns the request's seatList / categoryList into the two shapes
// the hold-mutation primitives consume: canonical seat_keys (plus the resolved
// rows, so a later seat conflict can be described as sector/row/number) and
// per-tier quantities. Returns ok=false after writing a Bil24 error envelope.
func (h *Handler) cartPayload(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	pricing cartPricing,
) ([]string, map[string]gen.SessionSeatRow, []hcheckout.HoldTierQuantity, bool) {
	if len(req.SeatList) > 0 {
		keys, byKey, ok := h.cartSeatPayload(ctx, w, req, cc)
		return keys, byKey, nil, ok
	}
	tiers, ok := h.cartCategoryPayload(ctx, w, req, cc, pricing)
	return nil, nil, tiers, ok
}

// cartSeatPayload resolves the seatList wire identifiers (spec §4:
// system_seat_id as an int64 literal) into seat_keys within the target session.
func (h *Handler) cartSeatPayload(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
) ([]string, map[string]gen.SessionSeatRow, bool) {
	seen := make(map[string]struct{}, len(req.SeatList))
	keys := make([]string, 0, len(req.SeatList))
	byKey := make(map[string]gen.SessionSeatRow, len(req.SeatList))

	for _, raw := range req.SeatList {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				"seatList entries must be non-empty seat identifiers",
			))
			return nil, nil, false
		}
		if _, dup := seen[raw]; dup {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("seatList contains duplicate seat %q", raw),
			))
			return nil, nil, false
		}
		seen[raw] = struct{}{}

		seat, err := h.resolveSeatToRow(ctx, raw, cc.sessionID)
		if err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				h.writeCartNotFound(w, req, cc)
			case errors.Is(err, ErrSeatIDInvalid):
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeInvalidRequest,
					fmt.Sprintf("seatList entry %q is not a valid seat identifier", raw),
				))
			default:
				h.logger.Error("bil24_compat: RESERVATION: seat lookup failed",
					slog.String("seat_raw", raw),
					slog.String("error", err.Error()),
				)
				h.writeCartTransient(w, req, cc)
			}
			return nil, nil, false
		}
		keys = append(keys, seat.SeatKey)
		byKey[seat.SeatKey] = seat
	}
	return keys, byKey, true
}

// cartCategoryPayload validates and aggregates the categoryList into per-tier
// quantities. Unknown tiers are -3, non-positive quantities -2, and a pwyw tier
// is refused with the user-visible 101 the spec mandates (the legacy wire has
// no chosen-price field).
func (h *Handler) cartCategoryPayload(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	pricing cartPricing,
) ([]hcheckout.HoldTierQuantity, bool) {
	order := make([]uuid.UUID, 0, len(req.CategoryList))
	qty := make(map[uuid.UUID]int32, len(req.CategoryList))

	for i, c := range req.CategoryList {
		if strings.TrimSpace(c.CategoryPriceID) == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].categoryPriceId is required", i),
			))
			return nil, false
		}
		tierID, err := h.resolveCategoryPriceID(ctx, c.CategoryPriceID)
		if err != nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].categoryPriceId must be a valid tier identifier", i),
			))
			return nil, false
		}
		if c.Quantity <= 0 {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].quantity must be >= 1", i),
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
			// The tier snapshot was loaded and does not carry this tier: the
			// categoryPriceId does not belong to the addressed action event.
			h.writeCartNotFound(w, req, cc)
			return nil, false
		}
		if _, exists := qty[tierID]; !exists {
			order = append(order, tierID)
		}
		qty[tierID] += int32(c.Quantity) //nolint:gosec // validated > 0 above
	}

	tiers := make([]hcheckout.HoldTierQuantity, 0, len(order))
	for _, id := range order {
		tiers = append(tiers, hcheckout.HoldTierQuantity{TierID: id, Quantity: qty[id]})
	}
	return tiers, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Whole-cart response projection
// ─────────────────────────────────────────────────────────────────────────────

// writeCartResponse emits the spec §7.4 success envelope describing the WHOLE
// cart: one seatList row per held ticket across every action event, the money
// fields, and cartTimeout — whole seconds until the nearest reservation expiry,
// 0 when the cart is empty.
func (h *Handler) writeCartResponse(ctx context.Context, w http.ResponseWriter, req bil24Request, cc cartCtx) {
	rows, err := h.cartDeps.Q.ListActiveGatewayCartReservations(ctx, cc.gw.ID)
	if err != nil {
		h.logger.Error("bil24_compat: RESERVATION: cart listing failed",
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("error", err.Error()),
		)
		h.writeCartTransient(w, req, cc)
		return
	}

	var (
		seatList  = make([]map[string]any, 0, 8)
		sum       int64
		currency  string
		nearest   time.Time
		pricingBy = map[uuid.UUID]cartPricing{}
	)

	for _, res := range rows {
		pricing, cached := pricingBy[res.SessionID]
		if !cached {
			p, pErr := h.cartSessionPricing(ctx, res.SessionID)
			if pErr != nil {
				h.writeCartTransient(w, req, cc)
				return
			}
			pricing = p
			pricingBy[res.SessionID] = p
		}
		if currency == "" {
			currency = pricing.currency
		}

		// AB-48 locked snapshot: GA lines carry the unit price that was in
		// force when the hold was taken, and the tier order that lets a GA
		// seat row without a tier stamp be attributed to its tier.
		items, iErr := h.cartDeps.Q.ListReservationGAItems(ctx, res.ID)
		if iErr != nil {
			h.logger.Error("bil24_compat: RESERVATION: GA line listing failed",
				slog.String("reservation_id", res.ID.String()),
				slog.String("error", iErr.Error()),
			)
			h.writeCartTransient(w, req, cc)
			return
		}
		locked := make(map[uuid.UUID]int64, len(items))
		queue := make([]uuid.UUID, 0, 8)
		for _, it := range items {
			locked[it.TierID] = it.UnitPrice
			if currency == "" {
				currency = it.Currency
			}
			for n := int32(0); n < it.Quantity; n++ {
				queue = append(queue, it.TierID)
			}
		}

		seats, sErr := h.cartDeps.Q.ListReservationSeats(ctx, res.ID)
		if sErr != nil {
			h.logger.Error("bil24_compat: RESERVATION: seat listing failed",
				slog.String("reservation_id", res.ID.String()),
				slog.String("error", sErr.Error()),
			)
			h.writeCartTransient(w, req, cc)
			return
		}

		actionEventID := h.compatActionEventID(ctx, res.SessionID)
		for _, s := range seats {
			var tierID uuid.UUID
			switch {
			case s.TierID != nil:
				tierID = *s.TierID
			case len(queue) > 0:
				tierID = queue[0]
				queue = queue[1:]
			}

			price := int64(0)
			if p, ok := locked[tierID]; ok {
				price = p
			} else if p, ok := pricing.price[tierID]; ok {
				price = p
			}
			sum += price

			var categoryPriceID any
			if tierID != uuid.Nil {
				categoryPriceID = h.compatCategoryPriceID(ctx, tierID)
			}
			seatList = append(seatList, map[string]any{
				"seatId":          s.SystemSeatID,
				"actionEventId":   actionEventID,
				"categoryPriceId": categoryPriceID,
				"tariffPlanId":    nil,
				"price":           price,
				"discount":        0,
			})
		}

		if nearest.IsZero() || res.ExpiresAt.Before(nearest) {
			nearest = res.ExpiresAt
		}
	}

	cartTimeout := int64(0)
	if !nearest.IsZero() && len(seatList) > 0 {
		cartTimeout = cartTimeoutSeconds(nearest)
	}

	// Spec §7.4: charge is the channel service fee applied to the net sum;
	// totalSum = sum - discount + charge. fee_percent is numeric(5,2), so the
	// arithmetic is done in float64 and marshals without a fractional part
	// whenever the result is integral (the seed's 0.00 case).
	var discount int64
	charge := float64(sum-discount) * cartFeePercent(cc.channel) / 100
	totalSum := float64(sum-discount) + charge

	extra := map[string]any{
		"cartTimeout": cartTimeout,
		"currency":    currency,
		"sum":         sum,
		"discount":    discount,
		"charge":      charge,
		"totalSum":    totalSum,
		"seatList":    seatList,
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, extra))
}

// cartFeePercent parses sales_channels.fee_percent (numeric(5,2), scanned as a
// string). An unparsable or absent value means "no service charge".
func cartFeePercent(channel gen.SalesChannelRow) float64 {
	pct, err := strconv.ParseFloat(strings.TrimSpace(channel.FeePercent), 64)
	if err != nil || pct <= 0 {
		return 0
	}
	return pct
}

// ─────────────────────────────────────────────────────────────────────────────
// Error envelopes
// ─────────────────────────────────────────────────────────────────────────────

// writeCartHoldError translates the typed hcheckout hold errors into the
// localized Bil24 envelopes spec §7.4 prescribes. Unlike the pre-#484
// writeHoldError, seat conflicts and sold-out categories are *user visible*
// (101) with a human-readable, translated description, because the WordPress
// plugin renders the description verbatim in the basket.
func (h *Handler) writeCartHoldError(
	w http.ResponseWriter,
	req bil24Request,
	cc cartCtx,
	seatByKey map[string]gen.SessionSeatRow,
	pricing cartPricing,
	err error,
) {
	var (
		conflicts  *hcheckout.SeatConflictsError
		capErr     *hcheckout.CapacityError
		notMutable *hcheckout.NotMutableError
	)
	switch {
	case errors.As(err, &conflicts):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, cc.locale, "bil24.seat_taken",
				"Seat is already taken", cartSeatParams(conflicts, seatByKey)),
		))
	case errors.As(err, &capErr):
		params := map[string]any{"name": "", "available": 0}
		if capErr.TierID != nil {
			params["name"] = pricing.name[*capErr.TierID]
		}
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, cc.locale, "bil24.category_sold_out",
				"Category is sold out", params),
		))
	case errors.Is(err, hcheckout.ErrHoldPricingModeUnsupported):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, cc.locale, "bil24.pricing_mode_unsupported",
				"pricing mode is not supported by this gateway", nil),
		))
	case errors.As(err, &notMutable):
		// The hold expired or was swept between two commands: the site must
		// re-reserve, which is exactly what bil24.hold_expired tells the buyer.
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, cc.locale, "bil24.hold_expired",
				"your hold has expired, please reserve again", nil),
		))
	case errors.Is(err, hcheckout.ErrHoldNotFound),
		errors.Is(err, hcheckout.ErrHoldSessionNotFound):
		h.writeCartNotFound(w, req, cc)
	case errors.Is(err, hcheckout.ErrHoldInvalidInput):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "invalid reservation payload",
		))
	case errors.Is(err, hcheckout.ErrHoldSeatsNotSupported):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"seatList is not supported on general_admission sessions; use categoryList",
		))
	case errors.Is(err, hcheckout.ErrHoldQuantityNotSupported):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"categoryList is not supported on assigned_seats sessions; use seatList",
		))
	default:
		h.logger.Error("bil24_compat: RESERVATION: cart mutation failed",
			slog.String("gateway_session_id", cc.gw.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to update reservation",
		))
	}
}

// cartSeatParams describes the FIRST conflicting seat as the sector / row /
// number triple the bil24.seat_taken message interpolates. The resolved row of
// the request is preferred; a conflict on a seat the request never named (the
// mutation primitives may report cart-side seats) falls back to splitting the
// canonical seat_key, whose shape is "<sector>-<row>-<number>".
func cartSeatParams(conflicts *hcheckout.SeatConflictsError, seatByKey map[string]gen.SessionSeatRow) map[string]any {
	params := map[string]any{"sector": "", "row": "", "number": ""}
	for _, c := range conflicts.Conflicts {
		key := c["seat_key"]
		if key == "" {
			continue
		}
		if seat, ok := seatByKey[key]; ok {
			params["sector"] = seat.SectorName
			params["row"] = seat.RowName
			params["number"] = seat.SeatNumber
			return params
		}
		if parts := strings.Split(key, "-"); len(parts) >= 3 {
			params["sector"] = strings.Join(parts[:len(parts)-2], "-")
			params["row"] = parts[len(parts)-2]
			params["number"] = parts[len(parts)-1]
			return params
		}
		params["sector"] = key
		return params
	}
	return params
}

// writeCartNotFound is the spec §7.4 "session / seat / category is not in this
// channel's scope" answer: resultCode -3 with a localized description.
func (h *Handler) writeCartNotFound(w http.ResponseWriter, req bil24Request, cc cartCtx) {
	writeBil24JSON(w, http.StatusOK, bil24Error(
		req.Command, ResultCodeNotFound,
		h.localizeDesc(req.Locale, cc.locale, "bil24.not_found",
			"resource not found in this channel", nil),
	))
}

// writeCartTransient reports a retryable infrastructure failure. Distinct from
// -99 so the plugin retries instead of surfacing a hard error to the buyer.
func (h *Handler) writeCartTransient(w http.ResponseWriter, req bil24Request, cc cartCtx) {
	_ = cc
	writeBil24JSON(w, http.StatusOK, bil24Error(
		req.Command, ResultCodeTransient, "temporary failure, please retry",
	))
}
