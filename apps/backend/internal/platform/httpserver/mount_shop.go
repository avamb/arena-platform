package httpserver

// mount_shop.go — the merchandise shop (spec 08_architecture/47 section 4,
// lane L1). Organization routes need shop.read / shop.manage plus membership
// of {org_id}; refund and cancel also run the web's password check
// (requireWebStepUp, like every money route). Public routes need no login
// and share the public feed's per-IP rate limit.
//
// The shop's payments arrive through the existing payment webhook routes:
// checkoutHandler wires shopPayments into hcheckout, which hands a payment
// intent without a checkout session to the shop on the webhook transaction.

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hshop"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
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
		svc = svc.WithPageExpirer(shopPageExpirer{modules: hcheckout.NewRefundModuleSource(gen.New(s.pgxPool), s.paymentModuleOptions())})
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
		ReturnPolicy: s.checkoutReturnURLPolicy(),
		Window:       widgetPaymentWindow(s.cfg),
		Grace:        widgetPaymentGrace(s.cfg),
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
	}
	return hshop.New(opts)
}

func (s *Server) mountShopRoutes(r chi.Router) {
	h := s.shopHandler()

	// Public storefront routes, mounted without a database too (503 then).
	// The static segments (by-host, checkout, pages) win over {org_slug}.
	r.Get("/public/shop/by-host", h.HandleByHost)
	r.Get("/public/shop/checkout/{order_token}", h.HandlePublicCheckoutStatus)
	r.Get("/public/shop/pages/{org_slug}", h.HandlePublicPages)
	r.Get("/public/shop/{org_slug}", h.HandlePublicCatalog)
	r.Get("/public/shop/{org_slug}/products/{slug}", h.HandlePublicProduct)
	r.Post("/public/shop/{org_slug}/checkout", h.HandlePublicCheckout)

	if !s.authEnabled() {
		return
	}
	const base = "/organizations/{org_id}/shop"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "shop.read", "shop")
		pr.Get(base+"/settings", s.orgGated(h.HandleGetSettings))
		pr.Get(base+"/products", s.orgGated(h.HandleListProducts))
		pr.Get(base+"/products/{product_id}", s.orgGated(h.HandleGetProduct))
		pr.Get(base+"/pages", s.orgGated(h.HandleListPages))
		pr.Get(base+"/orders", s.orgGated(h.HandleListOrders))
		pr.Get(base+"/orders/{order_id}", s.orgGated(h.HandleGetOrder))
		pr.Get(base+"/orders/{order_id}/courier-text", s.orgGated(h.HandleCourierText))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "shop.manage", "shop")
		pr.Put(base+"/settings", s.orgGated(h.HandlePutSettings))
		pr.Post(base+"/products", s.orgGated(h.HandleCreateProduct))
		pr.Patch(base+"/products/{product_id}", s.orgGated(h.HandlePatchProduct))
		pr.Delete(base+"/products/{product_id}", s.orgGated(h.HandleDeleteProduct))
		pr.Post(base+"/products/{product_id}/images", s.orgGated(h.HandleAddImage))
		pr.Delete(base+"/products/{product_id}/images/{media_id}", s.orgGated(h.HandleRemoveImage))
		pr.Put(base+"/products/{product_id}/variants", s.orgGated(h.HandlePutVariants))
		pr.Put(base+"/pages/{slug}", s.orgGated(h.HandlePutPage))
		pr.Delete(base+"/pages/{slug}", s.orgGated(h.HandleDeletePage))
		pr.Post(base+"/orders/{order_id}/ship", s.orgGated(h.HandleShipOrder))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "shop.manage", "shop")
		// Money: the web asks for the password again (spec 37 section 4.5).
		pr.Use(s.requireWebStepUp)
		pr.Post(base+"/orders/{order_id}/cancel", s.orgGated(h.HandleCancelOrder))
		pr.Post(base+"/orders/{order_id}/refund", s.orgGated(h.HandleRefundOrder))
	})
}
