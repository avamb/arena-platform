package hexport

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
)

// redemptionColumns are the columns of a promo code's usage file.
var redemptionColumns = []csvexport.Column{
	csvexport.ColOrder, csvexport.ColOrderStatus, csvexport.ColDate,
	csvexport.ColBuyer, csvexport.ColEmail, csvexport.ColDiscount, csvexport.ColCurrency,
}

// HandlePromoRedemptions serves
// GET /v1/organizations/{org_id}/promo-codes/{promo_code_id}/redemptions.csv —
// one row per use of the code: the order, its date, the buyer, the discount.
func (h *Handler) HandlePromoRedemptions(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	promoID, ok := uuidParam(w, r, "promo_code_id")
	if !ok {
		return
	}
	ctx := r.Context()
	promo, err := h.queries.GetPromoCodeByID(ctx, promoID, orgID)
	if isNoRows(err) {
		notFound(w, r, "promo.not_found", "promo code not found")
		return
	}
	if err != nil {
		h.internal(w, r, "promo", err)
		return
	}
	total, err := h.queries.CountExportPromoRedemptions(ctx, promoID)
	if err != nil {
		h.internal(w, r, "count", err)
		return
	}
	if h.tooManyRows(w, r, total) {
		return
	}
	locale := csvexport.LocaleFromRequest(r)

	s := begin(w, filename("promo", promo.Code, time.Now(), time.UTC))
	if err := s.csv.WriteHeader(csvexport.Header(locale, redemptionColumns...)); err != nil {
		h.abort(ctx, "header", err)
	}
	after := uuid.Nil
	for {
		rows, err := h.queries.ListExportPromoRedemptions(ctx, promoID, after, batchSize)
		if err != nil {
			h.abort(ctx, "rows", err)
		}
		for _, row := range rows {
			if err := s.csv.WriteRow(redemptionCells(row)...); err != nil {
				h.abort(ctx, "write", err)
			}
			after = row.ID
		}
		if err := s.flush(); err != nil {
			h.abort(ctx, "flush", err)
		}
		if len(rows) < int(batchSize) {
			return
		}
	}
}

// redemptionCells renders one use of the code. The date is the redemption's
// own instant in the venue's zone of the order's session.
func redemptionCells(row gen.ExportPromoRedemptionRow) []csvexport.Cell {
	loc := venueLocation(row.VenueTimezone)
	var order csvexport.Cell
	if row.OrderNumber != nil {
		order = csvexport.Number(*row.OrderNumber)
	}
	return []csvexport.Cell{
		order,
		csvexport.TextPtr(row.OrderStatus),
		csvexport.DateTime(row.RedeemedAt, loc),
		csvexport.TextPtr(row.BuyerName),
		csvexport.TextPtr(row.BuyerEmail),
		csvexport.Money(row.DiscountAmount),
		csvexport.TextPtr(row.Currency),
	}
}
