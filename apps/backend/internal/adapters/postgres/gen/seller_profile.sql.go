// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: seller_profile.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

const getOrganizationLegalForm = `-- name: GetOrganizationLegalForm :one
SELECT legal_form FROM organizations WHERE id = $1 AND deleted_at IS NULL`

// GetOrganizationLegalForm returns organizations.legal_form (nil = not given)
// of an active organization; pgx.ErrNoRows when there is none.
func (q *Queries) GetOrganizationLegalForm(ctx context.Context, orgID uuid.UUID) (*string, error) {
	var v *string
	err := q.db.QueryRow(ctx, getOrganizationLegalForm, orgID).Scan(&v)
	return v, err
}

const setOrganizationLegalForm = `-- name: SetOrganizationLegalForm :execrows
UPDATE organizations SET legal_form = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`

// SetOrganizationLegalForm stores (or, with nil, clears) the legal form and
// reports whether an active organization was updated.
func (q *Queries) SetOrganizationLegalForm(ctx context.Context, orgID uuid.UUID, legalForm *string) (bool, error) {
	tag, err := q.db.Exec(ctx, setOrganizationLegalForm, orgID, legalForm)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SellerChannelRow is a hosted-page channel with its seller-block settings.
type SellerChannelRow struct {
	ID         uuid.UUID
	Name       string
	ShowSeller bool
	// PublicContact is settings.hosted_page.public_contact as raw JSON, nil
	// when the channel has none.
	PublicContact []byte
}

const listSellerChannels = `-- name: ListSellerChannels :many
SELECT id, name,
       COALESCE(settings #>> '{hosted_page,show_seller}', '') = 'true' AS show_seller,
       settings #> '{hosted_page,public_contact}' AS public_contact
FROM sales_channels
WHERE org_id = $1
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,enabled}' = 'true'
ORDER BY created_at, id`

// ListSellerChannels lists the organization's hosted-page channels, oldest
// first, with their seller-block settings.
func (q *Queries) ListSellerChannels(ctx context.Context, orgID uuid.UUID) ([]SellerChannelRow, error) {
	rows, err := q.db.Query(ctx, listSellerChannels, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellerChannelRow
	for rows.Next() {
		var r SellerChannelRow
		if err := rows.Scan(&r.ID, &r.Name, &r.ShowSeller, &r.PublicContact); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const setChannelSellerDisplay = `-- name: SetChannelSellerDisplay :execrows
UPDATE sales_channels
SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{hosted_page}',
        CASE WHEN $4::jsonb IS NULL
             THEN (COALESCE(settings -> 'hosted_page', '{}'::jsonb) - 'public_contact')
                  || jsonb_build_object('show_seller', $3::boolean)
             ELSE COALESCE(settings -> 'hosted_page', '{}'::jsonb)
                  || jsonb_build_object('show_seller', $3::boolean, 'public_contact', $4::jsonb)
        END, true),
    updated_at = now()
WHERE id = $1
  AND org_id = $2
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,enabled}' = 'true'`

// SetChannelSellerDisplay writes show_seller and public_contact (nil removes
// the key) of a hosted-page channel of the organization and reports whether
// such a channel was updated. Every other settings key is kept.
func (q *Queries) SetChannelSellerDisplay(ctx context.Context, channelID, orgID uuid.UUID, showSeller bool, publicContact []byte) (bool, error) {
	var contact any
	if publicContact != nil {
		contact = string(publicContact)
	}
	tag, err := q.db.Exec(ctx, setChannelSellerDisplay, channelID, orgID, showSeller, contact)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
