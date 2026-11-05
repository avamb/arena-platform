package httpserver

// mount_shop.go — the merchandise shop (spec 08_architecture/47 section 4,
// lane L1). Organization routes need shop.read / shop.manage plus membership
// of {org_id}; refund and cancel also run the web's password check
// (requireWebStepUp, like every money route). Public routes need no login
// and share the public feed's per-IP rate limit.
//
// The shop is a paid module (spec 08_architecture/61 section 4.1): every
// organization route also passes the module gate (shopFull / shopOrders),
// which answers 404 shop.module_not_enabled - to a platform superadmin too -
// when the organization's shop mode does not allow the route. Catalog,
// products, pages, settings and the storefront switch need the module today
// (full); the order routes also work in orders_only, with no end date, after
// the module ended, while the organization has a paid or an open order.
//
// The shop's payments arrive through the existing payment webhook routes:
// checkoutHandler wires shopPayments into hcheckout, which hands a payment
// intent without a checkout session to the shop on the webhook transaction.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hshop"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/whitelabel"
)

// shopService builds the shop core over the raw pool; without one every call
// answers 503. The refund engine drives a shop refund right after it is
// recorded.
func (s *Server) shopService() *shop.Service {
	var db shop.DB
	if s.pgxPool != nil {
		db = s.pgxPool
	}
	svc := shop.New(db, s.logger)
	if eng := s.refundEngine(); eng != nil {
		svc = svc.WithRefunds(eng)
	}
	if s.pgxPool != nil {
		modules := hcheckout.NewRefundModuleSource(gen.New(s.pgxPool), s.paymentModuleOptions())
		svc = svc.WithPageExpirer(shopPageExpirer{modules: modules}).WithCurrencies(func(provider string) []string {
			d, ok := modules.Descriptor(provider)
			if !ok {
				return nil
			}
			return d.Capabilities.Currencies
		})
	}
	return svc
}

// shopPageExpirer closes a cancelled shop order's hosted payment page through
// the organization's own payment module, when the provider can do it
// (payments.HostedCheckoutExpirer: Stripe). Other providers are left to let
// their page expire on its own.
type shopPageExpirer struct {
	modules *hcheckout.RefundModuleSource
}

func (e shopPageExpirer) ExpireHostedPage(ctx context.Context, orgID uuid.UUID, provider, providerPaymentID string) error {
	if e.modules == nil {
		return nil
	}
	if d, ok := e.modules.Descriptor(provider); !ok || !d.Capabilities.HostedCheckout {
		return nil
	}
	mod, err := e.modules.Build(ctx, orgID, provider)
	if err != nil {
		return err
	}
	expirer, ok := mod.(payments.HostedCheckoutExpirer)
	if !ok {
		return nil
	}
	return expirer.ExpireCheckoutSession(ctx, providerPaymentID)
}

// shopPayments is the webhook's hand-off to the shop, nil without a raw pool
// (unit-test servers keep the webhook exactly as it was).
func (s *Server) shopPayments() hcheckout.ShopPayments {
	if s.pgxPool == nil {
		return nil
	}
	return shop.New(s.pgxPool, s.logger)
}

func (s *Server) shopHandler() *hshop.Handler {
	opts := hshop.Options{
		Service:      s.shopService(),
		Audit:        s.audit,
		Logger:       s.logger,
		MediaURL:     s.signedMediaURL,
		DownloadURL:  s.shopDownloadURL,
		APIBaseURL:   s.apiPublicURL(),
		ShopBaseURL:  s.shopPublicBaseURL(),
		ReturnPolicy: s.checkoutReturnURLPolicy(),
		Window:       widgetPaymentWindow(s.cfg),
		Grace:        widgetPaymentGrace(s.cfg),
		Bundle:       s.bundle,
	}
	if s.pgxPool != nil {
		opts.WhiteLabel = whitelabel.New(gen.New(s.pgxPool))
	}
	if starter := s.hostedPaymentStarter(); starter != nil {
		opts.Payments = starter
	}
	if s.publicFeedRL != nil {
		opts.IPLimit = s.publicFeedRL.CheckIP
		opts.TokenLimit = s.publicFeedRL.CheckCheckoutToken
	}
	if s.cfg != nil {
		opts.TrustedProxies = s.cfg.TrustedProxyCount
		opts.IPSalt = s.cfg.JWTSecretStub
		if secret := strings.TrimSpace(s.cfg.ShopTurnstileSecret); secret != "" {
			// Hostnames and Action stay open: the storefronts live on the
			// organizers' own domains, and the widget is the shop page's.
			opts.Captcha = &honboarding.TurnstileVerifier{Secret: secret}
			opts.CaptchaSiteKey = strings.TrimSpace(s.cfg.ShopTurnstileSiteKey)
			if opts.CaptchaSiteKey == "" && s.logger != nil {
				s.logger.Warn("shop: SHOP_TURNSTILE_SECRET is set without SHOP_TURNSTILE_SITE_KEY, storefronts cannot render the widget and every checkout is refused")
			}
		}
	}
	return hshop.New(opts)
}

// shopDownloadTTL is how long a digital product's download link works.
const shopDownloadTTL = 15 * time.Minute

// shopDownloadURL signs a short-lived download URL of a digital product's
// file, absolute like signedMediaURL; "" when the server has no media store
// or signing fails.
func (s *Server) shopDownloadURL(ctx context.Context, id uuid.UUID) (string, time.Time) {
	until := time.Now().Add(shopDownloadTTL)
	if s.media == nil {
		return "", until
	}
	raw, err := s.media.SignedDownloadURL(ctx, id, shopDownloadTTL)
	if err != nil || raw == "" {
		if err != nil && s.logger != nil {
			s.logger.Warn("shop: signing a download failed", "media_id", id.String(), "error", err.Error())
		}
		return "", until
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw, until
	}
	return s.apiPublicURL() + raw, until
}

func (s *Server) mountShopRoutes(r chi.Router) {
	h := s.shopHandler()

	// Public storefront routes, mounted without a database too (503 then).
	// The static segments (by-host, checkout, pages) win over {org_slug}.
	r.Get("/public/shop/by-host", h.HandleByHost)
	r.Get("/public/shop/checkout/{order_token}", h.HandlePublicCheckoutStatus)
	r.Get("/public/shop/checkout/{order_token}/referral", h.HandlePublicCheckoutReferral)
	r.Get("/public/shop/checkout/{order_token}/downloads", h.HandlePublicCheckoutDownloads)
	r.Get("/public/shop/checkout/{order_token}/downloads/{item_id}", h.HandlePublicCheckoutDownload)
	r.Get("/public/shop/pages/{org_slug}", h.HandlePublicPages)
	r.Get("/public/shop/{org_slug}", h.HandlePublicCatalog)
	r.Get("/public/shop/{org_slug}/products/{slug}", h.HandlePublicProduct)
	r.Get("/public/shop/{org_slug}/pages/{slug}", h.HandlePublicPage)
	r.Post("/public/shop/{org_slug}/checkout", h.HandlePublicCheckout)

	if !s.authEnabled() {
		return
	}
	const base = "/organizations/{org_id}/shop"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "shop.read", "shop")
		pr.Get(base+"/settings", s.orgGated(s.shopFull(h.HandleGetSettings)))
		pr.Get(base+"/products", s.orgGated(s.shopFull(h.HandleListProducts)))
		pr.Get(base+"/products/{product_id}", s.orgGated(s.shopFull(h.HandleGetProduct)))
		pr.Get(base+"/pages", s.orgGated(s.shopFull(h.HandleListPages)))
		pr.Get(base+"/orders", s.orgGated(s.shopOrders(h.HandleListOrders)))
		pr.Get(base+"/orders/counts", s.orgGated(s.shopOrders(h.HandleOrderCounts)))
		// A search is a read whose filters (a buyer's name or e-mail) travel in
		// the body, out of the proxy and Cloudflare access logs (SA-17).
		pr.Post(base+"/orders/search", s.orgGated(s.shopOrders(h.HandleSearchOrders)))
		pr.Get(base+"/orders/{order_id}", s.orgGated(s.shopOrders(h.HandleGetOrder)))
		pr.Get(base+"/orders/{order_id}/courier-text", s.orgGated(s.shopOrders(h.HandleCourierText)))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "shop.manage", "shop")
		pr.Put(base+"/settings", s.orgGated(s.shopFull(h.HandlePutSettings)))
		// The organization opens and closes its own storefront (spec 61
		// section 3.4); the domain stays the superadmin's (PUT settings).
		pr.Put(base+"/storefront", s.orgGated(s.shopFull(h.HandlePutStorefront)))
		pr.Post(base+"/products", s.orgGated(s.shopFull(h.HandleCreateProduct)))
		pr.Patch(base+"/products/{product_id}", s.orgGated(s.shopFull(h.HandlePatchProduct)))
		pr.Delete(base+"/products/{product_id}", s.orgGated(s.shopFull(h.HandleDeleteProduct)))
		pr.Post(base+"/products/{product_id}/images", s.orgGated(s.shopFull(h.HandleAddImage)))
		pr.Delete(base+"/products/{product_id}/images/{media_id}", s.orgGated(s.shopFull(h.HandleRemoveImage)))
		pr.Put(base+"/products/{product_id}/variants", s.orgGated(s.shopFull(h.HandlePutVariants)))
		pr.Put(base+"/pages/{slug}", s.orgGated(s.shopFull(h.HandlePutPage)))
		pr.Delete(base+"/pages/{slug}", s.orgGated(s.shopFull(h.HandleDeletePage)))
		pr.Post(base+"/orders/{order_id}/ship", s.orgGated(s.shopOrders(h.HandleShipOrder)))
		pr.Patch(base+"/orders/{order_id}", s.orgGated(s.shopOrders(h.HandlePatchOrder)))
		pr.Post(base+"/orders/{order_id}/resend-email", s.orgGated(s.shopOrders(h.HandleResendEmail)))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "shop.manage", "shop")
		// Money: the web asks for the password again (spec 37 section 4.5).
		pr.Use(s.requireWebStepUp)
		pr.Post(base+"/orders/{order_id}/cancel", s.orgGated(s.shopOrders(h.HandleCancelOrder)))
	})
	r.Group(func(pr chi.Router) {
		// A refund writes and approves a refunds row in one step, so it
		// needs the refund permissions a ticket refund needs as well.
		s.applyAuth(pr, "shop.manage", "shop")
		pr.Use(s.orgScoped("refund.create"))
		pr.Use(permissions.RequirePermission(s.perms, "refund.create", "refunds"))
		pr.Use(s.orgScoped("refund.approve"))
		pr.Use(permissions.RequirePermission(s.perms, "refund.approve", "refunds"))
		pr.Use(s.requireWebStepUp)
		pr.Post(base+"/orders/{order_id}/refund", s.orgGated(s.shopOrders(h.HandleRefundOrder)))
		// Resolving a parked order may return the whole payment (SB-04), so
		// it carries the refund permissions and the password check as well.
		pr.Post(base+"/orders/{order_id}/resolve", s.orgGated(s.shopOrders(h.HandleResolveOrder)))
	})
}

// shopFull lets a shop route through only while the organization has the
// shop module today (orgmodules.ModeFull).
func (s *Server) shopFull(h http.HandlerFunc) http.HandlerFunc {
	return s.shopModuleGate(false, h)
}

// shopOrders lets an order route through while the module is connected or
// the organization has a paid or an open shop order (orders_only).
func (s *Server) shopOrders(h http.HandlerFunc) http.HandlerFunc {
	return s.shopModuleGate(true, h)
}

// shopModuleGate resolves the organization's shop mode on the pool, BEFORE
// any transaction of the handler, and answers 404 shop.module_not_enabled
// when the mode does not allow the route: for the organization there is no
// shop, and a platform superadmin gets the same answer (the module is
// connected in the admin routes, not around them). The mode is parked on the
// context for the settings answers. Without a database the handler runs and
// answers 503 itself.
func (s *Server) shopModuleGate(ordersOK bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.pgxPool == nil {
			h(w, r)
			return
		}
		org, ok := httputil.UUIDPathParam(w, r, "org_id")
		if !ok {
			return
		}
		mode, err := orgmodules.ShopMode(r.Context(), gen.New(s.pgxPool), org, time.Now())
		if err != nil {
			if s.logger != nil {
				s.logger.Error("shop: resolving the shop module failed", "org_id", org.String(), "error", err.Error())
			}
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "the shop is not available right now, please try again in a moment", r))
			return
		}
		if mode != orgmodules.ModeFull && (!ordersOK || mode != orgmodules.ModeOrdersOnly) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"shop.module_not_enabled", "this organization has no shop: the shop module is not connected", r))
			return
		}
		h(w, r.WithContext(hshop.WithShopMode(r.Context(), mode)))
	}
}

// shopPublicBaseURL is SHOP_PUBLIC_BASE_URL ("" without a config).
func (s *Server) shopPublicBaseURL() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.ShopPublicBaseURL
}
