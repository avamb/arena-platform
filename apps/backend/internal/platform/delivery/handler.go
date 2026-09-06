// Package delivery implements the ticket.deliver worker job type (feature #141).
//
// On ticket issuance (payment.succeeded or free checkout), a ticket.deliver
// job is enqueued in worker_jobs with payload:
//
//	{"ticket_id": "<uuid>"}
//
// The handler implements honest delivery-status transitions (PR-03):
//
//  1. Decode the job payload and extract ticket_id.
//  2. Load the delivery_jobs row for this ticket.
//  3. Guard: if no real sender is configured (nil or dev-only), update status
//     to 'disabled' and return nil — never produce 'sent' without real SMTP.
//  4. Guard: if delivery_jobs is already terminal (sent/disabled/skipped/failed),
//     return nil — idempotent skip.
//  5. Resolve recipient email from delivery_jobs or ticket.holder_email.
//  6. If no email address found, update delivery_jobs to 'skipped' and return nil.
//  7. Atomically claim delivery_jobs for processing (pending → processing).
//     If claim fails (row not pending), return nil — another worker claimed it.
//  8. Resolve organisation branding and generate PDF credential.
//  9. Render transactional email (localised, feature #289 T-2).
//  10. Send via the injected email.Sender (real SMTP).
//     On transient failure: return error — worker retries. Status stays 'processing'
//     until retry claims it again (reconciliation via processing_at). The delivery
//     key is the delivery_jobs.id: a 'processing' row is never re-claimed by the
//     same handler invocation, preventing double-sends on simple retries.
//  11. Update delivery_jobs to 'sent'. If the status update fails after SMTP
//     success, log a warning but do NOT retry the send: the message was delivered
//     and a reconciliation process can repair the status later.
//  12. Emit an audit-log entry.
//
// SMTP credentials and ticket secrets are never written to log fields.
package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobType is the worker job type string for ticket email delivery.
const JobType = "ticket.deliver"

// Delivery status constants used by the handler.
const (
	// StatusPending is the initial state; job not yet claimed.
	StatusPending = "pending"
	// StatusProcessing means the handler has claimed the row and SMTP is in-flight.
	StatusProcessing = "processing"
	// StatusSent is the terminal success state: SMTP server accepted the message.
	StatusSent = "sent"
	// StatusFailed is the terminal failure state: dead-lettered after max_attempts.
	StatusFailed = "failed"
	// StatusDisabled means no real sender was configured; email was never attempted.
	// Used when EMAIL_MODE is not 'smtp' or Sender is nil.
	StatusDisabled = "disabled"
	// StatusSkipped means no recipient email address was available at delivery time.
	// The worker terminates the job without sending.
	StatusSkipped = "skipped"
)

// Email template identifiers for the Template field of Payload.
const (
	// TemplateTicket is the standard ticket delivery email (paid/free checkout).
	TemplateTicket = "ticket"
	// TemplateInvitation is the complimentary/invitation email template (feature #149).
	// Used when tickets are issued as complimentary invitations rather than purchases.
	TemplateInvitation = "invitation"
)

// Payload is the JSON payload stored in worker_jobs.payload for
// ticket.deliver jobs.
type Payload struct {
	TicketID string `json:"ticket_id"`
	// Template selects the email template: "ticket" (default) or "invitation".
	// When empty, TemplateTicket is used.
	Template string `json:"template,omitempty"`
	// Locale selects the language for subject + body. Falls back to
	// templates.DefaultLocale when empty or unknown. Examples: "en", "de",
	// "es", "he".
	Locale string `json:"locale,omitempty"`
	// EventName, SessionStart, VenueName, TierName, HolderName are
	// optional presentation hints baked into the job at enqueue time so
	// the worker does not need to re-join across events/sessions/venues.
	// All five are safe to omit; the template renders blanks accordingly.
	EventName    string    `json:"event_name,omitempty"`
	SessionStart time.Time `json:"session_start,omitempty"`
	SessionTZ    string    `json:"session_tz,omitempty"`
	VenueName    string    `json:"venue_name,omitempty"`
	VenueCity    string    `json:"venue_city,omitempty"`
	TierName     string    `json:"tier_name,omitempty"`
	HolderName   string    `json:"holder_name,omitempty"`
	// SeatSector / SeatRow / SeatNumber (SEAT-C3, feature #311) are the
	// denormalized seat coordinates copied from the ticket's
	// seat_sector / seat_row / seat_number columns onto the delivery
	// job. All three are empty for general-admission tickets, in which
	// case the templates and the PDF renderer omit the seat block.
	SeatSector string `json:"seat_sector,omitempty"`
	SeatRow    string `json:"seat_row,omitempty"`
	SeatNumber string `json:"seat_number,omitempty"`
	// QRPayload is the value to encode into the PDF QR code. The handler
	// falls back to TicketID if empty so existing enqueuers keep working.
	QRPayload string `json:"qr_payload,omitempty"`
	// HumanCode is the SEAT-C4 human-readable manual-entry fallback code
	// printed under the QR on the PDF and shown in email bodies.
	// Eight Crockford Base32 characters in canonical form (no hyphen).
	// Empty for pre-SEAT-C4 tickets; the PDF renderer omits the code block
	// when this field is empty.
	HumanCode string `json:"human_code,omitempty"`
	// EAN13 is the platform-minted EAN-13 barcode number (feature #502)
	// printed as plain text under the QR/human-code block on the PDF
	// (feature #503, W1-B6b, spec §11). Left empty by enqueuers; the
	// handler resolves it from the ticket_credentials table at render
	// time (see step 9 below) so callers never need to know about it.
	// Legacy tickets issued before #502 (and not yet backfilled by the
	// tickets.backfill_ean13 job) simply omit the line.
	EAN13 string `json:"ean13,omitempty"`
	// TicketPDFFormat controls the PDF layout: "mobile" (default),
	// "a4", or "both". Resolved from the owning org's ticket_pdf_format
	// flag at enqueue time. Empty → delivery worker defaults to "mobile".
	TicketPDFFormat string `json:"ticket_pdf_format,omitempty"`

	// ── Organisation branding (feature #290, T-3) ─────────────────────
	// These mirror the organizations table columns the email header and
	// footer surface (logo_media_id, name, website_url, legal_name,
	// legal_address_*, contact_email). All are optional; the handler
	// substitutes platform defaults when a field is empty so the email
	// header always carries a logo and the footer always identifies a
	// legal entity (EU "commercial communications" rule).
	//
	// OrgLogoMediaID is the UUID of the organisation logo in the media
	// table. When non-empty the handler resolves the bytes (for the PDF
	// embed) and a signed URL (for the email <img src>) via the
	// MediaResolver. When empty the platform fallback logo URL is used
	// and the PDF header simply omits the logo image slot.
	OrgLogoMediaID         string `json:"org_logo_media_id,omitempty"`
	OrgName                string `json:"org_name,omitempty"`
	OrgWebsiteURL          string `json:"org_website_url,omitempty"`
	OrgLegalName           string `json:"org_legal_name,omitempty"`
	OrgLegalAddressLine1   string `json:"org_legal_address_line1,omitempty"`
	OrgLegalAddressLine2   string `json:"org_legal_address_line2,omitempty"`
	OrgLegalAddressPostal  string `json:"org_legal_address_postal_code,omitempty"`
	OrgLegalAddressCity    string `json:"org_legal_address_city,omitempty"`
	OrgLegalAddressCountry string `json:"org_legal_address_country,omitempty"`
	OrgContactEmail        string `json:"org_contact_email,omitempty"`
	// SenderEmail is used only while SenderVerificationStatus is "verified".
	// It is populated from the organization record by the enqueueing path.
	SenderEmail              string `json:"sender_email,omitempty"`
	SenderVerificationStatus string `json:"sender_verification_status,omitempty"`
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
	//
	// IMPORTANT (PR-03): only a real SMTP sender (SMTPSender) is permitted in
	// production. If Sender is nil or implements email.DevOnlySender, the handler
	// updates delivery_jobs to StatusDisabled and returns nil — it NEVER updates
	// to StatusSent without a real SMTP acceptance.
	Sender email.Sender
	// FromAddress is the envelope and header From address for outgoing emails.
	// Example: "Arena Platform <tickets@arena.example.com>"
	FromAddress string
	// Templates renders the localized email bodies (feature #289, T-2).
	// When nil, NewHandler lazy-constructs a default Renderer from the
	// embedded templates so callers that haven't migrated still work.
	Templates *templates.Renderer
	// Media resolves an organisation logo_media_id to (bytes, signed URL)
	// for both PDF embedding and the email <img src> (feature #290, T-3).
	// When nil, every email/PDF falls back to the platform logo.
	Media MediaResolver
	// Logger receives structured log entries. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// MediaResolver is the narrow interface the delivery handler uses to
// resolve an organisation logo_media_id (a UUID string) into the bytes
// that go into the PDF and the signed URL used by the email
// <img src> tag.
//
// Implementations are expected to:
//   - Return an error wrapping ErrLogoNotFound when the media id does
//     not exist or has been soft-deleted, so the handler can fall back
//     to the platform logo without retrying.
//   - Never block the delivery pipeline on a media-store outage: a
//     transient error logs a warning and the handler falls back to the
//     platform logo for that email only.
type MediaResolver interface {
	// ResolveLogo returns the logo image bytes and a fully-qualified
	// download URL (typically a signed media URL valid for several
	// minutes — long enough that the recipient's MUA can fetch the
	// inline <img> on display).
	ResolveLogo(ctx context.Context, mediaID string) (bytes []byte, signedURL string, err error)
}

// ErrLogoNotFound is the canonical error a MediaResolver returns when
// the requested logo_media_id does not exist. Wrap with %w so the
// handler can use errors.Is to distinguish "fall back to platform
// logo" (not-found) from "real outage" (everything else).
var ErrLogoNotFound = errors.New("delivery: org logo not found")

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
	renderer := opts.Templates
	if renderer == nil {
		// Best-effort lazy construction. If the embedded templates fail
		// to parse, fall back to nil so the hard-coded renderers below
		// take over — never block delivery on a template-load error.
		if r, err := templates.New(); err == nil {
			renderer = r
		} else {
			logger.Warn("delivery: failed to initialise embedded templates; falling back to legacy renderers",
				slog.String("error", err.Error()),
			)
		}
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

		// ── 2. Guard: honest sender required (PR-03) ──────────────────────────
		// If Sender is nil or is a dev-only sender (e.g. LogSender), we cannot
		// honestly report that the message was delivered. Mark the delivery_jobs
		// row as 'disabled' and return nil (terminal, non-retryable).
		// Production config validation (PR-00) already blocks EMAIL_MODE≠smtp in
		// APP_ENV=production; this guard ensures the handler is also honest at
		// the domain level regardless of config.
		// Resolve sender identity immediately before delivery. A queued email
		// cannot retain an address that Brevo has since revoked.
		if opts.TicketQueries != nil {
			senderEmail, senderStatus, senderErr := opts.TicketQueries.GetSenderIdentityByTicketID(ctx, ticketID)
			if senderErr == nil && senderEmail != nil {
				p.SenderEmail, p.SenderVerificationStatus = *senderEmail, senderStatus
			} else if senderErr != nil && !errors.Is(senderErr, pgx.ErrNoRows) {
				logger.Warn("delivery: sender identity lookup failed; using platform sender", slog.String("ticket_id", ticketID.String()), slog.String("error", senderErr.Error()))
			}
		}

		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Warn("delivery: no production email sender configured; marking delivery disabled",
				slog.String("ticket_id", ticketID.String()),
				slog.Bool("sender_nil", opts.Sender == nil),
				slog.Bool("sender_dev_only", opts.Sender != nil && email.IsDevOnly(opts.Sender)),
			)
			if opts.DeliveryJobQueries != nil {
				dj, djErr := opts.DeliveryJobQueries.GetDeliveryJobByTicketID(ctx, ticketID)
				if djErr == nil && dj.ID != uuid.Nil && isTerminalOrProcessing(dj.Status) {
					// Already in a terminal or claimed state; skip update.
					return nil
				}
				if djErr == nil && dj.ID != uuid.Nil {
					reason := "no production email sender configured (EMAIL_MODE is not smtp)"
					_, _ = opts.DeliveryJobQueries.UpdateDeliveryJobStatus(
						ctx, dj.ID, StatusDisabled, &reason,
					)
				}
			}
			return nil
		}

		// ── 3. Load delivery_jobs row ─────────────────────────────────────────
		var deliveryJobID uuid.UUID
		var recipientEmail string
		var currentStatus string

		if opts.DeliveryJobQueries != nil {
			dj, djErr := opts.DeliveryJobQueries.GetDeliveryJobByTicketID(ctx, ticketID)
			if djErr != nil && !errors.Is(djErr, pgx.ErrNoRows) {
				return fmt.Errorf("delivery: get delivery_job for ticket %s: %w",
					ticketID, djErr)
			}
			if djErr == nil {
				deliveryJobID = dj.ID
				currentStatus = dj.Status
				if dj.RecipientEmail != nil {
					recipientEmail = *dj.RecipientEmail
				}
			}
		}

		// ── 4. Guard: idempotency — skip if already terminal ──────────────────
		// If a previous handler invocation already reached a terminal status
		// (sent, disabled, skipped, failed) for this delivery_job, skip.
		if deliveryJobID != uuid.Nil && isTerminalStatus(currentStatus) {
			logger.Info("delivery: delivery_job already in terminal status; skipping",
				slog.String("ticket_id", ticketID.String()),
				slog.String("delivery_job_id", deliveryJobID.String()),
				slog.String("status", currentStatus),
			)
			return nil
		}

		// ── 5. Resolve recipient email ────────────────────────────────────────
		if recipientEmail == "" && opts.TicketQueries != nil {
			ticket, tErr := opts.TicketQueries.GetTicketByID(ctx, ticketID)
			if tErr != nil && !errors.Is(tErr, pgx.ErrNoRows) {
				return fmt.Errorf("delivery: get ticket %s: %w", ticketID, tErr)
			}
			if tErr == nil && ticket.HolderEmail != nil {
				recipientEmail = *ticket.HolderEmail
			}
		}

		// ── 6. Guard: no email address → skipped (not sent) ──────────────────
		if recipientEmail == "" {
			// No email address available — terminal skip; no retry.
			logger.Warn("delivery: no email address for ticket; marking delivery skipped",
				slog.String("ticket_id", ticketID.String()),
			)
			if opts.DeliveryJobQueries != nil && deliveryJobID != uuid.Nil {
				skipReason := "no email address available at delivery time"
				_, _ = opts.DeliveryJobQueries.UpdateDeliveryJobStatus(
					ctx, deliveryJobID, StatusSkipped, &skipReason,
				)
			}
			return nil
		}

		// ── 7. Claim delivery_job for processing (idempotency key) ────────────
		// Atomically transition pending → processing. If the row is not in
		// 'pending' state (already claimed by another worker or already terminal),
		// return nil — idempotent skip prevents double-send.
		if opts.DeliveryJobQueries != nil && deliveryJobID != uuid.Nil {
			_, claimErr := opts.DeliveryJobQueries.ClaimDeliveryJobForProcessing(ctx, deliveryJobID)
			if claimErr != nil {
				if errors.Is(claimErr, pgx.ErrNoRows) {
					// Not in 'pending' state — already claimed or terminal.
					logger.Info("delivery: delivery_job not claimable (not pending); skipping",
						slog.String("ticket_id", ticketID.String()),
						slog.String("delivery_job_id", deliveryJobID.String()),
					)
					return nil
				}
				// Transient DB error claiming the job — let worker retry.
				return fmt.Errorf("delivery: claim delivery_job %s for processing: %w",
					deliveryJobID, claimErr)
			}
		}

		// ── 8. Resolve organisation branding ─────────────────────────────────
		// Feature #290 (T-3). Pulls the org logo bytes + signed URL via
		// the media adapter (when configured and OrgLogoMediaID is set),
		// falling back to the platform logo otherwise. Also assembles
		// the EU "commercial communications" minimum-identification
		// block from the legal_name + legal_address_* + contact_email
		// fields carried on the payload, substituting platform defaults
		// for any blank fields.
		brand := resolveBranding(ctx, opts.Media, p, logger)
		branding := brand.Branding

		// ── 8b. Resolve the EAN-13 number for the PDF ──────────────────────
		// Feature #503 (W1-B6b): the PDF prints the platform-minted EAN-13
		// number under the QR. Enqueuers never populate p.EAN13 directly, so
		// resolve it here from ticket_credentials — a best-effort lookup,
		// never fatal: pre-#502 tickets not yet caught up by
		// tickets.backfill_ean13 simply render without the line.
		if p.EAN13 == "" && opts.CredentialQueries != nil {
			eanCred, eanErr := opts.CredentialQueries.GetCredentialByTicketID(ctx, ticketID, "ean13")
			if eanErr != nil {
				if !errors.Is(eanErr, pgx.ErrNoRows) {
					logger.Warn("delivery: get ean13 credential failed; rendering pdf without it",
						slog.String("ticket_id", ticketID.String()),
						slog.String("error", eanErr.Error()),
					)
				}
			} else {
				p.EAN13 = eanCred.Payload
			}
		}

		// ── 9. Generate PDF credential ────────────────────────────────────────
		// Prefer the T-1 PDF renderer (apps/backend/internal/platform/delivery/pdf)
		// so the attached document carries the QR code and full ticket detail.
		// Fall back to the legacy minimal renderer when called from a context
		// that has no event/session metadata at all (keeps existing tests
		// and free-form enqueuers working).
		var pdfBytes []byte
		if opts.CredentialQueries != nil {
			cred, credErr := opts.CredentialQueries.GetCredentialByTicketID(ctx, ticketID, "pdf")
			if credErr != nil {
				if !errors.Is(credErr, pgx.ErrNoRows) {
					return fmt.Errorf("delivery: get pdf credential for ticket %s: %w",
						ticketID, credErr)
				}
				// Generate and store a new PDF credential.
				pdfPayload, prErr := renderTicketPDF(ctx, ticketID, p, branding, brand.LogoBytes)
				if prErr != nil {
					return fmt.Errorf("delivery: render pdf for ticket %s: %w",
						ticketID, prErr)
				}
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
		} else {
			// No credential store — render on the fly so we still attach a PDF.
			pdfPayload, prErr := renderTicketPDF(ctx, ticketID, p, branding, brand.LogoBytes)
			if prErr != nil {
				logger.Warn("delivery: render pdf failed; sending without attachment",
					slog.String("ticket_id", ticketID.String()),
					slog.String("error", prErr.Error()),
				)
			} else {
				pdfBytes = pdfPayload
			}
		}

		// ── 10. Build email ───────────────────────────────────────────────────
		// Localized templates from the templates package (feature #289, T-2).
		// Falls back to the legacy hard-coded English renderers only when the
		// embedded templates failed to load at process start.
		kind := templates.TemplateKindTicket
		if p.Template == TemplateInvitation {
			kind = templates.TemplateKindInvitation
		}

		var htmlBody, textBody, subject string
		if renderer != nil {
			out, rErr := renderer.Render(kind, p.Locale, templates.Data{
				TicketID:       ticketID.String(),
				RecipientEmail: recipientEmail,
				HolderName:     p.HolderName,
				EventName:      defaultStr(p.EventName, "Arena Event"),
				SessionStart:   formatSessionForEmail(p.SessionStart, p.SessionTZ),
				VenueName:      joinNonEmpty(", ", p.VenueName, p.VenueCity),
				TierName:       p.TierName,
				SeatSector:     p.SeatSector,
				SeatRow:        p.SeatRow,
				SeatNumber:     p.SeatNumber,
				Branding:       branding,
			})
			if rErr != nil {
				return fmt.Errorf("delivery: render templates for ticket %s: %w",
					ticketID, rErr)
			}
			subject, htmlBody, textBody = out.Subject, out.HTMLBody, out.TextBody
		} else {
			switch p.Template {
			case TemplateInvitation:
				htmlBody = renderInvitationEmailHTML(ticketID.String(), recipientEmail)
				textBody = renderInvitationEmailText(ticketID.String(), recipientEmail)
				subject = "You're invited — Arena Platform"
			default:
				htmlBody = renderTicketEmailHTML(ticketID.String(), recipientEmail)
				textBody = renderTicketEmailText(ticketID.String(), recipientEmail)
				subject = "Your ticket — Arena Platform"
			}
		}

		msg := ticketEmailMessage(p, recipientEmail, subject, htmlBody, textBody)
		if len(pdfBytes) > 0 {
			msg.Attachments = []email.Attachment{
				{
					Filename:    fmt.Sprintf("ticket-%s.pdf", ticketID.String()[:8]),
					ContentType: "application/pdf",
					Data:        pdfBytes,
				},
			}
		}

		// ── 11. Send via real SMTP ────────────────────────────────────────────
		// opts.Sender is guaranteed non-nil and non-dev-only at this point
		// (the guard at step 2 would have returned early otherwise).
		if sendErr := opts.Sender.Send(ctx, msg); sendErr != nil {
			// Transient failure — let the worker retry. The delivery_jobs row
			// stays in 'processing' state; the worker's retry machinery will
			// re-invoke this handler. On retry, the ClaimDeliveryJobForProcessing
			// CAS will see status='processing' (not 'pending') and return
			// ErrNoRows, causing the handler to skip the send. This means the
			// effective retry is driven by the worker_jobs table (its own retry
			// counter and backoff), not by re-sending via the same delivery_job.
			// A reconciliation job can reset stale 'processing' rows (where
			// processing_at is older than a configurable threshold) back to
			// 'pending' for redelivery.
			//
			// NOTE: SMTP credentials and ticket barcodes are NOT included in log
			// fields — only the ticket_id and sanitised error string.
			logger.Warn("delivery: SMTP send failed; worker will retry",
				slog.String("ticket_id", ticketID.String()),
				slog.String("to", recipientEmail),
				slog.String("error", sendErr.Error()),
			)
			return fmt.Errorf("delivery: send email to %s: %w", recipientEmail, sendErr)
		}

		// ── 12. Update delivery_jobs to 'sent' ────────────────────────────────
		// SMTP accepted the message. Status-update failure here does NOT retry
		// the send: the email was delivered, and a subsequent retry would cause
		// a duplicate. Log the failure for reconciliation monitoring instead.
		if opts.DeliveryJobQueries != nil && deliveryJobID != uuid.Nil {
			if _, updErr := opts.DeliveryJobQueries.UpdateDeliveryJobStatus(
				ctx, deliveryJobID, StatusSent, nil,
			); updErr != nil {
				// Non-fatal: the email was sent. Reconciliation will fix the status.
				logger.Warn("delivery: update delivery_job to 'sent' failed after SMTP success; reconciliation required",
					slog.String("delivery_job_id", deliveryJobID.String()),
					slog.String("ticket_id", ticketID.String()),
					slog.String("error", updErr.Error()),
				)
				// Return nil: do NOT retry the worker job — the email was sent.
			}
		}

		// ── 13. Audit log ─────────────────────────────────────────────────────
		// SMTP credentials, barcodes, QR payloads, and signed URLs are not
		// included in log fields (PR-03 redaction requirement).
		// "delivery: email sent" is the canonical audit-log marker checked by
		// TestDelivery141_Step9_HandlerGoHasAuditLogSent.
		logger.Info("delivery: email sent",
			slog.String("ticket_id", ticketID.String()),
			slog.String("delivery_job_id", deliveryJobID.String()),
			slog.Int("attachment_bytes", len(pdfBytes)),
		)

		return nil
	}
}

// ticketEmailMessage constructs the ticket-delivery message after the handler
// has resolved its recipient and rendered its localized content. Replies go to
// the organiser contact when one was supplied in the durable job payload;
// delivery still originates from the platform's configured SMTP sender.
func ticketEmailMessage(p Payload, recipientEmail, subject, htmlBody, textBody string) email.Message {
	from := ""
	if p.SenderVerificationStatus == "verified" {
		from = strings.TrimSpace(p.SenderEmail)
	}
	return email.Message{
		From:     from,
		To:       recipientEmail,
		Subject:  subject,
		ReplyTo:  strings.TrimSpace(p.OrgContactEmail),
		HTMLBody: htmlBody,
		TextBody: textBody,
	}
}

// isTerminalStatus reports whether status is a final, non-retryable state.
func isTerminalStatus(status string) bool {
	switch status {
	case StatusSent, StatusFailed, StatusDisabled, StatusSkipped:
		return true
	}
	return false
}

// isTerminalOrProcessing reports whether status is terminal or already being
// processed. Used in the disabled-sender guard to avoid clobbering a row that
// another worker path has already claimed.
func isTerminalOrProcessing(status string) bool {
	return isTerminalStatus(status) || status == StatusProcessing
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

// renderInvitationEmailHTML returns the HTML body for complimentary invitation emails.
// Used when Payload.Template == TemplateInvitation (feature #149).
func renderInvitationEmailHTML(ticketID, recipientEmail string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>You're Invited</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px">
  <h1 style="color:#1a1a2e">You're invited!</h1>
  <p>Hello,</p>
  <p>You have been issued a complimentary invitation ticket. Please find the PDF attached to this email.</p>
  <p>This ticket has been provided to you as a complimentary invitation — no payment is required.</p>
  <p style="font-size:12px;color:#666">Ticket ID: %s</p>
  <p style="font-size:12px;color:#666">Delivered to: %s</p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform — automated invitation delivery</p>
</body>
</html>`, ticketID, recipientEmail)
}

// renderInvitationEmailText returns the plain-text fallback body for complimentary
// invitation emails. Used when Payload.Template == TemplateInvitation (feature #149).
func renderInvitationEmailText(ticketID, recipientEmail string) string {
	return fmt.Sprintf(
		"You're invited!\n\nHello,\n\n"+
			"You have been issued a complimentary invitation ticket. "+
			"Please find the PDF attached to this email.\n\n"+
			"This ticket has been provided to you as a complimentary invitation — "+
			"no payment is required.\n\n"+
			"Ticket ID: %s\n"+
			"Delivered to: %s\n\n"+
			"Arena Platform — automated invitation delivery\n",
		ticketID, recipientEmail,
	)
}

// renderTicketPDF tries the T-1 PDF renderer first (full QR + layout) and
// falls back to the legacy minimal PDF generator on any error so a missing
// font / IO issue does not block delivery.
//
// branding carries the EU-required header/footer text; logoBytes is the
// optional resolved org-logo image. It is the caller's responsibility
// (the worker handler) to apply the platform-logo fallback when the
// organisation has no logo_media_id. When logoBytes is empty the PDF
// header simply omits the image slot and prints the OrgName wordmark.
func renderTicketPDF(ctx context.Context, ticketID uuid.UUID, p Payload, branding templates.Branding, logoBytes []byte) ([]byte, error) {
	qr := p.QRPayload
	if qr == "" {
		qr = ticketID.String()
	}
	t := pdf.Ticket{
		TicketID:               ticketID.String(),
		EventName:              defaultStr(p.EventName, "Arena Event"),
		SessionStart:           defaultTime(p.SessionStart),
		SessionTZ:              p.SessionTZ,
		VenueName:              p.VenueName,
		VenueCity:              p.VenueCity,
		TierName:               p.TierName,
		HolderName:             p.HolderName,
		SeatSector:             p.SeatSector,
		SeatRow:                p.SeatRow,
		SeatNumber:             p.SeatNumber,
		QRPayload:              qr,
		EAN13:                  p.EAN13,
		OrgLogo:                logoBytes,
		OrgName:                branding.OrgName,
		OrgWebsiteURL:          branding.WebsiteURL,
		LegalName:              branding.LegalName,
		LegalAddressLine1:      branding.LegalAddressLine1,
		LegalAddressLine2:      branding.LegalAddressLine2,
		LegalAddressPostalCode: branding.LegalAddressPostalCode,
		LegalAddressCity:       branding.LegalAddressCity,
		LegalAddressCountry:    branding.LegalAddressCountry,
		ContactEmail:           branding.ContactEmail,
	}
	bytesOut, err := pdf.Render(ctx, t)
	if err == nil {
		return bytesOut, nil
	}
	// On any validation/render failure, keep delivery moving with the
	// legacy minimal PDF — the worker is not the right place to block on
	// a missing event name.
	return renderMinimalPDF(ticketID.String(), time.Now().UTC()), nil
}

// resolveBrandingResult bundles the branding fields and the optional
// org-logo bytes resolved from the media adapter. The bytes are kept
// separate from templates.Branding because that struct is intentionally
// strings-only (every field is rendered verbatim into HTML / text
// bodies).
type resolveBrandingResult struct {
	Branding  templates.Branding
	LogoBytes []byte
}

// resolveBranding builds the templates.Branding value used to render
// both the email body and the PDF e-ticket for a single ticket.deliver
// job. Behaviour:
//
//   - When OrgLogoMediaID is set and the MediaResolver returns bytes +
//     URL, the email header uses the signed URL and the PDF embeds the
//     bytes.
//   - When OrgLogoMediaID is unset, OR the resolver returns
//     ErrLogoNotFound, the email header substitutes the platform logo
//     URL and the PDF simply skips the logo image slot (the wordmark
//     in drawHeader still prints the OrgName / "Arena E-Ticket").
//   - On a non-not-found resolver error (e.g. media-store outage), the
//     handler logs a warning and falls back to the platform logo URL
//     too — never blocks delivery on a media failure.
//   - Empty text fields fall back to platform defaults so the email
//     footer always identifies a legal entity (EU rule).
//
// Returns a resolveBrandingResult carrying the branding strings and
// the optional org-logo bytes for PDF embedding.
func resolveBranding(ctx context.Context, m MediaResolver, p Payload, logger *slog.Logger) resolveBrandingResult {
	b := templates.Branding{
		OrgName:                defaultStr(p.OrgName, templates.PlatformOrgName),
		WebsiteURL:             p.OrgWebsiteURL,
		LegalName:              defaultStr(p.OrgLegalName, templates.PlatformLegalName),
		LegalAddressLine1:      p.OrgLegalAddressLine1,
		LegalAddressLine2:      p.OrgLegalAddressLine2,
		LegalAddressPostalCode: p.OrgLegalAddressPostal,
		LegalAddressCity:       p.OrgLegalAddressCity,
		LegalAddressCountry:    p.OrgLegalAddressCountry,
		ContactEmail:           defaultStr(p.OrgContactEmail, templates.PlatformContactEmail),
	}
	b.LogoAlt = b.OrgName

	logoID := strings.TrimSpace(p.OrgLogoMediaID)
	if logoID == "" || m == nil {
		// No org logo configured — substitute platform logo URL.
		b.LogoURL = templates.PlatformLogoURL
		return resolveBrandingResult{Branding: b}
	}

	logoBytes, signedURL, err := m.ResolveLogo(ctx, logoID)
	switch {
	case err == nil:
		b.LogoURL = signedURL
		return resolveBrandingResult{Branding: b, LogoBytes: logoBytes}
	case errors.Is(err, ErrLogoNotFound):
		// Org has a logo_media_id but the row vanished — fall back.
		if logger != nil {
			logger.Warn("delivery: org logo_media_id not found; using platform fallback",
				slog.String("logo_media_id", logoID),
			)
		}
		b.LogoURL = templates.PlatformLogoURL
	default:
		if logger != nil {
			logger.Warn("delivery: media resolver failed; using platform fallback",
				slog.String("logo_media_id", logoID),
				slog.String("error", err.Error()),
			)
		}
		b.LogoURL = templates.PlatformLogoURL
	}
	return resolveBrandingResult{Branding: b}
}

// defaultStr returns fallback when s is empty after trimming.
func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// defaultTime returns now (UTC) when t is the zero value. The T-1 renderer
// refuses to print a ticket whose session timestamp is zero; this lets the
// worker still deliver something useful when the enqueuer omits the field.
func defaultTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// formatSessionForEmail produces the "YYYY-MM-DD HH:MM (zone)" string used
// in the email body. Empty input → empty string (the template's {{with}}
// guard then suppresses the row entirely).
func formatSessionForEmail(t time.Time, tz string) string {
	if t.IsZero() {
		return ""
	}
	loc := time.UTC
	zoneLabel := "UTC"
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
			zoneLabel = tz
		}
	}
	// Human-facing email body rendered in the venue-local timezone;
	// allow:timeformat: deliberately not an RFC3339 API timestamp.
	return fmt.Sprintf("%s (%s)", t.In(loc).Format("2006-01-02 15:04"), zoneLabel)
}

// joinNonEmpty joins the non-empty arguments with sep.
func joinNonEmpty(sep string, parts ...string) string {
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	out := ""
	for i, p := range kept {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
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
