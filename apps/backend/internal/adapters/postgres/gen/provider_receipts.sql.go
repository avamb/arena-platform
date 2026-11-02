// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: provider_receipts.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrgProviderReceiptSettings is the organization's provider-receipt setting
// (migration 0184): the owner's choice and the operator's "always send".
type OrgProviderReceiptSettings struct {
	Enabled bool
	Forced  bool
}

// Effective reports whether the provider is asked to e-mail its receipt.
func (s OrgProviderReceiptSettings) Effective() bool { return s.Enabled || s.Forced }

const getOrgProviderReceiptSettings = `-- name: GetOrgProviderReceiptSettings :one
SELECT provider_receipt_enabled, provider_receipt_forced
FROM organizations
WHERE id = $1 AND deleted_at IS NULL
`

// GetOrgProviderReceiptSettings reads the setting of one organization.
func (q *Queries) GetOrgProviderReceiptSettings(ctx context.Context, orgID uuid.UUID) (OrgProviderReceiptSettings, error) {
	var s OrgProviderReceiptSettings
	err := q.db.QueryRow(ctx, getOrgProviderReceiptSettings, orgID).Scan(&s.Enabled, &s.Forced)
	return s, err
}

const setOrgProviderReceiptEnabled = `-- name: SetOrgProviderReceiptEnabled :one
UPDATE organizations
SET provider_receipt_enabled = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING provider_receipt_enabled, provider_receipt_forced
`

// SetOrgProviderReceiptEnabled stores the owner's choice.
func (q *Queries) SetOrgProviderReceiptEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) (OrgProviderReceiptSettings, error) {
	var s OrgProviderReceiptSettings
	err := q.db.QueryRow(ctx, setOrgProviderReceiptEnabled, orgID, enabled).Scan(&s.Enabled, &s.Forced)
	return s, err
}

const setOrgProviderReceiptForced = `-- name: SetOrgProviderReceiptForced :one
UPDATE organizations
SET provider_receipt_forced = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING provider_receipt_enabled, provider_receipt_forced
`

// SetOrgProviderReceiptForced stores the platform operator's flag.
func (q *Queries) SetOrgProviderReceiptForced(ctx context.Context, orgID uuid.UUID, forced bool) (OrgProviderReceiptSettings, error) {
	var s OrgProviderReceiptSettings
	err := q.db.QueryRow(ctx, setOrgProviderReceiptForced, orgID, forced).Scan(&s.Enabled, &s.Forced)
	return s, err
}

// PaymentIntentReceiptRow is what the receipt archive job reads of one
// payment intent.
type PaymentIntentReceiptRow struct {
	ID                uuid.UUID
	OrgID             uuid.UUID
	Provider          string
	ProviderPaymentID *string
	ProviderChargeRef *string
	State             string
	ReceiptURL        *string
	ReceiptNumber     *string
	ReceiptMediaID    *uuid.UUID
	ReceiptArchivedAt *time.Time
}

const getPaymentIntentReceipt = `-- name: GetPaymentIntentReceipt :one
SELECT id, org_id, provider, provider_payment_id, provider_charge_ref, state,
       provider_receipt_url, provider_receipt_number,
       provider_receipt_media_id, provider_receipt_archived_at
FROM payment_intents
WHERE id = $1
`

// GetPaymentIntentReceipt reads one payment intent for the archive job.
func (q *Queries) GetPaymentIntentReceipt(ctx context.Context, id uuid.UUID) (PaymentIntentReceiptRow, error) {
	var r PaymentIntentReceiptRow
	err := q.db.QueryRow(ctx, getPaymentIntentReceipt, id).Scan(
		&r.ID, &r.OrgID, &r.Provider, &r.ProviderPaymentID, &r.ProviderChargeRef, &r.State,
		&r.ReceiptURL, &r.ReceiptNumber, &r.ReceiptMediaID, &r.ReceiptArchivedAt,
	)
	return r, err
}

const setPaymentIntentReceipt = `-- name: SetPaymentIntentReceipt :exec
UPDATE payment_intents
SET provider_receipt_url         = COALESCE($2, provider_receipt_url),
    provider_receipt_number      = COALESCE($3, provider_receipt_number),
    provider_receipt_media_id    = COALESCE($4, provider_receipt_media_id),
    provider_receipt_archived_at = CASE WHEN $4::uuid IS NOT NULL THEN now()
                                        ELSE provider_receipt_archived_at END,
    updated_at = now()
WHERE id = $1
`

// SetPaymentIntentReceipt stores the provider's receipt; nil keeps a value.
func (q *Queries) SetPaymentIntentReceipt(ctx context.Context, id uuid.UUID, url, number *string, mediaID *uuid.UUID) error {
	_, err := q.db.Exec(ctx, setPaymentIntentReceipt, id, url, number, mediaID)
	return err
}

const getTicketPaymentProvider = `-- name: GetTicketPaymentProvider :one
SELECT COALESCE((
    SELECT pi.provider
    FROM tickets t
    JOIN payment_intents pi ON pi.checkout_session_id = t.checkout_session_id
    WHERE t.id = $1 AND pi.state = 'succeeded'
    ORDER BY pi.created_at DESC
    LIMIT 1
), '')
`

// GetTicketPaymentProvider answers the provider the ticket was paid through,
// or "" when the ticket has no succeeded payment (invitation, free order,
// an order paid on a selling site).
func (q *Queries) GetTicketPaymentProvider(ctx context.Context, ticketID uuid.UUID) (string, error) {
	var provider string
	err := q.db.QueryRow(ctx, getTicketPaymentProvider, ticketID).Scan(&provider)
	return provider, err
}

const getOrderPaymentProvider = `-- name: GetOrderPaymentProvider :one
SELECT COALESCE((
    SELECT pi.provider
    FROM orders o
    JOIN payment_intents pi ON pi.checkout_session_id = o.checkout_session_id
    WHERE o.id = $1 AND pi.state = 'succeeded'
    ORDER BY pi.created_at DESC
    LIMIT 1
), '')
`

// GetOrderPaymentProvider answers the provider the order was paid through,
// or "" when it has no succeeded payment.
func (q *Queries) GetOrderPaymentProvider(ctx context.Context, orderID uuid.UUID) (string, error) {
	var provider string
	err := q.db.QueryRow(ctx, getOrderPaymentProvider, orderID).Scan(&provider)
	return provider, err
}

const getPaymentReceiptMediaID = `-- name: GetPaymentReceiptMediaID :one
SELECT id
FROM media_objects
WHERE owner_type = 'payment_receipt' AND owner_id = $1 AND org_id = $2
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1
`

// GetPaymentReceiptMediaID answers the live receipt copy an earlier run of
// the archive job saved for the payment intent; pgx.ErrNoRows when none.
func (q *Queries) GetPaymentReceiptMediaID(ctx context.Context, paymentIntentID, orgID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, getPaymentReceiptMediaID, paymentIntentID, orgID).Scan(&id)
	return id, err
}
