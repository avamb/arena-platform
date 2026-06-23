// gdpr_processor.go implements the background worker logic for GDPR data subject
// requests (feature #164).
//
// The GDPRProcessor is intended to be called from the arena-worker binary on a
// regular schedule (e.g. every 60 seconds). It polls pending data_subject_requests
// rows using FOR UPDATE SKIP LOCKED (PostgreSQL job-queue pattern) and processes
// them atomically.
//
// Processing modes:
//
//	export — collects all user data from the database and marshals it to a JSON
//	         document. The JSON is stored inline as a data URL in payload_url for
//	         the foundation milestone; a later milestone will upload to object storage
//	         and return a pre-signed URL.
//
//	delete — anonymizes the user's PII columns in the users table while retaining
//	         financial records (orders, payments) per Russian accounting law (5-year
//	         mandatory retention). The user_id FK in financial tables is retained so
//	         reconciliation remains possible.
//
// Retention policy matrix (per 10_compliance_security_privacy_ru.md §Privacy):
//
//	Table                  Action on delete request
//	─────────────────────  ────────────────────────────────────────────────────────
//	users                  Anonymize: email → deleted-{uuid}@arena.invalid,
//	                         password_hash → '', anonymized_at → now()
//	email_verification_tokens  CASCADE-deleted when user row is anonymized
//	                            (ON DELETE CASCADE on user_id FK)
//	refresh_tokens         CASCADE-deleted (ON DELETE CASCADE)
//	password_reset_tokens  CASCADE-deleted (ON DELETE CASCADE)
//	memberships            Retained with user_id FK (org membership history)
//	                         — may be purged in a later cleanup pass
//	orders / payments      RETAINED (accounting law — not yet implemented)
//	audit_events           RETAINED (tamper-evident audit trail)
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/jackc/pgx/v5"
)

// GDPRProcessor handles background processing of pending data_subject_requests.
type GDPRProcessor struct {
	pool    PoolDB
	queries *gen.Queries
	logger  *slog.Logger
}

// NewGDPRProcessor constructs a GDPRProcessor.
// pool must be a *pgxpool.Pool (or any PoolDB implementation) for transaction support.
// queries must be constructed from the same pool for read-only queries.
func NewGDPRProcessor(pool PoolDB, queries *gen.Queries, logger *slog.Logger) *GDPRProcessor {
	if logger == nil {
		logger = slog.Default()
	}
	return &GDPRProcessor{
		pool:    pool,
		queries: queries,
		logger:  logger,
	}
}

// ProcessPendingRequests polls up to limit pending data_subject_requests and
// processes each one. Returns the number of requests processed and any fatal
// error that prevented the poll itself (individual processing errors are logged
// but do not abort the batch).
func (p *GDPRProcessor) ProcessPendingRequests(ctx context.Context, limit int32) (int, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("gdpr_processor: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)
	pending, err := q.GetPendingDataSubjectRequests(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("gdpr_processor: poll pending requests: %w", err)
	}

	if len(pending) == 0 {
		// Nothing to do — commit to release the FOR UPDATE lock immediately.
		_ = tx.Commit(ctx)
		return 0, nil
	}

	// Mark all fetched requests as 'processing' so concurrent workers skip them.
	processingStatus := "processing"
	for _, req := range pending {
		if _, err := q.UpdateDataSubjectRequestStatus(ctx, req.ID, processingStatus, nil, nil); err != nil {
			p.logger.Error("gdpr_processor: mark processing failed",
				slog.String("request_id", req.ID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	// Commit the status updates so the lock is released.
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("gdpr_processor: commit processing marks: %w", err)
	}

	// Process each request in its own transaction.
	processed := 0
	for _, req := range pending {
		var procErr error
		switch req.RequestType {
		case "export":
			procErr = p.processExport(ctx, req)
		case "delete":
			procErr = p.processDelete(ctx, req)
		default:
			procErr = fmt.Errorf("unknown request_type: %s", req.RequestType)
		}

		if procErr != nil {
			p.logger.Error("gdpr_processor: processing failed",
				slog.String("request_id", req.ID.String()),
				slog.String("request_type", req.RequestType),
				slog.String("user_id", req.UserID.String()),
				slog.String("error", procErr.Error()),
			)
			// Mark as failed with the error message.
			failedStatus := "failed"
			errMsg := procErr.Error()
			if _, updateErr := p.queries.UpdateDataSubjectRequestStatus(ctx, req.ID, failedStatus, nil, &errMsg); updateErr != nil {
				p.logger.Error("gdpr_processor: mark failed update failed",
					slog.String("request_id", req.ID.String()),
					slog.String("error", updateErr.Error()),
				)
			}
			continue
		}
		processed++
	}

	return processed, nil
}

// processExport generates a JSON dump of all user data and stores the result
// in data_subject_requests.payload_url. For the foundation milestone, the dump
// is stored inline as a JSON string. A later milestone will upload to object
// storage (S3/MinIO) and return a pre-signed URL.
func (p *GDPRProcessor) processExport(ctx context.Context, req gen.DataSubjectRequestRow) error {
	// Fetch user data (no password_hash).
	userData, err := p.queries.GetUserExportData(ctx, req.UserID)
	if err != nil {
		return fmt.Errorf("processExport: get user data: %w", err)
	}

	// Fetch the user's memberships.
	memberships, err := p.queries.GetActiveRolesForUser(ctx, req.UserID)
	if err != nil {
		// Non-fatal: log and continue with empty membership list.
		p.logger.Warn("processExport: get memberships failed",
			slog.String("user_id", req.UserID.String()),
			slog.String("error", err.Error()),
		)
		memberships = []string{}
	}

	// Fetch previous GDPR requests (for completeness of the export).
	previousRequests, err := p.queries.ListDataSubjectRequestsByUser(ctx, req.UserID)
	if err != nil {
		previousRequests = []gen.DataSubjectRequestRow{}
	}

	type exportRequest struct {
		ID          string  `json:"id"`
		RequestType string  `json:"request_type"`
		Status      string  `json:"status"`
		CreatedAt   string  `json:"created_at"`
	}
	var exportReqs []exportRequest
	for _, r := range previousRequests {
		exportReqs = append(exportReqs, exportRequest{
			ID:          r.ID.String(),
			RequestType: r.RequestType,
			Status:      r.Status,
			CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	// Build the export document.
	exportDoc := map[string]any{
		"export_generated_at": time.Now().UTC().Format(time.RFC3339),
		"request_id":          req.ID.String(),
		"user": map[string]any{
			"id":                userData.ID.String(),
			"email":             userData.Email,
			"preferred_locale":  userData.PreferredLocale,
			"created_at":        userData.CreatedAt.UTC().Format(time.RFC3339),
			"email_verified_at": formatTimePtr(userData.EmailVerifiedAt),
			"consent_given_at":  formatTimePtr(userData.ConsentGivenAt),
			"marketing_consent": userData.MarketingConsent,
		},
		"roles":                    memberships,
		"data_subject_requests":    exportReqs,
	}

	exportJSON, err := json.Marshal(exportDoc)
	if err != nil {
		return fmt.Errorf("processExport: marshal export document: %w", err)
	}

	// Store the JSON inline as the payload_url for the foundation milestone.
	// Format: "inline:<JSON>" — a later milestone replaces this with an S3 URL.
	payloadURL := "inline:" + string(exportJSON)
	completedStatus := "completed"
	if _, err := p.queries.UpdateDataSubjectRequestStatus(ctx, req.ID, completedStatus, &payloadURL, nil); err != nil {
		return fmt.Errorf("processExport: update status to completed: %w", err)
	}

	p.logger.Info("gdpr_processor: export completed",
		slog.String("request_id", req.ID.String()),
		slog.String("user_id", req.UserID.String()),
		slog.Int("export_bytes", len(exportJSON)),
	)
	return nil
}

// processDelete anonymizes the user's PII. Financial records referencing the
// user_id are retained per Russian accounting law (mandatory 5-year retention
// for financial documents per Federal Law No. 402-FZ "On Accounting").
func (p *GDPRProcessor) processDelete(ctx context.Context, req gen.DataSubjectRequestRow) error {
	// AnonymizeUser replaces PII with placeholder values and sets anonymized_at.
	if err := p.queries.AnonymizeUser(ctx, req.UserID); err != nil {
		return fmt.Errorf("processDelete: anonymize user: %w", err)
	}

	// Mark the request as completed.
	completedStatus := "completed"
	if _, err := p.queries.UpdateDataSubjectRequestStatus(ctx, req.ID, completedStatus, nil, nil); err != nil {
		return fmt.Errorf("processDelete: update status to completed: %w", err)
	}

	p.logger.Info("gdpr_processor: deletion completed (PII anonymized)",
		slog.String("request_id", req.ID.String()),
		slog.String("user_id", req.UserID.String()),
	)
	return nil
}

// formatTimePtr formats a *time.Time as RFC3339 or returns nil for JSON serialization.
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
