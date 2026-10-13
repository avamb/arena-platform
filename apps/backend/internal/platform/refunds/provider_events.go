// provider_events.go — refund events of the provider that arena gave up on.
// Such an event has no refunds row to carry an alert (or must not move the
// one it has), so it leaves a visible record in the audit trail
// (v1.refund.provider_event_unresolved, resource provider_refund) and the
// sweep sends one ops alert per record, marking it alerted
// (v1.refund.provider_event_alerted) only after a confirmed delivery.
package refunds

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

const (
	actionProviderEventUnresolved = "v1.refund.provider_event_unresolved"
	actionProviderEventAlerted    = "v1.refund.provider_event_alerted"
)

// Kinds of an unresolved provider refund event (metadata.kind).
const (
	// UnresolvedUnconfirmed: a refund made at the provider for a payment of
	// the organization could not be read back; nothing was recorded.
	UnresolvedUnconfirmed = "unconfirmed"
	// UnresolvedKnownUnconfirmed: an event for a refund arena already has
	// could not be read back; the refund was left as it was.
	UnresolvedKnownUnconfirmed = "known_unconfirmed"
	// UnresolvedNoPayment: an event naming no payment could not be read back.
	UnresolvedNoPayment = "no_payment"
	// UnresolvedNoChargeRef: the event's payment matched none of the
	// organization's, and some of its payments still lack the provider's
	// charge reference, so a match could not be ruled out.
	UnresolvedNoChargeRef = "no_charge_ref"
)

// providerEventAlertWindow: records older than this are no longer alerted.
const providerEventAlertWindow = 30 * 24 * time.Hour

// UnresolvedEvent is one record to write.
type UnresolvedEvent struct {
	OrgID            uuid.UUID
	Provider         string
	ProviderRefundID string
	Kind             string
	ChargeRef        string
	RefundID         *uuid.UUID // arena's refund, for known_unconfirmed
	Attempts         int
	Detail           string
}

// recordUnresolved writes the record once per (organization, provider
// refund id, kind); a repeat is a no-op.
func (e *Engine) recordUnresolved(ctx context.Context, u UnresolvedEvent) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"provider_event:"+u.OrgID.String()+":"+u.ProviderRefundID); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_events
			WHERE resource_type = 'provider_refund' AND resource_id = $1 AND action = $2
			  AND metadata->>'org_id' = $3::text AND metadata->>'kind' = $4)`,
			u.ProviderRefundID, actionProviderEventUnresolved, u.OrgID, u.Kind).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_events (occurred_at, actor_type, action, resource_type, resource_id, metadata)
			VALUES (now(), 'system', $1, 'provider_refund', $2,
			        jsonb_build_object('org_id', $3::text, 'provider', $4::text, 'kind', $5::text,
			                           'charge_ref', $6::text, 'refund_id', $7::text, 'attempts', $8::int, 'detail', $9::text))`,
			actionProviderEventUnresolved, u.ProviderRefundID, u.OrgID, u.Provider, u.Kind,
			u.ChargeRef, uuidString(u.RefundID), u.Attempts, truncate(u.Detail, 300))
		if err == nil {
			e.logger.Error("refunds: A PROVIDER REFUND EVENT COULD NOT BE APPLIED; an operator is alerted",
				"event", "provider_refund_event_unresolved", "kind", u.Kind, "provider", u.Provider,
				"provider_refund_id", u.ProviderRefundID, "org_id", u.OrgID.String())
		}
		return err
	})
}

// alertProviderEvents sends one ops alert per unresolved record not alerted
// yet, oldest first. Only a confirmed delivery marks the record; the first
// failure ends the section (the next pass retries).
func (e *Engine) alertProviderEvents(ctx context.Context, n Notifier, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	type rec struct {
		id                                         uuid.UUID
		refundID, org, provider, kind, charge, our string
		attempts                                   int
	}
	var recs []rec
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT u.id, u.resource_id, COALESCE(u.metadata->>'org_id', ''), COALESCE(u.metadata->>'provider', ''),
			       COALESCE(u.metadata->>'kind', ''), COALESCE(u.metadata->>'charge_ref', ''), COALESCE(u.metadata->>'refund_id', ''),
			       COALESCE((u.metadata->>'attempts')::int, 0)
			FROM audit_events u
			WHERE u.resource_type = 'provider_refund' AND u.action = $1 AND u.occurred_at > $3
			  AND (cardinality($4::uuid[]) = 0 OR u.metadata->>'org_id' = ANY($4::uuid[]::text[]))
			  AND NOT EXISTS (SELECT 1 FROM audit_events a
			                  WHERE a.resource_type = 'provider_refund' AND a.resource_id = u.resource_id
			                    AND a.action = $2 AND a.metadata->>'unresolved_id' = u.id::text)
			ORDER BY u.occurred_at LIMIT `+strconv.Itoa(limit),
			actionProviderEventUnresolved, actionProviderEventAlerted, time.Now().Add(-providerEventAlertWindow), e.scopeOrgs())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.id, &r.refundID, &r.org, &r.provider, &r.kind, &r.charge, &r.our, &r.attempts); err != nil {
				return err
			}
			recs = append(recs, r)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, r := range recs {
		text := providerEventAlertText(r.kind, r.provider, r.refundID, r.org, r.charge, r.our, r.attempts)
		if derr := deliver(ctx, n, text); derr != nil {
			e.logger.Warn("refunds: provider-event alert not delivered; the next pass retries it",
				"provider_refund_id", r.refundID, "error", derr.Error())
			break
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertWriteTimeout)
		if err := e.inTx(wctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(wctx, `INSERT INTO audit_events (occurred_at, actor_type, action, resource_type, resource_id, metadata)
				VALUES (now(), 'system', $1, 'provider_refund', $2, jsonb_build_object('unresolved_id', $3::text, 'org_id', $4::text))`,
				actionProviderEventAlerted, r.refundID, r.id, r.org)
			return err
		}); err != nil {
			e.logger.Error("refunds: marking a provider-event alert delivered failed", "provider_refund_id", r.refundID, "error", err.Error())
		}
		cancel()
		sent++
	}
	return sent, nil
}

// recoverReadBack is how an event that could not be read back is replayed:
// the usual cause is a payment key that may not read refunds.
const recoverReadBack = " To replay it: fix the organization's payment key (it must be allowed to read refunds)," +
	" then resend the event from the provider dashboard (in Stripe: Developers, Events, the event, Resend)."

// providerEventAlertText is the ops message of one unresolved record: ids
// only, never the buyer, every value HTML-escaped.
func providerEventAlertText(kind, provider, refundID, org, chargeRef, ourRefund string, attempts int) string {
	esc := opsalert.EscapeHTML
	head := fmt.Sprintf("Refund %s at %s (organization %s", esc(refundID), esc(provider), esc(org))
	if chargeRef != "" {
		head += ", payment " + esc(chargeRef)
	}
	head += ")"
	switch kind {
	case UnresolvedKnownUnconfirmed:
		return head + fmt.Sprintf(" is arena refund %s; an update the provider sent for it could not be read back after %d deliveries, so the refund was left as it was. Check the refund in the provider dashboard and in arena."+recoverReadBack, esc(ourRefund), attempts)
	case UnresolvedNoPayment:
		return head + fmt.Sprintf(" names no payment and could not be read back after %d deliveries; it may not be an arena payment. Nothing was recorded. Check the provider dashboard."+recoverReadBack, attempts)
	case UnresolvedNoChargeRef:
		return head + " matched no payment of the organization, but some of its payments have no stored provider charge reference, so it may be one of them. Nothing was recorded and its tickets are unchanged: check in the provider dashboard which payment it refunds."
	}
	return head + fmt.Sprintf(" was made at the provider for a payment of the organization but could not be read back after %d deliveries. Nothing was recorded in arena and the order's tickets are still valid: check the provider dashboard."+recoverReadBack, attempts)
}
