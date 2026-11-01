// Package salesubscribe is "notify me when sales open" on a "coming soon"
// session (owner decisions 2026-10-31, migration 0183):
//
//   - A buyer leaves an address and ticks ONE consent; its version, time and
//     a salted hash of the client address are kept. The answer never says
//     whether the address was known (a 202 either way).
//   - Double opt-in: a confirmation letter (JobTypeConfirmEmail) carries a
//     one-time link; only its SHA-256 is stored, and the letter mints the
//     token at send time so it is never at rest in worker_jobs.
//   - When the session opens (salesstate.Effective is "open", by hand or
//     because a category's sale window started), every confirmed address
//     that was not told yet gets one letter (JobTypeOpenEmail) linking back
//     to the page it subscribed on, else to the event's page.
//   - Rows are kept: the consent also covers invitations to the organizer's
//     later events. A new subscription of the same address asks again.
package salesubscribe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

// ConsentVersion names the consent text the clients show. A client that sends
// another version showed an older text and must reload (ErrConsentOutdated).
const ConsentVersion = "sale-notify-2026-11-01"

// Worker jobs (handlers in delivery: NewSaleConfirmHandler, NewSaleOpenHandler).
const (
	JobTypeConfirmEmail = "sales_state.subscribe_confirm_email"
	JobTypeOpenEmail    = "sales_state.sale_open_email"
)

// Sources of a subscription.
const (
	SourceWidget = "widget"
	SourceSite   = "site"
)

const (
	// resendAfter: a repeated subscription of a pending address re-sends the
	// confirmation no more often than this.
	resendAfter = 10 * time.Minute
	// TokenTTL is how long a confirmation link works.
	TokenTTL = 7 * 24 * time.Hour
	// Per client address and per e-mail address, subscriptions an hour.
	perIPHourly    = 20
	perEmailHourly = 10
	maxPageURL     = 2000
)

var (
	ErrInvalidEmail    = errors.New("salesubscribe: invalid e-mail")
	ErrConsentRequired = errors.New("salesubscribe: consent required")
	ErrConsentOutdated = errors.New("salesubscribe: consent text outdated")
	ErrSalesOpen       = errors.New("salesubscribe: sales are open")
	ErrNotComingSoon   = errors.New("salesubscribe: session is not coming soon")
	ErrRateLimited     = errors.New("salesubscribe: too many subscriptions")
	ErrTokenInvalid    = errors.New("salesubscribe: link invalid or used")
)

// Target is the session a subscription is for, resolved by the caller from
// its own credential (a feed token, a gateway channel).
type Target struct {
	SessionID uuid.UUID
	EventID   uuid.UUID
	OrgID     uuid.UUID
	ChannelID *uuid.UUID
}

// Input is one "notify me" request.
type Input struct {
	Target
	Email          string
	Locale         string
	PageURL        string
	Source         string
	Consent        bool
	ConsentVersion string
	IPHash         string
}

// NormalizeEmail checks a bare address and lower-cases it.
func NormalizeEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 254 {
		return "", ErrInvalidEmail
	}
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Address != raw || !strings.Contains(raw[strings.LastIndex(raw, "@")+1:], ".") {
		return "", ErrInvalidEmail
	}
	return strings.ToLower(raw), nil
}

// CleanPageURL keeps an absolute http(s) page address without its fragment;
// anything else is dropped (the letter then links to the event's page).
func CleanPageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPageURL {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// CleanLocale keeps a short language tag ("es", "pt-BR"), else "".
func CleanLocale(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || len(raw) > 12 {
		return ""
	}
	for _, r := range raw {
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && r != '-' && r != '_' {
			return ""
		}
	}
	return raw
}

// Service writes and reads the subscriptions.
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

// Subscribe records a request and queues the confirmation letter when the
// address still has to confirm. A nil error is the only success answer the
// caller gives (202), whether the address was new, pending or confirmed.
func (s *Service) Subscribe(ctx context.Context, in Input) error {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return err
	}
	if !in.Consent {
		return ErrConsentRequired
	}
	if in.ConsentVersion != ConsentVersion {
		return ErrConsentOutdated
	}
	now := s.now().UTC()
	facts, err := gen.New(s.pool).ListSessionSalesFacts(ctx, []uuid.UUID{in.SessionID}, now)
	if err != nil {
		return err
	}
	f, ok := facts[in.SessionID]
	if !ok {
		return ErrNotComingSoon
	}
	switch salesstate.Effective(f.Manual, salesstate.FactsFromRow(f), now) {
	case salesstate.Soon:
	case salesstate.Open:
		return ErrSalesOpen
	default:
		return ErrNotComingSoon
	}
	hour := now.Add(-time.Hour)
	var byIP, byEmail int
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM session_sale_subscriptions WHERE $1 <> '' AND ip_hash = $1 AND updated_at > $3),
		       (SELECT count(*) FROM session_sale_subscriptions WHERE email_normalized = $2 AND updated_at > $3)`,
		in.IPHash, email, hour).Scan(&byIP, &byEmail); err != nil {
		return err
	}
	if byIP >= perIPHourly || byEmail >= perEmailHourly {
		return ErrRateLimited
	}
	source := in.Source
	if source != SourceSite {
		source = SourceWidget
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var (
		id       uuid.UUID
		status   string
		lastSent *time.Time
	)
	// A repeat keeps the row and its confirmation; a confirmed address that
	// was told about an earlier opening waits for the next one again.
	if err := tx.QueryRow(ctx, `
		INSERT INTO session_sale_subscriptions
		    (session_id, event_id, org_id, channel_id, email, email_normalized, locale, page_url, source,
		     consent_version, consent_at, ip_hash, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''), $11, $11)
		ON CONFLICT (session_id, email_normalized) DO UPDATE SET
		    channel_id = EXCLUDED.channel_id, email = EXCLUDED.email, locale = EXCLUDED.locale,
		    page_url = EXCLUDED.page_url, source = EXCLUDED.source,
		    consent_version = EXCLUDED.consent_version, consent_at = EXCLUDED.consent_at,
		    ip_hash = EXCLUDED.ip_hash, notified_at = NULL, updated_at = EXCLUDED.updated_at
		RETURNING id, status, confirm_sent_at`,
		in.SessionID, in.EventID, in.OrgID, in.ChannelID, strings.TrimSpace(in.Email), email,
		CleanLocale(in.Locale), CleanPageURL(in.PageURL), source, in.ConsentVersion, now, in.IPHash,
	).Scan(&id, &status, &lastSent); err != nil {
		return err
	}
	if status == "pending" && (lastSent == nil || now.Sub(*lastSent) >= resendAfter) {
		if _, err := tx.Exec(ctx, `UPDATE session_sale_subscriptions SET confirm_sent_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, JobTypeConfirmEmail, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// JobPayload is the payload of both letters.
type JobPayload struct {
	SubscriptionID string `json:"subscription_id"`
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func enqueue(ctx context.Context, db execer, jobType string, id uuid.UUID) error {
	body, err := json.Marshal(JobPayload{SubscriptionID: id.String()})
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, 5, 'pending', now())`, jobType, body)
	return err
}

// HashToken is what the table stores of a confirmation token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// NewToken mints a confirmation token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Confirmed is what a confirmation link resolves to.
type Confirmed struct {
	SubscriptionID uuid.UUID
	SessionID      uuid.UUID
	EventID        uuid.UUID
	OrgID          uuid.UUID
	PageURL        string
}

// Confirm redeems a link: the pending subscription becomes confirmed and the
// token dies. A used, expired or unknown token is ErrTokenInvalid.
func (s *Service) Confirm(ctx context.Context, token string) (Confirmed, error) {
	if len(strings.TrimSpace(token)) != 64 {
		return Confirmed{}, ErrTokenInvalid
	}
	now := s.now().UTC()
	var c Confirmed
	err := s.pool.QueryRow(ctx, `
		UPDATE session_sale_subscriptions
		SET    status = 'confirmed', confirmed_at = $2, confirm_token_hash = NULL, updated_at = $2
		WHERE  confirm_token_hash = $1 AND status = 'pending' AND confirm_sent_at > $3
		RETURNING id, session_id, event_id, org_id, page_url`,
		HashToken(token), now, now.Add(-TokenTTL)).Scan(&c.SubscriptionID, &c.SessionID, &c.EventID, &c.OrgID, &c.PageURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return Confirmed{}, ErrTokenInvalid
	}
	return c, err
}

// ClaimOpened finds the sessions with confirmed addresses not told yet,
// and for every one of them that is open now marks those addresses told and
// queues one letter each, in one transaction per session. It answers how
// many letters it queued.
func ClaimOpened(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT s.session_id FROM session_sale_subscriptions s
		WHERE  s.status = 'confirmed' AND s.notified_at IS NULL`)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	facts, err := gen.New(pool).ListSessionSalesFacts(ctx, ids, now)
	if err != nil {
		return 0, err
	}
	queued := 0
	for id, f := range facts {
		if f.Status == "cancelled" || salesstate.Effective(f.Manual, salesstate.FactsFromRow(f), now) != salesstate.Open {
			continue
		}
		n, err := claimSession(ctx, pool, id, now)
		queued += n
		if err != nil {
			return queued, err
		}
	}
	return queued, nil
}

func claimSession(ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID, now time.Time) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		UPDATE session_sale_subscriptions SET notified_at = $2, updated_at = $2
		WHERE  session_id = $1 AND status = 'confirmed' AND notified_at IS NULL
		RETURNING id`, sessionID, now)
	if err != nil {
		return 0, err
	}
	subs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	for _, id := range subs {
		if err := enqueue(ctx, tx, JobTypeOpenEmail, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(subs), nil
}
