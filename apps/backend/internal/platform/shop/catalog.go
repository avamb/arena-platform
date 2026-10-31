package shop

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	unorm "golang.org/x/text/unicode/norm"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// DefaultCurrency is the currency of a shop whose settings were never saved.
const DefaultCurrency = "EUR"

// DefaultMaxLineQty is how many units of one variant an order may hold when
// the settings say nothing else (shop_settings.max_line_qty).
const DefaultMaxLineQty = 24

// DefaultVariantTitle is the title of the one variant a simple product has
// (the Shopify and Dawn convention).
const DefaultVariantTitle = "Default Title"

// ─────────────────────────────────────────────────────────────────────────────
// Settings
// ─────────────────────────────────────────────────────────────────────────────

// SettingsView is the shop settings of an organization. Configured is false
// when the row does not exist yet (the defaults are shown).
type SettingsView struct {
	gen.ShopSettingsRow
	Configured bool
}

// GetSettings reads the settings, or the defaults when there are none yet.
func (s *Service) GetSettings(ctx context.Context, orgID uuid.UUID) (SettingsView, error) {
	if err := s.ready(); err != nil {
		return SettingsView{}, err
	}
	row, err := s.q.GetShopSettings(ctx, orgID)
	if err == nil {
		return SettingsView{ShopSettingsRow: row, Configured: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SettingsView{}, err
	}
	domain, derr := s.q.GetOrganizationShopDomain(ctx, orgID)
	if derr != nil && !errors.Is(derr, pgx.ErrNoRows) {
		return SettingsView{}, derr
	}
	return SettingsView{ShopSettingsRow: gen.ShopSettingsRow{
		OrgID: orgID, Currency: DefaultCurrency, ShopDomain: domain, MaxLineQty: DefaultMaxLineQty,
	}}, nil
}

// SettingsInput is the whole settings row (PUT replaces it). ShopDomain nil
// keeps the storefront host, "" clears it; only a caller with
// MayChangeDomain (a platform superadmin) may change it.
type SettingsInput struct {
	Currency         string
	ShippingFeeMinor int64
	ShippingLabel    string
	CountryISO2      *string
	AgeGateMinAge    *int32
	AnnouncementText string
	ContactEmail     *string
	LogisticsNote    string
	SalesChannelID   *uuid.UUID
	ShopDomain       *string
	MayChangeDomain  bool
	// MaxLineQty caps the units of one variant per order (1-99); nil keeps
	// the stored value (24 for a new shop).
	MaxLineQty *int32
	// Enabled opens (true) or closes (false) the storefront and its
	// checkout; nil keeps it. Like ShopDomain, only a caller with
	// MayChangeDomain (a platform superadmin) may change it.
	Enabled *bool
	// ExpectedUpdatedAt is the updated_at the editor loaded the settings
	// with. When it is set and the stored row has moved on (a colleague
	// saved meanwhile), the write is refused with 409 shop.settings_changed
	// and nothing is applied. nil skips the check.
	ExpectedUpdatedAt *time.Time
}

// CurrencySource tells which currencies a payment provider can charge in.
// An empty answer means the provider does not restrict the currency (or is
// not known), so nothing is refused on its account.
type CurrencySource func(provider string) []string

// sameInstant compares two instants at the precision PostgreSQL stores
// (microseconds), so a value that went through JSON still matches.
func sameInstant(a, b time.Time) bool {
	return a.UTC().Truncate(time.Microsecond).Equal(b.UTC().Truncate(time.Microsecond))
}

// checkCurrencySupported refuses a currency the shop's payment channel cannot
// charge in. channel nil means "the organization's own pick at checkout":
// the currency is then refused only when every live channel's provider
// refuses it. No channels, or a provider that states no list, refuse nothing.
func (s *Service) checkCurrencySupported(ctx context.Context, q *gen.Queries, orgID uuid.UUID, channel *uuid.UUID, currency string) error {
	if s.currencies == nil {
		return nil
	}
	var ids []uuid.UUID
	if channel != nil {
		ids = []uuid.UUID{*channel}
	} else {
		live, err := q.ListLiveSalesChannelIDs(ctx, orgID, MaxPaymentChannelCandidates)
		if err != nil {
			return err
		}
		ids = live
	}
	var providers, allowed []string
	for _, id := range ids {
		ch, err := q.GetSalesChannelByIDAnyKind(ctx, id, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		list := s.currencies(ch.Provider)
		if len(list) == 0 {
			return nil
		}
		for _, c := range list {
			if c == currency {
				return nil
			}
		}
		providers = append(providers, ch.Provider)
		if allowed == nil {
			allowed = list
		}
	}
	if len(providers) == 0 {
		return nil
	}
	e := invalid("shop.currency_unsupported", "the payment provider of the shop's sales channel cannot charge in "+currency)
	e.Details = map[string]any{"currency": currency, "providers": providers, "allowed": allowed}
	return e
}

func invalid(code, msg string) *Error { return newErr(http.StatusUnprocessableEntity, code, msg) }

// validateSettings checks and normalizes the input in place.
func validateSettings(in *SettingsInput) error {
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if !ValidCurrency(in.Currency) {
		return invalid("shop.invalid_currency", "currency must be a three-letter ISO code such as EUR or ILS")
	}
	if in.ShippingFeeMinor < 0 {
		return invalid("shop.invalid_shipping_fee", "shipping_fee_minor must be zero or more")
	}
	if in.CountryISO2 != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.CountryISO2))
		if c == "" {
			in.CountryISO2 = nil
		} else if !ValidCountry(c) {
			return invalid("shop.invalid_country", "country_iso2 must be a two-letter ISO country code")
		} else {
			in.CountryISO2 = &c
		}
	}
	if in.MaxLineQty != nil && (*in.MaxLineQty < 1 || *in.MaxLineQty > MaxLineQty) {
		return invalid("shop.invalid_max_line_qty", "max_line_qty must be between 1 and 99")
	}
	if in.AgeGateMinAge != nil && (*in.AgeGateMinAge < 1 || *in.AgeGateMinAge > 99) {
		return invalid("shop.invalid_age_gate", "age_gate_min_age must be between 1 and 99, or null for no age gate")
	}
	if in.ContactEmail != nil {
		e := strings.TrimSpace(*in.ContactEmail)
		if e == "" {
			in.ContactEmail = nil
		} else if !ValidEmail(e) {
			return invalid("shop.invalid_contact_email", "contact_email is not a valid e-mail address")
		} else {
			in.ContactEmail = &e
		}
	}
	if !textOK(in.ShippingLabel, MaxShortTextLen) || !textOK(in.AnnouncementText, MaxAnnouncement) || !textOK(in.LogisticsNote, MaxNoteLen) {
		return invalid("shop.text_too_long", "shipping_label is limited to 200 characters, announcement_text to 500, logistics_note to 2000")
	}
	return nil
}

// PutSettings validates and writes the settings and returns them.
func (s *Service) PutSettings(ctx context.Context, orgID uuid.UUID, in SettingsInput) (SettingsView, error) {
	if err := validateSettings(&in); err != nil {
		return SettingsView{}, err
	}
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		// Locked: an owner's save must not write a stale enabled (or
		// max_line_qty) back over the operator's change made meanwhile.
		cur, err := q.GetShopSettingsForUpdate(ctx, orgID)
		existing := true
		if errors.Is(err, pgx.ErrNoRows) {
			existing = false
			cur = gen.ShopSettingsRow{MaxLineQty: DefaultMaxLineQty}
		} else if err != nil {
			return err
		}
		if in.ExpectedUpdatedAt != nil && (!existing || !sameInstant(cur.UpdatedAt, *in.ExpectedUpdatedAt)) {
			e := newErr(http.StatusConflict, "shop.settings_changed",
				"the shop settings were saved by someone else since they were loaded; reload them and apply the change again")
			if existing {
				e.Details = map[string]any{"updated_at": cur.UpdatedAt.UTC().Format(time.RFC3339Nano)}
			}
			return e
		}
		sameChannel := existing && ((cur.SalesChannelID == nil && in.SalesChannelID == nil) ||
			(cur.SalesChannelID != nil && in.SalesChannelID != nil && *cur.SalesChannelID == *in.SalesChannelID))
		if !existing || in.Currency != cur.Currency || !sameChannel {
			if existing && in.Currency != cur.Currency {
				// Variant prices are minor units: a new currency would turn 160 ILS into 160 EUR.
				used, err := q.ShopHasProductsOrOrders(ctx, orgID)
				if err != nil {
					return err
				}
				if used {
					e := newErr(http.StatusConflict, "shop.currency_locked",
						"the shop already has products or orders in "+cur.Currency+"; prices are not converted, so the currency cannot change")
					e.Details = map[string]any{"currency": cur.Currency}
					return e
				}
			}
		}
		maxLine := cur.MaxLineQty
		if in.MaxLineQty != nil {
			maxLine = *in.MaxLineQty
		}
		enabled := cur.Enabled
		if in.Enabled != nil && *in.Enabled != cur.Enabled {
			if !in.MayChangeDomain {
				return newErr(http.StatusForbidden, "shop.enabled_superadmin_only", "only the platform operator can open or close the shop")
			}
			enabled = *in.Enabled
		}
		if in.SalesChannelID != nil {
			if _, err := q.GetSalesChannelByID(ctx, *in.SalesChannelID, orgID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return invalid("shop.invalid_channel", "sales_channel_id is not a sales channel of this organization")
				}
				return err
			}
		}
		if !existing || in.Currency != cur.Currency || !sameChannel {
			if err := s.checkCurrencySupported(ctx, q, orgID, in.SalesChannelID, in.Currency); err != nil {
				return err
			}
		}
		if in.ShopDomain != nil {
			domain, ok := NormalizeDomain(*in.ShopDomain)
			if !ok {
				return invalid("shop.invalid_domain", "shop_domain must be a bare host name such as shop.example.com")
			}
			current, err := q.GetOrganizationShopDomain(ctx, orgID)
			if err != nil {
				return err
			}
			cur := ""
			if current != nil {
				cur = *current
			}
			if domain != cur {
				if !in.MayChangeDomain {
					return newErr(http.StatusForbidden, "shop.domain_superadmin_only", "only the platform operator can change the shop domain")
				}
				var d *string
				if domain != "" {
					d = &domain
				}
				if err := q.SetOrganizationShopDomain(ctx, orgID, d); err != nil {
					if isUniqueViolation(err, "organizations_shop_domain_uq") {
						return newErr(http.StatusConflict, "shop.duplicate_domain", "another organization already uses this shop domain")
					}
					return err
				}
			}
		}
		return q.UpsertShopSettings(ctx, gen.UpsertShopSettingsParams{
			OrgID: orgID, Currency: in.Currency, ShippingFeeMinor: in.ShippingFeeMinor,
			ShippingLabel: in.ShippingLabel, CountryISO2: in.CountryISO2, AgeGateMinAge: in.AgeGateMinAge,
			AnnouncementText: in.AnnouncementText, ContactEmail: in.ContactEmail,
			LogisticsNote: in.LogisticsNote, SalesChannelID: in.SalesChannelID,
			Enabled: enabled, MaxLineQty: maxLine,
		})
	})
	if err != nil {
		return SettingsView{}, err
	}
	return s.GetSettings(ctx, orgID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Products
// ─────────────────────────────────────────────────────────────────────────────

// ProductView is a product with its pictures and live variants.
type ProductView struct {
	gen.ShopProductRow
	Images   []gen.ShopProductImageRow
	Variants []gen.ShopVariantRow
}

// views attaches pictures and variants to products, keeping their order.
func views(ctx context.Context, q *gen.Queries, products []gen.ShopProductRow) ([]ProductView, error) {
	out := make([]ProductView, 0, len(products))
	if len(products) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(products))
	for i, p := range products {
		ids[i] = p.ID
	}
	imgs, err := q.ListShopProductImages(ctx, ids)
	if err != nil {
		return nil, err
	}
	vars, err := q.ListShopVariants(ctx, ids)
	if err != nil {
		return nil, err
	}
	byImg := map[uuid.UUID][]gen.ShopProductImageRow{}
	for _, i := range imgs {
		byImg[i.ProductID] = append(byImg[i.ProductID], i)
	}
	byVar := map[uuid.UUID][]gen.ShopVariantRow{}
	for _, v := range vars {
		byVar[v.ProductID] = append(byVar[v.ProductID], v)
	}
	for _, p := range products {
		out = append(out, ProductView{ShopProductRow: p, Images: byImg[p.ID], Variants: byVar[p.ID]})
	}
	return out, nil
}

// ListProducts lists the organization's products; status "" lists all.
func (s *Service) ListProducts(ctx context.Context, orgID uuid.UUID, status string) ([]ProductView, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if status != "" && status != ProductDraft && status != ProductActive && status != ProductArchived {
		return nil, newErr(http.StatusBadRequest, "shop.invalid_status", "status must be draft, active or archived")
	}
	ps, err := s.q.ListShopProducts(ctx, orgID, status)
	if err != nil {
		return nil, err
	}
	return views(ctx, s.q, ps)
}

// GetProduct reads one product of the organization.
func (s *Service) GetProduct(ctx context.Context, orgID, id uuid.UUID) (ProductView, error) {
	if err := s.ready(); err != nil {
		return ProductView{}, err
	}
	return getProductView(ctx, s.q, orgID, id)
}

func getProductView(ctx context.Context, q *gen.Queries, orgID, id uuid.UUID) (ProductView, error) {
	p, err := q.GetShopProduct(ctx, id, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProductView{}, ErrProductNotFound
	}
	if err != nil {
		return ProductView{}, err
	}
	vs, err := views(ctx, q, []gen.ShopProductRow{p})
	if err != nil {
		return ProductView{}, err
	}
	return vs[0], nil
}

// GetActiveProductBySlug reads a product the storefront may show.
func (s *Service) GetActiveProductBySlug(ctx context.Context, orgID uuid.UUID, slug string) (ProductView, error) {
	if err := s.ready(); err != nil {
		return ProductView{}, err
	}
	norm, ok := NormalizeSlug(slug)
	if !ok {
		return ProductView{}, ErrProductNotFound
	}
	p, err := s.q.GetShopProductBySlug(ctx, orgID, norm)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.Status != ProductActive) {
		return ProductView{}, ErrProductNotFound
	}
	if err != nil {
		return ProductView{}, err
	}
	vs, err := views(ctx, s.q, []gen.ShopProductRow{p})
	if err != nil {
		return ProductView{}, err
	}
	return vs[0], nil
}

// ProductInput carries the editable fields of a product. On create a nil
// field takes its default (slug from the title, status draft); on update a nil
// field keeps the stored value.
type ProductInput struct {
	Slug        *string
	Title       *string
	BodyHTML    *string
	Status      *string
	ProductType *string
	Vendor      *string
	SortOrder   *int32
	// Kind is physical, digital or service; nil keeps it (physical on
	// create). Turning a product digital stops its variants tracking stock;
	// turning it away from digital drops its file.
	Kind *string
	// DigitalMediaID names the file a digital product sells (a media object
	// of the organization with owner_type shop_digital); nil keeps it,
	// ClearDigitalMedia removes it.
	DigitalMediaID    *uuid.UUID
	ClearDigitalMedia bool
	// OptionNames names the product's options (up to 3, e.g. Size, Colour),
	// matched by position with the variants' option1..option3 values; nil
	// keeps them, an empty list removes them.
	OptionNames *[]string
}

// apply merges the input over p and validates the result.
func (in ProductInput) apply(p *gen.ShopProductParams) error {
	if in.Title != nil {
		p.Title = strings.TrimSpace(*in.Title)
	}
	if p.Title == "" || !textOK(p.Title, MaxTitleLen) {
		return invalid("shop.invalid_title", "title is required and limited to 300 characters")
	}
	if in.Slug != nil {
		slug, ok := NormalizeSlug(*in.Slug)
		if !ok {
			return invalid("shop.invalid_slug", "slug must be 1-200 characters without spaces, '/', '?' or '#'")
		}
		p.Slug = slug
	}
	if p.Slug == "" {
		p.Slug = SlugFromTitle(p.Title)
	}
	if in.BodyHTML != nil {
		body, err := SanitizeBody(*in.BodyHTML, ProductBodyProfile)
		if err != nil {
			return err
		}
		p.BodyHTML = body
	}
	if in.Status != nil {
		p.Status = strings.TrimSpace(*in.Status)
	}
	if p.Status == "" {
		p.Status = ProductDraft
	}
	if p.Status != ProductDraft && p.Status != ProductActive && p.Status != ProductArchived {
		return invalid("shop.invalid_status", "status must be draft, active or archived")
	}
	if in.ProductType != nil {
		p.ProductType = strings.TrimSpace(*in.ProductType)
	}
	if in.Vendor != nil {
		p.Vendor = strings.TrimSpace(*in.Vendor)
	}
	if !textOK(p.ProductType, MaxShortTextLen) || !textOK(p.Vendor, MaxShortTextLen) {
		return invalid("shop.text_too_long", "product_type and vendor are limited to 200 characters")
	}
	if in.SortOrder != nil {
		p.SortOrder = *in.SortOrder
	}
	if in.Kind != nil {
		p.Kind = strings.TrimSpace(*in.Kind)
	}
	if p.Kind == "" {
		p.Kind = KindPhysical
	}
	if p.Kind != KindPhysical && p.Kind != KindDigital && p.Kind != KindService {
		return invalid("shop.invalid_kind", "kind must be physical, digital or service")
	}
	if in.ClearDigitalMedia {
		p.DigitalMediaID = nil
	}
	if in.DigitalMediaID != nil {
		if p.Kind != KindDigital {
			return invalid("shop.invalid_digital_media", "only a digital product sells a file (digital_media_id needs kind digital)")
		}
		id := *in.DigitalMediaID
		p.DigitalMediaID = &id
	}
	if p.Kind != KindDigital {
		p.DigitalMediaID = nil
	}
	if in.OptionNames != nil {
		names := *in.OptionNames
		if len(names) > MaxOptionNames {
			return invalid("shop.invalid_option_names", "a product has at most 3 option names")
		}
		slots := [MaxOptionNames]*string{}
		for i, n := range names {
			n = strings.TrimSpace(n)
			if n == "" || !textOK(n, 100) {
				return invalid("shop.invalid_option_names", "an option name is 1-100 characters")
			}
			slots[i] = &n
		}
		p.Option1Name, p.Option2Name, p.Option3Name = slots[0], slots[1], slots[2]
	}
	return nil
}

// checkDigitalMedia refuses a file that is not the organization's own
// shop_digital upload, when the product names one it did not name before.
func checkDigitalMedia(ctx context.Context, q *gen.Queries, orgID uuid.UUID, p gen.ShopProductParams, before *uuid.UUID) error {
	if p.DigitalMediaID == nil || (before != nil && *before == *p.DigitalMediaID) {
		return nil
	}
	ok, err := q.ShopDigitalMediaUsable(ctx, *p.DigitalMediaID, orgID)
	if err != nil {
		return err
	}
	if !ok {
		return invalid("shop.invalid_digital_media", "digital_media_id is not a file this organization uploaded with owner_type shop_digital")
	}
	return nil
}

// OptionNames lists a product's option names in order.
func OptionNames(p gen.ShopProductRow) []string {
	out := make([]string, 0, MaxOptionNames)
	for _, n := range []*string{p.Option1Name, p.Option2Name, p.Option3Name} {
		if n != nil {
			out = append(out, *n)
		}
	}
	return out
}

var errDuplicateSlug = newErr(http.StatusConflict, "shop.duplicate_slug", "another product of this shop already uses this slug")

// CreateProduct creates a product with its variants (one default variant
// when none is given) and the shop settings row when it is the first product.
func (s *Service) CreateProduct(ctx context.Context, orgID uuid.UUID, in ProductInput, variants []VariantInput) (ProductView, error) {
	p := gen.ShopProductParams{OrgID: orgID}
	if err := in.apply(&p); err != nil {
		return ProductView{}, err
	}
	if len(variants) == 0 {
		variants = []VariantInput{{Title: DefaultVariantTitle}}
	}
	if err := validateVariants(variants); err != nil {
		return ProductView{}, err
	}
	var id uuid.UUID
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		if err := q.EnsureShopSettings(ctx, orgID); err != nil {
			return err
		}
		if err := checkDigitalMedia(ctx, q, orgID, p, nil); err != nil {
			return err
		}
		row, err := q.InsertShopProduct(ctx, p)
		if err != nil {
			if isUniqueViolation(err, "shop_products_org_slug_uq") {
				return errDuplicateSlug
			}
			return err
		}
		id = row.ID
		return writeVariants(ctx, q, orgID, row.ID, row.Kind, variants, nil)
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, orgID, id)
}

// UpdateProduct merges in over the stored product, under the product's row
// lock (ReplaceVariants takes the same lock, so a kind change cannot leave
// tracked variants on a digital product).
func (s *Service) UpdateProduct(ctx context.Context, orgID, id uuid.UUID, in ProductInput) (ProductView, error) {
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		cur, err := q.GetShopProductForUpdate(ctx, id, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProductNotFound
		}
		if err != nil {
			return err
		}
		p := gen.ShopProductParams{
			ID: id, OrgID: orgID, Slug: cur.Slug, Title: cur.Title, BodyHTML: cur.BodyHTML,
			Status: cur.Status, ProductType: cur.ProductType, Vendor: cur.Vendor, SortOrder: cur.SortOrder,
			Kind: cur.Kind, DigitalMediaID: cur.DigitalMediaID,
			Option1Name: cur.Option1Name, Option2Name: cur.Option2Name, Option3Name: cur.Option3Name,
		}
		if err := in.apply(&p); err != nil {
			return err
		}
		if err := checkDigitalMedia(ctx, q, orgID, p, cur.DigitalMediaID); err != nil {
			return err
		}
		if _, err := q.UpdateShopProduct(ctx, p); err != nil {
			if isUniqueViolation(err, "shop_products_org_slug_uq") {
				return errDuplicateSlug
			}
			return err
		}
		if p.Kind == KindDigital && cur.Kind != KindDigital {
			return q.UntrackShopProductVariants(ctx, id)
		}
		return nil
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, orgID, id)
}

// DeleteProduct soft-deletes a product. Orders keep their snapshot.
func (s *Service) DeleteProduct(ctx context.Context, orgID, id uuid.UUID) error {
	if err := s.ready(); err != nil {
		return err
	}
	ok, err := s.q.SoftDeleteShopProduct(ctx, id, orgID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrProductNotFound
	}
	return nil
}

// AddImage attaches one of the organization's media objects to a product;
// position nil appends it after the last picture. Adding an attached picture
// again moves and relabels it.
func (s *Service) AddImage(ctx context.Context, orgID, productID, mediaID uuid.UUID, position *int32, alt string) (ProductView, error) {
	alt = strings.TrimSpace(alt)
	if !textOK(alt, MaxImageAltLen) {
		return ProductView{}, invalid("shop.text_too_long", "alt is limited to 500 characters")
	}
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		if _, err := q.GetShopProduct(ctx, productID, orgID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProductNotFound
			}
			return err
		}
		ok, err := q.ShopMediaUsable(ctx, mediaID, orgID)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("shop.invalid_media", "media_id is not a file uploaded by this organization")
		}
		pos := int32(0)
		if position != nil {
			pos = *position
		} else {
			imgs, err := q.ListShopProductImages(ctx, []uuid.UUID{productID})
			if err != nil {
				return err
			}
			for _, i := range imgs {
				if i.Position >= pos {
					pos = i.Position + 1
				}
			}
		}
		_, err = q.UpsertShopProductImage(ctx, productID, mediaID, pos, alt)
		return err
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, orgID, productID)
}

// RemoveImage detaches a picture from a product.
func (s *Service) RemoveImage(ctx context.Context, orgID, productID, mediaID uuid.UUID) (ProductView, error) {
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		if _, err := q.GetShopProduct(ctx, productID, orgID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProductNotFound
			}
			return err
		}
		ok, err := q.DeleteShopProductImage(ctx, productID, mediaID)
		if err != nil {
			return err
		}
		if !ok {
			return newErr(http.StatusNotFound, "shop.image_not_found", "this picture is not attached to the product")
		}
		return nil
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, orgID, productID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Variants
// ─────────────────────────────────────────────────────────────────────────────

// VariantInput is one variant of a PUT .../variants list. ID nil creates a
// variant; an ID must name a live variant of the product. TrackStock nil
// means true, Position nil means the index in the list.
type VariantInput struct {
	ID             *uuid.UUID
	Title          string
	SKU            *string
	PriceMinor     int64
	CompareAtMinor *int64
	Stock          int32
	TrackStock     *bool
	Position       *int32
	Option1        *string
	Option2        *string
	Option3        *string
	// ExpectedStock is the stock the editor loaded for this variant. When it
	// is set and the stock has changed since (a sale, an import, another
	// editor), the whole write is refused with 409 shop.stock_changed and
	// nothing is applied, so a stale screen cannot overwrite a sale.
	ExpectedStock *int32
	// KeepStock leaves the stock of an existing variant (ID set) exactly as
	// it is stored: Stock is ignored and no stock move is journaled. The
	// HTTP layer sets it for a row that names no expected_stock, so a screen
	// that did not touch the shelf cannot overwrite a sale.
	KeepStock bool
}

func validateVariants(vs []VariantInput) error {
	if len(vs) == 0 || len(vs) > MaxVariants {
		return invalid("shop.invalid_variants", "a product has between 1 and 100 variants")
	}
	seen := map[uuid.UUID]bool{}
	for i := range vs {
		v := &vs[i]
		v.Title = strings.TrimSpace(v.Title)
		if v.Title == "" {
			v.Title = DefaultVariantTitle
		}
		if !textOK(v.Title, MaxTitleLen) {
			return invalid("shop.invalid_variants", "a variant title is limited to 300 characters")
		}
		if v.ID != nil {
			if seen[*v.ID] {
				return invalid("shop.invalid_variants", "the same variant id appears twice")
			}
			seen[*v.ID] = true
		}
		if v.SKU != nil {
			sku := strings.TrimSpace(*v.SKU)
			if sku == "" {
				v.SKU = nil
			} else if !textOK(sku, MaxSKULen) {
				return invalid("shop.invalid_variants", "sku is limited to 100 characters")
			} else {
				v.SKU = &sku
			}
		}
		if v.PriceMinor < 0 || (v.CompareAtMinor != nil && *v.CompareAtMinor < 0) {
			return invalid("shop.invalid_variants", "price_minor and compare_at_minor must be zero or more")
		}
		if v.Stock < 0 {
			return invalid("shop.invalid_variants", "stock must be zero or more")
		}
		if v.ExpectedStock != nil && v.ID == nil {
			return invalid("shop.invalid_variants", "expected_stock is only for an existing variant (with its id)")
		}
		for _, o := range []*string{v.Option1, v.Option2, v.Option3} {
			if o != nil && !textOK(*o, MaxShortTextLen) {
				return invalid("shop.invalid_variants", "an option value is limited to 200 characters")
			}
		}
	}
	return nil
}

// writeVariants makes the product's live variants exactly vs: named ones are
// updated, new ones inserted, the rest soft-deleted. A stock change is
// journaled as a manual move. The live variants are locked in id order first
// and every expected_stock is checked against the locked stock before
// anything is written. A digital product's variants never track stock: they
// are written untracked with stock 0 and journal nothing.
func writeVariants(ctx context.Context, q *gen.Queries, orgID, productID uuid.UUID, kind string, vs []VariantInput, expectedIDs *[]uuid.UUID) error {
	if kind == KindDigital {
		untracked := false
		for i := range vs {
			vs[i].TrackStock = &untracked
			vs[i].Stock = 0
			vs[i].ExpectedStock = nil
		}
	}
	current, err := q.LockShopProductVariants(ctx, productID)
	if err != nil {
		return err
	}
	if expectedIDs != nil {
		if err := checkVariantSet(current, *expectedIDs); err != nil {
			return err
		}
	}
	var changed []map[string]any
	for _, v := range vs {
		if v.ID == nil || v.ExpectedStock == nil {
			continue
		}
		stock, ok := current[*v.ID]
		if !ok {
			return invalid("shop.invalid_variant", "variant "+v.ID.String()+" is not a variant of this product")
		}
		if stock != *v.ExpectedStock {
			changed = append(changed, map[string]any{
				"id": v.ID.String(), "expected_stock": *v.ExpectedStock, "stock": stock,
			})
		}
	}
	if len(changed) > 0 {
		e := newErr(http.StatusConflict, "shop.stock_changed",
			"the stock of a variant changed since it was loaded; reload the product and apply the change again")
		e.Details = map[string]any{"variants": changed}
		return e
	}
	keep := make([]uuid.UUID, 0, len(vs))
	for i, v := range vs {
		pos := int32(i) //nolint:gosec // bounded by MaxVariants
		if v.Position != nil {
			pos = *v.Position
		}
		track := true
		if v.TrackStock != nil {
			track = *v.TrackStock
		}
		p := gen.ShopVariantParams{
			ProductID: productID, Title: v.Title, SKU: v.SKU, PriceMinor: v.PriceMinor,
			CompareAtMinor: v.CompareAtMinor, Stock: v.Stock, TrackStock: track, Position: pos,
			Option1: v.Option1, Option2: v.Option2, Option3: v.Option3,
		}
		if v.ID != nil {
			p.ID = *v.ID
			if v.KeepStock {
				// Stock untouched: write back what the locked row holds.
				if stock, ok := current[*v.ID]; ok {
					p.Stock = stock
				}
			}
			prev, err := q.UpdateShopVariant(ctx, p)
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("shop.invalid_variant", "variant "+v.ID.String()+" is not a variant of this product")
			}
			if err != nil {
				return err
			}
			if d := p.Stock - prev; d != 0 && kind != KindDigital {
				if err := q.InsertShopStockMove(ctx, orgID, p.ID, nil, d, MoveManual); err != nil {
					return err
				}
			}
			keep = append(keep, p.ID)
			continue
		}
		id, err := q.InsertShopVariant(ctx, p)
		if err != nil {
			return err
		}
		if v.Stock != 0 {
			if err := q.InsertShopStockMove(ctx, orgID, id, nil, v.Stock, MoveManual); err != nil {
				return err
			}
		}
		keep = append(keep, id)
	}
	_, err = q.SoftDeleteShopVariantsExcept(ctx, productID, keep)
	return err
}

// checkVariantSet refuses a replace written against another set of variants
// than the product has now (a colleague added or removed one meanwhile): the
// replace deletes whatever it leaves out, so a stale list would silently
// remove the colleague's variant.
func checkVariantSet(current map[uuid.UUID]int32, expected []uuid.UUID) error {
	want := make(map[uuid.UUID]bool, len(expected))
	for _, id := range expected {
		want[id] = true
	}
	same := len(want) == len(current)
	if same {
		for id := range current {
			if !want[id] {
				same = false
				break
			}
		}
	}
	if same {
		return nil
	}
	ids := make([]string, 0, len(current))
	for id := range current {
		ids = append(ids, id.String())
	}
	sort.Strings(ids)
	e := newErr(http.StatusConflict, "shop.variants_changed",
		"the variants of this product changed since they were loaded; reload the product and apply the change again")
	e.Details = map[string]any{"variant_ids": ids}
	return e
}

// ReplaceVariants replaces the product's variant list.
func (s *Service) ReplaceVariants(ctx context.Context, orgID, productID uuid.UUID, vs []VariantInput) (ProductView, error) {
	return s.ReplaceVariantsChecked(ctx, orgID, productID, vs, nil)
}

// ReplaceVariantsChecked is ReplaceVariants that also refuses the write with
// 409 shop.variants_changed when the live variants of the product are not
// exactly expectedIDs (the set the editor loaded). nil skips the check.
func (s *Service) ReplaceVariantsChecked(ctx context.Context, orgID, productID uuid.UUID, vs []VariantInput, expectedIDs *[]uuid.UUID) (ProductView, error) {
	if err := validateVariants(vs); err != nil {
		return ProductView{}, err
	}
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		cur, err := q.GetShopProductForUpdate(ctx, productID, orgID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProductNotFound
			}
			return err
		}
		return writeVariants(ctx, q, orgID, productID, cur.Kind, vs, expectedIDs)
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, orgID, productID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pages
// ─────────────────────────────────────────────────────────────────────────────

// ListPages lists the static pages of the storefront.
func (s *Service) ListPages(ctx context.Context, orgID uuid.UUID) ([]gen.ShopPageRow, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.q.ListShopPages(ctx, orgID)
}

// PutPage creates or replaces a static page.
func (s *Service) PutPage(ctx context.Context, orgID uuid.UUID, slug, title, body string, position int32) (gen.ShopPageRow, error) {
	if err := s.ready(); err != nil {
		return gen.ShopPageRow{}, err
	}
	norm, ok := NormalizeSlug(slug)
	if !ok {
		return gen.ShopPageRow{}, invalid("shop.invalid_slug", "slug must be 1-200 characters without spaces, '/', '?' or '#'")
	}
	title = strings.TrimSpace(title)
	if title == "" || !textOK(title, MaxTitleLen) {
		return gen.ShopPageRow{}, invalid("shop.invalid_title", "title is required and limited to 300 characters")
	}
	body, err := SanitizeBody(body, PageBodyProfile)
	if err != nil {
		return gen.ShopPageRow{}, err
	}
	return s.q.UpsertShopPage(ctx, orgID, norm, title, body, position)
}

// GetPage returns the page a public request names: by slug, or failing that by
// the handle the page had on the shop it was imported from (legacy_slug). The
// row carries the canonical slug whichever address was asked for.
func (s *Service) GetPage(ctx context.Context, orgID uuid.UUID, slug string) (gen.ShopPageRow, error) {
	if err := s.ready(); err != nil {
		return gen.ShopPageRow{}, err
	}
	norm, ok := NormalizeSlug(unorm.NFC.String(slug))
	if !ok {
		return gen.ShopPageRow{}, ErrPageNotFound
	}
	p, err := s.q.GetShopPageBySlugOrLegacy(ctx, orgID, norm)
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.ShopPageRow{}, ErrPageNotFound
	}
	return p, err
}

// SetPageLegacySlug records the old address of a page (nil clears it). The
// value goes through the same normalization as a slug, in NFC, and a value
// equal to the page's own slug is stored as no alias. A handle another page of
// the organization already holds is a 409.
func (s *Service) SetPageLegacySlug(ctx context.Context, orgID uuid.UUID, slug string, legacy *string) error {
	if err := s.ready(); err != nil {
		return err
	}
	norm, ok := NormalizeSlug(slug)
	if !ok {
		return ErrPageNotFound
	}
	var val *string
	if legacy != nil {
		l, ok := NormalizeSlug(unorm.NFC.String(*legacy))
		if !ok {
			return invalid("shop.invalid_slug", "legacy slug must be 1-200 characters without spaces, '/', '?' or '#'")
		}
		if l != norm {
			val = &l
		}
	}
	found, err := s.q.SetShopPageLegacySlug(ctx, orgID, norm, val)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return newErr(http.StatusConflict, "shop.legacy_slug_taken", "another page of this shop already has that old address")
	}
	if err != nil {
		return err
	}
	if !found {
		return ErrPageNotFound
	}
	return nil
}

// DeletePage removes a static page.
func (s *Service) DeletePage(ctx context.Context, orgID uuid.UUID, slug string) error {
	if err := s.ready(); err != nil {
		return err
	}
	norm, ok := NormalizeSlug(slug)
	if !ok {
		return ErrPageNotFound
	}
	deleted, err := s.q.DeleteShopPage(ctx, orgID, norm)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrPageNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Storefront resolution
// ─────────────────────────────────────────────────────────────────────────────

// Storefront is an organization that has a shop, as the public routes see it.
type Storefront struct {
	Org      gen.OrganizationRow
	Settings SettingsView
}

// StorefrontBySlug resolves the public shop of an organization slug. An
// organization without saved shop settings, or whose shop the platform
// operator has not enabled, has no storefront (404 shop.not_found), whatever
// products it may have drafted.
func (s *Service) StorefrontBySlug(ctx context.Context, orgSlug string) (Storefront, error) {
	if err := s.ready(); err != nil {
		return Storefront{}, err
	}
	slug := strings.ToLower(strings.TrimSpace(orgSlug))
	if slug == "" {
		return Storefront{}, ErrShopNotFound
	}
	org, err := s.q.GetOrganizationBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Storefront{}, ErrShopNotFound
	}
	if err != nil {
		return Storefront{}, err
	}
	st, err := s.GetSettings(ctx, org.ID)
	if err != nil {
		return Storefront{}, err
	}
	if !st.Configured || !st.Enabled {
		return Storefront{}, ErrShopNotFound
	}
	return Storefront{Org: org, Settings: st}, nil
}

// SlugForHost resolves a storefront host (organizations.shop_domain) to the
// organization slug. A "www." prefix is tried without it too.
func (s *Service) SlugForHost(ctx context.Context, host string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	d, ok := NormalizeDomain(h)
	if !ok || d == "" {
		return "", ErrShopNotFound
	}
	for _, candidate := range []string{d, strings.TrimPrefix(d, "www.")} {
		_, slug, err := s.q.GetOrganizationByShopDomain(ctx, candidate)
		if err == nil {
			return slug, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	return "", ErrShopNotFound
}

// MaxPaymentChannelCandidates bounds how many channels the checkout tries
// when the settings name none.
const MaxPaymentChannelCandidates = 20

// PaymentChannelCandidates are the sales channels that may take the shop's
// money, in the order to try them: only the one the settings name when they
// name one, else the organization's live channels oldest first. The caller
// keeps the first whose provider can host a payment page with a configured
// provider account (the payment pre-flight), so an older channel on a
// provider that is not set up does not block the shop.
func (s *Service) PaymentChannelCandidates(ctx context.Context, st SettingsView) ([]uuid.UUID, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if st.SalesChannelID != nil {
		return []uuid.UUID{*st.SalesChannelID}, nil
	}
	ids, err := s.q.ListLiveSalesChannelIDs(ctx, st.OrgID, MaxPaymentChannelCandidates)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, newErr(http.StatusUnprocessableEntity, "checkout.payment_not_configured",
			"online payment for this shop is not set up yet, contact the shop")
	}
	return ids, nil
}

// PaymentChannel is the first of PaymentChannelCandidates.
func (s *Service) PaymentChannel(ctx context.Context, st SettingsView) (uuid.UUID, error) {
	if err := s.ready(); err != nil {
		return uuid.Nil, err
	}
	if st.SalesChannelID != nil {
		return *st.SalesChannelID, nil
	}
	id, err := s.q.GetFirstSalesChannelID(ctx, st.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, newErr(http.StatusUnprocessableEntity, "checkout.payment_not_configured",
			"online payment for this shop is not set up yet, contact the shop")
	}
	return id, err
}
