package sessionchange

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// channelHasSiteSQL is the per-channel form of the has_site test inside
// affectedOrdersSQL: a channel with an active bil24_wp webhook subscriber is a
// selling site, and a site writes to its own buyers from its own domain.
const channelHasSiteSQL = `
SELECT EXISTS (SELECT 1 FROM webhook_subscribers ws
                WHERE ws.channel_id = $1 AND ws.kind = 'bil24_wp' AND ws.active)`

// QueryRower is the one-row read ChannelHasSite needs; a pool, a connection and
// a transaction all satisfy it.
type QueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ChannelHasSite reports whether the sales channel is the "site" route — the
// same decision AffectedOrders makes per order — so other operator actions on
// an order (resending its tickets, EC-13) refuse exactly the orders a session
// change refuses and for the same reason.
func ChannelHasSite(ctx context.Context, db QueryRower, channelID uuid.UUID) (bool, error) {
	var has bool
	if err := db.QueryRow(ctx, channelHasSiteSQL, channelID).Scan(&has); err != nil {
		return false, fmt.Errorf("sessionchange: channel has site: %w", err)
	}
	return has, nil
}
