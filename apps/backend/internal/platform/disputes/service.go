package disputes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobBuildPackage builds the evidence package of a dispute (and, for an API
// channel, saves the provider draft with submit=false).
const JobBuildPackage = "dispute.build_package"

// ModuleSource builds an organization's payment module. hcheckout's
// RefundModuleSource is the production one; a context carrying
// refunds.WithProviderConfig builds from that named configuration.
type ModuleSource interface {
	Descriptor(provider string) (payments.Descriptor, bool)
	Build(ctx context.Context, orgID uuid.UUID, provider string) (payments.Module, error)
}

// Options configures a Service.
type Options struct {
	DB      DB
	Modules ModuleSource
	Logger  *slog.Logger
	// Now is the clock (tests).
	Now func() time.Time
	// CallTimeout bounds one provider call.
	CallTimeout time.Duration
}

// Service is the dispute engine.
type Service struct {
	db          DB
	modules     ModuleSource
	logger      *slog.Logger
	now         func() time.Time
	callTimeout time.Duration
}

// New builds a Service.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 40 * time.Second
	}
	return &Service{db: o.DB, modules: o.Modules, logger: o.Logger, now: o.Now, callTimeout: o.CallTimeout}
}

// responder builds the dispute API of the dispute's configuration, nil when
// the provider has none.
func (s *Service) responder(ctx context.Context, d Dispute) (payments.DisputeResponder, error) {
	if s.modules == nil {
		return nil, nil
	}
	desc, ok := s.modules.Descriptor(d.Provider)
	if !ok || !desc.Capabilities.Disputes {
		return nil, nil
	}
	if d.ConfigID != nil {
		ctx = refunds.WithProviderConfig(ctx, *d.ConfigID)
	}
	m, err := s.modules.Build(ctx, d.OrgID, d.Provider)
	if err != nil {
		return nil, err
	}
	r, _ := m.(payments.DisputeResponder)
	return r, nil
}

// RecordInput is one verified provider delivery about a dispute.
type RecordInput struct {
	OrgID    uuid.UUID
	ConfigID *uuid.UUID
	Provider string
	Event    payments.NormalizedEvent
}

// RecordResult says what the delivery did.
type RecordResult struct {
	// Found: the dispute names a payment of this organization.
	Found     bool
	DisputeID uuid.UUID
	Created   bool
	// Duplicate: this provider event was recorded before.
	Duplicate bool
}

// RecordProviderEvent records a VERIFIED dispute event. A provider that has
// a dispute API is asked for the dispute again: the body is only a hint. A
// dispute of a payment arena does not own is acknowledged untouched (the
// organizer's provider account also serves their other sites). An error is
// transient: the webhook answers 500 and the provider delivers again.
func (s *Service) RecordProviderEvent(ctx context.Context, in RecordInput) (RecordResult, error) {
	ev := in.Event
	if ev.Dispute == nil || strings.TrimSpace(ev.Dispute.ID) == "" {
		return RecordResult{}, nil
	}
	pd := *ev.Dispute
	var resp payments.DisputeResponder
	channel := ChannelEmail
	if s.modules != nil {
		if desc, ok := s.modules.Descriptor(in.Provider); ok && desc.Capabilities.Disputes {
			channel = ChannelAPI
			probe := Dispute{OrgID: in.OrgID, Provider: in.Provider, ConfigID: in.ConfigID}
			r, err := s.responder(ctx, probe)
			if err != nil {
				return RecordResult{}, fmt.Errorf("disputes: build module: %w", err)
			}
			resp = r
		}
	}
	if resp != nil {
		cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
		fresh, err := resp.GetDispute(cctx, pd.ID)
		cancel()
		if err != nil {
			return RecordResult{}, fmt.Errorf("disputes: read the dispute back: %w", err)
		}
		fresh.FundsWithdrawn = pd.FundsWithdrawn
		fresh.FundsReinstated = pd.FundsReinstated
		pd = fresh
	}

	pi, err := s.findPayment(ctx, in.OrgID, in.Provider, pd.PaymentIntentID)
	if err != nil {
		return RecordResult{}, err
	}
	if pi == nil && pd.ChargeID != "" && resp != nil {
		cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
		facts, ferr := resp.ChargeFacts(cctx, pd.ChargeID)
		cancel()
		if ferr != nil {
			return RecordResult{}, fmt.Errorf("disputes: read the charge: %w", ferr)
		}
		if pi, err = s.findPayment(ctx, in.OrgID, in.Provider, facts.PaymentIntentID); err != nil {
			return RecordResult{}, err
		}
	}
	if pi == nil {
		return RecordResult{}, nil
	}

	settings, err := s.SettingsFor(ctx, in.OrgID)
	if err != nil {
		return RecordResult{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RecordResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	res := RecordResult{Found: true}
	d, err := getDisputeByProviderID(ctx, tx, in.Provider, pd.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		d, err = insertDispute(ctx, tx, newDisputeRow{
			OrgID: in.OrgID, Provider: in.Provider, ProviderDisputeID: &pd.ID, Channel: channel,
			PaymentIntentID: &pi.id, OrderID: pi.orderID, ConfigID: in.ConfigID,
			AmountMinor: firstPositive(pd.AmountMinor, pi.amount), Currency: firstNonEmpty(pd.Currency, pi.currency),
			Reason: pd.Reason, ProviderStatus: pd.Status, IsInquiry: isInquiry(pd.Status),
			IsChargeRefundable: pd.IsChargeRefundable, DueBy: pd.DueBy, OpenedAt: firstTime(pd.Created, now),
			Settings: settings,
		})
		if err != nil {
			return RecordResult{}, err
		}
		res.Created = true
		if _, err := insertEvent(ctx, tx, d, "opened", "", pd.Status, Actor{Label: "provider"}, map[string]any{
			"amount_minor": d.AmountMinor, "currency": d.Currency, "reason": d.Reason,
		}); err != nil {
			return RecordResult{}, err
		}
		if _, err := worker.EnqueueInTx(ctx, tx, JobBuildPackage, map[string]string{"dispute_id": d.ID.String()}, 5); err != nil {
			return RecordResult{}, err
		}
	case err != nil:
		return RecordResult{}, err
	default:
		if d.OrgID != in.OrgID {
			// The provider id is global; a dispute of another organization
			// is never touched through this one's configuration.
			return RecordResult{}, nil
		}
	}
	res.DisputeID = d.ID

	eventID := ev.ProviderEventID
	if eventID == "" {
		// A provider without event ids (AllPay): one row per type and status.
		eventID = ev.Type + ":" + pd.ID + ":" + pd.Status
	}
	inserted, err := insertEvent(ctx, tx, d, "provider_event", eventID, pd.Status, Actor{Label: "provider"}, map[string]any{
		"event_type": ev.Type, "status": pd.Status, "amount_minor": pd.AmountMinor,
		"funds_withdrawn": pd.FundsWithdrawn, "funds_reinstated": pd.FundsReinstated,
	})
	if err != nil {
		return RecordResult{}, err
	}
	if !inserted {
		res.Duplicate = true
		if err := tx.Commit(ctx); err != nil {
			return RecordResult{}, err
		}
		return res, nil
	}
	if !res.Created {
		if err := applyProviderState(ctx, tx, d, pd, now); err != nil {
			return RecordResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordResult{}, err
	}
	return res, nil
}

type paymentRef struct {
	id       uuid.UUID
	orderID  *uuid.UUID
	amount   int64
	currency string
}

// findPayment finds the organization's payment a provider reference names:
// Stripe's pi_… is provider_charge_ref, AllPay's order id is both.
func (s *Service) findPayment(ctx context.Context, orgID uuid.UUID, provider, ref string) (*paymentRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	var p paymentRef
	err := s.db.QueryRow(ctx, `SELECT pi.id, o.id, pi.amount, pi.currency
		FROM payment_intents pi
		LEFT JOIN orders o ON o.checkout_session_id = pi.checkout_session_id
		WHERE pi.org_id = $1 AND pi.provider = $2
		  AND (pi.provider_charge_ref = $3 OR pi.provider_payment_id = $3)
		ORDER BY pi.created_at DESC LIMIT 1`, orgID, provider, ref).Scan(&p.id, &p.orderID, &p.amount, &p.currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("disputes: find payment: %w", err)
	}
	return &p, nil
}

type newDisputeRow struct {
	OrgID              uuid.UUID
	Provider           string
	ProviderDisputeID  *string
	Channel            string
	PaymentIntentID    *uuid.UUID
	OrderID            *uuid.UUID
	ConfigID           *uuid.UUID
	AmountMinor        int64
	Currency           string
	Reason             string
	ProviderStatus     string
	IsInquiry          bool
	IsChargeRefundable bool
	DueBy              *time.Time
	DueBySource        string
	RespondTo          string
	OpenedAt           time.Time
	Settings           Settings
}

func insertDispute(ctx context.Context, q querier, r newDisputeRow) (Dispute, error) {
	dueSource := r.DueBySource
	due := r.DueBy
	switch {
	case dueSource != "":
	case due != nil:
		dueSource = "provider"
	default:
		t := r.OpenedAt.Add(DefaultDueAfter)
		due, dueSource = &t, "default"
	}
	cur := strings.ToUpper(strings.TrimSpace(r.Currency))
	state, _ := providerTransition("", r.ProviderStatus, false)
	return scanDispute(q.QueryRow(ctx, `INSERT INTO payment_disputes
		(org_id, provider, provider_dispute_id, channel, payment_intent_id, order_id, config_id,
		 amount_minor, currency, reason, reason_group, provider_status, state, is_inquiry,
		 is_charge_refundable, due_by, due_by_source, respond_to, low_value, opened_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		RETURNING `+disputeColumns,
		r.OrgID, r.Provider, r.ProviderDisputeID, r.Channel, r.PaymentIntentID, r.OrderID, r.ConfigID,
		r.AmountMinor, cur, r.Reason, ReasonGroupOf(r.Reason), r.ProviderStatus, state, r.IsInquiry,
		r.IsChargeRefundable, due, dueSource, r.RespondTo, r.Settings.IsLowValue(r.AmountMinor, cur), r.OpenedAt))
}

// applyProviderState moves the dispute after a provider event.
func applyProviderState(ctx context.Context, q querier, d Dispute, pd payments.Dispute, now time.Time) error {
	state, outcome := providerTransition(d.State, pd.Status, d.SubmittedAt != nil)
	var outcomePtr *string
	if outcome != "" {
		outcomePtr = &outcome
	}
	_, err := q.Exec(ctx, `UPDATE payment_disputes SET
			provider_status = $2,
			state = $3,
			outcome = COALESCE($4, outcome),
			closed_at = CASE WHEN $4::text IS NOT NULL THEN COALESCE(closed_at, $5) ELSE closed_at END,
			is_inquiry = $6,
			is_charge_refundable = $7,
			due_by = CASE WHEN $8::timestamptz IS NOT NULL THEN $8 ELSE due_by END,
			due_by_source = CASE WHEN $8::timestamptz IS NOT NULL THEN 'provider' ELSE due_by_source END,
			reason = CASE WHEN $9 <> '' THEN $9 ELSE reason END,
			reason_group = CASE WHEN $9 <> '' THEN $10 ELSE reason_group END,
			amount_minor = CASE WHEN $11 > 0 THEN $11 ELSE amount_minor END,
			funds_withdrawn_at = CASE WHEN $12 THEN COALESCE(funds_withdrawn_at, $5) ELSE funds_withdrawn_at END,
			funds_reinstated_at = CASE WHEN $13 THEN COALESCE(funds_reinstated_at, $5) ELSE funds_reinstated_at END,
			updated_at = $5
		WHERE id = $1`,
		d.ID, pd.Status, state, outcomePtr, now, isInquiry(pd.Status) || (d.IsInquiry && pd.Status == ""),
		pd.IsChargeRefundable, pd.DueBy, pd.Reason, ReasonGroupOf(pd.Reason), pd.AmountMinor,
		pd.FundsWithdrawn, pd.FundsReinstated)
	return err
}

// ManualInput is a dispute the organizer enters from a provider's letter.
type ManualInput struct {
	OrderID   uuid.UUID
	Reason    string
	DueBy     *time.Time
	RespondTo string
	// ProviderDisputeID is the provider's own reference, when the letter has one.
	ProviderDisputeID string
	// AmountMinor; 0 takes the order's total.
	AmountMinor int64
}

// ErrInvalid is a refused input; Error() is the code.
type ErrInvalid struct{ Code, Message string }

func (e *ErrInvalid) Error() string { return e.Code + ": " + e.Message }

// CreateManual records a hand-entered dispute of the organization's order.
// It goes into the same queue and gets the same package; the answer goes by
// e-mail (channel manual).
func (s *Service) CreateManual(ctx context.Context, orgID uuid.UUID, in ManualInput, actor Actor) (Dispute, error) {
	var (
		piID     *uuid.UUID
		provider string
		total    int64
		currency string
		cfgID    *uuid.UUID
	)
	err := s.db.QueryRow(ctx, `SELECT pi.id, COALESCE(pi.provider, ''), o.total, o.currency
		FROM orders o
		LEFT JOIN LATERAL (SELECT id, provider FROM payment_intents
		                   WHERE checkout_session_id = o.checkout_session_id AND org_id = o.org_id
		                   ORDER BY created_at DESC LIMIT 1) pi ON true
		WHERE o.id = $1 AND o.org_id = $2`, in.OrderID, orgID).Scan(&piID, &provider, &total, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return Dispute{}, &ErrInvalid{Code: "dispute.order_not_found", Message: "the order is not one of this organization's"}
	}
	if err != nil {
		return Dispute{}, err
	}
	if provider == "" {
		return Dispute{}, &ErrInvalid{Code: "dispute.order_not_paid", Message: "the order has no card payment to dispute"}
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "general"
	}
	amount := in.AmountMinor
	if amount <= 0 || amount > total {
		amount = total
	}
	var provID *string
	if v := strings.TrimSpace(in.ProviderDisputeID); v != "" {
		// The provider id is unique across the platform; a reference from a
		// letter is only unique inside one organization.
		v = "manual:" + orgID.String() + ":" + v
		provID = &v
	}
	settings, err := s.SettingsFor(ctx, orgID)
	if err != nil {
		return Dispute{}, err
	}
	dueSource := ""
	if in.DueBy != nil {
		dueSource = "organizer"
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Dispute{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if provID != nil {
		if existing, gerr := getDisputeByProviderID(ctx, tx, provider, *provID); gerr == nil && existing.OrgID == orgID {
			return existing, nil
		}
	}
	d, err := insertDispute(ctx, tx, newDisputeRow{
		OrgID: orgID, Provider: provider, ProviderDisputeID: provID, Channel: ChannelManual,
		PaymentIntentID: piID, OrderID: &in.OrderID, ConfigID: cfgID, AmountMinor: amount, Currency: currency,
		Reason: reason, DueBy: in.DueBy, DueBySource: dueSource, RespondTo: strings.TrimSpace(in.RespondTo),
		OpenedAt: s.now().UTC(), Settings: settings,
	})
	if err != nil {
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
			return Dispute{}, &ErrInvalid{Code: "dispute.duplicate", Message: "a dispute with this reference already exists"}
		}
		return Dispute{}, err
	}
	if _, err := insertEvent(ctx, tx, d, "opened", "", "", actor, map[string]any{"manual": true, "reason": reason}); err != nil {
		return Dispute{}, err
	}
	if _, err := worker.EnqueueInTx(ctx, tx, JobBuildPackage, map[string]string{"dispute_id": d.ID.String()}, 5); err != nil {
		return Dispute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Dispute{}, err
	}
	return d, nil
}

func firstPositive(a, b int64) int64 {
	if a > 0 {
		return a
	}
	return b
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}
