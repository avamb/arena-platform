package gen

import (
	"context"
	"encoding/json"
)

// ThankYouPostersOn reads settings.site.thank_you_posters: false only for a JSON
// boolean false; absent, null or any other value means posters are shown.
func ThankYouPostersOn(raw []byte) bool {
	var b bool
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &b) != nil {
		return true
	}
	return b
}

const getFeedTokenSiteBlocks = `SELECT sc.settings #> '{site,thank_you}', sc.settings #> '{site,socials}',
       sc.settings #> '{site,thank_you_posters}'
FROM agent_feed_tokens ft
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE ft.token = $1 AND ft.is_active = true AND sc.deleted_at IS NULL`

// GetFeedTokenSiteBlocks returns ONLY sales_channels.settings.site.thank_you,
// settings.site.socials (nil when absent) and settings.site.thank_you_posters
// raw (see ThankYouPostersOn) of the token's channel.
// Hand-written, not sqlc-generated. It never reads any other part of the
// settings (they hold track_key_hash and the contact-form recipients).
func (q *Queries) GetFeedTokenSiteBlocks(ctx context.Context, token string) (thankYou []byte, socials []byte, postersRaw []byte, err error) {
	err = q.db.QueryRow(ctx, getFeedTokenSiteBlocks, token).Scan(&thankYou, &socials, &postersRaw)
	return thankYou, socials, postersRaw, err
}
