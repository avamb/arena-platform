package hshop

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// ─── settings ────────────────────────────────────────────────────────────────

// HandleGetSettings serves GET .../shop/settings.
func (h *Handler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	st, err := h.o.Service.GetSettings(r.Context(), org)
	if err != nil {
		h.fail(w, r, "read settings", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toSettingsJSON(st))
}

type settingsBody struct {
	Currency         string     `json:"currency"`
	ShippingFeeMinor int64      `json:"shipping_fee_minor"`
	ShippingLabel    string     `json:"shipping_label"`
	CountryISO2      *string    `json:"country_iso2"`
	AgeGateMinAge    *int32     `json:"age_gate_min_age"`
	AnnouncementText string     `json:"announcement_text"`
	ContactEmail     *string    `json:"contact_email"`
	LogisticsNote    string     `json:"logistics_note"`
	SalesChannelID   *uuid.UUID `json:"sales_channel_id"`
	ShopDomain       *string    `json:"shop_domain"`
}

// HandlePutSettings serves PUT .../shop/settings. shop_domain may be changed
// by a platform superadmin only.
func (h *Handler) HandlePutSettings(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	var b settingsBody
	if !readBody(w, r, &b, false) {
		return
	}
	st, err := h.o.Service.PutSettings(r.Context(), org, shop.SettingsInput{
		Currency: b.Currency, ShippingFeeMinor: b.ShippingFeeMinor, ShippingLabel: b.ShippingLabel,
		CountryISO2: b.CountryISO2, AgeGateMinAge: b.AgeGateMinAge, AnnouncementText: b.AnnouncementText,
		ContactEmail: b.ContactEmail, LogisticsNote: b.LogisticsNote, SalesChannelID: b.SalesChannelID,
		ShopDomain: b.ShopDomain, MayChangeDomain: auth.HasSuperadminOrgAccess(r.Context()),
	})
	if err != nil {
		h.fail(w, r, "write settings", err)
		return
	}
	h.writeAudit(r, "v1.shop.settings.update", "shop_settings", org.String(), org, map[string]any{
		"currency": st.Currency, "shipping_fee_minor": st.ShippingFeeMinor,
	})
	httputil.WriteJSON(w, http.StatusOK, toSettingsJSON(st))
}

// ─── products ────────────────────────────────────────────────────────────────

// HandleListProducts serves GET .../shop/products[?status=].
func (h *Handler) HandleListProducts(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	ps, err := h.o.Service.ListProducts(r.Context(), org, r.URL.Query().Get("status"))
	if err != nil {
		h.fail(w, r, "list products", err)
		return
	}
	out := make([]productJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, h.toProductJSON(r.Context(), p))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"products": out})
}

type variantBody struct {
	ID             *uuid.UUID `json:"id"`
	Title          string     `json:"title"`
	SKU            *string    `json:"sku"`
	PriceMinor     int64      `json:"price_minor"`
	CompareAtMinor *int64     `json:"compare_at_minor"`
	Stock          int32      `json:"stock"`
	TrackStock     *bool      `json:"track_stock"`
	Position       *int32     `json:"position"`
	Option1        *string    `json:"option1"`
	Option2        *string    `json:"option2"`
	Option3        *string    `json:"option3"`
	ExpectedStock  *int32     `json:"expected_stock"`
}

func toVariantInputs(bs []variantBody) []shop.VariantInput {
	out := make([]shop.VariantInput, 0, len(bs))
	for _, b := range bs {
		out = append(out, shop.VariantInput{
			ID: b.ID, Title: b.Title, SKU: b.SKU, PriceMinor: b.PriceMinor, CompareAtMinor: b.CompareAtMinor,
			Stock: b.Stock, TrackStock: b.TrackStock, Position: b.Position,
			Option1: b.Option1, Option2: b.Option2, Option3: b.Option3, ExpectedStock: b.ExpectedStock,
		})
	}
	return out
}

type productBody struct {
	Slug        *string       `json:"slug"`
	Title       *string       `json:"title"`
	BodyHTML    *string       `json:"body_html"`
	Status      *string       `json:"status"`
	ProductType *string       `json:"product_type"`
	Vendor      *string       `json:"vendor"`
	SortOrder   *int32        `json:"sort_order"`
	Variants    []variantBody `json:"variants"`
}

func (b productBody) input() shop.ProductInput {
	return shop.ProductInput{Slug: b.Slug, Title: b.Title, BodyHTML: b.BodyHTML, Status: b.Status,
		ProductType: b.ProductType, Vendor: b.Vendor, SortOrder: b.SortOrder}
}

// HandleCreateProduct serves POST .../shop/products.
func (h *Handler) HandleCreateProduct(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	var b productBody
	if !readBody(w, r, &b, false) {
		return
	}
	p, err := h.o.Service.CreateProduct(r.Context(), org, b.input(), toVariantInputs(b.Variants))
	if err != nil {
		h.fail(w, r, "create product", err)
		return
	}
	h.writeAudit(r, "v1.shop.product.create", "shop_product", p.ID.String(), org, map[string]any{"slug": p.Slug})
	httputil.WriteJSON(w, http.StatusCreated, h.toProductJSON(r.Context(), p))
}

// HandleGetProduct serves GET .../shop/products/{product_id}.
func (h *Handler) HandleGetProduct(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "product_id", shop.ErrProductNotFound)
	if !ok {
		return
	}
	p, err := h.o.Service.GetProduct(r.Context(), org, id)
	if err != nil {
		h.fail(w, r, "read product", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.toProductJSON(r.Context(), p))
}

// HandlePatchProduct serves PATCH .../shop/products/{product_id}; variants
// are replaced through PUT .../variants, not here.
func (h *Handler) HandlePatchProduct(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "product_id", shop.ErrProductNotFound)
	if !ok {
		return
	}
	var b productBody
	if !readBody(w, r, &b, false) {
		return
	}
	if b.Variants != nil {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_body", "variants are replaced with PUT .../variants")
		return
	}
	p, err := h.o.Service.UpdateProduct(r.Context(), org, id, b.input())
	if err != nil {
		h.fail(w, r, "update product", err)
		return
	}
	h.writeAudit(r, "v1.shop.product.update", "shop_product", p.ID.String(), org, map[string]any{"slug": p.Slug, "status": p.Status})
	httputil.WriteJSON(w, http.StatusOK, h.toProductJSON(r.Context(), p))
}

// HandleDeleteProduct serves DELETE .../shop/products/{product_id}.
func (h *Handler) HandleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "product_id", shop.ErrProductNotFound)
	if !ok {
		return
	}
	if err := h.o.Service.DeleteProduct(r.Context(), org, id); err != nil {
		h.fail(w, r, "delete product", err)
		return
	}
	h.writeAudit(r, "v1.shop.product.delete", "shop_product", id.String(), org, nil)
	w.WriteHeader(http.StatusNoContent)
}

type imageBody struct {
	MediaID  uuid.UUID `json:"media_id"`
	Position *int32    `json:"position"`
	Alt      string    `json:"alt"`
}

// HandleAddImage serves POST .../shop/products/{product_id}/images.
func (h *Handler) HandleAddImage(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "product_id", shop.ErrProductNotFound)
	if !ok {
		return
	}
	var b imageBody
	if !readBody(w, r, &b, false) {
		return
	}
	if b.MediaID == uuid.Nil {
		writeErr(w, r, http.StatusUnprocessableEntity, "shop.invalid_media", "media_id is required")
		return
	}
	p, err := h.o.Service.AddImage(r.Context(), org, id, b.MediaID, b.Position, b.Alt)
	if err != nil {
		h.fail(w, r, "add image", err)
		return
	}
	h.writeAudit(r, "v1.shop.product.image_add", "shop_product", id.String(), org, map[string]any{"media_id": b.MediaID.String()})
	httputil.WriteJSON(w, http.StatusOK, h.toProductJSON(r.Context(), p))
}

// HandleRemoveImage serves DELETE .../shop/products/{product_id}/images/{media_id}.
func (h *Handler) HandleRemoveImage(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "product_id", shop.ErrProductNotFound)
	if !ok {
		return
	}
	media, err := uuid.Parse(chi.URLParam(r, "media_id"))
	if err != nil {
		writeErr(w, r, http.StatusNotFound, "shop.image_not_found", "this picture is not attached to the product")
		return
	}
	p, err := h.o.Service.RemoveImage(r.Context(), org, id, media)
	if err != nil {
		h.fail(w, r, "remove image", err)
		return
	}
	h.writeAudit(r, "v1.shop.product.image_remove", "shop_product", id.String(), org, map[string]any{"media_id": media.String()})
	httputil.WriteJSON(w, http.StatusOK, h.toProductJSON(r.Context(), p))
}

// HandlePutVariants serves PUT .../shop/products/{product_id}/variants.
func (h *Handler) HandlePutVariants(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "product_id", shop.ErrProductNotFound)
	if !ok {
		return
	}
	var b struct {
		Variants []variantBody `json:"variants"`
	}
	if !readBody(w, r, &b, false) {
		return
	}
	p, err := h.o.Service.ReplaceVariants(r.Context(), org, id, toVariantInputs(b.Variants))
	if err != nil {
		h.fail(w, r, "replace variants", err)
		return
	}
	h.writeAudit(r, "v1.shop.product.variants_replace", "shop_product", id.String(), org, map[string]any{"variants": len(p.Variants)})
	httputil.WriteJSON(w, http.StatusOK, h.toProductJSON(r.Context(), p))
}

// ─── pages ───────────────────────────────────────────────────────────────────

// HandleListPages serves GET .../shop/pages.
func (h *Handler) HandleListPages(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	ps, err := h.o.Service.ListPages(r.Context(), org)
	if err != nil {
		h.fail(w, r, "list pages", err)
		return
	}
	out := make([]pageJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, toPageJSON(p))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"pages": out})
}

// HandlePutPage serves PUT .../shop/pages/{slug}.
func (h *Handler) HandlePutPage(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	var b struct {
		Title    string `json:"title"`
		BodyHTML string `json:"body_html"`
		Position int32  `json:"position"`
	}
	if !readBody(w, r, &b, false) {
		return
	}
	p, err := h.o.Service.PutPage(r.Context(), org, chi.URLParam(r, "slug"), b.Title, b.BodyHTML, b.Position)
	if err != nil {
		h.fail(w, r, "write page", err)
		return
	}
	h.writeAudit(r, "v1.shop.page.put", "shop_page", p.ID.String(), org, map[string]any{"slug": p.Slug})
	httputil.WriteJSON(w, http.StatusOK, toPageJSON(p))
}

// HandleDeletePage serves DELETE .../shop/pages/{slug}.
func (h *Handler) HandleDeletePage(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")
	if err := h.o.Service.DeletePage(r.Context(), org, slug); err != nil {
		h.fail(w, r, "delete page", err)
		return
	}
	h.writeAudit(r, "v1.shop.page.delete", "shop_page", slug, org, nil)
	w.WriteHeader(http.StatusNoContent)
}
