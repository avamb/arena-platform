// scanner_callback.go — POST /v1/scanner/scan-events ingestion endpoint
// (feature #293 / S-2).
//
// External scanner devices periodically POST a batch of scan reports to this
// endpoint.  Each report records a single physical scan attempt at the gate
// (admitted or denied).  The endpoint is authenticated via an
// agent_feed_tokens bearer value presented in the Authorization header — the
// same credential family used by the public feed read APIs (ADR-013).
//
// Request:
//
//	POST /v1/scanner/scan-events
//	Authorization: Bearer <agent_feed_token>
//	Content-Type:  application/json
//
//	{
//	  "scans": [
//	    {
//	      "credential_code": "abcd…",                  // QR payload / barcode ref
//	      "scanned_at":      "2026-06-30T18:01:23Z",   // RFC3339, scanner-clock
//	      "gate":            "Gate 12",                // optional, human-readable
//	      "device_id":       "scanner-007",            // optional, stable identifier
//	      "result":          "admitted"                // "admitted" | "denied"
//	    }
//	  ]
//	}
//
// Response (200):
//
//	{
//	  "results": [
//	    {
//	      "credential_code": "abcd…",
//	      "scanned_at":      "2026-06-30T18:01:23Z",
//	      "scan_event_id":   "01HZ…",                  // uuid of the row
//	      "ticket_id":       "01HZ…",                  // null when unresolved
//	      "duplicate":       false,                    // true on idempotent replay
//	      "first_admission": true                      // true when this scan set tickets.used_at
//	    }
//	  ]
//	}
//
// Idempotency:  unique (credential_code, scanned_at) constraint on scan_events
// collapses retried requests to a no-op.  The handler still returns 200 with
// the original row identifiers so the scanner side can mark the batch as
// acknowledged.  Side effects (tickets.used_at update, outbox emit) are
// suppressed on the replay path.
//
// Outbox:  every newly-inserted scan_events row with a resolved ticket emits
// a v1.ticket.scanned outbox event so downstream consumers (analytics,
// reporting fan-out) can react.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// ─────────────────────────────────────────────────────────────────────────────
// Outbox event constants
// ─────────────────────────────────────────────────────────────────────────────

// TicketScannedEventType is the outbox event emitted once per newly-inserted
// scan_events row whose credential_code resolved to a known ticket.  Subscribers
// can use this to fan out scan notifications, refresh dashboards, or update
// analytics pipelines.
const TicketScannedEventType = "v1.ticket.scanned"

// ─────────────────────────────────────────────────────────────────────────────
// Request / response types
// ─────────────────────────────────────────────────────────────────────────────

// scannerScanInput is one entry in the POST body.  ScannedAt is required and
// must be RFC3339; CredentialCode and Result are required strings; Gate and
// DeviceID are optional free-form labels.
type scannerScanInput struct {
	CredentialCode string `json:"credential_code"`
	ScannedAt      string `json:"scanned_at"`
	Gate           string `json:"gate"`
	DeviceID       string `json:"device_id"`
	Result         string `json:"result"`
}

// scannerScanBatchRequest is the top-level POST body.
type scannerScanBatchRequest struct {
	Scans []scannerScanInput `json:"scans"`
}

// scannerScanResult is one entry in the response array, returned in the same
// order as the input scans slice.  TicketID may be null when the
// credential_code could not be resolved to a known ticket.
type scannerScanResult struct {
	CredentialCode string  `json:"credential_code"`
	ScannedAt      string  `json:"scanned_at"`
	ScanEventID    string  `json:"scan_event_id,omitempty"`
	TicketID       *string `json:"ticket_id"`
	Result         string  `json:"result"`
	Duplicate      bool    `json:"duplicate"`
	FirstAdmission bool    `json:"first_admission"`
	Error          string  `json:"error,omitempty"`
}

// scannerScanBatchResponse is the top-level response body.
type scannerScanBatchResponse struct {
	Results []scannerScanResult `json:"results"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

// maxScannerBatchSize caps the number of scans accepted in a single POST so
// one device cannot monopolise the listener.  Scanners that need to backfill
// a long offline window MUST page their callbacks.
const maxScannerBatchSize = 500

// ─────────────────────────────────────────────────────────────────────────────
// Handler
// ─────────────────────────────────────────────────────────────────────────────

// handleScannerScanEvents accepts a batch of scan reports from an external
// scanner device.  See the file header for the wire contract.
func (s *Server) handleScannerScanEvents(w http.ResponseWriter, r *http.Request) {
	if s.feedTokenQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "scanner ingest is not available", r,
		))
		return
	}

	// ── Bearer token extraction ──────────────────────────────────────────────
	token := extractBearerToken(r.Header.Get("Authorization"))
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="arena-scanner"`)
		writeJSON(w, http.StatusUnauthorized, errorEnvelope(
			"scanner.missing_token", "Authorization: Bearer <agent_feed_token> required", r,
		))
		return
	}

	ctx := r.Context()

	scope, err := s.feedTokenQueries.ResolveFeedTokenScannerScope(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="arena-scanner"`)
			writeJSON(w, http.StatusUnauthorized, errorEnvelope(
				"scanner.invalid_token", "agent feed token is unknown or revoked", r,
			))
			return
		}
		s.logger.Error("scanner_callback: resolve feed token failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"scanner.auth_lookup_failed", "failed to validate scanner credential", r,
		))
		return
	}

	// ── Parse body ──────────────────────────────────────────────────────────
	var body scannerScanBatchRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"scanner.invalid_body", "request body must be valid JSON with a 'scans' array", r,
		))
		return
	}
	if len(body.Scans) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"scanner.empty_batch", "scans array must contain at least one entry", r,
		))
		return
	}
	if len(body.Scans) > maxScannerBatchSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorEnvelope(
			"scanner.batch_too_large",
			"scans array exceeds maximum batch size; page the callback",
			r,
		))
		return
	}

	// ── Process each scan ───────────────────────────────────────────────────
	results := make([]scannerScanResult, 0, len(body.Scans))
	for _, in := range body.Scans {
		results = append(results, s.processScannerScan(ctx, scope.OrgID, in))
	}

	// Best-effort touch on last_used_at so operators can see active scanners.
	if err := s.feedTokenQueries.TouchFeedTokenLastUsed(ctx, token); err != nil {
		s.logger.Warn("scanner_callback: touch feed token last_used_at failed",
			slog.String("error", err.Error()),
		)
	}

	s.logger.Info("scanner_callback: batch ingested",
		slog.String("org_id", scope.OrgID.String()),
		slog.String("sales_channel_id", scope.SalesChannelID.String()),
		slog.Int("batch_size", len(body.Scans)),
	)

	writeJSON(w, http.StatusOK, scannerScanBatchResponse{Results: results})
}

// processScannerScan ingests a single scan input row.  Returns a response
// entry describing the outcome.  Per-scan validation errors (bad timestamp,
// bad result enum) are reported on the returned scannerScanResult.Error
// field instead of failing the whole batch — partial-success semantics
// mirror the way scanner devices already debatch their uploads.
func (s *Server) processScannerScan(ctx context.Context, orgID uuid.UUID, in scannerScanInput) scannerScanResult {
	res := scannerScanResult{
		CredentialCode: in.CredentialCode,
		ScannedAt:      in.ScannedAt,
		Result:         in.Result,
	}

	// Reject obviously malformed entries up-front.
	if in.CredentialCode == "" {
		res.Error = "credential_code is required"
		return res
	}
	scannedAt, err := time.Parse(time.RFC3339, in.ScannedAt)
	if err != nil {
		res.Error = "scanned_at must be RFC3339"
		return res
	}
	switch in.Result {
	case "admitted", "denied":
		// ok
	default:
		res.Error = `result must be "admitted" or "denied"`
		return res
	}

	// Best-effort: resolve the credential to a known ticket.  Unresolved
	// scans still get persisted (audit trail) but without ticket lineage.
	var (
		eventID       *uuid.UUID
		sessionID     *uuid.UUID
		ticketID      *uuid.UUID
		usedAtBefore  *time.Time
	)
	resolved, lookupErr := s.feedTokenQueries.ResolveScanCredentialByTicketQR(ctx, in.CredentialCode)
	switch {
	case lookupErr == nil:
		id := resolved.TicketID
		ticketID = &id
		sid := resolved.SessionID
		sessionID = &sid
		eid := resolved.EventID
		eventID = &eid
		usedAtBefore = resolved.TicketUsedAt
	case errors.Is(lookupErr, pgx.ErrNoRows):
		// Unresolved credential — preserve audit trail without FKs.
	default:
		s.logger.Error("scanner_callback: resolve credential failed",
			slog.String("credential_code_prefix", credentialPrefixForLog(in.CredentialCode)),
			slog.String("error", lookupErr.Error()),
		)
		res.Error = "credential lookup failed"
		return res
	}

	inserted, err := s.feedTokenQueries.InsertScanEvent(
		ctx,
		orgID, eventID, sessionID, ticketID,
		in.CredentialCode, scannedAt, in.Gate, in.DeviceID, in.Result,
	)
	if err != nil {
		s.logger.Error("scanner_callback: insert scan_events failed",
			slog.String("credential_code_prefix", credentialPrefixForLog(in.CredentialCode)),
			slog.String("error", err.Error()),
		)
		res.Error = "scan_events insert failed"
		return res
	}

	res.ScanEventID = inserted.ID.String()
	res.Duplicate = !inserted.Inserted
	if ticketID != nil {
		t := ticketID.String()
		res.TicketID = &t
	}

	// Side effects only run on first insert (suppressed on idempotent replay).
	if inserted.Inserted && ticketID != nil {
		if in.Result == "admitted" {
			if err := s.feedTokenQueries.MarkTicketUsedAtIfUnset(ctx, *ticketID, scannedAt); err != nil {
				s.logger.Warn("scanner_callback: mark ticket used_at failed",
					slog.String("ticket_id", ticketID.String()),
					slog.String("error", err.Error()),
				)
			} else if usedAtBefore == nil {
				// We only flip first_admission when used_at was previously NULL.
				res.FirstAdmission = true
			}
		}

		// Emit one v1.ticket.scanned outbox event per first-insert scan with a
		// known ticket.  Subscribers (analytics, reporting) fan out from here.
		payload := map[string]any{
			"ticket_id":       ticketID.String(),
			"session_id":      sessionID.String(),
			"event_id":        eventID.String(),
			"org_id":          orgID.String(),
			"credential_code": in.CredentialCode,
			"scanned_at":      scannedAt.UTC().Format(time.RFC3339),
			"result":          in.Result,
			"gate":            in.Gate,
			"device_id":       in.DeviceID,
			"scan_event_id":   inserted.ID.String(),
		}
		s.publishScannerEvent(ctx, outbox.Event{
			AggregateType: TicketAggregateType,
			AggregateID:   ticketID.String(),
			EventType:     TicketScannedEventType,
			Payload:       payload,
		})
	}

	return res
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// extractBearerToken parses an "Authorization: Bearer <token>" header value
// and returns the token, or the empty string when the header is missing /
// malformed.
func extractBearerToken(headerValue string) string {
	if headerValue == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(headerValue) <= len(prefix) || !strings.EqualFold(headerValue[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(headerValue[len(prefix):])
}

// credentialPrefixForLog returns a short non-reversible prefix of the
// credential value for log lines.  We never log the full credential to avoid
// leaking bearer-quality strings into structured-log sinks.
func credentialPrefixForLog(code string) string {
	const max = 8
	if len(code) <= max {
		return code
	}
	return code[:max] + "…"
}
