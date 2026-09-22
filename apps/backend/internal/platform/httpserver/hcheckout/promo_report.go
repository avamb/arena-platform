// promo_report.go — the organizer's promo usage report:
//
//	GET /v1/organizations/{org_id}/promo-code-redemptions            (promo.read)
//	GET /v1/organizations/{org_id}/promo-code-redemptions?format=csv
//
// Every redemption of the organization's codes, newest first, joined to the
// order it paid for. `promo_code_id` narrows it to one code. The CSV is the
// same rows for a spreadsheet — the owner asked for an export on
// 2026-09-22 (08_architecture/25_promo_codes_plan_ru.md §6).
package hcheckout

import (
	"bytes"
	"encoding/csv"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// promoRedemptionResponse is one report line. Money is in minor units of
// `currency`; the order fields are null for a redemption whose order is gone.
type promoRedemptionResponse struct {
	ID             string  `json:"id"`
	PromoCodeID    string  `json:"promo_code_id"`
	Code           string  `json:"code"`
	RedeemedAt     string  `json:"redeemed_at"`
	DiscountAmount int64   `json:"discount_amount"`
	OrderAmount    int64   `json:"order_amount"`
	OrderID        *string `json:"order_id"`
	OrderNumber    *int64  `json:"order_number"`
	OrderStatus    *string `json:"order_status"`
	Currency       *string `json:"currency"`
	BuyerEmail     *string `json:"buyer_email"`
	SessionID      *string `json:"session_id"`
	ChannelID      *string `json:"channel_id"`
	ChannelName    *string `json:"channel_name"`
}

func promoRedemptionFromRow(r gen.PromoCodeRedemptionReportRow) promoRedemptionResponse {
	uuidStr := func(u *uuid.UUID) *string {
		if u == nil {
			return nil
		}
		s := u.String()
		return &s
	}
	return promoRedemptionResponse{
		ID:             r.ID.String(),
		PromoCodeID:    r.PromoCodeID.String(),
		Code:           r.Code,
		RedeemedAt:     r.RedeemedAt.UTC().Format(time.RFC3339),
		DiscountAmount: r.DiscountAmount,
		OrderAmount:    r.OrderAmount,
		OrderID:        uuidStr(r.OrderID),
		OrderNumber:    r.OrderNumber,
		OrderStatus:    r.OrderStatus,
		Currency:       r.Currency,
		BuyerEmail:     r.BuyerEmail,
		SessionID:      uuidStr(r.SessionID),
		ChannelID:      uuidStr(r.ChannelID),
		ChannelName:    r.ChannelName,
	}
}

// promoRedemptionsCSV renders the report as a spreadsheet: one header row,
// money as decimal major units so a column sums without a conversion.
func promoRedemptionsCSV(rows []gen.PromoCodeRedemptionReportRow) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{
		"code", "redeemed_at", "order_number", "order_status", "currency",
		"order_amount", "discount_amount", "buyer_email", "channel", "session_id",
	})
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, r := range rows {
		orderNumber := ""
		if r.OrderNumber != nil {
			orderNumber = strconv.FormatInt(*r.OrderNumber, 10)
		}
		sessionID := ""
		if r.SessionID != nil {
			sessionID = r.SessionID.String()
		}
		_ = w.Write([]string{
			r.Code,
			r.RedeemedAt.UTC().Format(time.RFC3339),
			orderNumber,
			str(r.OrderStatus),
			str(r.Currency),
			minorToDecimal(r.OrderAmount),
			minorToDecimal(r.DiscountAmount),
			str(r.BuyerEmail),
			str(r.ChannelName),
			sessionID,
		})
	}
	w.Flush()
	return buf.Bytes()
}

// minorToDecimal renders minor units with two decimals ("40500" -> "405.00").
func minorToDecimal(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return sign + strconv.FormatInt(minor/100, 10) + "." + leftPad2(minor%100)
}

func leftPad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// HandleListPromoRedemptions serves
// GET /v1/organizations/{org_id}/promo-code-redemptions. Requires JWT +
// "promo.read". Query: promo_code_id (optional UUID), format=csv (optional).
func (h *Handler) HandleListPromoRedemptions(w http.ResponseWriter, r *http.Request) {
	if h.promoQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	var promoCodeID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("promo_code_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_promo_code_id", "promo_code_id must be a UUID", r,
				map[string]any{"field": "promo_code_id"},
			))
			return
		}
		promoCodeID = &id
	}

	rows, err := h.promoQueries.ListPromoCodeRedemptionsByOrg(ctx, orgID, promoCodeID)
	if err != nil {
		h.logger.Error("promo: redemptions report failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo.report_failed", "failed to list promo code redemptions", r,
		))
		return
	}

	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="promo-code-redemptions.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(promoRedemptionsCSV(rows))
		return
	}

	out := make([]promoRedemptionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, promoRedemptionFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"redemptions": out})
}
