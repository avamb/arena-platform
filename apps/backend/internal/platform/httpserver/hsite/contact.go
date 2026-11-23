package hsite

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/contactform"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// Limits of one message and of the senders (the hosted page's form uses the
// same numbers; the per-IP budget is shared with it).
const (
	MaxBodyBytes      = 16 << 10
	MaxNameRunes      = 100
	MinMessageRunes   = 10
	MaxMessageRunes   = 2000
	PerIPPerHour      = 5
	PerChannelPerHour = 30
	minPhoneChars     = 5
	maxPhoneChars     = 30
)

// ContactHandler serves POST /v1/public/feeds/{feed_token}/organizer-contact.
type ContactHandler struct {
	Pool           *pgxpool.Pool
	Guard          Guard
	IPSalt         string
	TrustedProxies int
	Logger         *slog.Logger
}

// ContactRequest is the body of the route. Unknown fields are refused.
type ContactRequest struct {
	// Topic is contact (default), private_event or partnership; it is also
	// the Turnstile widget action of the form.
	Topic string `json:"topic"`
	// Company (required), City and Website (optional) belong to the
	// partnership topic only; other topics ignore them.
	Company string `json:"company"`
	City    string `json:"city"`
	Website string `json:"website"`
	// Name is the first name; LastName the surname.
	Name           string `json:"name"`
	LastName       string `json:"last_name"`
	Email          string `json:"email"`
	Phone          string `json:"phone"`
	Message        string `json:"message"`
	Consent        bool   `json:"consent"`
	TurnstileToken string `json:"turnstile_token"`
	Locale         string `json:"locale"`
}

func (h *ContactHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func bad(w http.ResponseWriter, r *http.Request, code, msg, field string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(code, msg, r, map[string]any{"field": field}))
}

func notAvailable(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"contact.not_available", "This site has no contact form. Check the address of the page.", r))
}

// ValidPhone reports whether s looks like a phone number: 5 to 30 characters
// of digits, spaces and + ( ) - . only, with at least 5 digits.
func ValidPhone(s string) bool {
	if n := utf8.RuneCountInString(s); n < minPhoneChars || n > maxPhoneChars {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == ' ' || r == '+' || r == '(' || r == ')' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return digits >= minPhoneChars
}

// MaxWebsiteRunes is the longest website address the form takes.
const MaxWebsiteRunes = 200

// ValidWebsite reports whether s (already trimmed) is an http or https URL
// with a host, or a bare host such as "example.com" or "www.example.com/x".
func ValidWebsite(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > MaxWebsiteRunes {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	raw := s
	if strings.Contains(s, "://") {
		low := strings.ToLower(s)
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			return false
		}
	} else {
		if strings.ContainsAny(s, ":@\\") && !strings.Contains(s, "/") {
			return false
		}
		raw = "https://" + s
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return false
	}
	host := u.Hostname()
	// A bare word is not a site: a dot (or an IP/localhost-free host) is needed.
	return strings.Contains(host, ".") && !strings.HasPrefix(host, ".") && !strings.HasSuffix(host, ".")
}

// HandleSend serves the route. The Idempotency-Key middleware in front of it
// (scope = feed token) is what makes a retry with the same key and body answer
// the first 202 instead of storing and mailing the message twice.
func (h *ContactHandler) HandleSend(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.Pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil || len(raw) > MaxBodyBytes {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelope(
			"contact.too_large", "Your message is too long. Shorten it to 2000 characters and send it again.", r))
		return
	}
	var req ContactRequest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		bad(w, r, "contact.invalid_body", "We could not read the form. Reload the page and try again.", "body")
		return
	}
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		topic = contactform.TopicContact
	}
	if !contactform.ValidTopic(topic) {
		bad(w, r, "contact.invalid_topic", "Choose one of the topics: contact, private_event or partnership.", "topic")
		return
	}
	partner := topic == contactform.TopicPartnership
	var company, city, website string
	if partner {
		company = strings.Join(strings.Fields(req.Company), " ")
		if company == "" || utf8.RuneCountInString(company) > MaxNameRunes {
			bad(w, r, "contact.invalid_company", "Enter your company name, up to 100 characters.", "company")
			return
		}
		city = strings.Join(strings.Fields(req.City), " ")
		if utf8.RuneCountInString(city) > MaxNameRunes {
			bad(w, r, "contact.invalid_city", "Enter your city, up to 100 characters.", "city")
			return
		}
		website = strings.TrimSpace(req.Website)
		if website != "" && !ValidWebsite(website) {
			bad(w, r, "contact.invalid_website", "Enter the address of your website, for example https://example.com.", "website")
			return
		}
	}
	name := strings.Join(strings.Fields(req.Name), " ")
	if name == "" || utf8.RuneCountInString(name) > MaxNameRunes {
		bad(w, r, "contact.invalid_name", "Enter your name, up to 100 characters.", "name")
		return
	}
	lastName := strings.Join(strings.Fields(req.LastName), " ")
	if utf8.RuneCountInString(lastName) > MaxNameRunes {
		bad(w, r, "contact.invalid_last_name", "Enter your last name, up to 100 characters.", "last_name")
		return
	}
	email, ok := sellerinfo.NormalizeEmail(req.Email)
	if !ok {
		bad(w, r, "contact.invalid_email", "Enter your e-mail address so the organizer can answer you, for example anna@example.com.", "email")
		return
	}
	phone := strings.Join(strings.Fields(req.Phone), " ")
	if phone != "" && !ValidPhone(phone) {
		bad(w, r, "contact.invalid_phone", "Enter a phone number with digits, for example +972 50 123 4567.", "phone")
		return
	}
	msg := strings.TrimSpace(strings.ReplaceAll(req.Message, "\r\n", "\n"))
	n := utf8.RuneCountInString(msg)
	if n > MaxMessageRunes || (n < MinMessageRunes && (n > 0 || topic == contactform.TopicPrivateEvent || partner)) {
		bad(w, r, "contact.invalid_message", "Write a message of 10 to 2000 characters.", "message")
		return
	}
	if !req.Consent {
		bad(w, r, "contact.consent_required", "Tick the box to agree that we pass your message to the organizer.", "consent")
		return
	}

	ctx := r.Context()
	ch, err := ChannelByFeedToken(ctx, h.Pool, chi.URLParam(r, "feed_token"))
	if err != nil {
		if err == ErrNoChannel {
			notAvailable(w, r)
			return
		}
		h.fail(w, r, "channel", err)
		return
	}
	// A topic without addresses of its own falls back to the usual chain; a
	// channel with neither has nobody to write to.
	if len(contactform.RecipientsFor(ch.Recipients, topic)) == 0 && !ch.HasFallbackAddress {
		notAvailable(w, r)
		return
	}
	ip := httputil.TrustedClientIP(r, h.TrustedProxies)
	if !WriteCaptchaOutcome(w, r, "contact", h.Guard.Captcha(ctx, ch, req.TurnstileToken, ip, topic)) {
		return
	}

	locale := strings.ToLower(strings.TrimSpace(req.Locale))
	if l := utf8.RuneCountInString(locale); l < 2 || l > 10 {
		locale = "en"
	}
	ipHash := HashIP(h.IPSalt, ip)

	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Same lock key as the hosted page's form: one budget per channel.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('organizer-contact:' || $1::text, 0))`, ch.ID); err != nil {
		h.fail(w, r, "lock", err)
		return
	}
	var perIP, perChannel int
	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM organizer_contact_messages WHERE ip_hash = $1 AND $1 <> '' AND created_at > now() - interval '1 hour'),
		  (SELECT count(*) FROM organizer_contact_messages WHERE channel_id = $2 AND created_at > now() - interval '1 hour')`,
		ipHash, ch.ID).Scan(&perIP, &perChannel); err != nil {
		h.fail(w, r, "count", err)
		return
	}
	if perIP >= PerIPPerHour || perChannel >= PerChannelPerHour {
		w.Header().Set("Retry-After", strconv.Itoa(3600))
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"contact.rate_limited", "This site accepts a limited number of messages each hour. Try again in an hour.", r))
		return
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO organizer_contact_messages
		    (org_id, channel_id, page_slug, sender_name, sender_email, message, locale, ip_hash, topic, last_name, phone, company, city, website)
		VALUES ($1, $2, 'feed-site', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
		ch.OrgID, ch.ID, name, email, msg, locale, ipHash, topic, lastName, phone, company, city, website).Scan(&id); err != nil {
		h.fail(w, r, "insert", err)
		return
	}
	if _, err := worker.EnqueueInTx(ctx, tx, contactform.JobType, contactform.Payload{MessageID: id.String()}, 8); err != nil {
		h.fail(w, r, "enqueue", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	// Neither the address nor the message is logged.
	h.logger().Info("site contact: message queued", slog.String("org_id", ch.OrgID.String()),
		slog.String("message_id", id.String()), slog.String("topic", topic))
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

func (h *ContactHandler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.logger().Error("site contact: "+op+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"contact.send_failed", "We could not send your message. Try again in a minute.", r))
}

// compile-time: the verifier type the server builds.
var _ honboarding.Verifier = (*honboarding.TurnstileVerifier)(nil)
