// Package hmacsscan receives the scans MACS, the gate system of the
// WordPress clients, posts back to arena (wave B, task M1, plan section 8.12,
// spec 17 "Scans from MACS").
//
//	POST /v1/macs/scan-events
//	X-MACS-Subscriber-Id: <webhook_subscribers.id of kind 'macs'>
//	X-MACS-Signature:     sha256=<hex HMAC-SHA256 of the raw body>
//
// The body is signed with the SAME signing_secret arena signs its own MACS
// webhooks with (macs/dispatcher.go), so one secret serves both directions.
// The subscriber is the organization: a scan is recorded only for a ticket
// of that subscriber's organization, any other ticket is rejected per item
// with the same code as an unknown one.
//
// Every accepted scan is one scan_events row with source='macs', written in
// its own transaction together with the tickets.used_at stamp of a first
// admission, its audit row and the v1.ticket.scanned outbox event, so the
// event exists exactly when the scan does. Idempotency is
// (subscriber_id, external_scan_id): a batch sent again answers every item
// as a duplicate and writes nothing.
package hmacsscan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// Request headers of the route.
const (
	HeaderSubscriberID = "X-MACS-Subscriber-Id"
	HeaderSignature    = "X-MACS-Signature"
)

// MaxBatchSize caps the scans of one POST, like the scanner callback. An
// offline scanner with a longer backlog pages its uploads.
const MaxBatchSize = 500

// MaxBodyBytes caps the raw body: 500 scans of the longest allowed fields
// stay well under it.
const MaxBodyBytes = 1 << 20

// TxStarter opens the per-scan transactions.
type TxStarter interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// SubscriberLookup finds the MACS subscriber the batch names.
type SubscriberLookup interface {
	GetMACSSubscriberByID(ctx context.Context, id uuid.UUID) (gen.WebhookSubscriberRow, error)
}

// Limiter is the per-subscriber batch budget. Take records an allowed
// batch only, so a refused one does not push the window further out.
type Limiter interface {
	Take(key string) bool
}

// Handler serves POST /v1/macs/scan-events.
type Handler struct {
	pool        TxStarter
	subscribers SubscriberLookup
	outbox      outbox.Writer
	audit       audit.Writer
	limiter     Limiter
	logger      *slog.Logger
	now         func() time.Time
}

// New builds the handler. pool and subscribers nil make the route answer
// 503; outboxWriter, auditWriter and limiter nil switch that part off (unit
// tests only: production wires all three).
func New(pool TxStarter, subscribers SubscriberLookup, outboxWriter outbox.Writer, auditWriter audit.Writer, limiter Limiter, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		pool:        pool,
		subscribers: subscribers,
		outbox:      outboxWriter,
		audit:       auditWriter,
		limiter:     limiter,
		logger:      logger,
		now:         time.Now,
	}
}

// batchRequest is the POST body. Unknown keys are ignored, so MACS can add
// fields without breaking older arena versions.
type batchRequest struct {
	Scans []ScanInput `json:"scans"`
}

// batchResponse is the 202 body: one result per scan, in request order.
type batchResponse struct {
	Accepted   int          `json:"accepted"`
	Duplicates int          `json:"duplicates"`
	Rejected   int          `json:"rejected"`
	Results    []ScanResult `json:"results"`
}

// HandleScanEvents verifies the subscriber's signature and records the
// batch. Request-level failures answer an error envelope; per-scan problems
// are reported on the scan's own result.
func (h *Handler) HandleScanEvents(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.pool == nil || h.subscribers == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "MACS scan intake is not available", r))
		return
	}
	ctx := r.Context()

	subID, err := uuid.Parse(strings.TrimSpace(r.Header.Get(HeaderSubscriberID)))
	sig := strings.TrimSpace(r.Header.Get(HeaderSignature))
	if err != nil || sig == "" {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"macs.missing_signature", HeaderSubscriberID+" (a uuid) and "+HeaderSignature+" are required", r))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelope(
				"macs.body_too_large", "the body is larger than 1 MiB; page the upload", r))
			return
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"macs.invalid_body", "the body could not be read", r))
		return
	}

	sub, err := h.subscribers.GetMACSSubscriberByID(ctx, subID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		h.refuseSignature(w, r, subID, "unknown subscriber")
		return
	case err != nil:
		h.logger.Error("macs_scan_events: subscriber lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"macs.temporarily_unavailable", "the scans could not be checked now; send the batch again", r))
		return
	}
	// An inactive subscriber, one without an organization or without a
	// secret can verify nothing: refused exactly like a wrong signature, so
	// the answer never tells which subscriber ids exist.
	if !sub.Active || sub.OrgID == nil || sub.SigningSecret == "" || !VerifySignature(sub.SigningSecret, body, sig) {
		h.refuseSignature(w, r, subID, "signature")
		return
	}

	if h.limiter != nil && !h.limiter.Take(sub.ID.String()) {
		w.Header().Set("Retry-After", "60")
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"macs.rate_limited", "too many batches from this subscriber; send the rest in a minute", r))
		return
	}

	var req batchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"macs.invalid_body", "the body must be a JSON object with a scans array", r))
		return
	}
	if len(req.Scans) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"macs.empty_batch", "the scans array must hold at least one scan", r))
		return
	}
	if len(req.Scans) > MaxBatchSize {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelope(
			"macs.batch_too_large", "at most 500 scans per batch; page the upload", r))
		return
	}

	results := h.processBatch(ctx, sub.ID, *sub.OrgID, req.Scans)

	resp := batchResponse{Results: results}
	retryable := 0
	for _, res := range results {
		switch res.Status {
		case StatusAccepted:
			resp.Accepted++
		case StatusDuplicate:
			resp.Duplicates++
		default:
			resp.Rejected++
			if res.Retryable {
				retryable++
			}
		}
	}
	if retryable == len(results) {
		// Nothing could be written (the database is down): the whole batch
		// is sent again, which is safe because every scan is idempotent.
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"macs.temporarily_unavailable", "the scans could not be recorded now; send the batch again", r))
		return
	}
	h.logger.Info("macs_scan_events: batch recorded",
		slog.String("subscriber_id", sub.ID.String()),
		slog.String("org_id", sub.OrgID.String()),
		slog.Int("scans", len(results)),
		slog.Int("accepted", resp.Accepted),
		slog.Int("duplicates", resp.Duplicates),
		slog.Int("rejected", resp.Rejected),
	)
	httputil.WriteJSON(w, http.StatusAccepted, resp)
}

func (h *Handler) refuseSignature(w http.ResponseWriter, r *http.Request, subID uuid.UUID, why string) {
	h.logger.Warn("macs_scan_events: batch refused",
		slog.String("subscriber_id", subID.String()), slog.String("reason", why))
	httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
		"macs.invalid_signature", "the signature does not match an active MACS subscriber", r))
}
