// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: shop catalog of migration 0172 (spec 08_architecture/47 section 2)
//
// The organizer's merchandise shop: settings, products, product images,
// variants and static pages. Orders and stock live in shop_orders.sql.go.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Settings
// ─────────────────────────────────────────────────────────────────────────────

// ShopSettingsRow is one shop_settings row plus organizations.shop_domain,
// which lives on the organization (one storefront host per organization).
type ShopSettingsRow struct {
	OrgID            uuid.UUID  `json:"org_id"`
	Currency         string     `json:"currency"`
	ShippingFeeMinor int64      `json:"shipping_fee_minor"`
	ShippingLabel    string     `json:"shipping_label"`
	CountryISO2      *string    `json:"country_iso2"`
	AgeGateMinAge    *int32     `json:"age_gate_min_age"`
	AnnouncementText string     `json:"announcement_text"`
	ContactEmail     *string    `json:"contact_email"`
	LogisticsNote    string     `json:"logistics_note"`
	SalesChannelID   *uuid.UUID `json:"sales_channel_id"`
	Enabled          bool       `json:"enabled"`
	MaxLineQty       int32      `json:"max_line_qty"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ShopDomain       *string    `json:"shop_domain"`
}

const shopSettingsColumns = `s.org_id, s.currency, s.shipping_fee_minor, s.shipping_label, s.country_iso2,
       s.age_gate_min_age, s.announcement_text, s.contact_email, s.logistics_note,
       s.sales_channel_id, s.enabled, s.max_line_qty, s.updated_at, o.shop_domain`

func scanShopSettingsRow(row interface{ Scan(dest ...any) error }) (ShopSettingsRow, error) {
	var s ShopSettingsRow
	err := row.Scan(&s.OrgID, &s.Currency, &s.ShippingFeeMinor, &s.ShippingLabel, &s.CountryISO2,
		&s.AgeGateMinAge, &s.AnnouncementText, &s.ContactEmail, &s.LogisticsNote,
		&s.SalesChannelID, &s.Enabled, &s.MaxLineQty, &s.UpdatedAt, &s.ShopDomain)
	return s, err
}

const getShopSettings = `-- name: GetShopSettings :one
SELECT ` + shopSettingsColumns + `
FROM   shop_settings s
JOIN   organizations o ON o.id = s.org_id
WHERE  s.org_id = $1`

// GetShopSettings reads the shop settings of an organization. pgx.ErrNoRows
// means the organization has no shop yet.
func (q *Queries) GetShopSettings(ctx context.Context, orgID uuid.UUID) (ShopSettingsRow, error) {
	return scanShopSettingsRow(q.db.QueryRow(ctx, getShopSettings, orgID))
}

const ensureShopSettings = `-- name: EnsureShopSettings :exec
INSERT INTO shop_settings (org_id) VALUES ($1)
ON CONFLICT (org_id) DO NOTHING`

// EnsureShopSettings creates the default settings row of an organization when
// it has none (spec: the row is created with the first product).
func (q *Queries) EnsureShopSettings(ctx context.Context, orgID uuid.UUID) error {
	_, err := q.db.Exec(ctx, ensureShopSettings, orgID)
	return err
}

const upsertShopSettings = `-- name: UpsertShopSettings :exec
INSERT INTO shop_settings (org_id, currency, shipping_fee_minor, shipping_label, country_iso2,
                           age_gate_min_age, announcement_text, contact_email, logistics_note,
                           sales_channel_id, enabled, max_line_qty, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
ON CONFLICT (org_id) DO UPDATE SET
    currency = EXCLUDED.currency, shipping_fee_minor = EXCLUDED.shipping_fee_minor,
    shipping_label = EXCLUDED.shipping_label, country_iso2 = EXCLUDED.country_iso2,
    age_gate_min_age = EXCLUDED.age_gate_min_age, announcement_text = EXCLUDED.announcement_text,
    contact_email = EXCLUDED.contact_email, logistics_note = EXCLUDED.logistics_note,
    sales_channel_id = EXCLUDED.sales_channel_id, enabled = EXCLUDED.enabled,
    max_line_qty = EXCLUDED.max_line_qty, updated_at = now()`

// UpsertShopSettingsParams is the whole settings row (PUT replaces it).
type UpsertShopSettingsParams struct {
	OrgID            uuid.UUID
	Currency         string
	ShippingFeeMinor int64
	ShippingLabel    string
	CountryISO2      *string
	AgeGateMinAge    *int32
	AnnouncementText string
	ContactEmail     *string
	LogisticsNote    string
	SalesChannelID   *uuid.UUID
	Enabled          bool
	MaxLineQty       int32
}

// UpsertShopSettings writes the settings row of an organization.
func (q *Queries) UpsertShopSettings(ctx context.Context, p UpsertShopSettingsParams) error {
	_, err := q.db.Exec(ctx, upsertShopSettings, p.OrgID, p.Currency, p.ShippingFeeMinor, p.ShippingLabel,
		p.CountryISO2, p.AgeGateMinAge, p.AnnouncementText, p.ContactEmail, p.LogisticsNote, p.SalesChannelID,
		p.Enabled, p.MaxLineQty)
	return err
}

const setOrganizationShopDomain = `-- name: SetOrganizationShopDomain :exec
UPDATE organizations SET shop_domain = $2, updated_at = now() WHERE id = $1`

// SetOrganizationShopDomain sets or clears (nil) the storefront host. The
// unique index organizations_shop_domain_uq answers 23505 for a host another
// organization holds.
func (q *Queries) SetOrganizationShopDomain(ctx context.Context, orgID uuid.UUID, domain *string) error {
	_, err := q.db.Exec(ctx, setOrganizationShopDomain, orgID, domain)
	return err
}

const getOrganizationShopDomain = `-- name: GetOrganizationShopDomain :one
SELECT shop_domain FROM organizations WHERE id = $1 AND deleted_at IS NULL`

// GetOrganizationShopDomain reads the storefront host of an organization
// (nil when it has none). pgx.ErrNoRows when the organization does not exist.
func (q *Queries) GetOrganizationShopDomain(ctx context.Context, orgID uuid.UUID) (*string, error) {
	var d *string
	err := q.db.QueryRow(ctx, getOrganizationShopDomain, orgID).Scan(&d)
	return d, err
}

const getOrganizationByShopDomain = `-- name: GetOrganizationByShopDomain :one
SELECT o.id, o.slug
FROM   organizations o
JOIN   shop_settings s ON s.org_id = o.id
WHERE  o.shop_domain = $1 AND o.deleted_at IS NULL AND s.enabled`

// GetOrganizationByShopDomain resolves a storefront host to the organization
// that owns it (only an organization whose shop is enabled). pgx.ErrNoRows when none.
func (q *Queries) GetOrganizationByShopDomain(ctx context.Context, host string) (uuid.UUID, string, error) {
	var id uuid.UUID
	var slug string
	err := q.db.QueryRow(ctx, getOrganizationByShopDomain, host).Scan(&id, &slug)
	return id, slug, err
}

const getFirstSalesChannelID = `-- name: GetFirstSalesChannelID :one
SELECT id FROM sales_channels
WHERE  org_id = $1 AND deleted_at IS NULL
ORDER  BY created_at, id
LIMIT  1`

// GetFirstSalesChannelID is the organization's oldest live sales channel.
func (q *Queries) GetFirstSalesChannelID(ctx context.Context, orgID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, getFirstSalesChannelID, orgID).Scan(&id)
	return id, err
}

const listLiveSalesChannelIDs = `-- name: ListLiveSalesChannelIDs :many
SELECT id FROM sales_channels
WHERE  org_id = $1 AND deleted_at IS NULL
ORDER  BY created_at, id
LIMIT  $2`

// ListLiveSalesChannelIDs lists at most limit live sales channels of the
// organization, oldest first: the candidates whose provider may take shop
// payments when the settings name none.
func (q *Queries) ListLiveSalesChannelIDs(ctx context.Context, orgID uuid.UUID, limit int32) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, listLiveSalesChannelIDs, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Products
// ─────────────────────────────────────────────────────────────────────────────

// ShopProductRow is one shop_products row.
type ShopProductRow struct {
	ID          uuid.UUID  `json:"id"`
	OrgID       uuid.UUID  `json:"org_id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	BodyHTML    string     `json:"body_html"`
	Status      string     `json:"status"`
	ProductType string     `json:"product_type"`
	Vendor      string     `json:"vendor"`
	SortOrder   int32      `json:"sort_order"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
	// Kind is physical, digital or service (spec 47 section 1b).
	Kind           string     `json:"kind"`
	DigitalMediaID *uuid.UUID `json:"digital_media_id"`
	Option1Name    *string    `json:"option1_name"`
	Option2Name    *string    `json:"option2_name"`
	Option3Name    *string    `json:"option3_name"`
}

const shopProductColumns = `id, org_id, slug, title, body_html, status, product_type, vendor, sort_order,
       created_at, updated_at, deleted_at, kind, digital_media_id, option1_name, option2_name, option3_name`

func scanShopProductRow(row interface{ Scan(dest ...any) error }) (ShopProductRow, error) {
	var p ShopProductRow
	err := row.Scan(&p.ID, &p.OrgID, &p.Slug, &p.Title, &p.BodyHTML, &p.Status, &p.ProductType,
		&p.Vendor, &p.SortOrder, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt,
		&p.Kind, &p.DigitalMediaID, &p.Option1Name, &p.Option2Name, &p.Option3Name)
	return p, err
}

// ShopProductParams are the editable columns of a product.
type ShopProductParams struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	Slug        string
	Title       string
	BodyHTML    string
	Status      string
	ProductType string
	Vendor      string
	SortOrder   int32
	// Kind "" means physical.
	Kind           string
	DigitalMediaID *uuid.UUID
	Option1Name    *string
	Option2Name    *string
	Option3Name    *string
}

func (p ShopProductParams) kind() string {
	if p.Kind == "" {
		return "physical"
	}
	return p.Kind
}

const insertShopProduct = `-- name: InsertShopProduct :one
INSERT INTO shop_products (org_id, slug, title, body_html, status, product_type, vendor, sort_order,
                           kind, digital_media_id, option1_name, option2_name, option3_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING ` + shopProductColumns

// InsertShopProduct creates a product (ID is ignored). A slug already used by
// a live product of the organization is a 23505 on shop_products_org_slug_uq.
func (q *Queries) InsertShopProduct(ctx context.Context, p ShopProductParams) (ShopProductRow, error) {
	return scanShopProductRow(q.db.QueryRow(ctx, insertShopProduct, p.OrgID, p.Slug, p.Title, p.BodyHTML,
		p.Status, p.ProductType, p.Vendor, p.SortOrder, p.kind(), p.DigitalMediaID,
		p.Option1Name, p.Option2Name, p.Option3Name))
}

const updateShopProduct = `-- name: UpdateShopProduct :one
UPDATE shop_products
SET    slug = $3, title = $4, body_html = $5, status = $6, product_type = $7, vendor = $8,
       sort_order = $9, kind = $10, digital_media_id = $11, option1_name = $12, option2_name = $13,
       option3_name = $14, updated_at = now()
WHERE  id = $1 AND org_id = $2 AND deleted_at IS NULL
RETURNING ` + shopProductColumns

// UpdateShopProduct writes every editable column of a live product of the
// organization. pgx.ErrNoRows when it does not exist there.
func (q *Queries) UpdateShopProduct(ctx context.Context, p ShopProductParams) (ShopProductRow, error) {
	return scanShopProductRow(q.db.QueryRow(ctx, updateShopProduct, p.ID, p.OrgID, p.Slug, p.Title,
		p.BodyHTML, p.Status, p.ProductType, p.Vendor, p.SortOrder, p.kind(), p.DigitalMediaID,
		p.Option1Name, p.Option2Name, p.Option3Name))
}

const getShopProduct = `-- name: GetShopProduct :one
SELECT ` + shopProductColumns + `
FROM   shop_products
WHERE  id = $1 AND org_id = $2 AND deleted_at IS NULL`

// GetShopProduct reads a live product of the organization.
func (q *Queries) GetShopProduct(ctx context.Context, id, orgID uuid.UUID) (ShopProductRow, error) {
	return scanShopProductRow(q.db.QueryRow(ctx, getShopProduct, id, orgID))
}

const getShopProductBySlug = `-- name: GetShopProductBySlug :one
SELECT ` + shopProductColumns + `
FROM   shop_products
WHERE  org_id = $1 AND slug = $2 AND deleted_at IS NULL`

// GetShopProductBySlug reads a live product of the organization by slug.
func (q *Queries) GetShopProductBySlug(ctx context.Context, orgID uuid.UUID, slug string) (ShopProductRow, error) {
	return scanShopProductRow(q.db.QueryRow(ctx, getShopProductBySlug, orgID, slug))
}

const listShopProducts = `-- name: ListShopProducts :many
SELECT ` + shopProductColumns + `
FROM   shop_products
WHERE  org_id = $1 AND deleted_at IS NULL
  AND  ($2::text = '' OR status = $2)
ORDER  BY sort_order, created_at, id`

// ListShopProducts lists the live products of an organization; status ""
// lists every status.
func (q *Queries) ListShopProducts(ctx context.Context, orgID uuid.UUID, status string) ([]ShopProductRow, error) {
	rows, err := q.db.Query(ctx, listShopProducts, orgID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopProductRow
	for rows.Next() {
		p, err := scanShopProductRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const softDeleteShopProduct = `-- name: SoftDeleteShopProduct :execrows
UPDATE shop_products SET deleted_at = now(), updated_at = now(), status = 'archived'
WHERE  id = $1 AND org_id = $2 AND deleted_at IS NULL`

// SoftDeleteShopProduct deletes a product; its slug becomes free again and
// past orders keep their snapshot. Reports whether a row was deleted.
func (q *Queries) SoftDeleteShopProduct(ctx context.Context, id, orgID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, softDeleteShopProduct, id, orgID)
	return tag.RowsAffected() == 1, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Images
// ─────────────────────────────────────────────────────────────────────────────

// ShopProductImageRow is one picture of a product.
type ShopProductImageRow struct {
	ID        uuid.UUID `json:"id"`
	ProductID uuid.UUID `json:"product_id"`
	MediaID   uuid.UUID `json:"media_id"`
	Position  int32     `json:"position"`
	Alt       string    `json:"alt"`
	CreatedAt time.Time `json:"created_at"`
}

const shopImageColumns = `id, product_id, media_id, position, alt, created_at`

func scanShopProductImageRow(row interface{ Scan(dest ...any) error }) (ShopProductImageRow, error) {
	var i ShopProductImageRow
	err := row.Scan(&i.ID, &i.ProductID, &i.MediaID, &i.Position, &i.Alt, &i.CreatedAt)
	return i, err
}

const upsertShopProductImage = `-- name: UpsertShopProductImage :one
INSERT INTO shop_product_images (product_id, media_id, position, alt)
VALUES ($1, $2, $3, $4)
ON CONFLICT (product_id, media_id) DO UPDATE SET position = EXCLUDED.position, alt = EXCLUDED.alt
RETURNING ` + shopImageColumns

// UpsertShopProductImage attaches a media object to a product, or moves and
// relabels it when it is attached already.
func (q *Queries) UpsertShopProductImage(ctx context.Context, productID, mediaID uuid.UUID, position int32, alt string) (ShopProductImageRow, error) {
	return scanShopProductImageRow(q.db.QueryRow(ctx, upsertShopProductImage, productID, mediaID, position, alt))
}

const deleteShopProductImage = `-- name: DeleteShopProductImage :execrows
DELETE FROM shop_product_images WHERE product_id = $1 AND media_id = $2`

// DeleteShopProductImage detaches a media object from a product.
func (q *Queries) DeleteShopProductImage(ctx context.Context, productID, mediaID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, deleteShopProductImage, productID, mediaID)
	return tag.RowsAffected() == 1, err
}

const listShopProductImages = `-- name: ListShopProductImages :many
SELECT ` + shopImageColumns + `
FROM   shop_product_images
WHERE  product_id = ANY($1::uuid[])
ORDER  BY product_id, position, created_at, id`

// ListShopProductImages lists the pictures of the given products in order.
func (q *Queries) ListShopProductImages(ctx context.Context, productIDs []uuid.UUID) ([]ShopProductImageRow, error) {
	rows, err := q.db.Query(ctx, listShopProductImages, productIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopProductImageRow
	for rows.Next() {
		i, err := scanShopProductImageRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

const shopMediaUsable = `-- name: ShopMediaUsable :one
SELECT EXISTS (SELECT 1 FROM media_objects
               WHERE id = $1 AND org_id = $2 AND owner_type = $3 AND deleted_at IS NULL)`

// ShopMediaUsable reports whether a media object is a live picture of the
// organization: its own upload with owner_type shop_product.
func (q *Queries) ShopMediaUsable(ctx context.Context, mediaID, orgID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, shopMediaUsable, mediaID, orgID, "shop_product").Scan(&ok)
	return ok, err
}

// ShopDigitalMediaUsable reports whether a media object is a live file of the
// organization uploaded as a digital product's file (owner_type
// shop_digital), never a picture.
func (q *Queries) ShopDigitalMediaUsable(ctx context.Context, mediaID, orgID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, shopMediaUsable, mediaID, orgID, "shop_digital").Scan(&ok)
	return ok, err
}

const untrackShopProductVariants = `-- name: UntrackShopProductVariants :exec
UPDATE shop_variants SET track_stock = false, updated_at = now()
WHERE  id IN (SELECT id FROM shop_variants
              WHERE product_id = $1 AND deleted_at IS NULL AND track_stock
              ORDER BY id FOR UPDATE)`

// UntrackShopProductVariants stops every live variant of a product from
// tracking stock (a product that became digital), locking them in id order.
func (q *Queries) UntrackShopProductVariants(ctx context.Context, productID uuid.UUID) error {
	_, err := q.db.Exec(ctx, untrackShopProductVariants, productID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Variants
// ─────────────────────────────────────────────────────────────────────────────

// ShopVariantRow is one live variant. Held is the quantity pending orders
// hold right now (status pending_payment, not past expires_at); Available is
// Stock - Held, never below 0.
type ShopVariantRow struct {
	ID             uuid.UUID `json:"id"`
	ProductID      uuid.UUID `json:"product_id"`
	Title          string    `json:"title"`
	SKU            *string   `json:"sku"`
	PriceMinor     int64     `json:"price_minor"`
	CompareAtMinor *int64    `json:"compare_at_minor"`
	Stock          int32     `json:"stock"`
	TrackStock     bool      `json:"track_stock"`
	Position       int32     `json:"position"`
	Option1        *string   `json:"option1"`
	Option2        *string   `json:"option2"`
	Option3        *string   `json:"option3"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Held           int32     `json:"held"`
}

// ShopHeldSQL is the quantity of variant v.id that pending orders hold. Every
// availability decision goes through it so the checkout and the catalog can
// never disagree.
const ShopHeldSQL = `COALESCE((SELECT sum(oi.qty) FROM shop_order_items oi
                 JOIN shop_orders o ON o.id = oi.order_id
                 WHERE oi.variant_id = v.id AND o.status = 'pending_payment' AND o.expires_at > now()), 0)::int`

const shopVariantColumns = `v.id, v.product_id, v.title, v.sku, v.price_minor, v.compare_at_minor, v.stock,
       v.track_stock, v.position, v.option1, v.option2, v.option3, v.created_at, v.updated_at, ` + ShopHeldSQL

func scanShopVariantRow(row interface{ Scan(dest ...any) error }) (ShopVariantRow, error) {
	var v ShopVariantRow
	err := row.Scan(&v.ID, &v.ProductID, &v.Title, &v.SKU, &v.PriceMinor, &v.CompareAtMinor, &v.Stock,
		&v.TrackStock, &v.Position, &v.Option1, &v.Option2, &v.Option3, &v.CreatedAt, &v.UpdatedAt, &v.Held)
	return v, err
}

const listShopVariants = `-- name: ListShopVariants :many
SELECT ` + shopVariantColumns + `
FROM   shop_variants v
WHERE  v.product_id = ANY($1::uuid[]) AND v.deleted_at IS NULL
ORDER  BY v.product_id, v.position, v.created_at, v.id`

// ListShopVariants lists the live variants of the given products in order.
func (q *Queries) ListShopVariants(ctx context.Context, productIDs []uuid.UUID) ([]ShopVariantRow, error) {
	rows, err := q.db.Query(ctx, listShopVariants, productIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopVariantRow
	for rows.Next() {
		v, err := scanShopVariantRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ShopVariantParams are the editable columns of a variant.
type ShopVariantParams struct {
	ID             uuid.UUID
	ProductID      uuid.UUID
	Title          string
	SKU            *string
	PriceMinor     int64
	CompareAtMinor *int64
	Stock          int32
	TrackStock     bool
	Position       int32
	Option1        *string
	Option2        *string
	Option3        *string
}

const insertShopVariant = `-- name: InsertShopVariant :one
INSERT INTO shop_variants (product_id, title, sku, price_minor, compare_at_minor, stock, track_stock,
                           position, option1, option2, option3)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id`

// InsertShopVariant creates a variant and returns its id (ID is ignored).
func (q *Queries) InsertShopVariant(ctx context.Context, p ShopVariantParams) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, insertShopVariant, p.ProductID, p.Title, p.SKU, p.PriceMinor, p.CompareAtMinor,
		p.Stock, p.TrackStock, p.Position, p.Option1, p.Option2, p.Option3).Scan(&id)
	return id, err
}

const updateShopVariant = `-- name: UpdateShopVariant :one
WITH old AS (
    SELECT id, stock FROM shop_variants
    WHERE  id = $1 AND product_id = $2 AND deleted_at IS NULL
    FOR UPDATE
)
UPDATE shop_variants v
SET    title = $3, sku = $4, price_minor = $5, compare_at_minor = $6, stock = $7, track_stock = $8,
       position = $9, option1 = $10, option2 = $11, option3 = $12, updated_at = now()
FROM   old
WHERE  v.id = old.id
RETURNING old.stock`

// UpdateShopVariant writes every editable column of a live variant of the
// product. pgx.ErrNoRows when the variant is not the product's. It returns
// the stock the row had before the update, for the stock journal.
func (q *Queries) UpdateShopVariant(ctx context.Context, p ShopVariantParams) (previousStock int32, err error) {
	err = q.db.QueryRow(ctx, updateShopVariant, p.ID, p.ProductID, p.Title, p.SKU, p.PriceMinor,
		p.CompareAtMinor, p.Stock, p.TrackStock, p.Position, p.Option1, p.Option2, p.Option3).Scan(&previousStock)
	return previousStock, err
}

const lockShopProductVariants = `-- name: LockShopProductVariants :many
SELECT id, stock FROM shop_variants
WHERE  product_id = $1 AND deleted_at IS NULL
ORDER  BY id
FOR UPDATE`

// LockShopProductVariants locks the product's live variants in id order (the
// order the checkout and the payment take them in) and returns each one's
// stock as it is under the lock.
func (q *Queries) LockShopProductVariants(ctx context.Context, productID uuid.UUID) (map[uuid.UUID]int32, error) {
	rows, err := q.db.Query(ctx, lockShopProductVariants, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int32{}
	for rows.Next() {
		var id uuid.UUID
		var stock int32
		if err := rows.Scan(&id, &stock); err != nil {
			return nil, err
		}
		out[id] = stock
	}
	return out, rows.Err()
}

const softDeleteShopVariantsExcept = `-- name: SoftDeleteShopVariantsExcept :execrows
UPDATE shop_variants SET deleted_at = now(), updated_at = now()
WHERE  product_id = $1 AND deleted_at IS NULL AND NOT (id = ANY($2::uuid[]))`

// SoftDeleteShopVariantsExcept deletes every live variant of the product that
// keep does not name. Order items keep pointing at a deleted variant.
func (q *Queries) SoftDeleteShopVariantsExcept(ctx context.Context, productID uuid.UUID, keep []uuid.UUID) (int64, error) {
	if keep == nil {
		keep = []uuid.UUID{}
	}
	tag, err := q.db.Exec(ctx, softDeleteShopVariantsExcept, productID, keep)
	return tag.RowsAffected(), err
}

// ShopCheckoutVariantRow is a variant locked for a checkout, with its product.
type ShopCheckoutVariantRow struct {
	ShopVariantRow
	ProductTitle  string
	ProductStatus string
	OrgID         uuid.UUID
	ProductKind   string
}

const lockShopVariantRows = `-- name: LockShopVariantRows :exec
SELECT id FROM shop_variants WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`

const readShopVariantsForCheckout = `-- name: ReadShopVariantsForCheckout :many
SELECT ` + shopVariantColumns + `, p.title, p.status, p.org_id, p.kind
FROM   shop_variants v
JOIN   shop_products p ON p.id = v.product_id
WHERE  v.id = ANY($1::uuid[]) AND v.deleted_at IS NULL AND p.deleted_at IS NULL AND p.org_id = $2
ORDER  BY v.id`

// LockShopVariantsForCheckout locks the variant rows among ids in id order
// (one lock order for every writer, so two carts never deadlock) in ONE
// statement, and only then reads the live variants of the organization's
// live products with their held quantity in a SECOND statement. The split is
// the point: under READ COMMITTED a statement that waited on a row lock
// re-checks only the locked row, so a held subquery evaluated in the locking
// statement itself would read the hold of the order that just committed as
// absent and both carts would sell the last unit. A variant that is not there
// (deleted, another organization's) is simply absent from the result.
func (q *Queries) LockShopVariantsForCheckout(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]ShopCheckoutVariantRow, error) {
	if _, err := q.db.Exec(ctx, lockShopVariantRows, ids); err != nil {
		return nil, err
	}
	rows, err := q.db.Query(ctx, readShopVariantsForCheckout, ids, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopCheckoutVariantRow
	for rows.Next() {
		var c ShopCheckoutVariantRow
		v := &c.ShopVariantRow
		if err := rows.Scan(&v.ID, &v.ProductID, &v.Title, &v.SKU, &v.PriceMinor, &v.CompareAtMinor, &v.Stock,
			&v.TrackStock, &v.Position, &v.Option1, &v.Option2, &v.Option3, &v.CreatedAt, &v.UpdatedAt, &v.Held,
			&c.ProductTitle, &c.ProductStatus, &c.OrgID, &c.ProductKind); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Pages
// ─────────────────────────────────────────────────────────────────────────────

// ShopPageRow is one static storefront page.
type ShopPageRow struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	BodyHTML  string    `json:"body_html"`
	Position  int32     `json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const shopPageColumns = `id, org_id, slug, title, body_html, position, created_at, updated_at`

func scanShopPageRow(row interface{ Scan(dest ...any) error }) (ShopPageRow, error) {
	var p ShopPageRow
	err := row.Scan(&p.ID, &p.OrgID, &p.Slug, &p.Title, &p.BodyHTML, &p.Position, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

const listShopPages = `-- name: ListShopPages :many
SELECT ` + shopPageColumns + ` FROM shop_pages WHERE org_id = $1 ORDER BY position, slug`

// ListShopPages lists the static pages of an organization's storefront.
func (q *Queries) ListShopPages(ctx context.Context, orgID uuid.UUID) ([]ShopPageRow, error) {
	rows, err := q.db.Query(ctx, listShopPages, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopPageRow
	for rows.Next() {
		p, err := scanShopPageRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const upsertShopPage = `-- name: UpsertShopPage :one
INSERT INTO shop_pages (org_id, slug, title, body_html, position)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (org_id, slug) DO UPDATE SET title = EXCLUDED.title, body_html = EXCLUDED.body_html,
       position = EXCLUDED.position, updated_at = now()
RETURNING ` + shopPageColumns

// UpsertShopPage creates or replaces a static page by slug.
func (q *Queries) UpsertShopPage(ctx context.Context, orgID uuid.UUID, slug, title, body string, position int32) (ShopPageRow, error) {
	return scanShopPageRow(q.db.QueryRow(ctx, upsertShopPage, orgID, slug, title, body, position))
}

const deleteShopPage = `-- name: DeleteShopPage :execrows
DELETE FROM shop_pages WHERE org_id = $1 AND slug = $2`

// DeleteShopPage removes a static page.
func (q *Queries) DeleteShopPage(ctx context.Context, orgID uuid.UUID, slug string) (bool, error) {
	tag, err := q.db.Exec(ctx, deleteShopPage, orgID, slug)
	return tag.RowsAffected() == 1, err
}
