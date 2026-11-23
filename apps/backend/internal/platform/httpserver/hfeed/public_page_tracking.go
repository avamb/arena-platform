package hfeed

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// trackingEnabledSQL mirrors conversions.PGStore.Enqueue: a hosted page shows
// the cookie banner when the organization can send ad conversions anywhere —
// an active Meta connection with a stored token and a selected dataset, or
// manual Meta (pixel + CAPI token) or GA4 (measurement id + API secret)
// credentials. It reads no secret value, only whether it is non-empty.
const trackingEnabledSQL = `
SELECT EXISTS (
         SELECT 1
           FROM org_meta_connections c
           JOIN org_meta_assets a ON a.org_id = c.org_id AND a.kind = 'dataset' AND a.selected
          WHERE c.org_id = $1 AND c.status = 'active' AND c.access_token_enc IS NOT NULL)
    OR EXISTS (
         SELECT 1
           FROM org_conversion_settings st
          WHERE st.org_id = $1
            AND ((COALESCE(st.meta_pixel_id, '') <> '' AND COALESCE(st.meta_capi_token, '') <> '')
              OR (COALESCE(st.ga4_measurement_id, '') <> '' AND COALESCE(st.ga4_api_secret, '') <> '')))`

// rowQuerier is the one pool method the flag needs.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// orgTrackingEnabled answers the page's `org.tracking_enabled`. It runs on the
// pool before the response is built, never inside a transaction. A failed read
// says false (logged): the page then shows no banner and sets no cookie.
func orgTrackingEnabled(ctx context.Context, db rowQuerier, logger *slog.Logger, orgID uuid.UUID) bool {
	if db == nil {
		return false
	}
	var on bool
	if err := db.QueryRow(ctx, trackingEnabledSQL, orgID).Scan(&on); err != nil {
		if logger != nil {
			logger.Warn("public_page: tracking flag lookup failed",
				slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		}
		return false
	}
	return on
}

// trackingEnabled is the handler-side shortcut over the public queries' pool.
func (h *Handler) trackingEnabled(ctx context.Context, orgID uuid.UUID) bool {
	if h.publicFeedQueries == nil {
		return false
	}
	return orgTrackingEnabled(ctx, h.publicFeedQueries.DB(), h.logger, orgID)
}
