// Package mytickets is "my tickets by link" for a static organizer site
// (spec 62 §7, item P-7).
//
//   - A buyer types an e-mail on the site's /my-tickets/ page. The request is
//     routed by the site's FEED TOKEN (not by an organization slug, because the
//     hosted-page routes need a channel with hosted_page.enabled). The answer
//     is 202 whatever the address is, so nobody can probe which addresses have
//     bought; a letter is queued only when the address has an order of that
//     organization.
//   - The letter carries a one-time token. Only its SHA-256 is stored, the
//     worker mints it at send time (it is never at rest in worker_jobs), it
//     works for 30 minutes and is not spent by opening the page: a mail
//     scanner that opens the link changes nothing, the buyer's button press
//     exchanges it (Exchange) - atomically, so a second exchange fails.
//   - The exchange yields a bearer session of 4 hours (only its hash is
//     stored; no cookie on the API domain). Logout revokes it.
//   - A session sees the ticket orders of ONE organization whose buyer_email
//     equals the address that got the link. Other addresses of the same person
//     are never mixed in: owning them is not proven.
//
// Rate limits count requests in the database: 3 letters an hour per address and
// 10 an hour per hashed client address.
package mytickets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
)

// JobTypeLinkEmail is the worker job that mints the token and mails the link
// (handler: delivery.NewMyTicketsLinkHandler).
const JobTypeLinkEmail = "my_tickets.link_email"

const (
	// LinkTTL is how long an e-mailed link works.
	LinkTTL = 30 * time.Minute
	// SessionTTL is how long a session lasts.
	SessionTTL = 4 * time.Hour
	// PageSize is the number of orders on a page of the list.
	PageSize = 20

	perEmailHourly = 3
	perIPHourly    = 10
	keepRows       = 7 * 24 * time.Hour
)

var (
	// ErrInvalidEmail is a syntactically unusable address.
	ErrInvalidEmail = errors.New("mytickets: invalid e-mail")
	// ErrFeedTokenUnknown is a feed token that is unknown or revoked.
	ErrFeedTokenUnknown = errors.New("mytickets: unknown feed token")
	// ErrLinkInvalid is a link that is unknown, used or expired.
	ErrLinkInvalid = errors.New("mytickets: link invalid or used")
	// ErrSessionInvalid is a session that is unknown, expired or revoked.
	ErrSessionInvalid = errors.New("mytickets: session invalid")
)

// Service reads and writes the link requests and sessions.
type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// New builds a service; now is time.Now when nil.
func New(pool *pgxpool.Pool, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{pool: pool, now: now}
}

// HashToken is what the tables store of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// NewToken mints a 64-character hex token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// LinkInput is one request for a link.
type LinkInput struct {
	FeedToken string
	Email     string
	Locale    string
	// PageURL is the site page the letter links to (already checked against
	// the allowed origins by the caller), "" for the default.
	PageURL string
	// IPHash is a salted hash of the client address, "" when unknown.
	IPHash string
}

// JobPayload is the payload of the link letter job.
type JobPayload struct {
	RequestID string `json:"request_id"`
}

// ticketOrderSources are the order sources a buyer sees (shop orders are a
// separate table and never appear).
const ticketOrderSources = `('public_feed','bil24_gateway','checkout_api','complimentary')`

// orderVisible is the SQL condition for an order a buyer may see: it was paid
// at some time. Unpaid carts and expired holds are not tickets.
const orderVisible = `(o.paid_at IS NOT NULL OR o.status IN ('paid','refunded','partially_refunded'))`

// EscapeLike makes s safe as an ILIKE pattern that matches s exactly.
func EscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// RequestLink records a request and queues the letter when the address has
// orders of the feed token's organization. A nil error is the only success
// answer, whether or not a letter was queued; limits are enforced silently.
func (s *Service) RequestLink(ctx context.Context, in LinkInput) error {
	email, err := salesubscribe.NormalizeEmail(in.Email)
	if err != nil {
		return ErrInvalidEmail
	}
	var orgID, channelID uuid.UUID
	err = s.pool.QueryRow(ctx, `
		SELECT sc.org_id, sc.id
		FROM   agent_feed_tokens ft
		JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
		WHERE  ft.token = $1 AND ft.is_active = true AND sc.deleted_at IS NULL`, in.FeedToken).Scan(&orgID, &channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFeedTokenUnknown
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	// Housekeeping: nothing here is needed after a week.
	if _, err := s.pool.Exec(ctx, `DELETE FROM my_tickets_link_requests WHERE created_at < $1`, now.Add(-keepRows)); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM my_tickets_sessions WHERE expires_at < $1`, now.Add(-keepRows)); err != nil {
		return err
	}
	hour := now.Add(-time.Hour)
	var byEmail, byIP int
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM my_tickets_link_requests WHERE email_normalized = $1 AND created_at > $3),
		       (SELECT count(*) FROM my_tickets_link_requests WHERE $2 <> '' AND ip_hash = $2 AND created_at > $3)`,
		email, in.IPHash, hour).Scan(&byEmail, &byIP); err != nil {
		return err
	}
	if byEmail >= perEmailHourly || byIP >= perIPHourly {
		return nil
	}
	var hasOrders bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM orders o
		    WHERE  o.org_id = $1 AND o.buyer_email ILIKE $2 AND o.source IN `+ticketOrderSources+`
		      AND  `+orderVisible+`)`, orgID, EscapeLike(email)).Scan(&hasOrders); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO my_tickets_link_requests (org_id, channel_id, email_normalized, locale, page_url, ip_hash, created_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7)
		RETURNING id`, orgID, channelID, email, salesubscribe.CleanLocale(in.Locale),
		salesubscribe.CleanPageURL(in.PageURL), in.IPHash, now).Scan(&id); err != nil {
		return err
	}
	if hasOrders {
		body, err := json.Marshal(JobPayload{RequestID: id.String()})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			VALUES ($1, $2::jsonb, 5, 'pending', now())`, JobTypeLinkEmail, body); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Session is an authenticated "my tickets" session.
type Session struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Email string
}

// Exchange spends a link token and opens a session. A second exchange of the
// same token, an expired or an unknown one is ErrLinkInvalid. It answers the
// bearer token and its expiry.
func (s *Service) Exchange(ctx context.Context, linkToken string) (string, time.Time, error) {
	linkToken = strings.TrimSpace(linkToken)
	if len(linkToken) != 64 {
		return "", time.Time{}, ErrLinkInvalid
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var orgID uuid.UUID
	var email string
	err = tx.QueryRow(ctx, `
		UPDATE my_tickets_link_requests SET used_at = $2
		WHERE  token_hash = $1 AND used_at IS NULL AND token_expires_at > $2
		RETURNING org_id, email_normalized`, HashToken(linkToken), now).Scan(&orgID, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, ErrLinkInvalid
	}
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(SessionTTL)
	if _, err := tx.Exec(ctx, `
		INSERT INTO my_tickets_sessions (org_id, email_normalized, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, orgID, email, HashToken(token), now, expires); err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Authenticate resolves a bearer token to its live session.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	token = strings.TrimSpace(token)
	if len(token) != 64 {
		return Session{}, ErrSessionInvalid
	}
	var sess Session
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, email_normalized FROM my_tickets_sessions
		WHERE  token_hash = $1 AND revoked_at IS NULL AND expires_at > $2`,
		HashToken(token), s.now().UTC()).Scan(&sess.ID, &sess.OrgID, &sess.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	return sess, err
}

// Revoke ends a session on the server ("log out").
func (s *Service) Revoke(ctx context.Context, sess Session) error {
	_, err := s.pool.Exec(ctx, `UPDATE my_tickets_sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`,
		sess.ID, s.now().UTC())
	return err
}

// Ticket is one ticket of a listed order.
type Ticket struct {
	ID        uuid.UUID
	Status    string
	Sector    *string
	Row       *string
	Number    *string
	TierName  *string
	HumanCode *string
}

// Order is one listed order.
type Order struct {
	ID          uuid.UUID
	OrderNumber int64
	Status      string
	Total       int64
	Currency    string
	PaidAt      *time.Time
	CreatedAt   time.Time
	EventID     uuid.UUID
	EventName   string
	EventSlug   *string
	SessionID   uuid.UUID
	StartAt     time.Time
	// SessionStatus is the session's own status; 'cancelled' means the
	// date was cancelled.
	SessionStatus string
	VenueName     *string
	Timezone      *string
	Tickets       []Ticket
}

// ListOrders returns one page of the session's orders, newest session first,
// and the total count. page is 1-based.
func (s *Service) ListOrders(ctx context.Context, sess Session, page int) ([]Order, int, error) {
	if page < 1 {
		page = 1
	}
	pattern := EscapeLike(sess.Email)
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM orders o
		WHERE  o.org_id = $1 AND o.buyer_email ILIKE $2 AND o.source IN `+ticketOrderSources+`
		  AND  `+orderVisible, sess.OrgID, pattern).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.system_id, o.status, o.total, o.currency, o.paid_at, o.created_at, o.checkout_session_id,
		       e.id, e.name, e.slug, s.id, s.start_at, s.status, v.name, v.timezone
		FROM   orders o
		JOIN   sessions s ON s.id = o.session_id
		JOIN   events e   ON e.id = o.event_id
		LEFT   JOIN venues v ON v.id = s.venue_id
		WHERE  o.org_id = $1 AND o.buyer_email ILIKE $2 AND o.source IN `+ticketOrderSources+`
		  AND  `+orderVisible+`
		ORDER  BY s.start_at DESC, o.created_at DESC, o.id
		LIMIT  $3 OFFSET $4`, sess.OrgID, pattern, PageSize, (page-1)*PageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var orders []Order
	var checkoutIDs []uuid.UUID
	for rows.Next() {
		var o Order
		var cs uuid.UUID
		if err := rows.Scan(&o.ID, &o.OrderNumber, &o.Status, &o.Total, &o.Currency, &o.PaidAt, &o.CreatedAt, &cs,
			&o.EventID, &o.EventName, &o.EventSlug, &o.SessionID, &o.StartAt, &o.SessionStatus, &o.VenueName, &o.Timezone); err != nil {
			return nil, 0, err
		}
		orders = append(orders, o)
		checkoutIDs = append(checkoutIDs, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	if len(orders) == 0 {
		return orders, total, nil
	}
	tix, err := s.pool.Query(ctx, `
		SELECT t.checkout_session_id, t.id, t.status, t.seat_sector, t.seat_row, t.seat_number, tt.name, c.human_code
		FROM   tickets t
		LEFT   JOIN ticket_tiers tt ON tt.id = t.tier_id
		LEFT   JOIN ticket_credentials c ON c.ticket_id = t.id AND c.type = 'static_qr'
		WHERE  t.checkout_session_id = ANY($1)
		ORDER  BY t.checkout_session_id, t.ordinal`, checkoutIDs)
	if err != nil {
		return nil, 0, err
	}
	defer tix.Close()
	byCS := map[uuid.UUID][]Ticket{}
	for tix.Next() {
		var cs uuid.UUID
		var t Ticket
		if err := tix.Scan(&cs, &t.ID, &t.Status, &t.Sector, &t.Row, &t.Number, &t.TierName, &t.HumanCode); err != nil {
			return nil, 0, err
		}
		byCS[cs] = append(byCS[cs], t)
	}
	if err := tix.Err(); err != nil {
		return nil, 0, err
	}
	for i := range orders {
		orders[i].Tickets = byCS[checkoutIDs[i]]
		if orders[i].Tickets == nil {
			orders[i].Tickets = []Ticket{}
		}
	}
	return orders, total, nil
}

// OwnsTicket reports whether the ticket belongs to the given order and the
// order belongs to the session's organization and address. Used before a PDF
// is handed out: a foreign organization, order or ticket answers false.
func (s *Service) OwnsTicket(ctx context.Context, sess Session, orderID, ticketID uuid.UUID) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1
		    FROM   orders o
		    JOIN   tickets t ON t.checkout_session_id = o.checkout_session_id
		    WHERE  o.id = $1 AND t.id = $2
		      AND  o.org_id = $3 AND o.buyer_email ILIKE $4 AND o.source IN `+ticketOrderSources+`
		      AND  `+orderVisible+`)`, orderID, ticketID, sess.OrgID, EscapeLike(sess.Email)).Scan(&ok)
	return ok, err
}
