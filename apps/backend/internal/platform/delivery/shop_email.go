// shop_email.go — the shop.order_email worker job: the letters arena writes to
// the buyer of a merchandise shop order (spec 47 section 7, lane L5).
//
// internal/platform/shop queues one job in the SAME transaction that marks the
// order paid ("received"), shipped ("shipped") or a refund succeeded
// ("refunded", one letter per refund). The handler:
//
//  1. loads the order, its lines, the shop settings and the organization;
//  2. skips what no longer makes sense (an order that was cancelled or expired
//     in between, a buyer without an address, a deployment without a
//     production sender) and ends without an error, so nothing retries
//     forever;
//  3. renders the letter in the buyer's language (shop_orders.buyer_locale, en
//     when unknown) through the templates package, with Reply-To set to the
//     shop's contact e-mail, and sends it;
//  4. remembers the send in shop_orders.metadata (email_<kind>_sent_at), so a
//     redelivered job never mails twice.
//
// The status link is the storefront's result page: the organization's own
// domain (organizations.shop_domain) when it has one, else the platform shop
// host (SHOP_PUBLIC_BASE_URL) plus the organization's slug. Digital lines
// point at that page, where the short-lived download links are issued; a
// signed download URL never goes into a letter. An order with nothing to
// ship (only digital or service lines) carries no address and no shipping
// fee. Buyer addresses never reach the logs.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// ShopEmailOptions are the dependencies of the shop.order_email job.
type ShopEmailOptions struct {
	// HandlerOptions supplies the sender, media resolver and templates.
	HandlerOptions
	// DB reads the order, the shop settings and the organization.
	DB gen.DBTX
	// ShopBaseURL is SHOP_PUBLIC_BASE_URL, the platform's storefront origin
	// for organizations without their own domain. Empty: such an order's
	// letter carries no status link.
	ShopBaseURL string
}

// NewShopEmailHandler builds the worker handler of shop.JobTypeOrderEmail.
func NewShopEmailHandler(opts ShopEmailOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: shop e-mail templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	h := &shopEmail{opts: opts, logger: logger, renderer: renderer, q: gen.New(opts.DB)}
	return h.handle
}

type shopEmail struct {
	opts     ShopEmailOptions
	logger   *slog.Logger
	renderer *templates.Renderer
	q        *gen.Queries
}

func (h *shopEmail) handle(ctx context.Context, payload []byte) error {
	var p shop.OrderEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		h.logger.Error("delivery: malformed shop e-mail payload", slog.String("error", err.Error()))
		return nil // permanent: a retry cannot fix a bad payload
	}
	orderID, err1 := uuid.Parse(p.OrderID)
	orgID, err2 := uuid.Parse(p.OrgID)
	var kind string
	switch p.Kind {
	case shop.EmailReceived:
		kind = templates.TemplateKindShopOrderReceived
	case shop.EmailShipped:
		kind = templates.TemplateKindShopOrderShipped
	case shop.EmailRefunded:
		kind = templates.TemplateKindShopOrderRefunded
	}
	var refundID uuid.UUID
	if p.Kind == shop.EmailRefunded {
		var err3 error
		if refundID, err3 = uuid.Parse(p.RefundID); err3 != nil {
			h.logger.Error("delivery: shop refund e-mail without a refund id", slog.String("order_id", p.OrderID))
			return nil
		}
	}
	if err1 != nil || err2 != nil || kind == "" {
		h.logger.Error("delivery: invalid ids or kind in shop e-mail payload", slog.String("kind", p.Kind))
		return nil
	}

	o, err := h.q.GetShopOrder(ctx, orderID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.logger.Warn("delivery: shop e-mail for an order that is gone; dropping", slog.String("order_id", orderID.String()))
		return nil
	}
	if err != nil {
		return fmt.Errorf("delivery: read shop order %s: %w", orderID, err)
	}
	if reason := shopEmailSkipReason(o, p.Kind); reason != "" {
		h.logger.Info("delivery: shop e-mail not sent", slog.String("order_id", orderID.String()), slog.String("reason", reason))
		return nil
	}
	// A person asked for this letter again: the marker of the first send must
	// not swallow it. A redelivered job of the same request is a duplicate
	// letter at worst, which the one-per-minute limit of the route bounds.
	if !p.Resend {
		if sent, err := h.alreadySent(ctx, orderID, sentKey(p.Kind, refundID)); err != nil {
			return err
		} else if sent {
			return nil
		}
	}
	if h.opts.Sender == nil || email.IsDevOnly(h.opts.Sender) {
		h.logger.Info("delivery: shop e-mail not sent", slog.String("order_id", orderID.String()),
			slog.String("reason", "no production e-mail sender configured"))
		return nil
	}

	var refund *shopRefund
	if p.Kind == shop.EmailRefunded {
		r, err := h.loadRefund(ctx, o, refundID)
		if err != nil {
			return err
		}
		if r == nil {
			h.logger.Info("delivery: shop e-mail not sent", slog.String("order_id", orderID.String()),
				slog.String("reason", "the refund did not succeed or is not this order's"))
			return nil
		}
		refund = r
	}
	msg, err := h.buildMessage(ctx, o, kind, refund)
	if err != nil {
		return err
	}
	if err := h.opts.Sender.Send(ctx, msg); err != nil {
		h.logger.Warn("delivery: shop e-mail send failed; worker will retry",
			slog.String("order_id", orderID.String()), slog.String("kind", p.Kind))
		return fmt.Errorf("delivery: send shop e-mail for order %s: %w", orderID, err)
	}
	if err := h.markSent(ctx, orderID, sentKey(p.Kind, refundID)); err != nil {
		// The letter is out; failing here would retry (and mail) it again.
		h.logger.Error("delivery: could not remember the shop e-mail", slog.String("order_id", orderID.String()),
			slog.String("error", err.Error()))
	}
	h.logger.Info("delivery: shop e-mail sent", slog.String("order_id", orderID.String()), slog.String("kind", p.Kind))
	return nil
}

// shopEmailSkipReason is why a letter must not go out ("" = send it).
func shopEmailSkipReason(o gen.ShopOrderRow, kind string) string {
	if strings.TrimSpace(o.BuyerEmail) == "" {
		return "no address for the buyer"
	}
	switch o.Status {
	case shop.StatusCancelled, shop.StatusExpired, shop.StatusPendingPayment:
		return "the order is " + o.Status
	case shop.StatusManualReview:
		// Money that went back is the buyer's business even while the order
		// waits for the organizer's decision.
		if kind != shop.EmailRefunded {
			return "the order is " + o.Status
		}
	}
	if kind == shop.EmailShipped && o.DeliveryKind == shop.DeliveryNone {
		return "nothing ships: a digital or service order"
	}
	if kind == shop.EmailShipped && o.ShippedAt == nil {
		return "the order is not shipped"
	}
	return ""
}

// sentKey is the shop_orders.metadata key that remembers a send. The refund
// letter is remembered per refund (an order may be refunded several times).
func sentKey(kind string, refundID uuid.UUID) string {
	if kind == shop.EmailRefunded {
		return "email_" + kind + "_" + refundID.String() + "_sent_at"
	}
	return "email_" + kind + "_sent_at"
}

func (h *shopEmail) alreadySent(ctx context.Context, orderID uuid.UUID, key string) (bool, error) {
	var sent bool
	if err := h.opts.DB.QueryRow(ctx, `SELECT metadata ? $2 FROM shop_orders WHERE id = $1`, orderID, key).Scan(&sent); err != nil {
		return false, fmt.Errorf("delivery: read shop e-mail marker: %w", err)
	}
	return sent, nil
}

func (h *shopEmail) markSent(ctx context.Context, orderID uuid.UUID, key string) error {
	_, err := h.opts.DB.Exec(ctx,
		`UPDATE shop_orders SET metadata = metadata || jsonb_build_object($2::text, now()) WHERE id = $1`,
		orderID, key)
	return err
}

// shopRefund is the refund a refunded letter is about: its own amount and what
// the order had got back once it succeeded (the refunds that succeeded up to
// and including it), so a letter that is sent late, or sent again, still says
// what it said the first time.
type shopRefund struct {
	Amount, Running int64
}

// loadRefund reads the refund of the order's payment. nil: it is not this
// order's, or it is not a succeeded one (the provider reversed it).
func (h *shopEmail) loadRefund(ctx context.Context, o gen.ShopOrderRow, id uuid.UUID) (*shopRefund, error) {
	if o.PaymentIntentID == nil {
		return nil, nil
	}
	var r shopRefund
	err := h.opts.DB.QueryRow(ctx, `
		SELECT r.amount,
		       (SELECT COALESCE(sum(x.amount), 0)::bigint FROM refunds x
		         WHERE x.payment_intent_id = r.payment_intent_id AND x.state = 'succeeded'
		           AND (x.updated_at, x.id) <= (r.updated_at, r.id))
		  FROM refunds r
		 WHERE r.id = $1 AND r.payment_intent_id = $2 AND r.state = 'succeeded'`,
		id, *o.PaymentIntentID).Scan(&r.Amount, &r.Running)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("delivery: read shop refund %s: %w", id, err)
	}
	return &r, nil
}

// shopOrg is the organization data a shop letter needs.
type shopOrg struct {
	Name, Slug                              string
	Website, LegalName, Line1, Line2        *string
	Postal, City, Country, ContactEmail     *string
	LogoMediaID                             *uuid.UUID
	SenderEmail                             *string
	SenderStatus                            string
	RegNumber, TaxID, TaxScheme, ShopDomain *string
}

func (h *shopEmail) loadOrg(ctx context.Context, orgID uuid.UUID) (shopOrg, error) {
	var g shopOrg
	err := h.opts.DB.QueryRow(ctx, `
		SELECT name, slug, website_url, legal_name, legal_address_line1, legal_address_line2,
		       legal_address_postal_code, legal_address_city, legal_address_country, contact_email,
		       logo_media_id, sender_email, sender_verification_status,
		       registration_number, tax_id, tax_id_scheme, shop_domain
		  FROM organizations WHERE id = $1`, orgID).Scan(
		&g.Name, &g.Slug, &g.Website, &g.LegalName, &g.Line1, &g.Line2,
		&g.Postal, &g.City, &g.Country, &g.ContactEmail,
		&g.LogoMediaID, &g.SenderEmail, &g.SenderStatus,
		&g.RegNumber, &g.TaxID, &g.TaxScheme, &g.ShopDomain)
	return g, err
}

// statusURL is the storefront's result page of the order, where the status
// and the download buttons are: the organization's own domain when it has
// one, else the platform shop host plus the organization's slug.
func statusURL(g shopOrg, platformBase, token string) string {
	const path = "/checkout/result?checkout_token="
	if d := ptrStr(g.ShopDomain); d != "" {
		return "https://" + d + path + token
	}
	base := strings.TrimRight(strings.TrimSpace(platformBase), "/")
	if base == "" || g.Slug == "" {
		return ""
	}
	return base + "/" + g.Slug + path + token
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// buildShopOrderData turns the order into what the templates print. Pure.
func buildShopOrderData(o gen.ShopOrderRow, items []gen.ShopOrderItemRow, statusLink string) *templates.ShopOrderData {
	d := &templates.ShopOrderData{
		OrderNumber:  fmt.Sprintf("%d", o.Number),
		Note:         strings.TrimSpace(o.DeliveryNote),
		TrackingNote: strings.TrimSpace(o.TrackingNote),
		StatusURL:    statusLink,
		Total:        shop.FormatMinor(o.Total, o.Currency),
	}
	physical := len(items) == 0 // an order without lines is judged by its delivery kind
	for _, it := range items {
		kind := it.ProductKind // empty on a line bought before kinds existed
		switch kind {
		case templates.ShopKindDigital, templates.ShopKindService:
		default:
			kind = templates.ShopKindPhysical
			physical = true
		}
		line := templates.ShopLine{
			Title: shop.ItemLabel(it), Qty: int(it.Qty), Kind: kind,
			UnitPrice: shop.FormatMinor(it.UnitPriceMinor, o.Currency),
			LineTotal: shop.FormatMinor(it.TotalMinor, o.Currency),
		}
		d.Items = append(d.Items, line)
		if kind == templates.ShopKindDigital {
			d.Digital = append(d.Digital, line)
		}
	}
	if o.Discount > 0 {
		d.Discount = shop.FormatMinor(o.Discount, o.Currency)
	}
	if physical && o.ShippingFee > 0 {
		d.Shipping = shop.FormatMinor(o.ShippingFee, o.Currency)
	}
	// The goods subtotal is only worth a row when something else changes the total.
	if d.Discount != "" || d.Shipping != "" {
		d.Subtotal = shop.FormatMinor(o.Subtotal, o.Currency)
	}
	if o.DeliveryKind == shop.DeliveryNone {
		physical = false // a digital-only cart: no address, no shipping fee
	}
	if physical {
		if o.DeliveryKind == "pickup" {
			d.Pickup = true
		} else {
			d.Address = shop.CourierFieldsOf(o).Lines()
		}
	}
	return d
}

func (h *shopEmail) buildMessage(ctx context.Context, o gen.ShopOrderRow, kind string, refund *shopRefund) (email.Message, error) {
	items, err := h.q.ListShopOrderItems(ctx, []uuid.UUID{o.ID})
	if err != nil {
		return email.Message{}, fmt.Errorf("delivery: read shop order lines: %w", err)
	}
	g, err := h.loadOrg(ctx, o.OrgID)
	if err != nil {
		return email.Message{}, fmt.Errorf("delivery: read organization of shop order: %w", err)
	}
	contact := ""
	if st, err := h.q.GetShopSettings(ctx, o.OrgID); err == nil {
		contact = ptrStr(st.ContactEmail)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return email.Message{}, fmt.Errorf("delivery: read shop settings: %w", err)
	}
	// Only the shop's own contact address is printed: the organization's
	// contact_email is never published on a hosted storefront (owner rule
	// 2026-10-13), so there is no fallback to it. No address, no contact line.

	p := Payload{
		OrgName: g.Name, OrgWebsiteURL: ptrStr(g.Website), OrgLegalName: ptrStr(g.LegalName),
		OrgLegalAddressLine1: ptrStr(g.Line1), OrgLegalAddressLine2: ptrStr(g.Line2),
		OrgLegalAddressPostal: ptrStr(g.Postal), OrgLegalAddressCity: ptrStr(g.City),
		OrgLegalAddressCountry: ptrStr(g.Country), OrgContactEmail: contact,
		OrgRegistrationNumber: ptrStr(g.RegNumber), OrgTaxID: ptrStr(g.TaxID), OrgTaxIDScheme: ptrStr(g.TaxScheme),
		SenderEmail: ptrStr(g.SenderEmail), SenderVerificationStatus: g.SenderStatus,
	}
	if g.LogoMediaID != nil {
		p.OrgLogoMediaID = g.LogoMediaID.String()
	}
	branding := resolveBranding(ctx, h.opts.Media, p, h.logger).Branding

	locale := ""
	if o.BuyerLocale != nil {
		locale = *o.BuyerLocale
	}
	data := templates.Data{
		RecipientEmail: o.BuyerEmail,
		HolderName:     strings.TrimSpace(o.BuyerName),
		Branding:       branding,
		Shop:           buildShopOrderData(o, items, statusURL(g, h.opts.ShopBaseURL, o.OrderToken)),
	}
	if refund != nil {
		data.Shop.RefundAmount = shop.FormatMinor(refund.Amount, o.Currency)
		// A test file of this package declares its own min, so no builtin here.
		running := refund.Running
		if running > o.Total {
			running = o.Total
		}
		data.Shop.RefundedTotal = shop.FormatMinor(running, o.Currency)
		data.Shop.RefundFull = refund.Running >= o.Total
	}
	out, err := h.renderer.Render(kind, locale, data)
	if err != nil {
		return email.Message{}, fmt.Errorf("delivery: render shop e-mail: %w", err)
	}
	return ticketEmailMessage(p, o.BuyerEmail, out.Subject, out.HTMLBody, out.TextBody), nil
}
