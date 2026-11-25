package gen

import "context"

const getFeedTokenSiteBlocks = `SELECT sc.settings #> '{site,thank_you}', sc.settings #> '{site,socials}'
FROM agent_feed_tokens ft
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE ft.token = $1 AND ft.is_active = true AND sc.deleted_at IS NULL`

// GetFeedTokenSiteBlocks returns ONLY sales_channels.settings.site.thank_you
// and settings.site.socials of the token's channel (nil when absent).
// Hand-written, not sqlc-generated. It never reads any other part of the
// settings (they hold track_key_hash and the contact-form recipients).
func (q *Queries) GetFeedTokenSiteBlocks(ctx context.Context, token string) (thankYou []byte, socials []byte, err error) {
	err = q.db.QueryRow(ctx, getFeedTokenSiteBlocks, token).Scan(&thankYou, &socials)
	return thankYou, socials, err
}
