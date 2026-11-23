package gen

import "context"

const getFeedTokenSiteThankYou = `SELECT sc.settings #> '{site,thank_you}'
FROM agent_feed_tokens ft
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE ft.token = $1 AND ft.is_active = true AND sc.deleted_at IS NULL`

// GetFeedTokenSiteThankYou returns ONLY sales_channels.settings.site.thank_you
// of the token's channel (nil when absent). Hand-written, not sqlc-generated.
// It never reads any other part of the settings.
func (q *Queries) GetFeedTokenSiteThankYou(ctx context.Context, token string) ([]byte, error) {
	var raw []byte
	err := q.db.QueryRow(ctx, getFeedTokenSiteThankYou, token).Scan(&raw)
	return raw, err
}
