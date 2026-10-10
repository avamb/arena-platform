// Package refunds is the refund ENGINE (spec
// 08_architecture/36_payment_modules_refunds_acquiring_ru.md §4 and §7,
// PAY-03): the one place arena returns money through a payment module.
//
// The order of operations is the whole point, and it never changes:
//
//  1. RECORD — the refund rows are written and committed in their own
//     transaction, which takes pg_advisory_xact_lock on the payment as its
//     first statement. Every writer of a payment's refunds (this package,
//     the flat POST /v1/refunds and approve routes, arena-api and
//     arena-worker alike) takes the same lock, so two concurrent refunds of
//     one payment serialize and the sum of its live refunds can never
//     exceed the payment.
//  2. CALL — the provider is called OUTSIDE any database transaction, through
//     the module's payments.Refunder, with the refund id as idempotency key,
//     so a retry can never return the money twice. A short claim transaction
//     before the call stamps provider_attempted_at: a refund with a call in
//     flight (or one younger than a minute) is never called again, and no
//     two refunds of one payment are at the provider at once.
//  3. RESULT — the answer is written in a new transaction.
//  4. TICKET — only when the provider ACCEPTED the refund (succeeded, or
//     pending at an async provider) is the ticket cancelled, through
//     htickets.CancelTicketTx with the refund id (owner decision 2026-09-20:
//     money first, then the ticket). A refusal leaves the ticket valid and
//     the refund failed with the provider's code; an unknown outcome
//     (timeout, 5xx) leaves it provider_pending for refund.sweep.
//
// The engine names no provider: it asks a ModuleSource (wired from the
// module registry) and acts on the descriptor's capabilities.
package refunds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// Refund states (refunds_state_check, migration 0028).
const (
	StateRequested       = "requested"
	StateApproved        = "approved"
	StateProviderPending = "provider_pending"
	StateSucceeded       = "succeeded"
	StateFailed          = "failed"
	StateRejected        = "rejected"
	StateManualReview    = "manual_review"
)

// providerManual is not a module: it marks a payment taken by the SELLER
// (the Bil24 gateway's sites) outside arena — spec 36 §5 lists it, with
// "none", as an allowed marker. orderSourceGateway is the same fact on the
// order.
const (
	providerManual     = "manual"
	orderSourceGateway = "bil24_gateway"
)

// Timings.
const (
	// DefaultCallTimeout bounds one provider call (module build, charge
	// reference, refund) and, separately, the recording of its answer.
	DefaultCallTimeout = 25 * time.Second
	// CallStaleAfter: a refund whose last call started longer ago than this
	// and has no provider refund id may be called again (same idempotency
	// key). Spec §7: never touch one younger than a minute. A call plus the
	// recording of its answer (2 x CallTimeout) must fit inside it, or a
	// second attempt could start while the first is still alive.
	CallStaleAfter = time.Minute
	// WorkerStaleClaimTimeout mirrors the worker's default stale-claim
	// timeout (internal/platform/worker): a sweep pass that outlives it is
	// handed to another worker while it still runs.
	WorkerStaleClaimTimeout = 5 * time.Minute
)

// Validate checks the timing options against the fixed limits above
// (third review, L2). New panics on an invalid configuration: the options
// are code, not user input, so a bad value must stop the process at start-up
// rather than double-refund in production.
func (o Options) Validate() error {
	call, pass := o.CallTimeout, o.SweepPassTimeout
	if call <= 0 {
		call = DefaultCallTimeout
	}
	if pass <= 0 {
		pass = DefaultSweepPassTimeout
	}
	if 2*call >= CallStaleAfter {
		return fmt.Errorf("refunds: 2 x CallTimeout (%s) must stay below CallStaleAfter (%s)", 2*call, CallStaleAfter)
	}
	if bound := SweepWorstCase(call, pass); bound >= WorkerStaleClaimTimeout {
		return fmt.Errorf("refunds: a sweep pass may take %s, not below the worker's stale-claim timeout %s", bound, WorkerStaleClaimTimeout)
	}
	return nil
}

// SweepWorstCase is the longest one refund.sweep pass can run with these
// timings (fifth review, LOW a). The sections (park, retry, lookup, repair)
// add up to the pass timeout, and each runs on its own deadline, so their
// overruns add up too: the last item of a section may outlive its deadline
// by what runs on detached contexts —
//   - retry: a drive's call and the recording of its answer (2 x call),
//     then one ticket cancellation already started (ticketCancelTimeout)
//     and the settle writes (settleWriteTimeout);
//   - lookup: one lookup call, then the same settle tail;
//   - repair: the settle tail;
//   - alerts: their own budget plus one bookkeeping write.
func SweepWorstCase(call, pass time.Duration) time.Duration {
	tail := ticketCancelTimeout + settleWriteTimeout
	return pass + alertSectionBudget + alertWriteTimeout +
		(2*call + tail) + (call + tail) + tail
}

// DB is what the engine needs from the pool: transactions only, so the
// hcheckout handler's narrow TxStarter and a *pgxpool.Pool both fit.
type DB interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// ModuleSource reaches the payment modules without naming a provider.
type ModuleSource interface {
	// Descriptor returns the REGISTERED descriptor of provider (the zero
	// value when the registry does not know it) and whether a module exists
	// behind it (false for an unknown or a declared-only provider).
	Descriptor(provider string) (payments.Descriptor, bool)
	// Build constructs provider's module from orgID's OWN configuration
	// (every organizer holds their own provider account). A configuration
	// that is missing or unusable is a *ConfigError; any other error is
	// transient.
	Build(ctx context.Context, orgID uuid.UUID, provider string) (payments.Module, error)
}

// ConfigError: the organization has no usable configuration for the
// provider. Calling cannot succeed until an operator fixes it, so a refund
// that meets it fails (no money moved) rather than waiting.
type ConfigError struct {
	Code    string
	Message string
}

func (e *ConfigError) Error() string { return e.Code + ": " + e.Message }

// CancelRequest asks the ticket side to cancel one ticket for a refund the
// provider accepted.
type CancelRequest struct {
	TicketID uuid.UUID
	RefundID uuid.UUID
	// Amount is the refunded amount in minor units, stamped on
	// tickets.refund_price.
	Amount int64
	Reason string
}

// ErrTicketNotActive is returned by a CancelTicketFunc when the ticket was
// already cancelled — by an operator, or by a concurrent run of this
// engine. It is not a failure of the refund.
var ErrTicketNotActive = errors.New("refunds: ticket is no longer active")

// CancelTicketFunc cancels one ticket (htickets.CancelTicketTx with the
// refund id): inventory release, barcode revocation, audit, the
// v1.ticket.cancelled event.
type CancelTicketFunc func(ctx context.Context, req CancelRequest) error

// PublishRefundedFunc emits v1.ticket.refunded per ticket once the money
// went back (hscanner.PublishTicketRefundedV1Events).
type PublishRefundedFunc func(ctx context.Context, ticketIDs []string, checkoutSessionID, refundID, currency string, amount int64)

// Actor is who asked for a refund, for the audit trail. ID must be a uuid
// string or empty (audit_events.actor_id is a uuid column).
type Actor struct {
	Type string
	ID   string
}

// Options wires an Engine.
type Options struct {
	DB              DB
	Modules         ModuleSource
	CancelTicket    CancelTicketFunc
	PublishRefunded PublishRefundedFunc
	Audit           audit.Writer
	Metrics         *observability.Metrics
	Logger          *slog.Logger
	// CallTimeout bounds one provider call; DefaultCallTimeout when zero.
	CallTimeout time.Duration
	// SweepOrgs restricts refund.sweep to these organizations. Tests only:
	// production leaves it nil and sweeps every organization.
	SweepOrgs []uuid.UUID
	// SweepPassTimeout bounds one refund.sweep pass; DefaultSweepPassTimeout
	// when zero. It must stay under the worker's stale-claim timeout.
	SweepPassTimeout time.Duration
	// SweepMaxCalls caps the provider calls of one pass; DefaultSweepMaxCalls
	// when zero.
	SweepMaxCalls int
}

// Engine drives refunds through payment modules.
type Engine struct {
	db              DB
	modules         ModuleSource
	cancelTicket    CancelTicketFunc
	publishRefunded PublishRefundedFunc
	audit           audit.Writer
	metrics         *observability.Metrics
	logger          *slog.Logger
	callTimeout     time.Duration
	sweepOrgs       []uuid.UUID
	// sweep bounds (sweep.go)
	sweepPassTimeout time.Duration
	sweepMaxCalls    int
}

// New builds an Engine. DB and Modules are required for anything to work;
// a nil CancelTicket means accepted refunds leave tickets to refund.sweep's
// repair pass (which then also has none) — only tests do that.
func New(o Options) *Engine {
	if err := o.Validate(); err != nil {
		// allow:panic: boot-time configuration guard; the timing options are code constants, and unsafe ones could double-refund.
		panic(err)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = DefaultCallTimeout
	}
	if o.SweepPassTimeout <= 0 {
		o.SweepPassTimeout = DefaultSweepPassTimeout
	}
	if o.SweepMaxCalls <= 0 {
		o.SweepMaxCalls = DefaultSweepMaxCalls
	}
	return &Engine{
		db: o.DB, modules: o.Modules, cancelTicket: o.CancelTicket, publishRefunded: o.PublishRefunded,
		audit: o.Audit, metrics: o.Metrics, logger: o.Logger, callTimeout: o.CallTimeout, sweepOrgs: o.SweepOrgs,
		sweepPassTimeout: o.SweepPassTimeout, sweepMaxCalls: o.SweepMaxCalls,
	}
}

// Error is a refusal the HTTP layer answers with Status and Code. Nothing
// was written when an Error is returned by CreateBatch or a route check.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func refusal(status int, code, msg string, details map[string]any) *Error {
	return &Error{Status: status, Code: code, Message: msg, Details: details}
}

// TicketCancelRequestedByPrefix starts refunds.requested_by of the refund
// POST /v1/tickets/{id}/cancel writes for refund_mode=automatic (htickets
// cancel.go): a ticket-less refund that speaks for that ONE ticket, which
// the operator already cancelled (PAY-03 fourth review, H-1). It is only a
// label: the engine reads the meaning from refunds.cancelled_ticket_id, and
// POST /v1/refunds refuses a requested_by that starts with it (fifth
// review, M-4).
const TicketCancelRequestedByPrefix = "ticket.cancel:"

// Error codes (spec 36 §8).
const (
	CodeSellerSiteOrder        = "refund.seller_site_order"
	CodeProviderNotSupported   = "refund.provider_not_supported"
	CodeNothingToRefund        = "refund.nothing_to_refund"
	CodeAmountExceeds          = "refund.amount_exceeds_refundable"
	CodeTicketNotRefundable    = "refund.ticket_not_refundable"
	CodeInProgress             = "refund.in_progress"
	CodePartialNotSupported    = "refund.partial_not_supported"
	CodeInvalidRequest         = "refund.invalid_request"
	CodeIdempotencyKeyReused   = "refund.idempotency_key_reused"
	CodeOrderNotFound          = "refund.order_not_found"
	CodeEngineUnavailable      = "refund.engine_unavailable"
	failureCodeUnknownProvider = "provider_not_supported"
)

// Route is how money of a payment goes back (spec 36 §6, the part PAY-03
// needs; RouteForOrder as the single public decision point is PAY-09).
type Route string

const (
	// RouteArenaProvider: a module with Refund — the engine returns it.
	RouteArenaProvider Route = "arena_provider"
	// RouteSellerSite: the seller took the money (manual / Bil24 gateway).
	RouteSellerSite Route = "seller_site"
	// RouteUnsupported: a known provider arena cannot refund through.
	RouteUnsupported Route = "unsupported"
	// RouteUnknownProvider: a provider the registry does not know at all
	// (the test-only mock provider). Refused like RouteUnsupported: no
	// route may pretend to refund money arena cannot send back (PAY-03
	// review).
	RouteUnknownProvider Route = "unknown_provider"
)

// RouteFor decides the route of a payment by its provider and the order's
// source. It reads only static descriptors — no network, no secrets.
func (e *Engine) RouteFor(provider, orderSource string) Route {
	return RouteFor(e.modules, provider, orderSource)
}

// RouteFor is the route decision against any descriptor source.
func RouteFor(m ModuleSource, provider, orderSource string) Route {
	p := payments.NormalizeProviderName(provider)
	if p == providerManual || orderSource == orderSourceGateway {
		return RouteSellerSite
	}
	if m == nil {
		return RouteUnknownProvider
	}
	d, ok := m.Descriptor(p)
	if !ok {
		if d.Name != "" {
			// Registered but declared only: known, cannot refund.
			return RouteUnsupported
		}
		return RouteUnknownProvider
	}
	if !d.Capabilities.Refund {
		return RouteUnsupported
	}
	return RouteArenaProvider
}

// RouteRefusal is the error for a route the engine cannot drive, or nil.
func RouteRefusal(route Route, provider string) *Error {
	switch route {
	case RouteSellerSite:
		return refusal(http.StatusConflict, CodeSellerSiteOrder,
			"this order was paid on the seller's own site; the refund is made there", nil)
	case RouteUnsupported, RouteUnknownProvider:
		return refusal(http.StatusUnprocessableEntity, CodeProviderNotSupported,
			"arena cannot return money through this payment provider yet; refund it in the provider's dashboard",
			map[string]any{"provider": provider})
	}
	return nil
}

// LockPayment takes the per-payment advisory lock every writer of a
// payment's refunds holds (spec §7: the calls come from arena-api and
// arena-worker, so an in-process mutex would not serialize them). It must be
// the first statement of the caller's transaction.
func LockPayment(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('arena.refund.payment:' || $1::text, 0))`,
		paymentIntentID)
	return err
}

func (e *Engine) observe(outcome string) {
	if e.metrics == nil || e.metrics.RefundProviderCallsTotal == nil {
		return
	}
	e.metrics.RefundProviderCallsTotal.WithLabelValues(outcome).Inc()
}

func (e *Engine) writeAudit(ctx context.Context, tx pgx.Tx, ev audit.Event) error {
	if e.audit == nil {
		return nil
	}
	ev.OccurredAt = time.Now().UTC()
	return e.audit.WriteTx(ctx, tx, ev)
}
