// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: invitation_pdf_notices.sql.go (spec 46, INV-09)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// InvitationPDFNotice is a staff invitation issued from Telegram whose PDF the
// bot has to put into the issuer's chat. It carries no e-mail or phone of the
// guest: the bot needs the name, the order number and the date only.
type InvitationPDFNotice struct {
	OrderID         uuid.UUID `json:"order_id"`
	OrgID           uuid.UUID `json:"org_id"`
	SystemID        int64     `json:"system_id"`
	TelegramUserID  int64     `json:"telegram_user_id"`
	Locale          string    `json:"locale"`
	GuestName       string    `json:"guest_name"`
	EventName       string    `json:"event_name"`
	SessionStartAt  time.Time `json:"session_start_at"`
	SessionTimezone string    `json:"session_timezone"`
}

const claimInvitationPDFNotices = `-- name: ClaimInvitationPDFNotices :many
WITH picked AS (
    SELECT o.id, o.org_id, o.system_id, l.telegram_user_id, l.locale,
           COALESCE(btrim(o.buyer_name), '') AS guest_name,
           e.name AS event_name, s.start_at,
           COALESCE(v.timezone, 'UTC') AS tz
    FROM   orders o
    JOIN   sessions s ON s.id = o.session_id
    JOIN   events e ON e.id = s.event_id
    LEFT   JOIN venues v ON v.id = s.venue_id
    CROSS  JOIN LATERAL (
               SELECT bl.telegram_user_id, bl.locale
               FROM   bot_telegram_links bl
               WHERE  bl.user_id = o.issued_by AND bl.revoked_at IS NULL
               ORDER  BY bl.updated_at DESC
               LIMIT  1) l
    WHERE  o.source = 'complimentary'
      AND  o.issued_via IS NOT NULL
      AND  o.issued_by IS NOT NULL
      AND  o.metadata->'invitation'->>'via' IN ('telegram_bot', 'telegram_miniapp')
      AND  o.metadata->'invitation'->>'pdf_notified_at' IS NULL
      AND  o.created_at > now() - interval '1 day'
      AND  EXISTS (SELECT 1 FROM order_items oi WHERE oi.order_id = o.id)
      AND  NOT EXISTS (SELECT 1 FROM order_items oi WHERE oi.order_id = o.id AND oi.ticket_id IS NULL)
    ORDER  BY o.created_at, o.id
    LIMIT  $1
    FOR UPDATE OF o SKIP LOCKED
), marked AS (
    UPDATE orders o
    SET    metadata = jsonb_set(o.metadata, '{invitation,pdf_notified_at}', to_jsonb(now()))
    FROM   picked p
    WHERE  o.id = p.id
    RETURNING o.id
)
SELECT p.id, p.org_id, p.system_id, p.telegram_user_id, p.locale, p.guest_name,
       p.event_name, p.start_at, p.tz
FROM   picked p
JOIN   marked m ON m.id = p.id
ORDER  BY p.id`

// ClaimInvitationPDFNotices hands out up to limit staff invitations that were
// issued from Telegram (orders.metadata.invitation.via), by a user with a live
// bot link, whose tickets are ALL issued, and stamps each with
// metadata.invitation.pdf_notified_at in the same statement. An invitation is
// handed out at most once: a crash between the claim and the message loses
// one message, never repeats it. Invitations older than a day are never
// handed out, so a bot that was down for long does not flood a chat.
func (q *Queries) ClaimInvitationPDFNotices(ctx context.Context, limit int32) ([]InvitationPDFNotice, error) {
	rows, err := q.db.Query(ctx, claimInvitationPDFNotices, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationPDFNotice
	for rows.Next() {
		var n InvitationPDFNotice
		if err := rows.Scan(&n.OrderID, &n.OrgID, &n.SystemID, &n.TelegramUserID, &n.Locale,
			&n.GuestName, &n.EventName, &n.SessionStartAt, &n.SessionTimezone); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
