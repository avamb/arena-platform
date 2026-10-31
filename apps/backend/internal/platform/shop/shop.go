// Package shop is the organizer's merchandise shop (spec
// 08_architecture/47_merch_shop_module_ru.md, lane L1 "shop core"): the
// catalog (settings, products, variants, pictures, static pages), the
// storefront order with its stock hold, and the order's life after payment
// (paid, shipped, cancelled, expired, refunded).
//
// Money: every amount is bigint minor units in the shop's currency
// (shop_settings.currency). The organization takes the money through its OWN
// payment provider account, exactly like tickets: a shop order owns a
// payment_intents row whose checkout_session_id is NULL, and
// shop_orders.payment_intent_id points back at it. The payment webhook
// (hcheckout) hands a shop payment to FulfillPaidOrderTx / MarkPaymentFailedTx
// inside its own transaction.
//
// Stock: shop_variants.stock is what is on the shelf. An order holds its
// quantities while it is pending_payment and not past expires_at
// (gen.ShopHeldSQL); availability is stock minus held. Payment turns the hold
// into a sale (stock goes down), the expiry sweep (JobType) or a cancellation
// ends it. shop_stock_moves journals every movement.
//
// This package never imports an HTTP package: hcheckout imports it, and the
// HTTP layer (httpserver/hshop) imports both.
package shop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// Order statuses (shop_orders_status_check, migration 0172).
const (
	StatusPendingPayment    = "pending_payment"
	StatusPaid              = "paid"
	StatusShipped           = "shipped"
	StatusCancelled         = "cancelled"
	StatusExpired           = "expired"
	StatusRefunded          = "refunded"
	StatusPartiallyRefunded = "partially_refunded"
	// StatusManualReview is a payment that arrived after the order's hold
	// ended while its goods were no longer there: the money is recorded,
	// nothing was taken off the shelf, and the operator ships (when the
	// goods turn up) or refunds it. The ops watchdog alerts on it.
	StatusManualReview = "manual_review"
)

// Product kinds (shop_products_kind_check, spec 47 section 1b). A physical
// product tracks stock and needs a shipping address; a digital one sells a
// file (owner_type shop_digital) and never tracks stock; a service needs no
// address and may track stock or not.
const (
	KindPhysical = "physical"
	KindDigital  = "digital"
	KindService  = "service"
)

// MaxOptionNames is how many option names (size, colour, ...) a product has.
const MaxOptionNames = 3

// DigitalOwnerType is the media owner_type of a digital product's file;
// PictureOwnerType that of a product picture. They never mix.
const (
	DigitalOwnerType = "shop_digital"
	PictureOwnerType = "shop_product"
)

// Product statuses (shop_products_status_check).
const (
	ProductDraft    = "draft"
	ProductActive   = "active"
	ProductArchived = "archived"
)

// Outbox event types; aggregate_type is AggregateType, aggregate_id the order id.
const (
	AggregateType      = "shop_order"
	EventOrderPaid     = "v1.shop.order.paid"
	EventOrderShipped  = "v1.shop.order.shipped"
	EventOrderRefunded = "v1.shop.order.refunded"
	// EventOrderManualReview: a late payment was parked in manual_review.
	EventOrderManualReview = "v1.shop.order.manual_review"
)

// Stock move reasons (shop_stock_moves_reason_check).
const (
	MoveReserve = "reserve"
	MoveRelease = "release"
	MoveSale    = "sale"
	MoveRefund  = "refund"
	MoveManual  = "manual"
	MoveImport  = "import"
)

// Error is a refusal the HTTP layer answers as is: Status, a stable Code and
// a buyer- or operator-readable Message. Details, when set, go into the error
// envelope (the out-of-stock list).
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// AsError unwraps a typed refusal.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Common refusals.
var (
	ErrShopNotFound    = newErr(http.StatusNotFound, "shop.not_found", "this shop does not exist")
	ErrProductNotFound = newErr(http.StatusNotFound, "shop.product_not_found", "product not found")
	ErrOrderNotFound   = newErr(http.StatusNotFound, "shop.order_not_found", "order not found")
	ErrPageNotFound    = newErr(http.StatusNotFound, "shop.page_not_found", "page not found")
)

// DB is what the service needs from the pool: queries and transactions.
// *pgxpool.Pool satisfies it.
type DB interface {
	gen.DBTX
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// RefundDriver sends an approved refund to its provider (the refund engine,
// *refunds.Engine). nil leaves the refund for the worker's refund.sweep.
type RefundDriver interface {
	Drive(ctx context.Context, id uuid.UUID) (refunds.Refund, error)
}

// Service is the shop core.
type Service struct {
	db      DB
	q       *gen.Queries
	events  outbox.Writer
	refunds RefundDriver
	pages   PageExpirer
	logger  *slog.Logger
	// currencies tells which currencies a payment provider can charge in
	// (settings are refused with 422 shop.currency_unsupported otherwise).
	currencies CurrencySource
}

// PageExpirer closes a provider's hosted payment page before its own expiry
// (Stripe's checkout session expire). providerPaymentID is the stored
// payment_intents.provider_payment_id. A provider that cannot do it answers
// nil; an error is logged, never surfaced: a payment that still arrives is a
// late payment and FulfillPaidOrderTx handles it without overselling.
type PageExpirer interface {
	ExpireHostedPage(ctx context.Context, orgID uuid.UUID, provider, providerPaymentID string) error
}

// New builds the service. db may be nil: every call then fails with
// ErrUnavailable, which the HTTP layer answers 503.
func New(db DB, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{db: db, events: outbox.NewPGEventsWriter(nil), logger: logger}
	if db != nil {
		s.q = gen.New(db)
	}
	return s
}

// WithRefunds sets the refund engine that drives a refund right after it is
// recorded.
func (s *Service) WithRefunds(d RefundDriver) *Service {
	s.refunds = d
	return s
}

// WithPageExpirer sets what closes a cancelled order's hosted payment page.
func (s *Service) WithPageExpirer(e PageExpirer) *Service {
	s.pages = e
	return s
}

// WithCurrencies sets where the settings check a currency against the shop's
// payment provider.
func (s *Service) WithCurrencies(c CurrencySource) *Service {
	s.currencies = c
	return s
}

// WithEvents replaces the outbox writer (tests).
func (s *Service) WithEvents(w outbox.Writer) *Service {
	if w != nil {
		s.events = w
	}
	return s
}

// ErrUnavailable: the service has no database.
var ErrUnavailable = newErr(http.StatusServiceUnavailable, "dependency.database_unavailable", "database is not available")

func (s *Service) ready() error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	return nil
}

// inTx runs fn in one transaction, committing when it returns nil.
func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx, q *gen.Queries) error) error {
	if err := s.ready(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("shop: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := fn(tx, s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("shop: commit: %w", err)
	}
	return nil
}

// isUniqueViolation reports a 23505, optionally on one constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" {
		return false
	}
	return constraint == "" || pg.ConstraintName == constraint
}

// appendEvent writes one outbox event of an order inside tx.
func (s *Service) appendEvent(ctx context.Context, tx pgx.Tx, eventType string, o gen.ShopOrderRow, extra map[string]any) error {
	payload := map[string]any{
		"order_id":       o.ID.String(),
		"org_id":         o.OrgID.String(),
		"number":         o.Number,
		"status":         o.Status,
		"currency":       o.Currency,
		"total":          o.Total,
		"refunded_minor": o.RefundedMinor,
	}
	for k, v := range extra {
		payload[k] = v
	}
	return s.events.Append(ctx, tx, outbox.Event{
		AggregateType: AggregateType,
		AggregateID:   o.ID.String(),
		EventType:     eventType,
		Payload:       payload,
	})
}
