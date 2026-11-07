package ticketreissue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// ProviderEmailJobType moves the buyer's address at the payment provider
// after a reissue was applied (decision R-17, П-2).
const ProviderEmailJobType = "reissue.provider_email"

// TxBeginner opens a transaction on a pool or an outer transaction.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// LetterFacts is what the letter with the new tickets says (spec 63
// section 4.6). Found is false for a letter that carries no ticket of an open
// reissue: an ordinary letter.
type LetterFacts struct {
	Found       bool
	Reason      string
	OrderNumber int64
}

// Typo reports whether the letter explains a typo in the address.
func (f LetterFacts) Typo() bool { return f.Reason == ReasonWrongAddress }

// FactsForLetter reads the open reissue a new ticket belongs to. A reissue
// already applied answers Found false: a later resend of the same tickets is
// an ordinary letter.
func FactsForLetter(ctx context.Context, q Querier, ticketID uuid.UUID) (LetterFacts, error) {
	var f LetterFacts
	err := q.QueryRow(ctx, `
		SELECT r.reason, COALESCE(o.system_id, 0)
		FROM   ticket_reissue_items i
		JOIN   ticket_reissues r ON r.id = i.reissue_id
		LEFT   JOIN orders o ON o.id = r.order_id
		WHERE  i.new_ticket_id = $1 AND r.state IN ('pending', 'send_failed')`, ticketID).Scan(&f.Reason, &f.OrderNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return LetterFacts{}, nil
	}
	if err != nil {
		return LetterFacts{}, fmt.Errorf("ticketreissue: letter facts: %w", err)
	}
	f.Found = true
	return f, nil
}

// AfterLetter is called by the delivery worker once the relay answered for a
// letter that carried ticketIDs to recipient. sent=true applies every open
// reissue those tickets belong to (decision R-2): the order's buyer e-mail
// becomes the new address and the order moves to the customer of that
// address (decision R-13). sent=false marks them 'send_failed'. Nothing
// happens for a letter that carries no reissued ticket.
func AfterLetter(ctx context.Context, db TxBeginner, logger *slog.Logger, ticketIDs []uuid.UUID, recipient string, sent bool) error {
	if db == nil || len(ticketIDs) == 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ticketreissue: begin after-letter: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT DISTINCT r.id, r.order_id, r.complimentary_issuance_id, r.org_id, COALESCE(r.old_email, ''), r.new_email, r.state
		FROM   ticket_reissue_items i
		JOIN   ticket_reissues r ON r.id = i.reissue_id
		WHERE  i.new_ticket_id = ANY($1) AND r.state IN ('pending', 'send_failed')
		  AND  lower(r.new_email) = lower($2)`, ticketIDs, strings.TrimSpace(recipient))
	if err != nil {
		return fmt.Errorf("ticketreissue: find open reissues: %w", err)
	}
	type open struct {
		id, orgID           uuid.UUID
		orderID, issuanceID *uuid.UUID
		oldEmail            string
		email, state        string
	}
	var opens []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.orderID, &o.issuanceID, &o.orgID, &o.oldEmail, &o.email, &o.state); err != nil {
			rows.Close()
			return err
		}
		opens = append(opens, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(opens) == 0 {
		return nil
	}
	for _, o := range opens {
		if !sent {
			if _, err := tx.Exec(ctx, `UPDATE ticket_reissues
				SET state = 'send_failed', letter_failed_at = now(), updated_at = now()
				WHERE id = $1 AND state = 'pending'`, o.id); err != nil {
				return fmt.Errorf("ticketreissue: mark send_failed: %w", err)
			}
			continue
		}
		if o.issuanceID != nil {
			if err := applyInvitation(ctx, tx, o.id, *o.issuanceID, o.oldEmail, o.email); err != nil {
				return err
			}
			continue
		}
		if o.orderID == nil {
			continue
		}
		if err := apply(ctx, tx, logger, o.id, *o.orderID, o.orgID, o.email); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// apply moves the order to the new address on tx.
func apply(ctx context.Context, tx pgx.Tx, logger *slog.Logger, reissueID, orderID, orgID uuid.UUID, email string) error {
	var (
		state               string
		channelID, csID     uuid.UUID
		customerBefore      *uuid.UUID
		receiptOn, forcedOn bool
	)
	// The reissue row first, then the order (the reissue transaction took
	// the order first, but it never touches a reissue row it did not just
	// insert, so the two cannot wait on each other).
	if err := tx.QueryRow(ctx, `SELECT state FROM ticket_reissues WHERE id = $1 FOR UPDATE`, reissueID).Scan(&state); err != nil {
		return fmt.Errorf("ticketreissue: lock reissue: %w", err)
	}
	if state == StateApplied {
		return nil
	}
	if err := tx.QueryRow(ctx, `
		SELECT o.channel_id, o.checkout_session_id, o.customer_id,
		       COALESCE(org.provider_receipt_enabled, false), COALESCE(org.provider_receipt_forced, false)
		FROM   orders o JOIN organizations org ON org.id = o.org_id
		WHERE  o.id = $1 FOR UPDATE OF o`, orderID).Scan(&channelID, &csID, &customerBefore, &receiptOn, &forcedOn); err != nil {
		return fmt.Errorf("ticketreissue: lock order: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET buyer_email = $2, updated_at = now() WHERE id = $1`, orderID, email); err != nil {
		return fmt.Errorf("ticketreissue: change order address: %w", err)
	}

	// The customer of the new address, found or created, in a SAVEPOINT: a
	// failure leaves the address changed and only logs (spec 63 section 4.5).
	// Only the e-mail is given: the phone of the order may belong to the old
	// customer, who must not be changed.
	if err := rebindCustomer(ctx, tx, orderID, orgID, channelID, email); err != nil {
		logger.Warn("ticketreissue: customer rebind failed; the order keeps its customer",
			slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
	}

	provider, err := loadProvider(ctx, tx, csID)
	if err != nil {
		return err
	}
	var providerState string
	switch {
	case provider.Name == "":
		providerState = "not_applicable"
	case provider.EmailUpdatable && (receiptOn || forcedOn):
		providerState = "queued"
	case provider.EmailUpdatable:
		// The organizer switched the provider's receipts off: Stripe would
		// send a receipt the organizer did not want.
		providerState = "not_applicable"
	default:
		providerState = "manual"
	}
	if _, err := tx.Exec(ctx, `UPDATE ticket_reissues
		SET state = 'applied', applied_at = now(), provider_email_state = $2, updated_at = now()
		WHERE id = $1`, reissueID, providerState); err != nil {
		return fmt.Errorf("ticketreissue: mark applied: %w", err)
	}
	if providerState == "queued" {
		if _, err := worker.EnqueueInTx(ctx, tx, ProviderEmailJobType, ProviderEmailPayload{ReissueID: reissueID.String()}, 5); err != nil {
			return fmt.Errorf("ticketreissue: queue provider address: %w", err)
		}
	}
	hist, _ := json.Marshal(map[string]any{"reissue_id": reissueID.String(), "new_email": MaskEmail(email)})
	if _, err := gen.New(tx).InsertOrderEvent(ctx, orderID, AppliedOrderEventType, "system", hist); err != nil {
		return fmt.Errorf("ticketreissue: order event: %w", err)
	}
	return nil
}

func rebindCustomer(ctx context.Context, tx pgx.Tx, orderID, orgID, channelID uuid.UUID, email string) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = sp.Rollback(ctx) }()
	store := customers.NewStoreFromQueries(gen.New(sp))
	res, err := customers.Resolve(ctx, store, customers.ResolveInput{
		Email:     email,
		ChannelID: channelID,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	if err := customers.LinkOrg(ctx, store, res.Customer.ID, orgID, "order"); err != nil {
		return err
	}
	if _, err := sp.Exec(ctx, `UPDATE orders SET customer_id = $2 WHERE id = $1`, orderID, res.Customer.ID); err != nil {
		return err
	}
	return sp.Commit(ctx)
}

// ProviderEmailPayload is the worker payload of ProviderEmailJobType.
type ProviderEmailPayload struct {
	ReissueID string `json:"reissue_id"`
}

// ProviderEmailTarget is what the provider job reads.
type ProviderEmailTarget struct {
	ReissueID  uuid.UUID
	OrgID      uuid.UUID
	Provider   string
	PaymentRef string
	Email      string
	State      string
}

// LoadProviderEmailTarget reads the payment of an applied reissue.
func LoadProviderEmailTarget(ctx context.Context, q Querier, reissueID uuid.UUID) (ProviderEmailTarget, error) {
	var t ProviderEmailTarget
	t.ReissueID = reissueID
	err := q.QueryRow(ctx, `
		SELECT r.org_id, pi.provider, COALESCE(NULLIF(pi.provider_charge_ref, ''), pi.provider_payment_id, ''),
		       r.new_email, COALESCE(r.provider_email_state, '')
		FROM   ticket_reissues r
		JOIN   orders o ON o.id = r.order_id
		JOIN   LATERAL (SELECT provider, provider_charge_ref, provider_payment_id FROM payment_intents
		                WHERE checkout_session_id = o.checkout_session_id AND state = 'succeeded'
		                ORDER BY created_at DESC LIMIT 1) pi ON true
		WHERE  r.id = $1`, reissueID).Scan(&t.OrgID, &t.Provider, &t.PaymentRef, &t.Email, &t.State)
	if err != nil {
		return t, err
	}
	return t, nil
}

// SetProviderEmailState records the provider job's outcome. errText is cut to
// 300 characters and never carries the address.
func SetProviderEmailState(ctx context.Context, db interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, reissueID uuid.UUID, state, errText string) error {
	if len(errText) > 300 {
		errText = errText[:300]
	}
	var id uuid.UUID
	return db.QueryRow(ctx, `UPDATE ticket_reissues
		SET provider_email_state = $2, provider_email_error = NULLIF($3, ''), updated_at = now()
		WHERE id = $1 RETURNING id`, reissueID, state, errText).Scan(&id)
}
