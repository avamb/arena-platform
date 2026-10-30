package hshop

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// settingsJSON is the shop settings as the organization routes answer them.
type settingsJSON struct {
	OrgID            string     `json:"org_id"`
	Configured       bool       `json:"configured"`
	Currency         string     `json:"currency"`
	ShippingFeeMinor int64      `json:"shipping_fee_minor"`
	ShippingLabel    string     `json:"shipping_label"`
	CountryISO2      *string    `json:"country_iso2"`
	AgeGateMinAge    *int32     `json:"age_gate_min_age"`
	AnnouncementText string     `json:"announcement_text"`
	ContactEmail     *string    `json:"contact_email"`
	LogisticsNote    string     `json:"logistics_note"`
	SalesChannelID   *string    `json:"sales_channel_id"`
	ShopDomain       *string    `json:"shop_domain"`
	MaxLineQty       int32      `json:"max_line_qty"`
	Enabled          bool       `json:"enabled"`
	UpdatedAt        *time.Time `json:"updated_at"`
}

func uuidPtrStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func toSettingsJSON(v shop.SettingsView) settingsJSON {
	out := settingsJSON{
		OrgID: v.OrgID.String(), Configured: v.Configured, Currency: v.Currency,
		ShippingFeeMinor: v.ShippingFeeMinor, ShippingLabel: v.ShippingLabel, CountryISO2: v.CountryISO2,
		AgeGateMinAge: v.AgeGateMinAge, AnnouncementText: v.AnnouncementText, ContactEmail: v.ContactEmail,
		LogisticsNote: v.LogisticsNote, SalesChannelID: uuidPtrStr(v.SalesChannelID), ShopDomain: v.ShopDomain,
		MaxLineQty: v.MaxLineQty, Enabled: v.Enabled,
	}
	if v.Configured {
		t := v.UpdatedAt
		out.UpdatedAt = &t
	}
	return out
}

// imageJSON is one product picture.
type imageJSON struct {
	MediaID  string `json:"media_id"`
	URL      string `json:"url"`
	Position int32  `json:"position"`
	Alt      string `json:"alt"`
	// Width and Height are the picture's pixel size, for the card's aspect
	// before it loads; absent when unknown.
	Width  *int32 `json:"width,omitempty"`
	Height *int32 `json:"height,omitempty"`
}

// variantJSON is one variant as the organization sees it.
type variantJSON struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	SKU            *string `json:"sku"`
	PriceMinor     int64   `json:"price_minor"`
	CompareAtMinor *int64  `json:"compare_at_minor"`
	Stock          int32   `json:"stock"`
	TrackStock     bool    `json:"track_stock"`
	Held           int32   `json:"held"`
	Available      *int    `json:"available"`
	Position       int32   `json:"position"`
	Option1        *string `json:"option1"`
	Option2        *string `json:"option2"`
	Option3        *string `json:"option3"`
}

// productJSON is a product as the organization routes answer it.
type productJSON struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	BodyHTML    string `json:"body_html"`
	Status      string `json:"status"`
	ProductType string `json:"product_type"`
	Vendor      string `json:"vendor"`
	SortOrder   int32  `json:"sort_order"`
	Kind        string `json:"kind"`
	// DigitalMediaID is the file a digital product sells; never on the
	// storefront.
	DigitalMediaID *string       `json:"digital_media_id"`
	OptionNames    []string      `json:"option_names"`
	Images         []imageJSON   `json:"images"`
	Variants       []variantJSON `json:"variants"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// available is nil for a variant that does not track stock (unlimited).
func available(v gen.ShopVariantRow) *int {
	a := shop.Available(v)
	if a < 0 {
		return nil
	}
	return &a
}

func (h *Handler) images(ctx context.Context, rows []gen.ShopProductImageRow) []imageJSON {
	out := make([]imageJSON, 0, len(rows))
	for _, i := range rows {
		url := ""
		if h.o.MediaURL != nil {
			url = h.o.MediaURL(ctx, i.MediaID)
		}
		out = append(out, imageJSON{MediaID: i.MediaID.String(), URL: url, Position: i.Position, Alt: i.Alt,
			Width: i.Width, Height: i.Height})
	}
	return out
}

func (h *Handler) toProductJSON(ctx context.Context, p shop.ProductView) productJSON {
	out := productJSON{
		ID: p.ID.String(), OrgID: p.OrgID.String(), Slug: p.Slug, Title: p.Title, BodyHTML: p.BodyHTML,
		Status: p.Status, ProductType: p.ProductType, Vendor: p.Vendor, SortOrder: p.SortOrder,
		Kind: p.Kind, DigitalMediaID: uuidPtrStr(p.DigitalMediaID), OptionNames: shop.OptionNames(p.ShopProductRow),
		Images: h.images(ctx, p.Images), Variants: make([]variantJSON, 0, len(p.Variants)),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	for _, v := range p.Variants {
		out.Variants = append(out.Variants, variantJSON{
			ID: v.ID.String(), Title: v.Title, SKU: v.SKU, PriceMinor: v.PriceMinor, CompareAtMinor: v.CompareAtMinor,
			Stock: v.Stock, TrackStock: v.TrackStock, Held: v.Held, Available: available(v), Position: v.Position,
			Option1: v.Option1, Option2: v.Option2, Option3: v.Option3,
		})
	}
	return out
}

// publicAvailable is the quantity the storefront may show and offer: the shelf
// stock capped at the most one order can hold (settings.max_line_qty), so the
// catalog never discloses the shelf beyond what a buyer can put in a cart. A
// variant that does not track stock offers max_line_qty; a shop with no line
// limit (0) reports the shelf figure, nil for untracked stock.
func publicAvailable(shelf *int, maxLineQty int32) *int {
	limit := int(maxLineQty)
	if limit <= 0 {
		return shelf
	}
	if shelf == nil || *shelf > limit {
		return &limit
	}
	return shelf
}

// publicVariantJSON is a variant as the storefront sees it: no stock counts
// beyond what a buyer can order, and no article number.
type publicVariantJSON struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	PriceMinor     int64   `json:"price_minor"`
	CompareAtMinor *int64  `json:"compare_at_minor"`
	Available      *int    `json:"available"`
	InStock        bool    `json:"in_stock"`
	Option1        *string `json:"option1"`
	Option2        *string `json:"option2"`
	Option3        *string `json:"option3"`
}

// publicProductJSON is a product on the storefront.
type publicProductJSON struct {
	Slug          string              `json:"slug"`
	Title         string              `json:"title"`
	BodyHTML      string              `json:"body_html"`
	ProductType   string              `json:"product_type"`
	Vendor        string              `json:"vendor"`
	Kind          string              `json:"kind"`
	OptionNames   []string            `json:"option_names"`
	PriceMinMinor int64               `json:"price_min_minor"`
	Images        []imageJSON         `json:"images"`
	Variants      []publicVariantJSON `json:"variants"`
}

func (h *Handler) toPublicProduct(ctx context.Context, p shop.ProductView, maxLineQty int32) publicProductJSON {
	out := publicProductJSON{
		Slug: p.Slug, Title: p.Title, BodyHTML: p.BodyHTML, ProductType: p.ProductType, Vendor: p.Vendor,
		Kind: p.Kind, OptionNames: shop.OptionNames(p.ShopProductRow),
		Images: h.images(ctx, p.Images), Variants: make([]publicVariantJSON, 0, len(p.Variants)),
	}
	for i, v := range p.Variants {
		if i == 0 || v.PriceMinor < out.PriceMinMinor {
			out.PriceMinMinor = v.PriceMinor
		}
		shelf := available(v)
		out.Variants = append(out.Variants, publicVariantJSON{
			ID: v.ID.String(), Title: v.Title, PriceMinor: v.PriceMinor, CompareAtMinor: v.CompareAtMinor,
			Available: publicAvailable(shelf, maxLineQty), InStock: shelf == nil || *shelf > 0, Option1: v.Option1, Option2: v.Option2, Option3: v.Option3,
		})
	}
	return out
}

// pageJSON is a static storefront page.
type pageJSON struct {
	Slug       string    `json:"slug"`
	LegacySlug *string   `json:"legacy_slug"`
	Title      string    `json:"title"`
	BodyHTML   string    `json:"body_html"`
	Position   int32     `json:"position"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toPageJSON(p gen.ShopPageRow) pageJSON {
	return pageJSON{Slug: p.Slug, LegacySlug: p.LegacySlug, Title: p.Title, BodyHTML: p.BodyHTML, Position: p.Position, UpdatedAt: p.UpdatedAt}
}

// orderItemJSON is one order line.
type orderItemJSON struct {
	VariantID      *string `json:"variant_id"`
	ProductTitle   string  `json:"product_title"`
	VariantTitle   string  `json:"variant_title"`
	SKU            *string `json:"sku"`
	Qty            int32   `json:"qty"`
	UnitPriceMinor int64   `json:"unit_price_minor"`
	TotalMinor     int64   `json:"total_minor"`
	ProductKind    string  `json:"product_kind"`
}

func toItemsJSON(items []gen.ShopOrderItemRow) []orderItemJSON {
	out := make([]orderItemJSON, 0, len(items))
	for _, it := range items {
		out = append(out, orderItemJSON{
			VariantID: uuidPtrStr(it.VariantID), ProductTitle: it.ProductTitle, VariantTitle: it.VariantTitle,
			SKU: it.SKU, Qty: it.Qty, UnitPriceMinor: it.UnitPriceMinor, TotalMinor: it.TotalMinor,
			ProductKind: it.ProductKind,
		})
	}
	return out
}

// paymentJSON is the payment of an order.
type paymentJSON struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	State    string `json:"state"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// addressJSON is where the goods go.
type addressJSON struct {
	Name     string `json:"name"`
	Address1 string `json:"address1"`
	Address2 string `json:"address2"`
	City     string `json:"city"`
	Postal   string `json:"postal"`
	Country  string `json:"country"`
}

// buyerJSON is who paid.
type buyerJSON struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

// staffNoteJSON is the internal note of an order; empty text means no note.
type staffNoteJSON struct {
	Text string     `json:"text"`
	By   string     `json:"by"`
	At   *time.Time `json:"at"`
}

// orderJSON is an order as the organization routes answer it.
type orderJSON struct {
	ID            string          `json:"id"`
	OrgID         string          `json:"org_id"`
	Number        int64           `json:"number"`
	Status        string          `json:"status"`
	Source        string          `json:"source"`
	ExternalRef   *string         `json:"external_ref"`
	CustomerID    *string         `json:"customer_id"`
	Buyer         buyerJSON       `json:"buyer"`
	Ship          addressJSON     `json:"ship"`
	DeliveryKind  string          `json:"delivery_kind"`
	DeliveryNote  string          `json:"delivery_note"`
	Currency      string          `json:"currency"`
	Subtotal      int64           `json:"subtotal"`
	Discount      int64           `json:"discount"`
	ShippingFee   int64           `json:"shipping_fee"`
	Total         int64           `json:"total"`
	RefundedMinor int64           `json:"refunded_minor"`
	BuyerLocale   *string         `json:"buyer_locale"`
	TrackingNote  string          `json:"tracking_note"`
	StaffNote     staffNoteJSON   `json:"staff_note"`
	Items         []orderItemJSON `json:"items"`
	Payment       *paymentJSON    `json:"payment"`
	ExpiresAt     *time.Time      `json:"expires_at"`
	PaidAt        *time.Time      `json:"paid_at"`
	ShippedAt     *time.Time      `json:"shipped_at"`
	CancelledAt   *time.Time      `json:"cancelled_at"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func toOrderJSON(v shop.OrderView) orderJSON {
	o := v.ShopOrderRow
	out := orderJSON{
		ID: o.ID.String(), OrgID: o.OrgID.String(), Number: o.Number, Status: o.Status, Source: o.Source,
		ExternalRef: o.ExternalRef, CustomerID: uuidPtrStr(o.CustomerID),
		Buyer: buyerJSON{Name: o.BuyerName, Email: o.BuyerEmail, Phone: o.BuyerPhone},
		Ship: addressJSON{Name: o.ShipName, Address1: o.ShipAddress1, Address2: o.ShipAddress2, City: o.ShipCity,
			Postal: o.ShipPostal, Country: o.ShipCountry},
		DeliveryKind: o.DeliveryKind, DeliveryNote: o.DeliveryNote, Currency: o.Currency,
		Subtotal: o.Subtotal, Discount: o.Discount, ShippingFee: o.ShippingFee, Total: o.Total,
		RefundedMinor: o.RefundedMinor, BuyerLocale: o.BuyerLocale, TrackingNote: o.TrackingNote,
		StaffNote: staffNoteJSON{Text: o.StaffNote, By: o.StaffNoteBy, At: o.StaffNoteAt},
		Items:     toItemsJSON(v.Items), ExpiresAt: o.ExpiresAt, PaidAt: o.PaidAt, ShippedAt: o.ShippedAt,
		CancelledAt: o.CancelledAt, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
	if v.Payment != nil {
		out.Payment = &paymentJSON{ID: v.Payment.ID.String(), Provider: v.Payment.Provider, State: v.Payment.State,
			Amount: v.Payment.Amount, Currency: v.Payment.Currency}
	}
	return out
}
