package hshop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// publicSettingsJSON is what the storefront needs of the shop settings.
type publicSettingsJSON struct {
	Currency         string  `json:"currency"`
	ShippingFeeMinor int64   `json:"shipping_fee_minor"`
	ShippingLabel    string  `json:"shipping_label"`
	CountryISO2      *string `json:"country_iso2"`
	AgeGateMinAge    *int32  `json:"age_gate_min_age"`
	AnnouncementText string  `json:"announcement_text"`
	ContactEmail     *string `json:"contact_email"`
	MaxLineQty       int32   `json:"max_line_qty"`
}

// publicOrgJSON names the shop's organization.
type publicOrgJSON struct {
	Slug       string  `json:"slug"`
	Name       string  `json:"name"`
	ShopDomain *string `json:"shop_domain"`
}

// pageLinkJSON is a static page in the catalog's page list.
type pageLinkJSON struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// HandlePublicCatalog serves GET /v1/public/shop/{org_slug}: the settings, the
// active products with their variants and pictures, and the page list.
func (h *Handler) HandlePublicCatalog(w http.ResponseWriter, r *http.Request) {
	if h.limited(w, r, "") {
		return
	}
	sf, err := h.o.Service.StorefrontBySlug(r.Context(), chi.URLParam(r, "org_slug"))
	if err != nil {
		h.fail(w, r, "public catalog", err)
		return
	}
	ps, err := h.o.Service.ListProducts(r.Context(), sf.Org.ID, shop.ProductActive)
	if err != nil {
		h.fail(w, r, "public catalog products", err)
		return
	}
	pages, err := h.o.Service.ListPages(r.Context(), sf.Org.ID)
	if err != nil {
		h.fail(w, r, "public catalog pages", err)
		return
	}
	products := make([]publicProductJSON, 0, len(ps))
	for _, p := range ps {
		products = append(products, h.toPublicProduct(r.Context(), p))
	}
	links := make([]pageLinkJSON, 0, len(pages))
	for _, p := range pages {
		links = append(links, pageLinkJSON{Slug: p.Slug, Title: p.Title})
	}
	st := sf.Settings
	cacheable(w)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"org": publicOrgJSON{Slug: sf.Org.Slug, Name: sf.Org.Name, ShopDomain: st.ShopDomain},
		"settings": publicSettingsJSON{
			Currency: st.Currency, ShippingFeeMinor: st.ShippingFeeMinor, ShippingLabel: st.ShippingLabel,
			CountryISO2: st.CountryISO2, AgeGateMinAge: st.AgeGateMinAge, AnnouncementText: st.AnnouncementText,
			ContactEmail: st.ContactEmail, MaxLineQty: st.MaxLineQty,
		},
		"products": products,
		"pages":    links,
	})
}

// HandlePublicProduct serves GET /v1/public/shop/{org_slug}/products/{slug}.
func (h *Handler) HandlePublicProduct(w http.ResponseWriter, r *http.Request) {
	if h.limited(w, r, "") {
		return
	}
	sf, err := h.o.Service.StorefrontBySlug(r.Context(), chi.URLParam(r, "org_slug"))
	if err != nil {
		h.fail(w, r, "public product", err)
		return
	}
	p, err := h.o.Service.GetActiveProductBySlug(r.Context(), sf.Org.ID, chi.URLParam(r, "slug"))
	if err != nil {
		h.fail(w, r, "public product", err)
		return
	}
	cacheable(w)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"currency": sf.Settings.Currency,
		"product":  h.toPublicProduct(r.Context(), p),
	})
}

// HandlePublicPages serves GET /v1/public/shop/pages/{org_slug}: every
// static page with its text.
func (h *Handler) HandlePublicPages(w http.ResponseWriter, r *http.Request) {
	if h.limited(w, r, "") {
		return
	}
	sf, err := h.o.Service.StorefrontBySlug(r.Context(), chi.URLParam(r, "org_slug"))
	if err != nil {
		h.fail(w, r, "public pages", err)
		return
	}
	ps, err := h.o.Service.ListPages(r.Context(), sf.Org.ID)
	if err != nil {
		h.fail(w, r, "public pages", err)
		return
	}
	out := make([]pageJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, toPageJSON(p))
	}
	cacheable(w)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"pages": out})
}

// HandleByHost serves GET /v1/public/shop/by-host?host=: the organization slug
// whose storefront lives on that host.
func (h *Handler) HandleByHost(w http.ResponseWriter, r *http.Request) {
	if h.limited(w, r, "") {
		return
	}
	slug, err := h.o.Service.SlugForHost(r.Context(), r.URL.Query().Get("host"))
	if err != nil {
		h.fail(w, r, "resolve host", err)
		return
	}
	cacheable(w)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"org_slug": slug})
}

type checkoutBody struct {
	Items []struct {
		VariantID uuid.UUID `json:"variant_id"`
		Qty       int       `json:"qty"`
	} `json:"items"`
	Buyer struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Phone string `json:"phone"`
	} `json:"buyer"`
	Ship struct {
		Name     string `json:"name"`
		Address1 string `json:"address1"`
		Address2 string `json:"address2"`
		City     string `json:"city"`
		Postal   string `json:"postal"`
		Country  string `json:"country"`
	} `json:"ship"`
	DeliveryNote string `json:"delivery_note"`
	Locale       string `json:"locale"`
	ReturnURL    string `json:"return_url"`
	AgeConfirmed bool   `json:"age_confirmed"`
}

// HandlePublicCheckout serves POST /v1/public/shop/{org_slug}/checkout.
//
// Order of operations (the ticket widget's, hfeed/payment_start.go): the
// payment pre-flight (return URL, the shop's payment channel and the
// organization's provider config) is read on the pool BEFORE the transaction;
// the order and its stock hold commit in one transaction that refuses a paid
// cart the pre-flight failed; the provider's hosted page is created AFTER the
// commit, never inside it. A page that cannot be created expires the order at
// once, so its goods go straight back on sale.
func (h *Handler) HandlePublicCheckout(w http.ResponseWriter, r *http.Request) {
	if h.limited(w, r, "") {
		return
	}
	ctx := r.Context()
	var b checkoutBody
	if !readBody(w, r, &b, false) {
		return
	}
	// Counted only once the body is a valid request, per shop and address.
	if h.checkoutLimited(w, r, strings.ToLower(strings.TrimSpace(chi.URLParam(r, "org_slug")))) {
		return
	}
	sf, err := h.o.Service.StorefrontBySlug(ctx, chi.URLParam(r, "org_slug"))
	if err != nil {
		h.fail(w, r, "checkout", err)
		return
	}

	// Pre-flight, on the pool, before the transaction.
	returnBase, ready := h.o.ReturnPolicy.Resolve(b.ReturnURL)
	var channelID uuid.UUID
	if ready == nil {
		channelID, ready = h.paymentChannel(ctx, sf)
	}

	in := shop.CheckoutInput{
		OrgID:        sf.Org.ID,
		Buyer:        shop.Buyer{Name: b.Buyer.Name, Email: b.Buyer.Email, Phone: b.Buyer.Phone},
		Ship:         shop.Address{Name: b.Ship.Name, Address1: b.Ship.Address1, Address2: b.Ship.Address2, City: b.Ship.City, Postal: b.Ship.Postal, Country: b.Ship.Country},
		DeliveryNote: b.DeliveryNote,
		Locale:       b.Locale,
		AgeConfirmed: b.AgeConfirmed,
		HoldFor:      h.o.Window + h.o.Grace,
		PaymentReady: ready,
		ChannelID:    channelID,
	}
	for _, it := range b.Items {
		in.Items = append(in.Items, shop.CheckoutItem{VariantID: it.VariantID, Qty: it.Qty})
	}
	placed, err := h.o.Service.PlaceOrder(ctx, in)
	if err != nil {
		h.fail(w, r, "checkout", err)
		return
	}
	o := placed.Order
	if placed.Paid {
		httputil.WriteJSON(w, http.StatusCreated, checkoutAnswer(o, ""))
		return
	}

	res, err := h.o.Payments.StartHostedCheckout(ctx, hfeed.HostedCheckoutRequest{
		OrgID:         o.OrgID,
		ChannelID:     channelID,
		ShopOrderID:   &o.ID,
		CheckoutToken: o.OrderToken,
		Amount:        o.Total,
		Currency:      o.Currency,
		ProductName:   fmt.Sprintf("%s order %d", sf.Org.Name, o.Number),
		BuyerEmail:    o.BuyerEmail,
		BuyerName:     o.BuyerName,
		BuyerPhone:    o.BuyerPhone,
		ReturnURL:     returnBase,
		ExpiresAtUnix: o.ExpiresAt.Add(-h.o.Grace).Unix(),
		BuyerLocale:   strings.TrimSpace(b.Locale),
	})
	if err != nil {
		h.abandon(r, o.ID, o.OrgID, err)
		if pse, typed := hfeed.AsPaymentStartError(err); typed {
			writeErr(w, r, pse.Status, pse.Code, pse.Message)
			return
		}
		// 503, never 502: Cloudflare replaces an origin 502 with its own page
		// and the JSON envelope would not reach the storefront (AGENTS.md).
		writeErr(w, r, http.StatusServiceUnavailable, hfeed.ErrCodePaymentStartFailed,
			"The payment service could not start this payment. Try again in a minute.")
		return
	}
	if _, err := h.o.Service.AttachPayment(ctx, o, shop.HostedPayment{
		Provider: res.Provider, ProviderPaymentID: res.SessionID, URL: res.RedirectURL,
	}); err != nil {
		// Without the payment intent a webhook could not find this payment:
		// better to lose the cart now than the payment later.
		h.abandon(r, o.ID, o.OrgID, err)
		h.o.Logger.Error("shop checkout: recording the payment intent failed; refusing to redirect",
			slog.String("order_id", o.ID.String()), slog.String("error", err.Error()))
		writeErr(w, r, http.StatusInternalServerError, hfeed.ErrCodePaymentStartFailed,
			"We could not start the payment. Try again in a minute.")
		return
	}
	h.o.Logger.Info("shop checkout: hosted payment started",
		slog.String("order_id", o.ID.String()), slog.Int64("total", o.Total), slog.String("currency", o.Currency))
	httputil.WriteJSON(w, http.StatusCreated, checkoutAnswer(o, res.RedirectURL))
}

// paymentChannel picks the sales channel whose provider takes the payment:
// the first candidate the payment pre-flight accepts (a provider that can
// host a page, with a configured provider account). When none does, the
// first candidate's refusal is the answer.
func (h *Handler) paymentChannel(ctx context.Context, sf shop.Storefront) (uuid.UUID, error) {
	candidates, err := h.o.Service.PaymentChannelCandidates(ctx, sf.Settings)
	if err != nil {
		return uuid.Nil, err
	}
	if h.o.Payments == nil {
		return candidates[0], &hfeed.PaymentStartError{Code: hfeed.ErrCodePaymentNotConfigured,
			Message: "Online payment is not set up here yet. Contact the shop.", Status: http.StatusServiceUnavailable}
	}
	var first error
	for _, id := range candidates {
		err := h.o.Payments.CheckPaymentConfigured(ctx, sf.Org.ID, id)
		if err == nil {
			return id, nil
		}
		if first == nil {
			first = err
		}
	}
	return candidates[0], first
}

// HandlePublicCheckoutDownloads serves GET
// /v1/public/shop/checkout/{order_token}/downloads: the files of the digital
// lines of a paid order, each behind a short-lived signed URL. An order that
// is not paid (pending, expired, cancelled, refunded) is the route's 404,
// like a token that names nothing.
func (h *Handler) HandlePublicCheckoutDownloads(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "order_token"))
	if h.limited(w, r, token) {
		return
	}
	downloads, err := h.o.Service.Downloads(r.Context(), token)
	if err != nil {
		if errors.Is(err, shop.ErrOrderNotFound) {
			writeErr(w, r, http.StatusNotFound, "shop.order_not_found", "order not found")
			return
		}
		h.fail(w, r, "downloads", err)
		return
	}
	type downloadJSON struct {
		ProductTitle string    `json:"product_title"`
		VariantTitle string    `json:"variant_title"`
		URL          string    `json:"url"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	out := make([]downloadJSON, 0, len(downloads))
	for _, d := range downloads {
		if h.o.DownloadURL == nil {
			writeErr(w, r, http.StatusServiceUnavailable, "shop.download_unavailable", "The files cannot be served right now. Try again in a minute.")
			return
		}
		url, until := h.o.DownloadURL(r.Context(), d.MediaID)
		if url == "" {
			writeErr(w, r, http.StatusServiceUnavailable, "shop.download_unavailable", "The files cannot be served right now. Try again in a minute.")
			return
		}
		out = append(out, downloadJSON{ProductTitle: d.ProductTitle, VariantTitle: d.VariantTitle, URL: url, ExpiresAt: until})
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"downloads": out})
}

// abandon expires an order whose payment could not be started. Best effort:
// the expiry sweep catches anything it misses.
func (h *Handler) abandon(r *http.Request, orderID, orgID uuid.UUID, cause error) {
	h.o.Logger.Warn("shop checkout: payment could not be started; the order is expired",
		slog.String("order_id", orderID.String()), slog.String("error", cause.Error()))
	if err := h.o.Service.AbandonOrder(r.Context(), orderID, orgID); err != nil {
		h.o.Logger.Error("shop checkout: expiring the abandoned order failed; the sweep will",
			slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
	}
}

func checkoutAnswer(o gen.ShopOrderRow, paymentURL string) map[string]any {
	return map[string]any{
		"order_token":  o.OrderToken,
		"number":       o.Number,
		"status":       shop.PublicStatus(o, false),
		"payment_url":  paymentURL,
		"currency":     o.Currency,
		"subtotal":     o.Subtotal,
		"shipping_fee": o.ShippingFee,
		"total":        o.Total,
		"expires_at":   o.ExpiresAt,
	}
}

// HandlePublicCheckoutStatus serves GET /v1/public/shop/checkout/{order_token}:
// where the buyer's order stands (pending, paid, expired, cancelled) and,
// while it is pending, the payment page to go back to.
func (h *Handler) HandlePublicCheckoutStatus(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "order_token"))
	if h.limited(w, r, token) {
		return
	}
	v, err := h.o.Service.OrderByToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, shop.ErrOrderNotFound) {
			writeErr(w, r, http.StatusNotFound, "shop.order_not_found", "order not found")
			return
		}
		h.fail(w, r, "checkout status", err)
		return
	}
	holdPassed := v.ExpiresAt != nil && !v.ExpiresAt.After(time.Now())
	status := shop.PublicStatus(v.ShopOrderRow, holdPassed)
	paymentURL := ""
	if status == "pending" && v.Payment != nil && v.Payment.HostedCheckoutURL != nil {
		paymentURL = *v.Payment.HostedCheckoutURL
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":       status,
		"number":       v.Number,
		"currency":     v.Currency,
		"subtotal":     v.Subtotal,
		"shipping_fee": v.ShippingFee,
		"total":        v.Total,
		"items":        toItemsJSON(v.Items),
		"payment_url":  paymentURL,
		"expires_at":   v.ExpiresAt,
		"paid_at":      v.PaidAt,
	})
}
