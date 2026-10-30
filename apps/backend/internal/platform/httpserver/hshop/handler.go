// Package hshop serves the merchandise shop (spec
// 08_architecture/47_merch_shop_module_ru.md section 4, lane L1).
//
// Organization routes, JWT or API key, reads need shop.read and writes
// shop.manage (the mount applies the gate and the membership check of
// {org_id}); every row is loaded by (id, org_id), so another organization's
// product or order is the route's own 404:
//
//	GET|PUT   /v1/organizations/{org_id}/shop/settings
//	GET|POST  /v1/organizations/{org_id}/shop/products
//	GET|PATCH|DELETE /v1/organizations/{org_id}/shop/products/{product_id}
//	POST      /v1/organizations/{org_id}/shop/products/{product_id}/images
//	DELETE    /v1/organizations/{org_id}/shop/products/{product_id}/images/{media_id}
//	PUT       /v1/organizations/{org_id}/shop/products/{product_id}/variants
//	GET       /v1/organizations/{org_id}/shop/pages
//	PUT|DELETE /v1/organizations/{org_id}/shop/pages/{slug}
//	GET       /v1/organizations/{org_id}/shop/orders
//	GET       /v1/organizations/{org_id}/shop/orders/counts
//	GET|PATCH /v1/organizations/{org_id}/shop/orders/{order_id}
//	POST      /v1/organizations/{org_id}/shop/orders/{order_id}/resend-email
//	POST      /v1/organizations/{org_id}/shop/orders/{order_id}/ship
//	POST      /v1/organizations/{org_id}/shop/orders/{order_id}/cancel
//	POST      /v1/organizations/{org_id}/shop/orders/{order_id}/refund
//	POST      /v1/organizations/{org_id}/shop/orders/{order_id}/resolve
//	GET       /v1/organizations/{org_id}/shop/orders/{order_id}/courier-text
//
// Public routes, no login, rate-limited per client IP like the feed:
//
//	GET  /v1/public/shop/by-host?host=
//	GET  /v1/public/shop/checkout/{order_token}
//	GET  /v1/public/shop/checkout/{order_token}/downloads
//	GET  /v1/public/shop/checkout/{order_token}/downloads/{item_id}
//	GET  /v1/public/shop/pages/{org_slug}
//	GET  /v1/public/shop/{org_slug}
//	GET  /v1/public/shop/{org_slug}/pages/{slug}
//	GET  /v1/public/shop/{org_slug}/products/{slug}
//	POST /v1/public/shop/{org_slug}/checkout
package hshop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// maxBodyBytes bounds a JSON body; a product description or page may be long.
const maxBodyBytes = 512 << 10

// Options wires the handler.
type Options struct {
	Service *shop.Service
	Audit   audit.Writer
	Logger  *slog.Logger
	// MediaURL turns a media id into an absolute signed URL ("" when it
	// cannot); nil leaves picture URLs empty.
	MediaURL func(ctx context.Context, id uuid.UUID) string
	// DownloadURL signs a short-lived absolute URL of a digital product's
	// file and says until when it works ("" when it cannot); nil answers
	// the downloads route 503.
	DownloadURL func(ctx context.Context, id uuid.UUID) (string, time.Time)
	// APIBaseURL is the public address of this API (no trailing slash), used
	// to build the absolute link of the download file route; empty uses the
	// address the request arrived on.
	APIBaseURL string
	// Payments creates the provider-hosted page; nil refuses paid carts.
	Payments hfeed.PaymentStarter
	// Bundle localizes the payment page's purpose line; nil keeps it English.
	Bundle *i18n.Bundle
	// ReturnPolicy decides where the buyer may be sent back to.
	ReturnPolicy hfeed.ReturnURLPolicy
	// Window is how long the hosted page stays open; Grace is added on top
	// for the order's hold, so the hold outlives the page.
	Window time.Duration
	Grace  time.Duration
	// IPLimit and TokenLimit are the per-minute buckets of the public
	// routes (nil = unlimited).
	IPLimit        func(ip string) (bool, int)
	TokenLimit     func(token string) (bool, int)
	TrustedProxies int
	// CheckoutIPLimit is the checkout's own budget per shop and client
	// address, counted once the body is valid JSON: placing an order holds
	// stock, so it is far tighter than reading the catalog. 0 means
	// DefaultCheckoutIPLimit, a negative value switches it off.
	CheckoutIPLimit int
	// Captcha verifies the Turnstile token of a checkout. Nil switches the
	// captcha off (the storefront then gets no captcha_site_key); set, a
	// checkout without a valid token is refused 422 before it holds stock.
	Captcha CaptchaVerifier
	// CaptchaSiteKey is the public Turnstile site key handed to the
	// storefront in the catalog settings; only published while Captcha is set.
	CaptchaSiteKey string
	// IPSalt salts the hash of the client key stored on an order (the cap on
	// live unpaid orders per address counts it); the server passes the JWT
	// secret, like the other tables that keep an address hash.
	IPSalt string
}

// CaptchaVerifier checks a Cloudflare Turnstile response token
// (honboarding.TurnstileVerifier is the implementation).
type CaptchaVerifier interface {
	Verify(ctx context.Context, token, remoteIP string) (bool, error)
}

// DefaultCheckoutIPLimit is how many checkouts one client address may start
// per minute in one shop.
const DefaultCheckoutIPLimit = 30

// Handler serves the shop routes. It is built once per server: the checkout
// limiter lives on it.
type Handler struct {
	o        Options
	checkout *minuteLimiter
	// resend allows one "send the buyer's letter again" per order per minute.
	resend *minuteLimiter
}

// New builds the handler.
func New(o Options) *Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Window <= 0 {
		o.Window = 31 * time.Minute
	}
	if o.Grace < 0 {
		o.Grace = 0
	}
	h := &Handler{o: o, resend: newMinuteLimiter(1)}
	switch {
	case o.CheckoutIPLimit == 0:
		h.checkout = newMinuteLimiter(DefaultCheckoutIPLimit)
	case o.CheckoutIPLimit > 0:
		h.checkout = newMinuteLimiter(o.CheckoutIPLimit)
	}
	return h
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

// fail answers an error: a shop refusal or a payment-start refusal as it is,
// anything else as a logged 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	if se, ok := shop.AsError(err); ok {
		if se.Details != nil {
			httputil.WriteJSON(w, se.Status, httputil.ErrorEnvelopeWithDetails(se.Code, se.Message, r, se.Details))
			return
		}
		writeErr(w, r, se.Status, se.Code, se.Message)
		return
	}
	if pse, ok := hfeed.AsPaymentStartError(err); ok {
		writeErr(w, r, pse.Status, pse.Code, pse.Message)
		return
	}
	h.o.Logger.Error("shop: "+what+" failed", slog.String("error", err.Error()))
	writeErr(w, r, http.StatusInternalServerError, "shop.failed", "could not complete the request, please try again")
}

// readBody decodes a JSON body strictly: one object, no unknown members. An
// empty body is accepted when allowEmpty (dst keeps its zero value).
func readBody(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_body", "the request body is too large or unreadable")
		return false
	}
	if strings.TrimSpace(string(raw)) == "" {
		if allowEmpty {
			return true
		}
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_body", "the request body is missing, send a JSON object as the documentation shows")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_body",
			"the request body is not valid JSON for this route, check the field names and types")
		return false
	}
	return true
}

// pathID reads a uuid path parameter; a malformed one is notFound (the row
// cannot exist).
func pathID(w http.ResponseWriter, r *http.Request, name string, notFound *shop.Error) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, name)))
	if err != nil {
		writeErr(w, r, notFound.Status, notFound.Code, notFound.Message)
		return uuid.Nil, false
	}
	return id, true
}

// orgID reads {org_id}; the mount's membership guard already checked it.
func orgID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	return httputil.UUIDPathParam(w, r, "org_id")
}

// actorLabel is the acting user's id for refunds.requested_by, or the
// principal type for an API key.
func actorLabel(ctx context.Context) string {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok {
		return ""
	}
	if actor.Type == auth.ActorTypeUser && actor.ID != "" {
		return "user:" + actor.ID
	}
	return string(actor.Type)
}

// writeAudit records one change after it committed. Best effort: a failed
// audit write is logged, the answer stands.
func (h *Handler) writeAudit(r *http.Request, action, resourceType, resourceID string, orgID uuid.UUID, meta map[string]any) {
	if h.o.Audit == nil {
		return
	}
	ctx := r.Context()
	actor, _ := auth.ActorFromContext(ctx)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["org_id"] = orgID.String()
	actorID := ""
	if _, err := uuid.Parse(actor.ID); err == nil {
		actorID = actor.ID
	}
	if err := h.o.Audit.Write(ctx, audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	}); err != nil {
		// The change itself already committed, so the answer stands; the log
		// line carries what the missing audit row would have named.
		h.o.Logger.Error("shop: audit write failed",
			slog.String("action", action), slog.String("resource_type", resourceType),
			slog.String("resource_id", resourceID), slog.String("org_id", orgID.String()),
			slog.String("error", err.Error()))
	}
}

// clientIP keys the per-IP bucket (never the raw X-Forwarded-For).
func (h *Handler) clientIP(r *http.Request) string {
	return httputil.TrustedClientIP(r, h.o.TrustedProxies)
}

// clientKey is the address the checkout is counted under: an IPv4 address,
// or the /64 prefix of an IPv6 one (httputil.ClientKey, the rule every other
// per-address limit of the API uses).
func (h *Handler) clientKey(r *http.Request) string {
	return httputil.ClientKey(h.clientIP(r))
}

// clientKeyHash is the salted sha256 of the client key stored on an order
// ("" when the address is unknown), never the address itself.
func (h *Handler) clientKeyHash(r *http.Request) string {
	key := h.clientKey(r)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(h.o.IPSalt + "|shop|" + key))
	return hex.EncodeToString(sum[:])
}

// limited applies the per-IP bucket and, when token is not empty, the
// per-token bucket; both are always counted. It answers 429 itself.
func (h *Handler) limited(w http.ResponseWriter, r *http.Request, token string) bool {
	okIP, retryIP := true, 0
	if h.o.IPLimit != nil {
		okIP, retryIP = h.o.IPLimit(h.clientIP(r))
	}
	okTok, retryTok := true, 0
	if token != "" && h.o.TokenLimit != nil {
		okTok, retryTok = h.o.TokenLimit(token)
	}
	if okIP && okTok {
		return false
	}
	retry := max(1, retryIP, retryTok)
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	writeErr(w, r, http.StatusTooManyRequests, "shop.rate_limited", "Too many requests. Wait one minute and try again.")
	return true
}

// checkoutLimited applies the checkout's own budget of one shop and client
// address. It answers 429 itself.
func (h *Handler) checkoutLimited(w http.ResponseWriter, r *http.Request, shopKey string) bool {
	if h.checkout == nil {
		return false
	}
	ok, retry := h.checkout.allow(shopKey+"|"+h.clientKey(r), time.Now())
	if ok {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	writeErr(w, r, http.StatusTooManyRequests, "shop.rate_limited", "Too many orders from this address. Wait one minute and try again.")
	return true
}

// cacheable marks a public read as cacheable for 30 seconds by origin.
func cacheable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=30")
	addVaryOrigin(w)
}

// addVaryOrigin adds Origin to Vary once (the CORS middleware may have).
func addVaryOrigin(w http.ResponseWriter) {
	for _, v := range w.Header().Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "Origin") {
				return
			}
		}
	}
	w.Header().Add("Vary", "Origin")
}
