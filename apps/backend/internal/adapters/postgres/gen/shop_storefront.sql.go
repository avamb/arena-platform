// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: the storefront switch of spec 08_architecture/61 section 3.4
//
// Who opens the shop's storefront: the organization itself (owner, manager,
// superadmin) while the shop module is connected, and only when the shop has
// something to sell and a way to take the money.

package gen

import (
	"context"

	"github.com/google/uuid"
)

const shopHasActiveProduct = `-- name: ShopHasActiveProduct :one
SELECT EXISTS (SELECT 1 FROM shop_products
               WHERE org_id = $1 AND status = 'active' AND deleted_at IS NULL)`

// ShopHasActiveProduct is true when the organization has at least one live
// product on sale (status active): a storefront without one is not opened.
func (q *Queries) ShopHasActiveProduct(ctx context.Context, orgID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, shopHasActiveProduct, orgID).Scan(&ok)
	return ok, err
}

const setShopSettingsEnabled = `-- name: SetShopSettingsEnabled :execrows
UPDATE shop_settings SET enabled = $2, updated_at = now() WHERE org_id = $1`

// SetShopSettingsEnabled opens (true) or closes (false) the storefront of an
// organization whose settings row exists; it reports how many rows changed
// (0 = no settings row).
func (q *Queries) SetShopSettingsEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) (int64, error) {
	tag, err := q.db.Exec(ctx, setShopSettingsEnabled, orgID, enabled)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
