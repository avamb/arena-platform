// Package reportdelivery implements the report.deliver worker job type (feature #160).
//
// # Overview
//
// After an event report is generated (state transitions to 'ready'), a
// report.deliver job is enqueued in worker_jobs. This worker handler:
//
//  1. Parses the job payload to extract the report_id.
//  2. Loads the event_reports row (waits/retries if not yet 'ready').
//  3. Loads all event_report_lines for the report.
//  4. Resolves report recipients by querying active memberships for the event's
//     org — roles organizer, agent, platform_operator are included.
//  5. Deduplicates recipients by normalized email: when the same user holds
//     multiple qualifying roles they receive exactly ONE combined-view email.
//     (SQL GROUP BY enforces this: one ReportRecipientRow per user_id.)
//  6. Renders a transactional HTML/text report email for each unique recipient
//     that summarises the report lines (sales, refunds, complimentary, scans).
//  7. Sends via the injected email.Sender.
//  8. Emits a structured audit log entry per delivery.
//
// # Retry behaviour
//
// When the report is not yet 'ready' (still pending/generating) the handler
// returns a retryable error so the worker machinery reattempts the job. After
// the report reaches a terminal state ('ready' or 'failed') the handler
// completes. If no recipients are resolved the job completes silently (no
// emails sent, but no error).
package reportdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// JobType is the worker job type string for post-event report delivery.
const JobType = "report.deliver"

// Payload is the JSON payload stored in worker_jobs.payload for report.deliver jobs.
type Payload struct {
	ReportID string `json:"report_id"`
}

// HandlerOptions bundles the dependencies required by the report delivery handler.
type HandlerOptions struct {
	// ReportQueries provides access to event_reports and memberships/users
	// (via GetReportRecipientsForOrg).
	ReportQueries *gen.Queries
	// Sender delivers the transactional email. When nil, emails are logged only
	// (development / test mode).
	Sender email.Sender
	// FromAddress is the envelope From address for outgoing emails.
	// Example: "Arena Platform <reports@arena.example.com>"
	FromAddress string
	// Logger receives structured log entries. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// NewHandler constructs a worker.HandlerFunc for report.deliver jobs.
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
			logger.Error("reportdelivery: malformed job payload",
				slog.String("payload", string(payload)),
				slog.String("error", err.Error()),
			)
			return nil // permanent failure — malformed payload; do not retry
		}

		reportID, err := uuid.Parse(p.ReportID)
		if err != nil {
			logger.Error("reportdelivery: invalid report_id in payload",
				slog.String("report_id", p.ReportID),
				slog.String("error", err.Error()),
			)
			return nil // permanent failure — bad UUID
		}

		// ── 2. Load event report ──────────────────────────────────────────────
		if opts.ReportQueries == nil {
			logger.Error("reportdelivery: ReportQueries not configured",
				slog.String("report_id", reportID.String()),
			)
			return nil
		}

		report, err := opts.ReportQueries.GetEventReportByID(ctx, reportID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				logger.Error("reportdelivery: report not found",
					slog.String("report_id", reportID.String()),
				)
				return nil // permanent failure — report deleted or never existed
			}
			return fmt.Errorf("reportdelivery: get report %s: %w", reportID, err)
		}

		// Retry if report is not yet in a terminal state.
		switch report.State {
		case "ready":
			// proceed
		case "failed":
			logger.Warn("reportdelivery: report generation failed; skipping delivery",
				slog.String("report_id", reportID.String()),
				slog.String("state", report.State),
			)
			return nil // terminal — do not deliver a failed report
		default:
			// pending or generating — retry after back-off
			return fmt.Errorf("reportdelivery: report %s is still %s; retrying",
				reportID, report.State)
		}

		// ── 3. Load report lines ──────────────────────────────────────────────
		lines, err := opts.ReportQueries.ListEventReportLinesByReport(ctx, reportID)
		if err != nil {
			return fmt.Errorf("reportdelivery: list lines for report %s: %w", reportID, err)
		}

		// ── 4. Resolve recipients (deduplicated by user / email) ──────────────
		// GetReportRecipientsForOrg returns one row per unique user_id, with
		// their roles aggregated — this is the feature #160 dedup rule:
		//   "organizer == agent → one email with combined view".
		recipients, err := opts.ReportQueries.GetReportRecipientsForOrg(ctx, report.OrgID)
		if err != nil {
			return fmt.Errorf("reportdelivery: resolve recipients for org %s: %w",
				report.OrgID, err)
		}

		if len(recipients) == 0 {
			logger.Warn("reportdelivery: no recipients found for org; skipping delivery",
				slog.String("report_id", reportID.String()),
				slog.String("org_id", report.OrgID.String()),
			)
			return nil
		}

		// ── 5. Send one email per unique recipient ────────────────────────────
		sent := 0
		for _, recipient := range recipients {
			htmlBody := renderReportEmailHTML(report, lines, recipient)
			textBody := renderReportEmailText(report, lines, recipient)

			msg := email.Message{
				To:       recipient.Email,
				Subject:  fmt.Sprintf("Post-event report — Arena Platform"),
				HTMLBody: htmlBody,
				TextBody: textBody,
			}

			if opts.Sender == nil {
				// Development / test mode — log only.
				logger.Warn("reportdelivery: no email sender configured; email not sent",
					slog.String("report_id", reportID.String()),
					slog.String("to", recipient.Email),
					slog.String("roles", recipient.Roles),
				)
			} else {
				if sendErr := opts.Sender.Send(ctx, msg); sendErr != nil {
					logger.Warn("reportdelivery: send failed; will retry",
						slog.String("report_id", reportID.String()),
						slog.String("to", recipient.Email),
						slog.String("error", sendErr.Error()),
					)
					return fmt.Errorf("reportdelivery: send report to %s: %w",
						recipient.Email, sendErr)
				}
			}

			logger.Info("reportdelivery: report delivered",
				slog.String("report_id", reportID.String()),
				slog.String("event_id", report.EventID.String()),
				slog.String("org_id", report.OrgID.String()),
				slog.String("to", recipient.Email),
				slog.String("roles", recipient.Roles),
				slog.Int("lines", len(lines)),
			)
			sent++
		}

		logger.Info("reportdelivery: delivery complete",
			slog.String("report_id", reportID.String()),
			slog.Int("recipients_sent", sent),
		)
		return nil
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Email renderers
// ──────────────────────────────────────────────────────────────────────────────

// formatMinorAmount formats a minor-unit amount (e.g. cents) as a readable
// string. Example: 12345 → "123.45".
func formatMinorAmount(minor int64) string {
	if minor == 0 {
		return "0.00"
	}
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

// formatReportWindowLine returns a human-readable window string for the report.
func formatReportWindowLine(report gen.EventReportRow) string {
	if report.ReportWindowStart != nil && report.ReportWindowEnd != nil {
		return fmt.Sprintf("%s – %s",
			report.ReportWindowStart.UTC().Format(time.RFC3339),
			report.ReportWindowEnd.UTC().Format(time.RFC3339),
		)
	}
	if report.GeneratedAt != nil {
		return report.GeneratedAt.UTC().Format(time.RFC3339)
	}
	return "N/A"
}

// renderReportEmailHTML returns an HTML email body for the post-event report.
// The body includes the recipient's combined roles and all report lines.
func renderReportEmailHTML(
	report gen.EventReportRow,
	lines []gen.EventReportLineRow,
	recipient gen.ReportRecipientRow,
) string {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Post-event Report</title></head>
<body style="font-family:sans-serif;max-width:640px;margin:0 auto;padding:24px">
  <h1 style="color:#1a1a2e">Post-event Report</h1>
`)

	fmt.Fprintf(&sb, "  <p>Hello,</p>\n")
	fmt.Fprintf(&sb, "  <p>Your post-event report is ready. You are receiving this as: <strong>%s</strong>.</p>\n",
		formatRolesDisplay(recipient.Roles))

	fmt.Fprintf(&sb, "  <p style=\"color:#555\">Report ID: %s<br>Event ID: %s<br>Period: %s</p>\n",
		report.ID, report.EventID, formatReportWindowLine(report))

	sb.WriteString("  <table style=\"width:100%;border-collapse:collapse;margin-top:16px\">\n")
	sb.WriteString("    <thead><tr style=\"background:#f0f0f0\">\n")
	sb.WriteString("      <th style=\"text-align:left;padding:8px\">Category</th>\n")
	sb.WriteString("      <th style=\"text-align:right;padding:8px\">Quantity</th>\n")
	sb.WriteString("      <th style=\"text-align:right;padding:8px\">Gross</th>\n")
	sb.WriteString("      <th style=\"text-align:right;padding:8px\">Net</th>\n")
	sb.WriteString("      <th style=\"text-align:right;padding:8px\">Currency</th>\n")
	sb.WriteString("    </tr></thead>\n    <tbody>\n")

	for _, l := range lines {
		fmt.Fprintf(&sb,
			"    <tr><td style=\"padding:8px\">%s</td>"+
				"<td style=\"text-align:right;padding:8px\">%d</td>"+
				"<td style=\"text-align:right;padding:8px\">%s</td>"+
				"<td style=\"text-align:right;padding:8px\">%s</td>"+
				"<td style=\"text-align:right;padding:8px\">%s</td></tr>\n",
			l.Category, l.Quantity,
			formatMinorAmount(l.GrossAmount),
			formatMinorAmount(l.NetAmount),
			l.Currency,
		)
	}

	sb.WriteString("    </tbody>\n  </table>\n")
	sb.WriteString("  <hr>\n")
	sb.WriteString("  <p style=\"font-size:11px;color:#999\">Arena Platform — automated report delivery</p>\n")
	sb.WriteString("</body>\n</html>")
	return sb.String()
}

// renderReportEmailText returns a plain-text fallback body for the post-event
// report delivery email.
func renderReportEmailText(
	report gen.EventReportRow,
	lines []gen.EventReportLineRow,
	recipient gen.ReportRecipientRow,
) string {
	var sb strings.Builder

	sb.WriteString("Post-event Report — Arena Platform\n\n")
	fmt.Fprintf(&sb, "Hello,\n\n")
	fmt.Fprintf(&sb, "Your post-event report is ready. You are receiving this as: %s.\n\n",
		formatRolesDisplay(recipient.Roles))
	fmt.Fprintf(&sb, "Report ID : %s\nEvent ID  : %s\nPeriod    : %s\n\n",
		report.ID, report.EventID, formatReportWindowLine(report))

	sb.WriteString("Category        | Qty   | Gross      | Net        | Currency\n")
	sb.WriteString("----------------|-------|------------|------------|----------\n")

	for _, l := range lines {
		fmt.Fprintf(&sb, "%-16s| %-6d| %-11s| %-11s| %s\n",
			l.Category, l.Quantity,
			formatMinorAmount(l.GrossAmount),
			formatMinorAmount(l.NetAmount),
			l.Currency,
		)
	}

	sb.WriteString("\nArena Platform — automated report delivery\n")
	return sb.String()
}

// formatRolesDisplay converts a comma-separated roles string into a
// human-readable representation, e.g. "agent,organizer" → "Agent, Organizer".
func formatRolesDisplay(roles string) string {
	parts := strings.Split(roles, ",")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, ", ")
}
