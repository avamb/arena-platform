package referral

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// IsSelfReferralBuyer is IsSelfReferral for a buyer who has no order yet: it
// reports whether the e-mail address or phone number a cart names belongs to
// the referrer of referrerOrderID (the order's own e-mail and phone, or any
// e-mail, phone or Telegram identity of its customer). It is the early,
// polite half of the self-invitation rule: the same cart is checked again
// once it is paid (RecordFriendOrderTx), which is the half that pays out.
//
// An address is compared lower-cased and trimmed, a phone by its digits (six
// or more). A cart that names neither is never "self".
func IsSelfReferralBuyer(ctx context.Context, db gen.DBTX, referrerOrderID uuid.UUID, email, phone string) (bool, error) {
	var same bool
	err := db.QueryRow(ctx, `
		WITH a AS (
			SELECT customer_id, lower(btrim(COALESCE(buyer_email, ''))) AS em,
			       regexp_replace(COALESCE(buyer_phone, ''), '\D', '', 'g') AS ph
			FROM orders WHERE id = $1),
		ia AS (
			SELECT i.kind, CASE WHEN i.kind = 'phone' THEN regexp_replace(i.value_normalized, '\D', '', 'g')
			                    ELSE lower(btrim(i.value_normalized)) END AS v
			FROM customer_identities i JOIN a ON i.customer_id = a.customer_id
			WHERE i.kind IN ('email', 'phone')
			UNION SELECT 'email', em FROM a WHERE em <> ''
			UNION SELECT 'phone', ph FROM a WHERE length(ph) >= 6),
		b AS (
			SELECT lower(btrim($2::text)) AS em, regexp_replace($3::text, '\D', '', 'g') AS ph)
		SELECT EXISTS (SELECT 1 FROM ia JOIN b ON ia.kind = 'email' AND b.em <> '' AND ia.v = b.em)
		    OR EXISTS (SELECT 1 FROM ia JOIN b ON ia.kind = 'phone' AND length(b.ph) >= 6 AND ia.v = b.ph)`,
		referrerOrderID, email, phone).Scan(&same)
	return same, err
}
