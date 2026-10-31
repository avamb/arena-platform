// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: invitations_delivery.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// InvitationDeliveryRow is what the first delivery of a checkout's tickets
// needs to know when the order is a complimentary one (spec 46, section 3.4).
type InvitationDeliveryRow struct {
	OrderID uuid.UUID
	// Source is orders.source; only 'complimentary' changes the delivery.
	Source string
	// BuyerEmail is the guest's own address (delivery guest_email), "" when
	// the order has none.
	BuyerEmail string
	// Delivery is orders.metadata.invitation.delivery ('guest_email',
	// 'my_email', 'pdf_only'), "" for a complimentary order issued another way.
	Delivery string
	// IssuerEmail is the e-mail of orders.issued_by, "" when nobody (an API
	// key) or no such user.
	IssuerEmail string
}

const getInvitationDeliveryByCheckoutSession = `-- name: GetInvitationDeliveryByCheckoutSession :one
SELECT o.id, o.source,
       COALESCE(btrim(o.buyer_email), ''),
       COALESCE(o.metadata->'invitation'->>'delivery', ''),
       COALESCE(btrim(u.email), '')
FROM   orders o
LEFT   JOIN users u ON u.id = o.issued_by
WHERE  o.checkout_session_id = $1`

// GetInvitationDeliveryByCheckoutSession reads the delivery facts of the order
// a checkout session produced; pgx.ErrNoRows when the checkout has no order.
func (q *Queries) GetInvitationDeliveryByCheckoutSession(ctx context.Context, checkoutSessionID uuid.UUID) (InvitationDeliveryRow, error) {
	var r InvitationDeliveryRow
	err := q.db.QueryRow(ctx, getInvitationDeliveryByCheckoutSession, checkoutSessionID).Scan(
		&r.OrderID, &r.Source, &r.BuyerEmail, &r.Delivery, &r.IssuerEmail,
	)
	return r, err
}
