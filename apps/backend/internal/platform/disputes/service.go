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
	res, err := s.recordProviderEvent(ctx, in)
	if errors.Is(err, errConcurrentFirst) {
		// A parallel delivery of the same dispute inserted it first: the
		// second attempt finds it and records this event on it.
		res, err = s.recordProviderEvent(ctx, in)
	}
	return res, err
}

var errConcurrentFirst = errors.New("disputes: a parallel delivery recorded the dispute first")

func (s *Service) recordProviderEvent(ctx context.Context, in RecordInput) (RecordResult, error) {
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
		if errors.Is(err, payments.ErrDisputeNotFound) {
			// No such dispute at the provider: nothing to record, and a
			// redelivery would not find it either.
			return RecordResult{}, nil
		}
		switch {
		case errors.Is(err, payments.ErrDisputeNotWritable):
			// The key may not read disputes. The delivery itself is verified,
			// so its body is recorded; the payment-config check tells the
			// organizer about the missing right.
			s.logger.Warn("disputes: the key cannot read the dispute, the event body is used",
				slog.String("org_id", in.OrgID.String()), slog.String("provider", in.Provider))
		case err != nil:
			return RecordResult{}, fmt.Errorf("disputes: read the dispute back: %w", err)
		default:
			fresh.FundsWithdrawn = pd.FundsWithdrawn
			fresh.FundsReinstated = pd.FundsReinstated
			pd = fresh
		}
	}

	pi, err := s.findPayment(ctx, in.OrgID, in.Provider, pd.PaymentIntentID)
	if err != nil {
		return RecordResult{}, err
	}
	if pi == nil && pd.ChargeID != "" && resp != nil {
		cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
		facts, ferr := resp.ChargeFacts(cctx, pd.ChargeID)
		cancel()
		switch {
		case errors.Is(ferr, payments.ErrDisputeNotWritable), errors.Is(ferr, payments.ErrDisputeNotFound):
			// A charge the key may not read, or one this account does not
			// have: a redelivery would end the same way.
			s.logger.Warn("disputes: the charge of a dispute cannot be read",
				slog.String("org_id", in.OrgID.String()), slog.String("provider", in.Provider))
		case ferr != nil:
			return RecordResult{}, fmt.Errorf("disputes: read the charge: %w", ferr)
		default:
			if pd.PaymentIntentID == "" {
				pd.PaymentIntentID = facts.PaymentIntentID
			}
			if pi, err = s.findPayment(ctx, in.OrgID, in.Provider, facts.PaymentIntentID); err != nil {
				return RecordResult{}, err
			}
		}
	}
	if pi == nil && pd.PaymentIntentID != "" && resp != nil {
		// A payment whose charge reference was never stored (it is known
		// only by its checkout id): resolve the latest few and look again.
		if pi, err = s.resolveMissingChargeRefs(ctx, in.OrgID, in.Provider, resp, pd.PaymentIntentID); err != nil {
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
			FundsWithdrawn: pd.FundsWithdrawn, FundsReinstated: pd.FundsReinstated, Now: now,
			OrgID: in.OrgID, Provider: in.Provider, ProviderDisputeID: &pd.ID, Channel: channel,
			PaymentIntentID: &pi.id, OrderID: pi.orderID, ConfigID: in.ConfigID,
			AmountMinor: firstPositive(pd.AmountMinor, pi.amount), Currency: firstNonEmpty(pd.Currency, pi.currency),
			Reason: pd.Reason, ProviderStatus: pd.Status, IsInquiry: isInquiry(pd.Status),
			IsChargeRefundable: pd.IsChargeRefundable, DueBy: pd.DueBy, OpenedAt: firstTime(pd.Created, now),
			Settings: settings,
		})
		if isUniqueViolation(err) {
			return RecordResult{}, errConcurrentFirst
		}
		if err != nil {
			return RecordResult{}, err
		}
		res.Created = true
		if !d.Open() {
			// First seen already closed: nobody needs to be told it opened.
			if _, err := tx.Exec(ctx, `INSERT INTO payment_dispute_notices (dispute_id, notice_key) VALUES ($1, 'opened')
				ON CONFLICT DO NOTHING`, d.ID); err != nil {
				return RecordResult{}, err
			}
		}
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
	FundsWithdrawn     bool
	FundsReinstated    bool
	Now                time.Time
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
	state, outcome := providerTransition("", r.ProviderStatus, false)
	at := r.Now
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var outcomePtr *string
	var closedAt, withdrawnAt, reinstatedAt *time.Time
	if outcome != "" {
		outcomePtr, closedAt = &outcome, &at
	}
	if r.FundsWithdrawn {
		withdrawnAt = &at
	}
	if r.FundsReinstated {
		reinstatedAt = &at
	}
	return scanDispute(q.QueryRow(ctx, `INSERT INTO payment_disputes
		(org_id, provider, provider_dispute_id, channel, payment_intent_id, order_id, config_id,
		 amount_minor, currency, reason, reason_group, provider_status, state, is_inquiry,
		 is_charge_refundable, due_by, due_by_source, respond_to, low_value, opened_at,
		 outcome, closed_at, funds_withdrawn_at, funds_reinstated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
		RETURNING `+disputeColumns,
		r.OrgID, r.Provider, r.ProviderDisputeID, r.Channel, r.PaymentIntentID, r.OrderID, r.ConfigID,
		r.AmountMinor, cur, r.Reason, ReasonGroupOf(r.Reason), r.ProviderStatus, state, r.IsInquiry,
		r.IsChargeRefundable, due, dueSource, r.RespondTo, r.Settings.IsLowValue(r.AmountMinor, cur), r.OpenedAt,
		outcomePtr, closedAt, withdrawnAt, reinstatedAt))
}

// applyProviderState moves the dispute after a provider event.
func applyProviderState(ctx context.Context, q querier, d Dispute, pd payments.Dispute, now time.Time) error {
	state, outcome := providerTransition(d.State, pd.Status, d.SubmittedAt != nil)
	var outcomePtr *string
	if outcome != "" {
		outcomePtr = &outcome
	}
	// An expired dispute the provider still waits an answer for is open
	// again: its outcome and the expiry notices go.
	reopened := d.State == StateExpired && state != StateExpired && outcome == ""
	if reopened {
		if _, err := q.Exec(ctx, `DELETE FROM payment_dispute_notices WHERE dispute_id = $1
			AND (notice_key LIKE 'due_%' OR notice_key = 'expired')`, d.ID); err != nil {
			return err
		}
	}
	_, err := q.Exec(ctx, `UPDATE payment_disputes SET
			provider_status = $2,
			state = $3,
			outcome = CASE WHEN $14 THEN NULL ELSE COALESCE($4, outcome) END,
			closed_at = CASE WHEN $14 THEN NULL WHEN $4::text IS NOT NULL THEN COALESCE(closed_at, $5) ELSE closed_at END,
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
		pd.FundsWithdrawn, pd.FundsReinstated, reopened)
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// resolveMissingChargeRefs asks the provider for the charge reference of the
// organization's latest succeeded payments that are known only by their
// checkout id, stores what it learns and looks for ref again.
func (s *Service) resolveMissingChargeRefs(ctx context.Context, orgID uuid.UUID, provider string, resp payments.DisputeResponder, ref string) (*paymentRef, error) {
	resolver, ok := resp.(payments.ChargeRefResolver)
	if !ok {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id, provider_payment_id FROM payment_intents
		WHERE org_id = $1 AND provider = $2 AND state = 'succeeded' AND provider_charge_ref IS NULL
		  AND COALESCE(provider_payment_id, '') <> '' AND created_at > now() - interval '180 days'
		ORDER BY created_at DESC LIMIT 5`, orgID, provider)
	if err != nil {
		return nil, fmt.Errorf("disputes: list payments without a charge reference: %w", err)
	}
	type cand struct {
		id  uuid.UUID
		ref string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.ref); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range cands {
		cctx, cancel := context.WithTimeout(ctx, s.callTimeout)
		got, rerr := resolver.ResolveChargeRef(cctx, c.ref)
		cancel()
		got = strings.TrimSpace(got)
		if rerr != nil || got == "" {
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2
			WHERE id = $1 AND provider_charge_ref IS NULL`, c.id, got); err != nil {
			return nil, err
		}
		if got == ref {
			return s.findPayment(ctx, orgID, provider, ref)
		}
	}
	return nil, nil
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
		if isUniqueViolation(err) {
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
