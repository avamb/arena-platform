// scanned.go - a ticket that was already scanned at the gate is never
// refunded (owner decision 2026-10-12). The refusal is hard on every arena
// surface that creates or sends a refund: the organizer API, the bot, the
// flat /v1/refunds routes, the ticket-cancel route and the engine's own
// claim. It judges by the scans recorded in arena (used_at, barcodes, the
// scanner's admitted scan events); an entry scanned only in MACS never
// reaches arena, so such a ticket is not blocked. Money and statuses stay as
// they are; the attempt is journaled and the owners are told once a day.
package refunds

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// failureTicketScanned is the failure_code of a refund stopped because its
// ticket was scanned.
const failureTicketScanned = "ticket_scanned"

// CodeTicketScanned is the 409 of a refund for a ticket that was scanned.
const CodeTicketScanned = "refund.ticket_scanned"

// CodeScanExceptionWholePayment: a refund of the whole payment that relies on
// a scanned ticket's exception must return everything that is left of the
// payment. A smaller amount cannot be tied to the scanned tickets, so it
// would spend their one-time exceptions on money that may belong to others.
const CodeScanExceptionWholePayment = "refund.scan_exception_whole_payment"

// JobTypeScannedAlertEmail is the worker job that mails the organization's
// owners about a blocked refund (handler: delivery.NewScannedAlertHandler).
const JobTypeScannedAlertEmail = "refund.scanned_alert_email"

// EventRefundBlockedScanned is the outbox event the sales bot announces.
const EventRefundBlockedScanned = "v1.refund.blocked_scanned"

// scannedNotifyEvery throttles the owner e-mail and the sales-bot message:
// one per ticket and actor per this interval.
const scannedNotifyEvery = "24 hours"

// ScannedAlertPayload is the job payload of JobTypeScannedAlertEmail.
type ScannedAlertPayload struct {
	OrgID      string `json:"org_id"`
	TicketID   string `json:"ticket_id"`
	ActorLabel string `json:"actor_label"`
	// NoticeID keys the per-address delivery marks of this one notice
	// (refund_scan_notice_sends): a retried job mails only the addresses
	// that have not received it yet.
	NoticeID string `json:"notice_id,omitempty"`
	// Kind is empty for a blocked refund and ScannedAlertKindException for a
	// superadmin's exception (with its Note and its Until, RFC 3339). The
	// operator's X-Admin-Reason never travels here: it stays in the audit.
	Kind  string `json:"kind,omitempty"`
	Note  string `json:"note,omitempty"`
	Until string `json:"until,omitempty"`
}

// ScannedAlertKindException marks the owners' e-mail about an exception.
const ScannedAlertKindException = "exception"

// ticketScannedSQL is true for a ticket t that was used: it carries used_at,
// or ANY barcode of it (whatever authority, the legacy_bil24 import's
// included) is scanned.
//
// An admitted scan event of the ticket's own organization counts too: the
// scanner records it even when stamping used_at failed.
const ticketScannedSQL = `(t.used_at IS NOT NULL OR EXISTS (
	SELECT 1 FROM barcodes sb WHERE sb.ticket_id = t.id AND (sb.status = 'scanned' OR sb.scanned_at IS NOT NULL))
	OR EXISTS (SELECT 1 FROM scan_events se
	           JOIN sessions ss ON ss.id = t.session_id
	           JOIN events se_ev ON se_ev.id = ss.event_id
	           WHERE se.ticket_id = t.id AND se.result = 'admitted' AND se.org_id = se_ev.org_id))`

// noScanSinceGrantSQL is true when ticket t was not scanned after its
// exception sx was granted: the exception covers the scans before it (a show
// stopped half-way, a scan made by mistake), and a later scan is a real entry.
const noScanSinceGrantSQL = `NOT (t.used_at > sx.created_at
	OR EXISTS (SELECT 1 FROM barcodes gb WHERE gb.ticket_id = t.id AND gb.scanned_at > sx.created_at)
	OR EXISTS (SELECT 1 FROM scan_events ge WHERE ge.ticket_id = t.id AND ge.result = 'admitted' AND ge.scanned_at > sx.created_at))`

// scanExemptLiveSQL is true for a ticket t with a platform superadmin's
// exception that is still unspent and unexpired (refund_scan_exceptions,
// one per ticket), and no scan since it was granted.
const scanExemptLiveSQL = `EXISTS (SELECT 1 FROM refund_scan_exceptions sx
	WHERE sx.ticket_id = t.id AND sx.used_at IS NULL AND sx.expires_at > now() AND ` + noScanSinceGrantSQL + `)`

// scanExemptForRefundSQL also lets through the refund r that spent the
// exception, at its approval and its claim (a scan since the grant still
// stops it before any money is sent). A refund of the whole payment (no
// ticket of its own) passes only on the exceptions it spent when it was
// written: a live exception granted later is not its to take, because the
// amount it returns cannot be tied to that ticket.
const scanExemptForRefundSQL = `EXISTS (SELECT 1 FROM refund_scan_exceptions sx
	WHERE sx.ticket_id = t.id
	  AND ((sx.used_at IS NULL AND sx.expires_at > now() AND COALESCE(r.ticket_id, r.cancelled_ticket_id) IS NOT NULL)
	       OR sx.used_by_refund = r.id)
	  AND ` + noScanSinceGrantSQL + `)`

// ticketBlockedSQL is a scanned ticket t without a live exception: no refund
// may be created for it.
const ticketBlockedSQL = `(` + ticketScannedSQL + ` AND NOT ` + scanExemptLiveSQL + `)`

// ticketBlockedForRefundSQL is the same for the stored refund r.
const ticketBlockedForRefundSQL = `(` + ticketScannedSQL + ` AND NOT ` + scanExemptForRefundSQL + `)`

// spendScanExceptionSQL spends the live exception of ticket $2 for refund $1
// (NULL for a cancellation without a refund row).
const spendScanExceptionSQL = `UPDATE refund_scan_exceptions SET used_at = now(), used_by_refund = $1
	WHERE ticket_id = $2 AND used_at IS NULL AND expires_at > now()`

// releaseScanExceptionsSQL gives back the exceptions refund $1 spent when it
// ended without returning money and cannot be sent again: it was rejected, or
// it failed (no provider refund id, or $2: an operator found it not refunded)
// and has no ticket of its own (a refund of the whole payment or of the
// ticket-cancel route, which has no retry). A refund of one ticket keeps its
// exception: its retry takes it over. The exception keeps its expiry; one
// that expired meanwhile is granted again.
const releaseScanExceptionsSQL = `UPDATE refund_scan_exceptions sx SET used_at = NULL, used_by_refund = NULL
	FROM refunds r
	WHERE r.id = $1 AND sx.used_by_refund = r.id
	  AND (r.state = 'rejected'
	       OR (r.state = 'failed' AND r.ticket_id IS NULL AND (r.provider_refund_id IS NULL OR $2::boolean)))`

// ReleaseScanExceptionsTx gives back the scan exceptions refund id spent, in
// the caller's transaction, when the refund ended without returning money
// (releaseScanExceptionsSQL). notRefunded: an operator checked the provider
// and found this refund did not go through.
func ReleaseScanExceptionsTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, notRefunded bool) error {
	_, err := tx.Exec(ctx, releaseScanExceptionsSQL, id, notRefunded)
	return err
}

// scannedOrder makes "the first scanned ticket" the same one every time.
const scannedOrder = ` ORDER BY t.system_ticket_id NULLS LAST, t.id`

func scannedRefusal(ticket uuid.UUID) *Error {
	return refusal(http.StatusConflict, CodeTicketScanned,
		"the ticket was already scanned at the gate, so it cannot be refunded",
		map[string]any{"ticket_id": ticket.String(), "reason": "ticket_scanned"})
}

func actorKey(a Actor) string {
	if a.ID == "" {
		return "system"
	}
	t := a.Type
	if t == "" {
		t = "user"
	}
	return t + ":" + a.ID
}

// What RecordScannedBlock did about telling the owners (error details
// "owners_notice"): a notice was queued now, one went out within the last
// day, or the organization has no address to send it to (the platform
// operator still hears about it through the sales bot). Empty: unknown.
const (
	NoticeQueued      = "queued"
	NoticeThrottled   = "throttled"
	NoticeNoRecipient = "no_recipient"
)

// RecordScannedBlock journals a refused attempt on a scanned ticket and, at
// most once per ticket and actor per day, queues the owners' e-mail and the
// sales-bot message. Best effort: the refusal itself never depends on it. It
// reports what it did about the owners, "" when the journal failed.
func (e *Engine) RecordScannedBlock(ctx context.Context, orgID, ticketID uuid.UUID, actor Actor, via string) string {
	wctx := context.WithoutCancel(ctx)
	notice := ""
	err := e.inTx(wctx, func(tx pgx.Tx) error {
		var label string
		if actor.ID != "" {
			if err := tx.QueryRow(wctx, `SELECT COALESCE(ak.name, NULLIF(btrim(concat_ws(' ', u.first_name, u.last_name)), ''), u.email, '')
				FROM (SELECT 1) one
				LEFT JOIN api_keys ak ON ak.id::text = $2 AND ak.org_id = $1
				LEFT JOIN users u ON u.id::text = $2`, orgID, actor.ID).Scan(&label); err != nil {
				return err
			}
		}
		key := actorKey(actor)
		// Throttle: this actor already told the owners about this ticket
		// within the interval -> journal the attempt, send nothing. The
		// decision is made by the upsert itself, so two first attempts at
		// once cannot both notify: the second waits on the unique key and
		// then sees the first one's notified_at.
		var notify bool
		if err := tx.QueryRow(wctx, `
			INSERT INTO refund_scan_blocks (org_id, ticket_id, actor_key, actor_label, notified_at)
			VALUES ($1, $2, $3, NULLIF($4, ''), now())
			ON CONFLICT (ticket_id, actor_key) DO UPDATE
			   SET attempts = refund_scan_blocks.attempts + 1, last_at = now(),
			       actor_label = COALESCE(NULLIF($4, ''), refund_scan_blocks.actor_label),
			       notified_at = CASE WHEN refund_scan_blocks.notified_at IS NULL
			                            OR refund_scan_blocks.notified_at < now() - $5::interval
			                          THEN now() ELSE refund_scan_blocks.notified_at END
			RETURNING notified_at = now()`,
			orgID, ticketID, key, label, scannedNotifyEvery).Scan(&notify); err != nil {
			return err
		}
		// An owner with an address, or the organization's contact address.
		var reachable bool
		if err := tx.QueryRow(wctx, `SELECT EXISTS (
				SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
				WHERE m.org_id = $1 AND m.role = 'org_admin' AND m.status = 'active' AND btrim(u.email) <> '')
			OR EXISTS (SELECT 1 FROM organizations o WHERE o.id = $1 AND btrim(COALESCE(o.contact_email, '')) <> '')`,
			orgID).Scan(&reachable); err != nil {
			return err
		}
		switch {
		case !reachable:
			notice = NoticeNoRecipient
		case notify:
			notice = NoticeQueued
		default:
			notice = NoticeThrottled
		}
		if notify {
			if reachable {
				payload, _ := json.Marshal(ScannedAlertPayload{OrgID: orgID.String(), TicketID: ticketID.String(), ActorLabel: label,
					NoticeID: uuid.NewString()})
				if _, err := tx.Exec(wctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
					VALUES ($1, $2::jsonb, 5, 'pending', now())`, JobTypeScannedAlertEmail, payload); err != nil {
					return err
				}
			} else {
				e.logger.Error("refunds: a refund blocked by a scan has nobody to tell in the organization",
					"org_id", orgID.String(), "ticket_id", ticketID.String())
			}
			evPayload, _ := json.Marshal(map[string]any{"ticket_id": ticketID.String(), "org_id": orgID.String(), "actor": label,
				"owners_unreachable": !reachable})
			if _, err := tx.Exec(wctx, `INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
				VALUES ('ticket', $1, $2, $3::jsonb)`, ticketID.String(), EventRefundBlockedScanned, evPayload); err != nil {
				return err
			}
		}
		return e.writeAudit(wctx, tx, audit.Event{ActorType: actor.Type, ActorID: actor.ID,
			Action: "v1.refund.blocked_scanned", ResourceType: "ticket", ResourceID: ticketID.String(),
			Metadata: map[string]any{"org_id": orgID.String(), "notified": notify, "owners_notice": notice, "via": via}})
	})
	if err != nil {
		e.logger.Error("refunds: journaling a refund blocked by a scan failed", "ticket_id", ticketID.String(), "error", err.Error())
		return ""
	}
	return notice
}

// ScannedRefusalWithNotice is ScannedRefusal carrying what was done about
// the owners, so a client never claims they were told when they were not.
func ScannedRefusalWithNotice(ticket uuid.UUID, notice string) *Error {
	e := scannedRefusal(ticket)
	e.Details["owners_notice"] = notice
	return e
}

// ScannedTicketOfPayment reports a scanned ticket among those sold with the
// payment (a refund of the whole payment would take its money back). A
// cancelled ticket counts too: cancelling a scanned ticket without money is
// allowed, and must not open the way to returning its money with the rest.
func (e *Engine) ScannedTicketOfPayment(ctx context.Context, paymentIntentID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	found := false
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT t.id FROM payment_intents pi
			JOIN tickets t ON t.checkout_session_id = pi.checkout_session_id
			WHERE pi.id = $1 AND `+ticketBlockedSQL+scannedOrder+` LIMIT 1`, paymentIntentID).Scan(&id)
		if err == nil {
			found = true
			return nil
		}
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	})
	return id, found, err
}

// blockScannedAtClaim is the claim's last line of defence: a refund that was
// requested before the scan and approved after it must not reach the
// provider. Under the payment lock, a waiting refund whose ticket was
// scanned is failed (nothing was sent) and the attempt is journaled.
func (e *Engine) blockScannedAtClaim(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Refund, uuid.UUID, error) {
	var ticket uuid.UUID
	// A refund of the whole payment (no ticket of its own) is held to every
	// ticket sold with that payment, a cancelled one included.
	err := tx.QueryRow(ctx, `SELECT t.id FROM refunds r
		JOIN payment_intents pi ON pi.id = r.payment_intent_id
		JOIN tickets t ON (t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
		                   OR (COALESCE(r.ticket_id, r.cancelled_ticket_id) IS NULL
		                       AND t.checkout_session_id = pi.checkout_session_id))
		WHERE r.id = $1 AND r.state = 'provider_pending' AND r.provider_refund_id IS NULL AND r.provider_attempts = 0
		  AND `+ticketBlockedForRefundSQL+scannedOrder+` LIMIT 1`, id).Scan(&ticket)
	if err == pgx.ErrNoRows {
		// Nothing blocks it. Its own ticket, scanned and passing on a live
		// exception, spends that exception on this refund now. A refund of
		// the whole payment passes only on the exceptions it already owns.
		_, serr := tx.Exec(ctx, `UPDATE refund_scan_exceptions sx SET used_at = now(), used_by_refund = $1
			FROM refunds r
			WHERE r.id = $1 AND sx.ticket_id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
			  AND sx.used_at IS NULL AND sx.expires_at > now()
			  AND r.state = 'provider_pending' AND r.provider_attempts = 0`, id)
		return nil, uuid.Nil, serr
	}
	if err != nil {
		return nil, uuid.Nil, err
	}
	r, err := scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = 'failed', failed_at = now(),
		failure_code = $3, failure_reason = $2, updated_at = now()
		WHERE id = $1 AND state = 'provider_pending' RETURNING `+refundColumns, id,
		"the ticket was scanned at the gate before the refund was sent; nothing was sent to the provider", failureTicketScanned))
	if err != nil {
		return nil, uuid.Nil, err
	}
	if err := ReleaseScanExceptionsTx(ctx, tx, r.ID, false); err != nil {
		return nil, uuid.Nil, err
	}
	return &r, ticket, nil
}

// ScannedRefusal is the error a surface answers for a scanned ticket.
func ScannedRefusal(ticket uuid.UUID) *Error { return scannedRefusal(ticket) }

// ScannedTicketForRefund reports the scanned ticket a stored refund would
// return money for: its own ticket, or, for a refund of the whole payment,
// any ticket sold with it (a cancelled one included).
func (e *Engine) ScannedTicketForRefund(ctx context.Context, refundID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	found := false
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT t.id FROM refunds r
			JOIN payment_intents pi ON pi.id = r.payment_intent_id
			JOIN tickets t ON (t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
			                   OR (COALESCE(r.ticket_id, r.cancelled_ticket_id) IS NULL AND t.checkout_session_id = pi.checkout_session_id))
			WHERE r.id = $1 AND `+ticketBlockedForRefundSQL+scannedOrder+` LIMIT 1`, refundID).Scan(&id)
		if err == nil {
			found = true
			return nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return id, found, err
}

// TicketScanned reports whether money may not be returned for the ticket
// because it was scanned at the gate (the same rule every refund check uses,
// a live superadmin exception included). spend spends that exception at once,
// for a path that writes no refund row of its own.
func (e *Engine) TicketScanned(ctx context.Context, ticketID uuid.UUID, spend bool) (bool, error) {
	var scanned bool
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT `+ticketBlockedSQL+` FROM tickets t WHERE t.id = $1`, ticketID).Scan(&scanned); err != nil {
			return err
		}
		if spend && !scanned {
			_, err := tx.Exec(ctx, spendScanExceptionSQL, nil, ticketID)
			return err
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return scanned, err
}

// GuardTicketMoneyTx is the scan rule inside the transaction that writes the
// money decision for one ticket (the ticket-cancel route): it reports whether
// money may NOT be returned for the ticket, and, when it may and spend is
// set, spends a live superadmin exception on refundID (nil for a decision
// that writes no refund row). Deciding and spending in the writer's own
// transaction means a request that is refused later never burns the
// exception, and a refund written on it owns it from the start.
func GuardTicketMoneyTx(ctx context.Context, tx pgx.Tx, ticketID uuid.UUID, refundID *uuid.UUID, spend bool) (bool, error) {
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT `+ticketBlockedSQL+` FROM tickets t WHERE t.id = $1`, ticketID).Scan(&blocked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if blocked || !spend {
		return blocked, nil
	}
	_, err := tx.Exec(ctx, spendScanExceptionSQL, refundID, ticketID)
	return false, err
}

// GuardPaymentMoneyTx is the same for a refund of the whole payment (the flat
// create route): blocked when any ticket sold with the payment is scanned
// without a live exception (that ticket is returned). When some scanned
// ticket passes on a live exception, the refund must return all that is left
// of the payment (amount == remaining, else a 409 *Error
// CodeScanExceptionWholePayment), and then every such exception is spent on
// refundID. An exception a concurrent writer spent first blocks its ticket.
func GuardPaymentMoneyTx(ctx context.Context, tx pgx.Tx, paymentIntentID, refundID uuid.UUID, amount, remaining int64) (uuid.UUID, bool, error) {
	blockedTicket := func() (uuid.UUID, bool, error) {
		var ticket uuid.UUID
		err := tx.QueryRow(ctx, `SELECT t.id FROM payment_intents pi
			JOIN tickets t ON t.checkout_session_id = pi.checkout_session_id
			WHERE pi.id = $1 AND `+ticketBlockedSQL+scannedOrder+` LIMIT 1`, paymentIntentID).Scan(&ticket)
		if err == nil {
			return ticket, true, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	if ticket, blocked, err := blockedTicket(); err != nil || blocked {
		return ticket, blocked, err
	}
	var exempt int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM payment_intents pi
		JOIN tickets t ON t.checkout_session_id = pi.checkout_session_id
		WHERE pi.id = $1 AND `+ticketScannedSQL+` AND `+scanExemptLiveSQL, paymentIntentID).Scan(&exempt); err != nil {
		return uuid.Nil, false, err
	}
	if exempt == 0 {
		return uuid.Nil, false, nil
	}
	if amount != remaining {
		return uuid.Nil, false, refusal(http.StatusConflict, CodeScanExceptionWholePayment,
			"a scanned ticket of this payment is refunded on a one-time exception: refund all that is left of the payment, or refund the tickets one by one",
			map[string]any{"requested": amount, "remaining": remaining})
	}
	tag, err := tx.Exec(ctx, `UPDATE refund_scan_exceptions sx SET used_at = now(), used_by_refund = $1
		FROM payment_intents pi JOIN tickets t ON t.checkout_session_id = pi.checkout_session_id
		WHERE pi.id = $2 AND sx.ticket_id = t.id AND sx.used_at IS NULL AND sx.expires_at > now()
		  AND `+ticketScannedSQL+` AND `+noScanSinceGrantSQL, refundID, paymentIntentID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if tag.RowsAffected() != exempt {
		return blockedTicket()
	}
	return uuid.Nil, false, nil
}
