// Package delivery implements the ticket.deliver worker job type (feature #141).
//
// On ticket issuance (payment.succeeded or free checkout), a ticket.deliver
// job is enqueued in worker_jobs with payload:
//
//	{"ticket_id": "<uuid>"}
//
// The handler:
//  1. Decodes the job payload to extract the ticket_id.
//  2. Loads the delivery_jobs row for this ticket.
//  3. Resolves the recipient email: delivery_jobs.recipient_email →
//     ticket.holder_email → skip (no email available).
//  4. Generates a PDF credential for the ticket (lazy-creates via
//     the credential generation logic).
//  5. Renders a transactional HTML email with the PDF as an attachment.
//  6. Sends via the injected email.Sender.
//  7. Updates the delivery_jobs row to status='sent'.
//  8. Emits an audit-log entry (slog.Info).
//
// On a non-nil handler error, the worker retries the job (up to
// max_attempts). After max_attempts the job moves to worker_dead_letter
// and delivery_jobs.status is set to 'failed'.
//
// Retry behaviour on transient SMTP errors is therefore handled entirely
// by the existing worker retry machinery — no extra retry loop in this
// package. Only genuinely terminal errors (no email address, invalid UUID)
// are swallowed here to prevent infinite retries.
package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// JobType is the worker job type string for ticket email delivery.
const JobType = "ticket.deliver"

// Payload is the JSON payload stored in worker_jobs.payload for
// ticket.deliver jobs.
type Payload struct {
	TicketID string `json:"ticket_id"`
}

// HandlerOptions bundles the dependencies required by the delivery handler.
type HandlerOptions struct {
	// TicketQueries provides access to the tickets table.
	TicketQueries *gen.Queries
	// DeliveryJobQueries provides access to the delivery_jobs table.
	DeliveryJobQueries *gen.Queries
	// CredentialQueries provides access to the ticket_credentials table.
	// Used to lazily generate the PDF credential for the ticket.
	CredentialQueries *gen.Queries
	// Sender delivers the transactional email.
	Sender email.Sender
	// FromAddress is the envelope and header From address for outgoing emails.
	// Example: "Arena Platform <tickets@arena.example.com>"
	FromAddress string
	// Logger receives structured log entries. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// NewHandler constructs a worker.HandlerFunc for ticket.deliver jobs.
//
// The returned HandlerFunc is safe for concurrent calls from the worker pool.
// It closes over the provided HandlerOptions — all dependencies must remain
// valid for the lifetime of the worker process.
func NewHandler(opts HandlerOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return func(ctx context.Context, payload []byte) error {
		// ── 1. Parse payload ──────────────────────────────────────────────────
		var p Payload
		if err := json.Unmarshal(payload, &p); err != nil {
			// Malformed payload: do not retry (permanent failure).
			logger.Error("delivery: malformed job payload",
				slog.String("payload", string(payload)),
				slog.String("error", err.Error()),
			)
			return nil // return nil to prevent infinite retries on bad payload
		}

		ticketID, err := uuid.Parse(p.TicketID)
		if err != nil {
			logger.Error("delivery: invalid ticket_id in payload",
				slog.String("ticket_id", p.TicketID),
				slog.String("error", err.Error()),
			)
			return nil // permanent failure — bad UUID
		}

		// ── 2. Load delivery_jobs row ─────────────────────────────────────────
		var deliveryJobID uuid.UUID
		var recipientEmail string

		if opts.DeliveryJobQueries != nil {
			dj, djErr := opts.DeliveryJobQueries.GetDeliveryJobByTicketID(ctx, ticketID)
			if djErr != nil && !errors.Is(djErr, pgx.ErrNoRows) {
				return fmt.Errorf("delivery: get delivery_job for ticket %s: %w",
					ticketID, djErr)
			}
			if djErr == nil {
				deliveryJobID = dj.ID
				if dj.RecipientEmail != nil {
					recipientEmail = *dj.RecipientEmail
				}
			}
		}

		// ── 3. Resolve recipient email ────────────────────────────────────────
		if recipientEmail == "" && opts.TicketQueries != nil {
			ticket, tErr := opts.TicketQueries.GetTicketByID(ctx, ticketID)
			if tErr != nil && !errors.Is(tErr, pgx.ErrNoRows) {
				return fmt.Errorf("delivery: get ticket %s: %w", ticketID, tErr)
			}
			if tErr == nil && ticket.HolderEmail != nil {
				recipientEmail = *ticket.HolderEmail
			}
		}

		if recipientEmail == "" {
			// No email address available — skip delivery gracefully.
			logger.Warn("delivery: no email address for ticket; skipping delivery",
				slog.String("ticket_id", ticketID.String()),
			)
			// Mark the delivery_jobs row as sent (no-op delivery).
			if opts.DeliveryJobQueries != nil && deliveryJobID != uuid.Nil {
				skipReason := "no email address available at delivery time"
				_, _ = opts.DeliveryJobQueries.UpdateDeliveryJobStatus(
					ctx, deliveryJobID, "sent", &skipReason,
				)
			}
			return nil
		}

		// ── 4. Generate PDF credential ────────────────────────────────────────
		var pdfBytes []byte
		if opts.CredentialQueries != nil {
			cred, credErr := opts.CredentialQueries.GetCredentialByTicketID(ctx, ticketID, "pdf")
			if credErr != nil {
				if !errors.Is(credErr, pgx.ErrNoRows) {
					return fmt.Errorf("delivery: get pdf credential for ticket %s: %w",
						ticketID, credErr)
				}
				// Generate and store a new PDF credential.
				pdfPayload := renderMinimalPDF(ticketID.String(), time.Now().UTC())
				encoded := base64.StdEncoding.EncodeToString(pdfPayload)
				cred, credErr = opts.CredentialQueries.InsertTicketCredential(
					ctx, ticketID, "pdf", encoded,
				)
				if credErr != nil {
					return fmt.Errorf("delivery: insert pdf credential for ticket %s: %w",
						ticketID, credErr)
				}
			}
			// Decode base64 payload back to bytes for attachment.
			var decErr error
			pdfBytes, decErr = base64.StdEncoding.DecodeString(cred.Payload)
			if decErr != nil {
				return fmt.Errorf("delivery: decode pdf payload for ticket %s: %w",
					ticketID, decErr)
			}
		}

		// ── 5. Build email ────────────────────────────────────────────────────
		htmlBody := renderTicketEmailHTML(ticketID.String(), recipientEmail)
		textBody := renderTicketEmailText(ticketID.String(), recipientEmail)

		msg := email.Message{
			To:       recipientEmail,
			Subject:  "Your ticket — Arena Platform",
			HTMLBody: htmlBody,
			TextBody: textBody,
		}
		if len(pdfBytes) > 0 {
			msg.Attachments = []email.Attachment{
				{
					Filename:    fmt.Sprintf("ticket-%s.pdf", ticketID.String()[:8]),
					ContentType: "application/pdf",
					Data:        pdfBytes,
				},
			}
		}

		// ── 6. Send ───────────────────────────────────────────────────────────
		if opts.Sender == nil {
			// No sender configured — log only (development mode).
			logger.Warn("delivery: no email sender configured; email not sent",
				slog.String("ticket_id", ticketID.String()),
				slog.String("to", recipientEmail),
			)
		} else {
			if sendErr := opts.Sender.Send(ctx, msg); sendErr != nil {
				// Transient failure — let the worker retry.
				logger.Warn("delivery: send failed; will retry",
					slog.String("ticket_id", ticketID.String()),
					slog.String("to", recipientEmail),
					slog.String("error", sendErr.Error()),
				)
				return fmt.Errorf("delivery: send email to %s: %w", recipientEmail, sendErr)
			}
		}

		// ── 7. Update delivery_jobs status ────────────────────────────────────
		if opts.DeliveryJobQueries != nil && deliveryJobID != uuid.Nil {
			if _, updErr := opts.DeliveryJobQueries.UpdateDeliveryJobStatus(
				ctx, deliveryJobID, "sent", nil,
			); updErr != nil {
				// Non-fatal: the email was sent. Log but don't retry the job
				// over a status update failure.
				logger.Warn("delivery: update delivery_job status failed",
					slog.String("delivery_job_id", deliveryJobID.String()),
					slog.String("error", updErr.Error()),
				)
			}
		}

		// ── 8. Audit log ──────────────────────────────────────────────────────
		logger.Info("delivery: email sent",
			slog.String("ticket_id", ticketID.String()),
			slog.String("to", recipientEmail),
			slog.Int("attachment_bytes", len(pdfBytes)),
		)

		return nil
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Email renderers
// ──────────────────────────────────────────────────────────────────────────────

// renderTicketEmailHTML returns a minimal HTML body for the ticket delivery email.
func renderTicketEmailHTML(ticketID, recipientEmail string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Your Ticket</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px">
  <h1 style="color:#1a1a2e">Your ticket is ready</h1>
  <p>Hello,</p>
  <p>Your ticket has been issued. Please find the PDF attached to this email.</p>
  <p style="font-size:12px;color:#666">Ticket ID: %s</p>
  <p style="font-size:12px;color:#666">Delivered to: %s</p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform — automated delivery</p>
</body>
</html>`, ticketID, recipientEmail)
}

// renderTicketEmailText returns a plain-text fallback body for the ticket delivery email.
func renderTicketEmailText(ticketID, recipientEmail string) string {
	return fmt.Sprintf(
		"Your ticket is ready\n\nHello,\n\n"+
			"Your ticket has been issued. Please find the PDF attached to this email.\n\n"+
			"Ticket ID: %s\n"+
			"Delivered to: %s\n\n"+
			"Arena Platform — automated delivery\n",
		ticketID, recipientEmail,
	)
}

// renderMinimalPDF generates a minimal valid PDF/1.4 document for the ticket.
// This is a lightweight alternative to loading the full credential generation
// code; the worker uses this when CredentialQueries is nil or when generating
// on-the-fly without a DB write.
func renderMinimalPDF(ticketID string, issuedAt time.Time) []byte {
	cs := fmt.Sprintf(
		"BT\n/F1 14 Tf\n72 720 Td\n(Arena Platform Ticket) Tj\n"+
			"/F1 10 Tf\n0 -30 Td\n(Ticket ID: %s) Tj\n"+
			"0 -20 Td\n(Issued: %s) Tj\nET",
		ticketID,
		issuedAt.Format(time.RFC3339),
	)

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	offsets := make([]int, 6)
	offsets[1] = buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	offsets[2] = buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	offsets[3] = buf.Len()
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]\n" +
		"   /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	offsets[4] = buf.Len()
	fmt.Fprintf(&buf, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(cs)+1, cs)
	offsets[5] = buf.Len()
	buf.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	xref := buf.Len()
	buf.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buf.Bytes()
}
