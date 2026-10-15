package hmacsscan

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// TicketScannedEventType is the outbox event of a recorded scan. arena's own
// scanner callback emits the same type (hscanner.TicketScannedEventType);
// the MACS one carries source "macs" in its payload.
const TicketScannedEventType = "v1.ticket.scanned"

// TicketAggregateType is the outbox aggregate of v1.ticket.scanned.
const TicketAggregateType = "ticket"

// AuditActorType is audit_events.actor_type of a MACS admission; actor_id is
// the subscriber's uuid.
const AuditActorType = "macs_subscriber"

// AuditActionAdmitted is the audit action of a first admission MACS reports.
const AuditActionAdmitted = "v1.ticket.admitted"

// Source is scan_events.source of the rows this route writes.
const Source = "macs"

// processBatch records every scan in its own transaction, the scans in order
// of scanned_at so a batch an offline scanner sends late stamps used_at with
// its earliest admission. Results come back in request order.
func (h *Handler) processBatch(ctx context.Context, subID, orgID uuid.UUID, in []ScanInput) []ScanResult {
	now := h.now()
	results := make([]ScanResult, len(in))
	valid := make([]int, 0, len(in))
	scans := make([]scan, len(in))
	for i, raw := range in {
		s, res, ok := validate(raw, now)
		if !ok {
			results[i] = res
			continue
		}
		scans[i] = s
		valid = append(valid, i)
	}
	sort.SliceStable(valid, func(a, b int) bool { return scans[valid[a]].scannedAt.Before(scans[valid[b]].scannedAt) })
	for _, i := range valid {
		results[i] = h.processOne(ctx, subID, orgID, scans[i])
	}
	return results
}

// errNotFound is a ticket that does not exist or belongs to another
// organization; both answer the same code.
var errNotFound = errors.New("ticket not found")

// errMismatch is a barcode and a ticket id that name different tickets.
var errMismatch = errors.New("ticket mismatch")

func (h *Handler) processOne(ctx context.Context, subID, orgID uuid.UUID, s scan) ScanResult {
	id := s.in.ScanID
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return h.unavailable(id, "begin", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	q := gen.New(tx)

	// A scan this subscriber already delivered: answered from the stored row,
	// nothing is written and nothing is locked.
	if prev, err := q.GetScanEventBySubscriberScan(ctx, subID, id); err == nil {
		return duplicateOf(id, prev)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return h.unavailable(id, "replay lookup", err)
	}

	t, err := resolveTicket(ctx, q, s, orgID)
	switch {
	case errors.Is(err, errNotFound):
		return rejected(id, CodeTicketNotFound, "no ticket of this organization has that id or barcode")
	case errors.Is(err, errMismatch):
		return rejected(id, CodeTicketMismatch, "system_ticket_id and barcode name different tickets")
	case err != nil:
		return h.unavailable(id, "resolve ticket", err)
	}

	row, inserted, err := q.InsertMACSScanEvent(ctx, gen.InsertMACSScanEventParams{
		OrgID: orgID, EventID: t.EventID, SessionID: t.SessionID, TicketID: t.TicketID,
		CredentialCode: s.credentialCode(), ScannedAt: s.scannedAt,
		Gate: s.in.Gate, DeviceID: s.in.ScannerID, Result: s.storedResult,
		ScannerLabel: s.in.ScannerName, ExternalScanID: id, SubscriberID: subID,
	})
	if err != nil {
		return h.unavailable(id, "insert", err)
	}
	if !inserted {
		// A concurrent copy of this batch won the race, or the same physical
		// scan (barcode and second) is already recorded under another scan id.
		if prev, err := q.GetScanEventBySubscriberScan(ctx, subID, id); err == nil {
			return duplicateOf(id, prev)
		}
		if prev, err := q.GetScanEventByCredential(ctx, s.credentialCode(), s.scannedAt); err == nil && prev.OrgID == orgID {
			return duplicateOf(id, prev)
		}
		return ScanResult{ScanID: id, Status: StatusDuplicate}
	}

	first := false
	if s.storedResult == "admitted" && t.Status == "active" && t.UsedAt == nil {
		// The ticket row is locked (FOR UPDATE in resolveTicket), so "used_at
		// was empty" is exact. Only a valid ticket is stamped: an offline
		// scanner may admit a ticket cancelled meanwhile, which is recorded as
		// a fact but does not make the cancelled ticket "used".
		if err := q.MarkTicketUsedAtIfUnset(ctx, t.TicketID, s.scannedAt); err != nil {
			return h.unavailable(id, "mark used", err)
		}
		first = true
	}

	if h.outbox != nil {
		if err := h.outbox.Append(ctx, tx, outbox.Event{
			AggregateType: TicketAggregateType,
			AggregateID:   t.TicketID.String(),
			EventType:     TicketScannedEventType,
			Payload:       scannedPayload(s, row, t, orgID, subID, first),
		}); err != nil {
			return h.unavailable(id, "outbox", err)
		}
	}
	if first && h.audit != nil {
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   h.now().UTC(),
			ActorType:    AuditActorType,
			ActorID:      subID.String(),
			Action:       AuditActionAdmitted,
			ResourceType: "ticket",
			ResourceID:   t.TicketID.String(),
			Metadata: map[string]any{
				"org_id":           orgID.String(),
				"source":           Source,
				"scan_event_id":    row.ID.String(),
				"external_scan_id": id,
				"scanned_at":       s.scannedAt.Format(time.RFC3339),
				"gate":             s.in.Gate,
				"scanner_id":       s.in.ScannerID,
				"scanner_name":     s.in.ScannerName,
			},
		}); err != nil {
			return h.unavailable(id, "audit", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return h.unavailable(id, "commit", err)
	}
	ticketID := t.TicketID.String()
	return ScanResult{ScanID: id, Status: StatusAccepted, ScanEventID: row.ID.String(), TicketID: &ticketID, FirstAdmission: first}
}

// resolveTicket finds and locks the ticket the scan names inside orgID; a
// ticket of another organization is never read, so it answers errNotFound
// exactly like an unknown one.
func resolveTicket(ctx context.Context, q *gen.Queries, s scan, orgID uuid.UUID) (gen.MACSScanTicketRow, error) {
	var (
		byID, byCode gen.MACSScanTicketRow
		haveID       bool
	)
	if s.hasSystemID {
		row, err := q.ResolveMACSScanTicketBySystemID(ctx, s.systemID, orgID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return row, errNotFound
			}
			return row, err
		}
		byID, haveID = row, true
	}
	if s.barcode != "" {
		row, err := resolveBarcode(ctx, q, s.barcode, orgID)
		switch {
		case errors.Is(err, errNotFound) && haveID:
			return byID, errMismatch
		case err != nil:
			return row, err
		}
		if haveID && row.TicketID != byID.TicketID {
			return byID, errMismatch
		}
		byCode = row
	}
	t := byID
	if !haveID {
		t = byCode
	}
	if t.OrgID != orgID {
		return gen.MACSScanTicketRow{}, errNotFound
	}
	return t, nil
}

// resolveBarcode finds the ticket of a 13-digit code: a stored barcode of
// any authority first, then the legacy PlatformCode formula.
func resolveBarcode(ctx context.Context, q *gen.Queries, code string, orgID uuid.UUID) (gen.MACSScanTicketRow, error) {
	row, err := q.ResolveMACSScanTicketByBarcode(ctx, code, orgID)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return row, err
	}
	if id, ok := legacySystemID(code); ok {
		row, err = q.ResolveMACSScanTicketBySystemID(ctx, id, orgID)
		if err == nil || !errors.Is(err, pgx.ErrNoRows) {
			return row, err
		}
	}
	return gen.MACSScanTicketRow{}, errNotFound
}

func duplicateOf(scanID string, prev gen.ScanEventRow) ScanResult {
	res := ScanResult{ScanID: scanID, Status: StatusDuplicate, ScanEventID: prev.ID.String()}
	if prev.TicketID != nil {
		t := prev.TicketID.String()
		res.TicketID = &t
	}
	return res
}

func (h *Handler) unavailable(scanID, step string, err error) ScanResult {
	h.logger.Error("macs_scan_events: scan not recorded",
		slog.String("scan_id", scanID), slog.String("step", step), slog.String("error", err.Error()))
	return ScanResult{ScanID: scanID, Status: StatusRejected, ErrorCode: CodeUnavailable,
		Error: "the scan could not be recorded now; send it again", Retryable: true}
}

// scannedPayload is the v1.ticket.scanned payload: the keys arena's own
// scanner writes, plus where the scan came from.
func scannedPayload(s scan, row gen.ScanEventRow, t gen.MACSScanTicketRow, orgID, subID uuid.UUID, first bool) map[string]any {
	return map[string]any{
		"ticket_id":        t.TicketID.String(),
		"session_id":       t.SessionID.String(),
		"event_id":         t.EventID.String(),
		"org_id":           orgID.String(),
		"credential_code":  s.credentialCode(),
		"scanned_at":       s.scannedAt.Format(time.RFC3339),
		"result":           s.storedResult,
		"gate":             s.in.Gate,
		"device_id":        s.in.ScannerID,
		"scan_event_id":    row.ID.String(),
		"source":           Source,
		"scanner_label":    s.in.ScannerName,
		"external_scan_id": s.in.ScanID,
		"subscriber_id":    subID.String(),
		"macs_result":      s.in.Result,
		"system_ticket_id": t.SystemTicketID,
		"first_admission":  first,
	}
}
