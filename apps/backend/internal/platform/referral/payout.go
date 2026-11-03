package referral

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// maxRewardAttempts is how many transient failures a reward survives before it
// is marked failed for a person to look at.
const maxRewardAttempts = 12

// NewPayoutHandler returns the worker handler of JobTypePayout.
func NewPayoutHandler(s *Service) func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, payload []byte) error {
		var p PayoutPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("referral: bad payout payload: %w", err)
		}
		id, err := uuid.Parse(p.RewardID)
		if err != nil {
			return fmt.Errorf("referral: bad reward id %q: %w", p.RewardID, err)
		}
		return s.Payout(ctx, id)
	}
}

// Payout pays one 'due' reward: a partial, money-only refund of the referrer's
// own payment through the refund engine (origin 'referral_cashback', tickets
// stay valid, a scanned ticket does not stop it), no confirmation by the
// organizer (owner decision R-RF4). Anything but 'due' is left alone, so a
// repeated job is a no-op.
//
// The decision is taken under the payment's advisory lock, inside the engine's
// own transaction (BatchInput.Prepare): the reward is still due, the friend's
// order is still paid, and the cap is applied to what the payment's refunds
// say right now. The Idempotency-Key "referral-<reward id>" makes a repeat
// answer the first refund. A provider that cannot return the money ends the
// reward 'not_possible' (R-RF5): no other card is tried.
//
// A returned error means "try again": the job runs again and the reward's
// attempts grow; after maxRewardAttempts the reward is marked failed.
func (s *Service) Payout(ctx context.Context, rewardID uuid.UUID) error {
	q := gen.New(s.pool)
	rw, err := q.GetReferralRewardByID(ctx, rewardID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if rw.State != StateDue {
		return nil
	}
	if rw.ReferrerShopOrderID != nil && rw.FriendShopOrderID != nil {
		return s.payoutShop(ctx, rw)
	}
	if rw.ReferrerOrderID == nil || rw.FriendOrderID == nil {
		return s.end(ctx, rw.ID, StateNotPossible, "no_order")
	}
	key := "referral-" + rw.ID.String()

	// A batch with the key already exists: an earlier run got as far as the
	// engine. Finish from it; creating again would have to repeat its amounts.
	if batchID, ok, err := findBatch(ctx, s.pool, rw.OrgID, key); err != nil {
		return err
	} else if ok {
		return s.settleBatch(ctx, rw, batchID, []string{StateDue})
	}

	referrer, err := q.GetReferralOrderFacts(ctx, *rw.ReferrerOrderID)
	if err != nil {
		return s.retryLater(ctx, rw.ID, fmt.Errorf("referral: referrer order: %w", err))
	}
	friend, err := q.GetReferralOrderFacts(ctx, *rw.FriendOrderID)
	if err != nil {
		return s.retryLater(ctx, rw.ID, fmt.Errorf("referral: friend order: %w", err))
	}
	if oc := friendOrderOutcome(friend.Status); oc != nil {
		return s.applyOutcome(ctx, rw.ID, oc)
	}
	// A dispute is read outside the engine's transaction: a table or column
	// this build does not know must not abort it.
	disputed, derr := friendDisputed(ctx, s.pool, friend.ID)
	if derr != nil {
		s.logger.Warn("referral: dispute check failed, paying on", "reward_id", rw.ID.String(), "error", derr.Error())
	}
	if disputed {
		return s.applyOutcome(ctx, rw.ID, &outcome{state: StateCancelled, reason: "friend_disputed"})
	}

	budgets, err := ticketBudgets(ctx, s.pool, referrer.ID)
	if err != nil {
		return s.retryLater(ctx, rw.ID, err)
	}
	initial := SplitOverTickets(rw.AmountMinor, budgets)
	if len(initial) == 0 {
		return s.end(ctx, rw.ID, StateNotPossible, "no_refundable_ticket")
	}

	res, err := s.engine.CreateBatch(ctx, refunds.BatchInput{
		OrgID: rw.OrgID, OrderID: referrer.ID, Items: engineItems(initial),
		Reason:        fmt.Sprintf("Referral cashback for friend order %d", friend.SystemID),
		CancelTickets: false, NotifyBuyer: false, IdempotencyKey: key,
		Actor: refunds.Actor{Type: "system"}, Via: "referral", Approved: true,
		Origin:  refunds.OriginReferralCashback,
		Prepare: s.prepare(rw, referrer),
	})
	if err != nil {
		var oc *outcome
		if errors.As(err, &oc) {
			return s.applyOutcome(ctx, rw.ID, oc)
		}
		var re *refunds.Error
		if errors.As(err, &re) {
			switch re.Code {
			case refunds.CodeInProgress, refunds.CodeEngineUnavailable, refunds.CodeIdempotencyKeyReused:
				return s.retryLater(ctx, rw.ID, err)
			}
			return s.end(ctx, rw.ID, StateNotPossible, re.Code)
		}
		return s.retryLater(ctx, rw.ID, err)
	}
	return s.applyRefunds(ctx, rw, res.BatchID, res.Refunds, []string{StateDue})
}

// prepare is the engine's Prepare hook: it runs under the payment's lock, in
// the transaction that writes the refund rows.
func (s *Service) prepare(rw gen.ReferralRewardRow, referrer gen.ReferralOrderFacts) func(ctx context.Context, tx pgx.Tx, pay refunds.Payment) ([]refunds.Item, error) {
	return func(ctx context.Context, tx pgx.Tx, pay refunds.Payment) ([]refunds.Item, error) {
		q := gen.New(tx)
		cur, err := q.GetReferralRewardByIDForUpdate(ctx, rw.ID)
		if err != nil {
			return nil, err
		}
		if cur.State != StateDue {
			return nil, &outcome{reason: "already_handled"}
		}
		if cur.FriendOrderID == nil {
			return nil, &outcome{state: StateNotPossible, reason: "no_order"}
		}
		var friendStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, *cur.FriendOrderID).Scan(&friendStatus); err != nil {
			return nil, err
		}
		if oc := friendOrderOutcome(friendStatus); oc != nil {
			return nil, oc
		}
		if pay.Currency != rw.Currency {
			return nil, &outcome{state: StateNotPossible, reason: "currency_mismatch"}
		}
		var cashback, other int64
		if err := tx.QueryRow(ctx, `SELECT
				COALESCE(SUM(amount) FILTER (WHERE origin = 'referral_cashback'), 0)::bigint,
				COALESCE(SUM(amount) FILTER (WHERE origin <> 'referral_cashback'), 0)::bigint
			FROM refunds WHERE payment_intent_id = $1 AND state NOT IN ('failed', 'rejected')`,
			pay.ID).Scan(&cashback, &other); err != nil {
			return nil, err
		}
		amount, _ := ApplyCap(cur.AmountMinor, ItemsBase(referrer.Subtotal, referrer.Discount), cashback, other, int(cur.CapPct))
		if amount == 0 {
			return nil, &outcome{state: StateCapped, reason: "cap_reached"}
		}
		budgets, err := ticketBudgets(ctx, tx, referrer.ID)
		if err != nil {
			return nil, err
		}
		items := SplitOverTickets(amount, budgets)
		if len(items) == 0 {
			return nil, &outcome{state: StateNotPossible, reason: "no_refundable_ticket"}
		}
		return engineItems(items), nil
	}
}

// friendOrderOutcome is what the friend order's status means for a reward not
// yet paid: nil when the order is paid; a cancellation when the friend got the
// money back or the order never became paid; a retry while it is parked.
func friendOrderOutcome(status string) *outcome {
	switch status {
	case "paid":
		return nil
	case "refunded", "partially_refunded":
		return &outcome{state: StateCancelled, reason: "friend_refunded"}
	case "pending_payment", "manual_review":
		return &outcome{retry: true, reason: "friend_order_" + status}
	default:
		return &outcome{state: StateCancelled, reason: "friend_not_paid"}
	}
}

func engineItems(items []Item) []refunds.Item {
	out := make([]refunds.Item, len(items))
	for i, it := range items {
		amount := it.Amount
		out[i] = refunds.Item{TicketID: it.TicketID, Amount: &amount}
	}
	return out
}

// applyOutcome ends (or postpones) a reward on a decision made without a
// refund.
func (s *Service) applyOutcome(ctx context.Context, id uuid.UUID, oc *outcome) error {
	switch {
	case oc.retry:
		return s.retryLater(ctx, id, oc)
	case oc.state == "":
		return nil // another run handled it
	default:
		return s.end(ctx, id, oc.state, oc.reason)
	}
}

// end moves a due reward to a final state with a reason.
func (s *Service) end(ctx context.Context, id uuid.UUID, state, reason string) error {
	_, err := s.setState(ctx, id, []string{StateDue}, stateChange{State: state, Reason: reason})
	return err
}

// retryLater counts a transient failure and returns cause so the job runs
// again; after maxRewardAttempts the reward is failed and the job stops.
func (s *Service) retryLater(ctx context.Context, id uuid.UUID, cause error) error {
	var attempts int32
	err := s.pool.QueryRow(ctx, `UPDATE referral_rewards
		SET attempts = attempts + 1,
		    next_attempt_at = now() + LEAST(interval '5 minutes' * (attempts + 1), interval '6 hours'),
		    updated_at = now()
		WHERE id = $1 AND state = 'due' RETURNING attempts`, id).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if attempts >= maxRewardAttempts {
		s.logger.Error("referral: payout gave up", "reward_id", id.String(), "error", cause.Error())
		return s.end(ctx, id, StateFailed, "too_many_attempts")
	}
	return cause
}

// stateChange is the new state of a reward; Amount and the refund links are
// kept when nil.
type stateChange struct {
	State  string
	Reason string
	// ProviderCode: nil keeps the stored code, a pointer to "" clears it.
	ProviderCode *string
	Amount       *int64
	RefundID     *uuid.UUID
	BatchID      *uuid.UUID
}

// setState changes a reward that is in one of the from states. It reports
// whether a row changed.
//
// A reward that reaches 'paid', or 'not_possible' because the provider refused
// the refund (LetterKindFor), queues the referrer's letter in the same
// transaction: the letter exists exactly when the state does. A reward the
// referrer was already told was paid, which later ends in such a refusal (the
// provider cancelled the refund after accepting it, or an operator closed it as
// not returned), queues ONE correction letter, marked by correction_sent_at.
// While the refund waits for a person (manual_review) nothing is sent.
func (s *Service) setState(ctx context.Context, id uuid.UUID, from []string, c stateChange) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var reason, providerCode string
	var hasReferrerOrder, letterSent, correctionSent bool
	err = tx.QueryRow(ctx, `UPDATE referral_rewards
		SET state = $3, reason = COALESCE(NULLIF($4, ''), reason),
		    amount_minor = COALESCE($5, amount_minor), refund_id = COALESCE($6, refund_id),
		    refund_batch_id = COALESCE($7, refund_batch_id), next_attempt_at = NULL, updated_at = now(),
		    provider_code = CASE WHEN $8::text IS NULL THEN provider_code ELSE NULLIF($8::text, '') END
		WHERE id = $1 AND state = ANY($2)
		RETURNING COALESCE(reason, ''), COALESCE(provider_code, ''),
		          (referrer_order_id IS NOT NULL OR referrer_shop_order_id IS NOT NULL),
		          letter_sent_at IS NOT NULL, correction_sent_at IS NOT NULL`,
		id, from, c.State, c.Reason, c.Amount, c.RefundID, c.BatchID, c.ProviderCode).
		Scan(&reason, &providerCode, &hasReferrerOrder, &letterSent, &correctionSent)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if kind, ok := LetterKindFor(c.State, reason, providerCode); ok && hasReferrerOrder {
		payload := CashbackEmailPayload{RewardID: id.String(), Kind: kind}
		switch {
		case !letterSent:
			// the first letter
		case kind == LetterUnavailable && !correctionSent:
			// The referrer was told the money went back (the only earlier
			// letter a reward can have): correct it, once.
			payload.Correction = true
		default:
			ok = false
		}
		if ok {
			if _, err := worker.EnqueueInTx(ctx, tx, JobTypeCashbackEmail, payload, letterMaxAttempts); err != nil {
				return false, fmt.Errorf("referral: enqueue cashback letter: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// settleBatch finishes a reward whose refund batch already exists: it asks the
// engine to drive any refund still unsent (a no-op for an answered one) and
// judges the reward by the refunds' states.
func (s *Service) settleBatch(ctx context.Context, rw gen.ReferralRewardRow, batchID uuid.UUID, from []string) error {
	ids, err := batchRefundIDs(ctx, s.pool, batchID)
	if err != nil {
		return err
	}
	refs := make([]refunds.Refund, 0, len(ids))
	for _, id := range ids {
		r, derr := s.engine.Drive(ctx, id)
		if derr != nil {
			s.logger.Warn("referral: drive failed, the refund sweep retries it", "refund_id", id.String(), "error", derr.Error())
			if r, derr = s.engine.GetRefund(ctx, id); derr != nil {
				return derr
			}
		}
		refs = append(refs, r)
	}
	return s.applyRefunds(ctx, rw, batchID, refs, from)
}

// applyRefunds writes the reward's state from its refunds.
func (s *Service) applyRefunds(ctx context.Context, rw gen.ReferralRewardRow, batchID uuid.UUID, refs []refunds.Refund, from []string) error {
	if len(refs) == 0 {
		return s.retryLater(ctx, rw.ID, errors.New("referral: the batch has no refunds"))
	}
	views := make([]refundView, len(refs))
	for i, r := range refs {
		views[i] = refundView{State: r.State, Amount: r.Amount, HasProviderRef: r.ProviderRefundID != nil, Attempted: r.ProviderAttempts > 0}
		if r.FailureCode != nil {
			views[i].FailureCode = *r.FailureCode
		}
	}
	state, reason, providerCode, amount := resultOf(views)
	if unchanged(rw, state, reason, providerCode) && rw.RefundBatchID != nil {
		return nil // nothing new
	}
	first := refs[0].ID
	c := stateChange{State: state, Reason: reason, ProviderCode: &providerCode, Amount: &amount, RefundID: &first, BatchID: &batchID}
	if state == StateNotPossible {
		// Nothing was returned, but the cashback stays owed on the row: the
		// organizer's report shows what was not paid. The cap sums count only
		// due, sent and paid rewards (SumReferrerLiveRewards).
		c.Amount = nil
	}
	_, err := s.setState(ctx, rw.ID, from, c)
	return err
}

// unchanged reports whether a reward already says what its refunds now say.
func unchanged(rw gen.ReferralRewardRow, state, reason, providerCode string) bool {
	if state != rw.State {
		return false
	}
	if reason != "" && (rw.Reason == nil || *rw.Reason != reason) {
		return false
	}
	return providerCode == "" || (rw.ProviderCode != nil && *rw.ProviderCode == providerCode)
}

// findBatch returns the refund batch created under key, if any.
func findBatch(ctx context.Context, db gen.DBTX, orgID uuid.UUID, key string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx, `SELECT id FROM refund_batches WHERE org_id = $1 AND idempotency_key = $2`, orgID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return id, err == nil, err
}

func batchRefundIDs(ctx context.Context, db gen.DBTX, batchID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, `SELECT id FROM refunds WHERE batch_id = $1 ORDER BY created_at, id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ticketBudgets is what can still be returned for each active ticket of the
// order: what it cost minus its live refunds. It counts every non-failed refund
// (the engine counts fewer), so the engine never refuses what this offers. A
// ticket under review hold, or with a cancelling refund in flight, is left out.
func ticketBudgets(ctx context.Context, db gen.DBTX, orderID uuid.UUID) ([]TicketBudget, error) {
	rows, err := db.Query(ctx, `
		SELECT t.id,
		       oi.total - COALESCE((SELECT SUM(r.amount) FROM refunds r
		                             WHERE r.ticket_id = t.id AND r.state NOT IN ('failed', 'rejected')), 0)::bigint
		FROM   order_items oi
		JOIN   tickets t ON t.id = oi.ticket_id
		WHERE  oi.order_id = $1 AND t.status = 'active' AND NOT t.review_hold AND oi.total IS NOT NULL
		  AND  NOT EXISTS (SELECT 1 FROM refunds r WHERE r.ticket_id = t.id AND r.cancel_ticket
		                    AND r.state NOT IN ('failed', 'rejected'))
		ORDER  BY t.system_ticket_id NULLS LAST, t.id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TicketBudget
	for rows.Next() {
		var b TicketBudget
		if err := rows.Scan(&b.TicketID, &b.Budget); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// disputesTable reports whether this database has payment_disputes (it arrives
// with the chargebacks work; until then nothing can be disputed here).
func disputesTable(ctx context.Context, db gen.DBTX) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT to_regclass('public.payment_disputes') IS NOT NULL`).Scan(&ok)
	return ok, err
}

// friendDisputed reports an open dispute (not a mere inquiry) on the friend's
// order. False when the disputes table does not exist yet.
func friendDisputed(ctx context.Context, db gen.DBTX, friendOrderID uuid.UUID) (bool, error) {
	ok, err := disputesTable(ctx, db)
	if err != nil || !ok {
		return false, err
	}
	var disputed bool
	err = db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payment_disputes WHERE order_id = $1 AND NOT is_inquiry)`,
		friendOrderID).Scan(&disputed)
	return disputed, err
}
