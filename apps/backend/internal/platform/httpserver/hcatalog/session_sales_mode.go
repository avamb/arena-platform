// session_sales_mode.go: the sales_mode / external block of POST and PATCH
// .../sessions (migration 0220, spec 63 §14.4). The rules themselves live in
// internal/platform/extsale; this file only reads the wire shape and writes
// the refusals in the house envelope.
package hcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/extsale"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// externalRequest is the "external" object of a session write.
type externalRequest struct {
	URL        string   `json:"url"`
	SellerName string   `json:"seller_name"`
	PriceFrom  *float64 `json:"price_from"`
	Currency   string   `json:"currency"`
}

// salesModeWrite is what a session write says about who sells the tickets.
type salesModeWrite struct {
	// Mode is "" when sales_mode was not sent.
	Mode string
	// ExternalPresent is true when "external" was sent and is not null.
	ExternalPresent bool
	// ExternalNull is true when "external": null was sent.
	ExternalNull bool
	// Sale is the checked partner block when ExternalPresent.
	Sale extsale.Sale
}

// writeExtsaleError writes a 422 for a refused partner block.
func writeExtsaleError(w http.ResponseWriter, r *http.Request, e *extsale.FieldError) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		e.Code, e.Message, r, map[string]any{"field": e.Field},
	))
}

// writeSalesModeLocked writes the 409 of a mode change the session's state forbids.
func writeSalesModeLocked(w http.ResponseWriter, r *http.Request, from, to, why string) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		extsale.CodeSalesModeLocked, why, r,
		map[string]any{"field": "sales_mode", "from": from, "to": to},
	))
}

// parseSalesModeWrite reads sales_mode and external of a session write and
// checks everything that needs no database. ok is false after the envelope
// was written.
func parseSalesModeWrite(w http.ResponseWriter, r *http.Request, rawMode *string, rawExternal json.RawMessage) (salesModeWrite, bool) {
	var out salesModeWrite
	if rawMode != nil {
		out.Mode = strings.TrimSpace(*rawMode)
		if !extsale.ValidMode(out.Mode) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				extsale.CodeInvalidSalesMode, "sales_mode must be one of: platform, external", r,
				map[string]any{"field": "sales_mode"},
			))
			return out, false
		}
	}
	if len(bytes.TrimSpace(rawExternal)) > 0 {
		if bytes.Equal(bytes.TrimSpace(rawExternal), []byte("null")) {
			out.ExternalNull = true
		} else {
			var ext externalRequest
			if err := json.Unmarshal(rawExternal, &ext); err != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"session.invalid_json", "external must be an object with url, seller_name, price_from and currency", r,
					map[string]any{"field": "external"},
				))
				return out, false
			}
			sale, ferr := extsale.Normalize(extsale.Input{
				URL: ext.URL, SellerName: ext.SellerName, PriceFrom: ext.PriceFrom, Currency: ext.Currency,
			})
			if ferr != nil {
				writeExtsaleError(w, r, ferr)
				return out, false
			}
			out.ExternalPresent = true
			out.Sale = sale
		}
	}
	if out.Mode == gen.SessionSalesModePlatform && out.ExternalPresent {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			extsale.CodeExternalUnused, "external applies only to sales_mode external", r,
			map[string]any{"field": "external"},
		))
		return out, false
	}
	if out.Mode == gen.SessionSalesModeExternal && out.ExternalNull {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			extsale.CodeExternalMissing, "sales_mode external needs the external block", r,
			map[string]any{"field": "external"},
		))
		return out, false
	}
	return out, true
}

// applySalesMode changes the sales mode of the session row `cur` inside tx,
// which already holds the session row lock. It returns the row to answer
// with and ok=false after writing an error envelope (the caller rolls back).
func (h *Handler) applySalesMode(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, cur gen.SessionRow, wr salesModeWrite) (gen.SessionRow, bool) {
	target := wr.Mode
	if target == "" {
		target = cur.SalesMode
		if wr.ExternalNull {
			target = gen.SessionSalesModePlatform
		}
	}
	switch {
	case target == gen.SessionSalesModePlatform && wr.ExternalPresent:
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			extsale.CodeExternalUnused, "external applies only to sales_mode external", r,
			map[string]any{"field": "external"},
		))
		return cur, false
	case target == gen.SessionSalesModeExternal && !wr.ExternalPresent && cur.SalesMode != gen.SessionSalesModeExternal:
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			extsale.CodeExternalMissing, "sales_mode external needs the external block", r,
			map[string]any{"field": "external"},
		))
		return cur, false
	case target == cur.SalesMode && !wr.ExternalPresent:
		return cur, true // nothing to change
	}

	q := h.sessionQueries.WithTx(tx)
	facts, err := q.GetSessionSalesModeLockFacts(ctx, cur.ID)
	if err != nil {
		h.logger.Error("session: sales mode lock facts failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.update_failed", "failed to update session", r,
		))
		return cur, false
	}
	if err := extsale.CheckSwitch(cur.SalesMode, target, facts); err != nil {
		why := "a session with orders, holds, tickets, seats or tiers cannot be handed to a partner box office"
		if target == gen.SessionSalesModePlatform {
			why = "a session with seats or tiers cannot go back to sales on the platform"
		}
		writeSalesModeLocked(w, r, cur.SalesMode, target, why)
		return cur, false
	}
	var url, name, currency *string
	var minor *int64
	if target == gen.SessionSalesModeExternal {
		s := wr.Sale
		url, name, minor, currency = &s.URL, &s.SellerName, s.PriceFromMinor, s.Currency
	}
	row, err := q.SetSessionSalesMode(ctx, cur.ID, cur.EventID, target, url, name, minor, currency)
	if err != nil {
		h.logger.Error("session: set sales mode failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.update_failed", "failed to update session", r,
		))
		return cur, false
	}
	return row, true
}

// writeSoldExternally answers 409 session.sold_externally when the session is
// sold by a partner box office: it owns no tiers and no seats.
func writeSoldExternally(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
		extsale.CodeSoldExternally,
		"this session is sold by a partner box office, so it has no tiers or seats here", r,
	))
}
