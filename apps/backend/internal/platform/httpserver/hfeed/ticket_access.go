// ticket_access.go — the buyer's ticket access log (migration 0184, spec 51
// decision R-13).
//
// A chargeback answer must show that the buyer actually got the tickets:
// Stripe's dispute evidence carries access_activity_log and
// customer_purchase_ip. The PDF attached to the ticket e-mail is invisible to
// arena (and Brevo never reports the reader's address), so the two public
// routes that hand tickets out record every download and view here:
//
//   - GET /v1/public/checkout/{token}/tickets/{id}/pdf → one 'pdf_download'
//     row, not repeated for the same ticket, address and source within a
//     minute;
//   - GET /v1/public/checkout/{token} answering a paid order with its tickets
//     → one 'tickets_view' row, not repeated from the same address within ten
//     minutes (the widget polls this answer).
//
// A request that looks like a machine — corporate mail security opening every
// link of a letter at delivery, a link preview, a script — is still recorded
// but marked likely_automated, and must never be quoted as the buyer's own
// download.
//
// Recording is best effort: a failure is logged and the buyer still gets the
// PDF or the status. The write is bounded by accessLogTimeout and detached
// from the request, so a client that hangs up does not lose the row and a slow
// database cannot hold the PDF back for long. The IP is stored in the clear
// (decision R-10).
package hfeed

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// The values of ticket_access_events.source and .action. They mirror the
// CHECK constraints of migration 0184; TestTicketAccessValues_MatchMigration
// keeps the two in step.
const (
	// TicketAccessSourceWeb is a request from the thank-you page, the widget
	// or any link other than the ticket e-mail's.
	TicketAccessSourceWeb = "web"
	// TicketAccessSourceEmail is the download link of the ticket e-mail
	// (?src=email).
	TicketAccessSourceEmail = "email"

	// TicketAccessActionPDFDownload is one ticket PDF handed out.
	TicketAccessActionPDFDownload = "pdf_download"
	// TicketAccessActionTicketsView is the order's tickets shown in the status
	// answer.
	TicketAccessActionTicketsView = "tickets_view"
)

// TicketAccessSourceQueryParam is the query parameter that names where a
// download link was followed from; the ticket e-mail sets it to "email".
const TicketAccessSourceQueryParam = "src"

// accessLogTimeout bounds one access-log write.
const accessLogTimeout = 2 * time.Second

// TicketAccessRecorder writes the access log. *gen.Queries satisfies it.
type TicketAccessRecorder interface {
	InsertTicketPDFDownload(ctx context.Context, arg gen.InsertTicketPDFDownloadParams) (bool, error)
	InsertTicketsView(ctx context.Context, arg gen.InsertTicketsViewParams) (bool, error)
}

// WithTicketAccessLog wires the access log; nil leaves it off. Returns the
// receiver for chaining.
func (h *Handler) WithTicketAccessLog(rec TicketAccessRecorder) *Handler {
	h.accessLog = rec
	return h
}

// ticketAccessSource is where the request came from: 'email' for the ticket
// e-mail's link, 'web' for everything else. An unknown value is 'web', never
// an error — the parameter only labels the row.
func ticketAccessSource(r *http.Request) string {
	if r.URL.Query().Get(TicketAccessSourceQueryParam) == TicketAccessSourceEmail {
		return TicketAccessSourceEmail
	}
	return TicketAccessSourceWeb
}

// automatedUserAgentMarks are user-agent fragments of mail scanners, link
// previews and scripts (lower case).
var automatedUserAgentMarks = []string{
	"bot", "crawler", "spider", "preview", "scanner",
	"mimecast", "proofpoint", "barracuda", "safelinks", "safe links",
	"ms-office", "microsoft office", "google-safety", "cloudflare",
	"go-http-client", "python", "curl", "wget", "java/", "okhttp",
	"headless", "phantomjs", "httpclient", "libwww",
}

// ticketAccessLooksAutomated is the heuristic behind likely_automated: a
// prefetch or preview header, no browser string at all, a known machine
// fragment in it, or no Accept-Language (every real browser sends one, mail
// scanners and scripts usually do not). It only labels the row; nothing is
// refused.
func ticketAccessLooksAutomated(r *http.Request) bool {
	for _, h := range []string{"Purpose", "Sec-Purpose", "X-Purpose", "X-Moz"} {
		v := strings.ToLower(r.Header.Get(h))
		if strings.Contains(v, "prefetch") || strings.Contains(v, "preview") {
			return true
		}
	}
	ua := strings.ToLower(strings.TrimSpace(r.UserAgent()))
	if ua == "" {
		return true
	}
	for _, m := range automatedUserAgentMarks {
		if strings.Contains(ua, m) {
			return true
		}
	}
	return strings.TrimSpace(r.Header.Get("Accept-Language")) == ""
}

// ticketAccessClient is the request's client address and browser string as
// the log stores them ("" for either when unknown). The router's
// trusted-proxy handling has already rewritten RemoteAddr, so no hop count is
// applied here (same as the checkout consent).
func ticketAccessClient(r *http.Request) (ip, userAgent string) {
	if p := consentClientIP(httputil.TrustedClientIP(r, 0)); p != nil {
		ip = *p
	}
	if p := capUserAgent(r.UserAgent()); p != nil {
		userAgent = *p
	}
	return ip, userAgent
}

// accessLogContext detaches the write from the request (a client that hangs
// up must not lose the row) and bounds it.
func accessLogContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), accessLogTimeout)
}

// recordPDFDownload logs one ticket PDF handed out.
func (h *Handler) recordPDFDownload(r *http.Request, checkoutSessionID, orgID, ticketID uuid.UUID) {
	if h.accessLog == nil {
		return
	}
	ip, ua := ticketAccessClient(r)
	ctx, cancel := accessLogContext(r)
	defer cancel()
	_, err := h.accessLog.InsertTicketPDFDownload(ctx, gen.InsertTicketPDFDownloadParams{
		CheckoutSessionID: checkoutSessionID,
		TicketID:          ticketID,
		OrgID:             orgID,
		Source:            ticketAccessSource(r),
		IP:                ip,
		UserAgent:         ua,
		LikelyAutomated:   ticketAccessLooksAutomated(r),
	})
	if err != nil {
		h.logger.Warn("ticket_access: pdf download not recorded",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// recordTicketsView logs that the status answer showed the order's tickets.
func (h *Handler) recordTicketsView(r *http.Request, checkoutSessionID, orgID uuid.UUID) {
	if h.accessLog == nil {
		return
	}
	ip, ua := ticketAccessClient(r)
	ctx, cancel := accessLogContext(r)
	defer cancel()
	_, err := h.accessLog.InsertTicketsView(ctx, gen.InsertTicketsViewParams{
		CheckoutSessionID: checkoutSessionID,
		OrgID:             orgID,
		Source:            ticketAccessSource(r),
		IP:                ip,
		UserAgent:         ua,
		LikelyAutomated:   ticketAccessLooksAutomated(r),
	})
	if err != nil {
		h.logger.Warn("ticket_access: tickets view not recorded",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()),
		)
	}
}
