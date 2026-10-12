// scanned.go - a ticket that was already scanned at the gate is never
// refunded (owner decision 2026-10-12). The refusal is hard on every surface
// that creates or sends a refund: the organizer API, the bot, the flat
// /v1/refunds routes and the engine's own claim. Money and statuses stay as
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

// CodeTicketScanned is the 409 of a refund for a ticket that was scanned.
const CodeTicketScanned = "refund.ticket_scanned"

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
}

// ticketScannedSQL is true for a ticket t that was used: it carries used_at,
// or ANY barcode of it (whatever authority, the legacy_bil24 import's
// included) is scanned.
const ticketScannedSQL = `(t.used_at IS NOT NULL OR EXISTS (
	SELECT 1 FROM barcodes sb WHERE sb.ticket_id = t.id AND (sb.status = 'scanned' OR sb.scanned_at IS NOT NULL)))`

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

// RecordScannedBlock journals a refused attempt on a scanned ticket and, at
// most once per ticket and actor per day, queues the owners' e-mail and the
// sales-bot message. Best effort: the refusal itself never depends on it.
func (e *Engine) RecordScannedBlock(ctx context.Context, orgID, ticketID uuid.UUID, actor Actor, via string) {
	wctx := context.WithoutCancel(ctx)
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
		// within the interval -> journal the attempt, send nothing.
		notify := true
		if err := tx.QueryRow(wctx, `SELECT notified_at IS NULL OR notified_at < now() - $3::interval
			FROM refund_scan_blocks WHERE ticket_id = $1 AND actor_key = $2 FOR UPDATE`,
			ticketID, key, scannedNotifyEvery).Scan(&notify); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(wctx, `
			INSERT INTO refund_scan_blocks (org_id, ticket_id, actor_key, actor_label, notified_at)
			VALUES ($1, $2, $3, NULLIF($4, ''), now())
			ON CONFLICT (ticket_id, actor_key) DO UPDATE
			   SET attempts = refund_scan_blocks.attempts + 1, last_at = now(),
			       actor_label = COALESCE(NULLIF($4, ''), refund_scan_blocks.actor_label),
			       notified_at = CASE WHEN $5 THEN now() ELSE refund_scan_blocks.notified_at END`,
			orgID, ticketID, key, label, notify); err != nil {
			return err
		}
		if notify {
			payload, _ := json.Marshal(ScannedAlertPayload{OrgID: orgID.String(), TicketID: ticketID.String(), ActorLabel: label})
			if _, err := tx.Exec(wctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
				VALUES ($1, $2::jsonb, 5, 'pending', now())`, JobTypeScannedAlertEmail, payload); err != nil {
				return err
			}
			evPayload, _ := json.Marshal(map[string]any{"ticket_id": ticketID.String(), "org_id": orgID.String(), "actor": label})
			if _, err := tx.Exec(wctx, `INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
				VALUES ('ticket', $1, $2, $3::jsonb)`, ticketID.String(), EventRefundBlockedScanned, evPayload); err != nil {
				return err
			}
		}
		return e.writeAudit(wctx, tx, audit.Event{ActorType: actor.Type, ActorID: actor.ID,
			Action: "v1.refund.blocked_scanned", ResourceType: "ticket", ResourceID: ticketID.String(),
			Metadata: map[string]any{"org_id": orgID.String(), "notified": notify, "via": via}})
	})
	if err != nil {
		e.logger.Error("refunds: journaling a refund blocked by a scan failed", "ticket_id", ticketID.String(), "error", err.Error())
	}
}

// ScannedTicketOfPayment reports a scanned ticket among those sold with the
// payment (a refund of the whole payment would take its money back).
func (e *Engine) ScannedTicketOfPayment(ctx context.Context, paymentIntentID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	found := false
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT t.id FROM payment_intents pi
			JOIN tickets t ON t.checkout_session_id = pi.checkout_session_id
			WHERE pi.id = $1 AND t.status = 'active' AND `+ticketScannedSQL+` LIMIT 1`, paymentIntentID).Scan(&id)
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
	err := tx.QueryRow(ctx, `SELECT t.id FROM refunds r
		JOIN tickets t ON t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
		WHERE r.id = $1 AND r.state = 'provider_pending' AND r.provider_refund_id IS NULL AND r.provider_attempts = 0
		  AND `+ticketScannedSQL, id).Scan(&ticket)
	if err == pgx.ErrNoRows {
		return nil, uuid.Nil, nil
	}
	if err != nil {
		return nil, uuid.Nil, err
	}
	r, err := scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = 'failed', failed_at = now(),
		failure_code = 'ticket_scanned', failure_reason = $2, updated_at = now()
		WHERE id = $1 AND state = 'provider_pending' RETURNING `+refundColumns, id,
		"the ticket was scanned at the gate before the refund was sent; nothing was sent to the provider"))
	if err != nil {
		return nil, uuid.Nil, err
	}
	return &r, ticket, nil
}

// ScannedRefusal is the error a surface answers for a scanned ticket.
func ScannedRefusal(ticket uuid.UUID) *Error { return scannedRefusal(ticket) }

// ScannedTicketForRefund reports the scanned ticket a stored refund would
// return money for: its own ticket, or, for a refund of the whole payment,
// any active ticket sold with it.
func (e *Engine) ScannedTicketForRefund(ctx context.Context, refundID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	found := false
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT t.id FROM refunds r
			JOIN payment_intents pi ON pi.id = r.payment_intent_id
			JOIN tickets t ON (t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
			                   OR (COALESCE(r.ticket_id, r.cancelled_ticket_id) IS NULL AND t.checkout_session_id = pi.checkout_session_id AND t.status = 'active'))
			WHERE r.id = $1 AND `+ticketScannedSQL+` LIMIT 1`, refundID).Scan(&id)
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
