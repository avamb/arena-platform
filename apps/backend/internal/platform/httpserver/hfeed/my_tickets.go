package hfeed

// my_tickets.go - "my tickets by link" (package mytickets, spec 62 §7, item
// P-7). Five public routes, no JWT:
//
//	POST   /v1/public/feeds/{feed_token}/my-tickets/link   {email, locale, page_url} -> 202 always
//	POST   /v1/public/my-tickets/session                   {token}                   -> 200 {session_token, expires_at} | 410
//	DELETE /v1/public/my-tickets/session                   Authorization: Bearer     -> 204
//	GET    /v1/public/my-tickets/orders?page=              Authorization: Bearer     -> 200
//	GET    /v1/public/my-tickets/orders/{order_id}/tickets/{ticket_id}/pdf   Bearer  -> PDF
//
// The link request answers 202 for a known and an unknown address alike, and
// for a request the limits swallowed, so the route cannot be used to learn
// who has bought. Nothing here logs an address, a token or a session.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsite"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/humancode"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
)

// WithMyTickets wires the "my tickets" service and the salt of the stored
// client-address hash. Returns the receiver for chaining.
func (h *Handler) WithMyTickets(svc *mytickets.Service, ipSalt string) *Handler {
	h.myTickets = svc
	h.myTicketsSalt = ipSalt
	return h
}

// MyTicketsCaptcha checks the Turnstile token of a link request for the feed
// token's channel (hsite.Guard.CaptchaForFeed with action my_tickets): a
// channel with site origins must send a valid token, any other channel is not
// gated.
type MyTicketsCaptcha func(ctx context.Context, feedToken, captchaToken, ip string) hsite.CaptchaOutcome

// WithMyTicketsCaptcha wires the Turnstile gate of the link request. Returns
// the receiver for chaining.
func (h *Handler) WithMyTicketsCaptcha(fn MyTicketsCaptcha) *Handler {
	h.myTicketsCaptcha = fn
	return h
}

// MyTicketsPageCheck reports whether pageURL lies on one of the site origins
// of the feed token's OWN channel (settings.site.origins). The global CORS
// allow-list is not enough: it holds every organizer's site, so a token of
// one organizer could mail a genuine link pointing at another's page.
type MyTicketsPageCheck func(ctx context.Context, feedToken, pageURL string) bool

// WithMyTicketsPageCheck wires the page_url check of the link request.
// Without it every request links the tickets site's own /my-tickets page.
// Returns the receiver for chaining.
func (h *Handler) WithMyTicketsPageCheck(fn MyTicketsPageCheck) *Handler {
	h.myTicketsPage = fn
	return h
}

func (h *Handler) myTicketsIPHash(r *http.Request) string {
	ip := h.clientIP(r)
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(h.myTicketsSalt + "|my-tickets|" + ip))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) myTicketsUnavailable(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
	))
}

// formatHumanCode shows the stored human code in its display form, nil stays nil.
func formatHumanCode(code *string) *string {
	if code == nil {
		return nil
	}
	f := humancode.Format(*code)
	return &f
}

func myTicketsHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// MyTicketsLinkRequest is the body of the link request.
type MyTicketsLinkRequest struct {
	Email  string `json:"email"`
	Locale string `json:"locale"`
	// PageURL is the site page the letter links to. It is used only when its
	// origin is one of the feed token's own channel site origins; otherwise
	// the tickets site's own /my-tickets page is linked.
	PageURL string `json:"page_url"`
	// TurnstileToken is the Cloudflare Turnstile token (action my_tickets).
	// Required when the channel lists site origins, ignored otherwise.
	TurnstileToken string `json:"turnstile_token,omitempty"`
}

// HandleMyTicketsRequestLink serves the link request.
func (h *Handler) HandleMyTicketsRequestLink(w http.ResponseWriter, r *http.Request) {
	myTicketsHeaders(w)
	if h.myTickets == nil {
		h.myTicketsUnavailable(w, r)
		return
	}
	feedToken := chi.URLParam(r, "feed_token")
	if !h.enforceRateLimit(w, r, "feed.rate_limited", h.rl.CheckFeedToken, feedToken) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	var req MyTicketsLinkRequest
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"my_tickets.invalid_json", "The request body is not valid JSON. Fix it and send it again.", r,
		))
		return
	}
	if h.myTicketsCaptcha != nil {
		outcome := h.myTicketsCaptcha(r.Context(), feedToken, req.TurnstileToken, h.clientIP(r))
		if !hsite.WriteCaptchaOutcome(w, r, "my_tickets", outcome) {
			return
		}
	}
	page := ""
	if strings.TrimSpace(req.PageURL) != "" && h.myTicketsPage != nil && h.myTicketsPage(r.Context(), feedToken, req.PageURL) {
		page = req.PageURL
	}
	err = h.myTickets.RequestLink(r.Context(), mytickets.LinkInput{
		FeedToken: feedToken, Email: req.Email, Locale: req.Locale, PageURL: page, IPHash: h.myTicketsIPHash(r),
	})
	switch {
	case err == nil:
		httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "check_email"})
	case errors.Is(err, mytickets.ErrInvalidEmail):
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"my_tickets.invalid_email", "Check the e-mail address and send it again.", r,
		))
	case errors.Is(err, mytickets.ErrFeedTokenUnknown):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"feed.token_not_found", "This ticket link does not work any more. Ask the organizer for a new link.", r,
		))
	default:
		h.logger.Error("my_tickets: link request failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"my_tickets.failed", "We could not process the request. Try again in a minute.", r,
		))
	}
}

// MyTicketsExchangeRequest is the body of the session exchange.
type MyTicketsExchangeRequest struct {
	Token string `json:"token"`
}

// HandleMyTicketsExchange serves the exchange of the e-mailed token for a
// session. A used, expired or unknown token is 410.
func (h *Handler) HandleMyTicketsExchange(w http.ResponseWriter, r *http.Request) {
	myTicketsHeaders(w)
	if h.myTickets == nil {
		h.myTicketsUnavailable(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	var req MyTicketsExchangeRequest
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"my_tickets.invalid_json", "The request body is not valid JSON. Fix it and send it again.", r,
		))
		return
	}
	// Per-IP only: the link token is the credential being guessed here.
	if !h.enforceRateLimit(w, r, "my_tickets.rate_limited", h.rl.CheckCheckoutToken, "mt-exchange|"+h.myTicketsIPHash(r)) {
		return
	}
	token, expires, err := h.myTickets.Exchange(r.Context(), req.Token)
	if errors.Is(err, mytickets.ErrLinkInvalid) {
		httputil.WriteJSON(w, http.StatusGone, httputil.ErrorEnvelope(
			"my_tickets.link_invalid", "This link was already used or has expired. Request a new one.", r,
		))
		return
	}
	if err != nil {
		h.logger.Error("my_tickets: exchange failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"my_tickets.failed", "We could not process the request. Try again in a minute.", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]string{
		"session_token": token,
		"expires_at":    expires.UTC().Format(time.RFC3339),
	})
}

// myTicketsSession authenticates the bearer token. It writes the error answer
// and reports false when there is no live session.
func (h *Handler) myTicketsSession(w http.ResponseWriter, r *http.Request) (mytickets.Session, bool) {
	myTicketsHeaders(w)
	if h.myTickets == nil {
		h.myTicketsUnavailable(w, r)
		return mytickets.Session{}, false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	token := ""
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		token = strings.TrimSpace(auth[7:])
	}
	if !h.enforceRateLimit(w, r, "my_tickets.rate_limited", h.rl.CheckCheckoutToken, "mt-session|"+token) {
		return mytickets.Session{}, false
	}
	sess, err := h.myTickets.Authenticate(r.Context(), token)
	if errors.Is(err, mytickets.ErrSessionInvalid) {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"my_tickets.session_invalid", "Your session has ended. Request a new link to open your tickets.", r,
		))
		return mytickets.Session{}, false
	}
	if err != nil {
		h.logger.Error("my_tickets: session lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"my_tickets.failed", "We could not process the request. Try again in a minute.", r,
		))
		return mytickets.Session{}, false
	}
	return sess, true
}

// HandleMyTicketsLogout revokes the session ("log out").
func (h *Handler) HandleMyTicketsLogout(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.myTicketsSession(w, r)
	if !ok {
		return
	}
	if err := h.myTickets.Revoke(r.Context(), sess); err != nil {
		h.logger.Error("my_tickets: revoke failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"my_tickets.failed", "We could not process the request. Try again in a minute.", r,
		))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type myTicketsTicketResponse struct {
	TicketID  string  `json:"ticket_id"`
	Status    string  `json:"status"`
	Sector    *string `json:"sector"`
	Row       *string `json:"row"`
	Number    *string `json:"number"`
	TierName  *string `json:"tier_name"`
	HumanCode *string `json:"human_code"`
	PDFURL    string  `json:"pdf_url"`
}

type myTicketsOrderResponse struct {
	OrderID     string `json:"order_id"`
	OrderNumber int64  `json:"order_number"`
	// Status is paid, refunded or cancelled (the public order statuses).
	Status    string `json:"status"`
	Total     int64  `json:"total"`
	Currency  string `json:"currency"`
	PaidAt    string `json:"paid_at,omitempty"`
	CreatedAt string `json:"created_at"`
	// SessionCancelled is true when the session's date was cancelled.
	SessionCancelled bool                      `json:"session_cancelled"`
	Event            checkoutStatusEvent       `json:"event"`
	Tickets          []myTicketsTicketResponse `json:"tickets"`
}

// HandleMyTicketsOrders lists the session's orders, 20 a page.
func (h *Handler) HandleMyTicketsOrders(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.myTicketsSession(w, r)
	if !ok {
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"my_tickets.invalid_page", "Send page as a whole number, 1 or more.", r,
			))
			return
		}
		page = n
	}
	orders, total, err := h.myTickets.ListOrders(r.Context(), sess, page)
	if err != nil {
		h.logger.Error("my_tickets: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"my_tickets.failed", "We could not process the request. Try again in a minute.", r,
		))
		return
	}
	out := make([]myTicketsOrderResponse, 0, len(orders))
	for _, o := range orders {
		item := myTicketsOrderResponse{
			OrderID:          o.ID.String(),
			OrderNumber:      o.OrderNumber,
			Status:           publicOrderStatus(publicStatusPaid, o.Status),
			Total:            o.Total,
			Currency:         o.Currency,
			CreatedAt:        o.CreatedAt.UTC().Format(time.RFC3339),
			SessionCancelled: o.SessionStatus == "cancelled",
			Event: checkoutStatusEvent{
				EventID:   o.EventID.String(),
				SessionID: o.SessionID.String(),
				Name:      o.EventName,
				Slug:      o.EventSlug,
				StartAt:   o.StartAt.UTC().Format(time.RFC3339),
				VenueName: o.VenueName,
			},
			Tickets: make([]myTicketsTicketResponse, 0, len(o.Tickets)),
		}
		if o.Timezone != nil && strings.TrimSpace(*o.Timezone) != "" {
			tz := strings.TrimSpace(*o.Timezone)
			item.Event.Timezone = &tz
		}
		if o.PaidAt != nil {
			item.PaidAt = o.PaidAt.UTC().Format(time.RFC3339)
		}
		for _, t := range o.Tickets {
			item.Tickets = append(item.Tickets, myTicketsTicketResponse{
				TicketID: t.ID.String(), Status: t.Status, Sector: t.Sector, Row: t.Row, Number: t.Number,
				TierName: t.TierName, HumanCode: formatHumanCode(t.HumanCode),
				PDFURL: h.absoluteAPIURL("/v1/public/my-tickets/orders/" + o.ID.String() + "/tickets/" + t.ID.String() + "/pdf"),
			})
		}
		out = append(out, item)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"orders": out,
		"pagination": publicFeedPagination{
			Page: page, PerPage: mytickets.PageSize, Total: total,
			TotalPages: max(1, (total+mytickets.PageSize-1)/mytickets.PageSize),
		},
	})
}

// HandleMyTicketsPDF hands out the PDF of one ticket of the session's own
// order. A foreign organization, order or ticket answers the same 404.
func (h *Handler) HandleMyTicketsPDF(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.myTicketsSession(w, r)
	if !ok {
		return
	}
	if h.credentialQueries == nil {
		h.myTicketsUnavailable(w, r)
		return
	}
	orderID, errO := uuid.Parse(chi.URLParam(r, "order_id"))
	ticketID, errT := uuid.Parse(chi.URLParam(r, "ticket_id"))
	if errO != nil || errT != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"my_tickets.invalid_id", "Send order_id and ticket_id as UUIDs.", r,
		))
		return
	}
	owns, err := h.myTickets.OwnsTicket(r.Context(), sess, orderID, ticketID)
	if err != nil {
		h.logger.Error("my_tickets: ownership check failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"my_tickets.failed", "We could not process the request. Try again in a minute.", r,
		))
		return
	}
	if !owns {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"my_tickets.ticket_not_found", "We cannot find this ticket.", r,
		))
		return
	}
	cred, err := h.credentialQueries.GetCredentialByTicketID(r.Context(), ticketID, "pdf")
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"checkout.pdf_not_found", "The ticket PDF is not ready yet. Try again in a minute.", r,
		))
		return
	}
	if err != nil {
		h.logger.Error("my_tickets: credential lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.credential_failed", "We could not load the ticket code. Try again in a minute.", r,
		))
		return
	}
	pdfBytes, err := base64.StdEncoding.DecodeString(cred.Payload)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.credential_malformed", "The stored ticket code is damaged. Write to support.", r,
		))
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"ticket-"+ticketID.String()+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}
