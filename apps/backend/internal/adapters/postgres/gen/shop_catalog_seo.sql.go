package gen

import (
	"context"

	"github.com/google/uuid"
)

// Shopify-import extras of a product and its variants (migration 0178, audit
// SA-29). They have their own writers, so the API's product and variant
// writes (InsertShopProduct, UpdateShopProduct, InsertShopVariant,
// UpdateShopVariant) neither need nor touch them.

const setShopProductSEO = `-- name: SetShopProductSEO :execrows
UPDATE shop_products
SET    seo_title = $3, seo_description = $4, tags = $5
WHERE  id = $1 AND org_id = $2 AND deleted_at IS NULL`

// SetShopProductSEO stores the search-engine title and description (nil =
// none) and the tags of a live product of the organization. It does not move
// updated_at: the import compares the product's own fields to decide whether
// a product changed. Reports whether a row was written.
func (q *Queries) SetShopProductSEO(ctx context.Context, id, orgID uuid.UUID, seoTitle, seoDescription *string, tags []string) (bool, error) {
	if tags == nil {
		tags = []string{}
	}
	tag, err := q.db.Exec(ctx, setShopProductSEO, id, orgID, seoTitle, seoDescription, tags)
	return tag.RowsAffected() == 1, err
}

// ShopVariantMetaRow is the weight and barcode of one live variant.
type ShopVariantMetaRow struct {
	ID          uuid.UUID `json:"id"`
	WeightGrams *int32    `json:"weight_grams"`
	Barcode     *string   `json:"barcode"`
}

const listShopVariantMeta = `-- name: ListShopVariantMeta :many
SELECT v.id, v.weight_grams, v.barcode
FROM   shop_variants v
WHERE  v.product_id = $1 AND v.deleted_at IS NULL`

// ListShopVariantMeta reads the weight and barcode of a product's live
// variants, keyed by variant id.
func (q *Queries) ListShopVariantMeta(ctx context.Context, productID uuid.UUID) (map[uuid.UUID]ShopVariantMetaRow, error) {
	rows, err := q.db.Query(ctx, listShopVariantMeta, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]ShopVariantMetaRow{}
	for rows.Next() {
		var m ShopVariantMetaRow
		if err := rows.Scan(&m.ID, &m.WeightGrams, &m.Barcode); err != nil {
			return nil, err
		}
		out[m.ID] = m
	}
	return out, rows.Err()
}

const setShopVariantMeta = `-- name: SetShopVariantMeta :execrows
UPDATE shop_variants
SET    weight_grams = $3, barcode = $4
WHERE  id = $1 AND product_id = $2 AND deleted_at IS NULL`

// SetShopVariantMeta stores the weight in grams and the barcode (nil = none)
// of a live variant of the product. Reports whether a row was written.
func (q *Queries) SetShopVariantMeta(ctx context.Context, id, productID uuid.UUID, weightGrams *int32, barcode *string) (bool, error) {
	tag, err := q.db.Exec(ctx, setShopVariantMeta, id, productID, weightGrams, barcode)
	return tag.RowsAffected() == 1, err
}
