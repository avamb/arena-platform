// Package marketing is the buyer's consent to an organization's mailings and
// the discount it earns (spec 51-1, owner decisions 2026-11-01/02):
//
//   - The organizer switches the tick on (org_growth_settings) and may name
//     one of the organization's own promo codes as the reward. The code must
//     be limited to one use per buyer.
//   - The widget shows the tick below the e-mail, never ticked in advance and
//     never required. A ticked consent is recorded at checkout/start in the
//     checkout transaction: the current state in marketing_subscriptions and
//     one row of the marketing_consents journal (evidence, never updated).
//   - Double opt-in: an address that has not confirmed gets one letter with a
//     confirmation link; the token is minted at send time, only its SHA-256 is
//     stored. The discount is given on the order at once, the organizer's
//     export lists confirmed addresses only.
//   - Every subscription carries a withdraw-only token; its link works with
//     no sign-in and is printed in the confirmation letter and the export.
package marketing

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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
)

// TextVersion names the consent text the clients show. A client that sends
// another version showed an older text and must reload (ErrTextOutdated).
// Change it together with MARKETING_TEXT_VERSION in the widget whenever
// the meaning of the text changes.
const TextVersion = "marketing-optin-2026-11-02"

// JobTypeConfirmEmail is the double opt-in letter (handler in delivery).
const JobTypeConfirmEmail = "marketing.confirm_email"

// Journal sources.
const (
	SourceWidget      = "widget"
	SourceConfirmLink = "confirm_link"
	SourceWithdraw    = "withdraw_link"
	SourceOrganizer   = "organizer"
	SourceErasure     = "erasure"
)

// Subscription statuses.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusWithdrawn = "withdrawn"
)

const (
	// resendAfter: a new grant of a pending address re-sends the letter no
	// more often than this.
	resendAfter = 10 * time.Minute
	// TokenTTL is how long a confirmation link works.
	TokenTTL     = 7 * 24 * time.Hour
	maxUserAgent = 512
	maxName      = 200
)

var (
	ErrInvalidEmail     = errors.New("marketing: invalid e-mail")
	ErrTextOutdated     = errors.New("marketing: consent text outdated")
	ErrTokenInvalid     = errors.New("marketing: link invalid or used")
	ErrCodeNotFound     = errors.New("marketing: promo code not found in the organization")
	ErrCodeNeedsPerUser = errors.New("marketing: the promo code must be limited to one use per buyer")
)

// DB is the database handle the package reads and writes through: a pool or
// a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Settings are one organization's switches. A missing row is the defaults.
type Settings struct {
	CartReminder       bool
	OptInEnabled       bool
	ConsentPromoCodeID *uuid.UUID
	UpdatedAt          *time.Time
}

// DefaultSettings: reminder on, tick off, no reward (owner decision В-4).
func DefaultSettings() Settings { return Settings{CartReminder: true} }

// LoadSettings reads the organization's switches.
func LoadSettings(ctx context.Context, db DB, orgID uuid.UUID) (Settings, error) {
	s := DefaultSettings()
	var updated time.Time
	err := db.QueryRow(ctx, `
		SELECT cart_reminder_enabled, marketing_optin_enabled, consent_promo_code_id, updated_at
		FROM   org_growth_settings WHERE org_id = $1`, orgID).
		Scan(&s.CartReminder, &s.OptInEnabled, &s.ConsentPromoCodeID, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	s.UpdatedAt = &updated
	return s, nil
}

// SaveSettings stores the switches. A named promo code must be a live code of
// the same organization limited to one use per buyer.
func SaveSettings(ctx context.Context, db DB, orgID uuid.UUID, in Settings, actor *uuid.UUID) (Settings, error) {
	if in.ConsentPromoCodeID != nil {
		row, err := gen.New(db).GetPromoCodeByID(ctx, *in.ConsentPromoCodeID, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Settings{}, ErrCodeNotFound
		}
		if err != nil {
			return Settings{}, err
		}
		manual, err := isOrganizerCode(ctx, db, row.ID)
		if err != nil {
			return Settings{}, err
		}
		if !manual {
			return Settings{}, ErrCodeNotFound
		}
		if row.MaxUsesPerCustomer == nil || *row.MaxUsesPerCustomer != 1 {
			return Settings{}, ErrCodeNeedsPerUser
		}
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO org_growth_settings
		    (org_id, cart_reminder_enabled, marketing_optin_enabled, consent_promo_code_id, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, now(), $5)
		ON CONFLICT (org_id) DO UPDATE SET
		    cart_reminder_enabled = EXCLUDED.cart_reminder_enabled,
		    marketing_optin_enabled = EXCLUDED.marketing_optin_enabled,
		    consent_promo_code_id = EXCLUDED.consent_promo_code_id,
		    updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`,
		orgID, in.CartReminder, in.OptInEnabled, in.ConsentPromoCodeID, actor); err != nil {
		return Settings{}, err
	}
	return LoadSettings(ctx, db, orgID)
}

// Offer is what a checkout of the organization offers: the tick, and the
// code the tick earns when the organizer named a live one.
type Offer struct {
	Enabled bool
	Code    *gen.PromoCodeRow
}

// LoadOffer reads the settings and the reward code. A reward code that was
// deleted or paused is no reward: the tick is still offered.
func LoadOffer(ctx context.Context, db DB, orgID uuid.UUID) (Offer, error) {
	s, err := LoadSettings(ctx, db, orgID)
	if err != nil || !s.OptInEnabled {
		return Offer{}, err
	}
	o := Offer{Enabled: true}
	if s.ConsentPromoCodeID == nil {
		return o, nil
	}
	row, err := gen.New(db).GetPromoCodeByID(ctx, *s.ConsentPromoCodeID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, nil
	}
	if err != nil {
		return Offer{}, err
	}
	if row.Status != "active" {
		return o, nil
	}
	manual, err := isOrganizerCode(ctx, db, row.ID)
	if err != nil {
		return Offer{}, err
	}
	if manual {
		o.Code = &row
	}
	return o, nil
}

// isOrganizerCode reports whether a promo code is one the organizer made. A
// personal "Refer a friend" code (spec 52) is never a reward for the tick: it
// is limited to one use per buyer like a reward code, and every subscriber
// would then get the friend's discount and pay its owner a cashback.
func isOrganizerCode(ctx context.Context, db DB, id uuid.UUID) (bool, error) {
	meta, err := gen.New(db).GetReferralPromoMeta(ctx, id)
	if err != nil {
		return false, err
	}
	return meta.Origin == "manual", nil
}

// IsRewardCode reports whether a code is the organization's reward for the
// tick: a buyer who types it without ticking is refused.
func (o Offer) IsRewardCode(id uuid.UUID) bool {
	return o.Code != nil && o.Code.ID == id
}

// Grant is one ticked consent at checkout.
type Grant struct {
	OrgID             uuid.UUID
	Email             string
	Name              string
	Locale            string
	TextVersion       string
	CheckoutSessionID *uuid.UUID
	PageURL           string
	ClientIP          string
	UserAgent         string
	Now               time.Time
}

// NormalizeEmail checks a bare address and lower-cases it.
func NormalizeEmail(raw string) (string, error) {
	e, err := salesubscribe.NormalizeEmail(raw)
	if err != nil {
		return "", ErrInvalidEmail
	}
	return e, nil
}

// RecordGrantTx writes the grant inside the caller's transaction: the
// subscription's current state, one journal row, and the confirmation letter
// when the address still has to confirm. A confirmed address stays confirmed.
func RecordGrantTx(ctx context.Context, tx DB, g Grant) (uuid.UUID, error) {
	email, err := NormalizeEmail(g.Email)
	if err != nil {
		return uuid.Nil, err
	}
	if g.TextVersion != TextVersion {
		return uuid.Nil, ErrTextOutdated
	}
	now := g.Now.UTC()
	if g.Now.IsZero() {
		now = time.Now().UTC()
	}
	token, err := newToken()
	if err != nil {
		return uuid.Nil, err
	}
	var (
		id       uuid.UUID
		status   string
		lastSent *time.Time
	)
	if err := tx.QueryRow(ctx, `
		INSERT INTO marketing_subscriptions
		    (org_id, email, email_normalized, buyer_name, locale, status, text_version, source,
		     page_url, granted_at, unsubscribe_token, created_at, updated_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, 'pending', $6, $7, $8, $9, $10, $9, $9)
		ON CONFLICT (org_id, email_normalized) DO UPDATE SET
		    email = EXCLUDED.email,
		    buyer_name = COALESCE(EXCLUDED.buyer_name, marketing_subscriptions.buyer_name),
		    locale = EXCLUDED.locale, text_version = EXCLUDED.text_version,
		    page_url = EXCLUDED.page_url, granted_at = EXCLUDED.granted_at,
		    status = CASE WHEN marketing_subscriptions.status = 'confirmed' THEN 'confirmed' ELSE 'pending' END,
		    withdrawn_at = NULL, updated_at = EXCLUDED.updated_at
		RETURNING id, status, confirm_sent_at`,
		g.OrgID, strings.TrimSpace(g.Email), email, capRunes(strings.TrimSpace(g.Name), maxName),
		salesubscribe.CleanLocale(g.Locale), g.TextVersion, SourceWidget,
		salesubscribe.CleanPageURL(g.PageURL), now, token,
	).Scan(&id, &status, &lastSent); err != nil {
		return uuid.Nil, err
	}
	if err := journal(ctx, tx, id, g.OrgID, "granted", g.TextVersion, g.Locale, SourceWidget,
		g.CheckoutSessionID, g.ClientIP, g.UserAgent, now); err != nil {
		return uuid.Nil, err
	}
	if status == StatusPending && (lastSent == nil || now.Sub(*lastSent) >= resendAfter) {
		if _, err := tx.Exec(ctx, `UPDATE marketing_subscriptions SET confirm_sent_at = $2 WHERE id = $1`, id, now); err != nil {
			return uuid.Nil, err
		}
		if err := enqueue(ctx, tx, id); err != nil {
			return uuid.Nil, err
		}
	}
	return id, nil
}

func journal(ctx context.Context, db DB, subID, orgID uuid.UUID, action, version, locale, source string,
	checkoutID *uuid.UUID, ip, ua string, at time.Time) error {
	_, err := db.Exec(ctx, `
		INSERT INTO marketing_consents
		    (subscription_id, org_id, action, text_version, locale, source, checkout_session_id,
		     client_ip, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''), $10)`,
		subID, orgID, action, version, salesubscribe.CleanLocale(locale), source, checkoutID,
		strings.TrimSpace(ip), capRunes(ua, maxUserAgent), at)
	return err
}

// JobPayload is the payload of the confirmation letter.
type JobPayload struct {
	SubscriptionID string `json:"subscription_id"`
}

func enqueue(ctx context.Context, db DB, id uuid.UUID) error {
	body, err := json.Marshal(JobPayload{SubscriptionID: id.String()})
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, 5, 'pending', now())`, JobTypeConfirmEmail, body)
	return err
}

// HashToken is what the table stores of a confirmation token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// NewToken mints a token (64 hex characters).
func NewToken() (string, error) { return newToken() }

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Visit is the click on a link: who clicked, from where.
type Visit struct {
	ClientIP  string
	UserAgent string
	Now       time.Time
}

// Landing is where a link sends the buyer afterwards.
type Landing struct {
	OrgID   uuid.UUID
	PageURL string
	Locale  string
	OrgName string
}

// Confirm redeems a confirmation link: the pending subscription becomes
// confirmed and the token dies. A used, expired or unknown token is
// ErrTokenInvalid.
func Confirm(ctx context.Context, db TxBeginner, token string, v Visit) (Landing, error) {
	if len(strings.TrimSpace(token)) != 64 {
		return Landing{}, ErrTokenInvalid
	}
	now := v.Now.UTC()
	if v.Now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return Landing{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var (
		id      uuid.UUID
		l       Landing
		version string
	)
	err = tx.QueryRow(ctx, `
		UPDATE marketing_subscriptions
		SET    status = 'confirmed', confirmed_at = $2, confirm_token_hash = NULL, updated_at = $2
		WHERE  confirm_token_hash = $1 AND status = 'pending' AND confirm_sent_at > $3
		RETURNING id, org_id, page_url, locale, text_version`,
		HashToken(strings.TrimSpace(token)), now, now.Add(-TokenTTL)).Scan(&id, &l.OrgID, &l.PageURL, &l.Locale, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Landing{}, ErrTokenInvalid
	}
	if err != nil {
		return Landing{}, err
	}
	if err := journal(ctx, tx, id, l.OrgID, "confirmed", version, l.Locale, SourceConfirmLink, nil, v.ClientIP, v.UserAgent, now); err != nil {
		return Landing{}, err
	}
	return l, tx.Commit(ctx)
}

// LookupConfirm reads what a live confirmation token names, without changing
// anything: the link's GET only shows a page with a button, because mail
// scanners open every link of a letter by themselves and a scanner's click
// must never stand as the buyer's confirmation. A used, expired or unknown
// token is ErrTokenInvalid.
func LookupConfirm(ctx context.Context, db DB, token string, now time.Time) (Landing, error) {
	if len(strings.TrimSpace(token)) != 64 {
		return Landing{}, ErrTokenInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	var l Landing
	err := db.QueryRow(ctx, `
		SELECT s.org_id, s.page_url, s.locale, o.name
		FROM   marketing_subscriptions s JOIN organizations o ON o.id = s.org_id
		WHERE  s.confirm_token_hash = $1 AND s.status = 'pending' AND s.confirm_sent_at > $2`,
		HashToken(strings.TrimSpace(token)), now.UTC().Add(-TokenTTL)).Scan(&l.OrgID, &l.PageURL, &l.Locale, &l.OrgName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Landing{}, ErrTokenInvalid
	}
	return l, err
}

// TxBeginner opens a transaction (a pool).
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Unsubscribed is what a withdraw-only token names.
type Unsubscribed struct {
	Landing
	Email     string
	Withdrawn bool // true when it was already withdrawn before this visit
}

// LookupUnsubscribe reads what a withdraw-only token names, without
// changing anything (the page asks before it withdraws: mail scanners open
// links by themselves).
func LookupUnsubscribe(ctx context.Context, db DB, token string) (Unsubscribed, error) {
	token = strings.TrimSpace(token)
	if len(token) != 64 {
		return Unsubscribed{}, ErrTokenInvalid
	}
	var u Unsubscribed
	var status string
	err := db.QueryRow(ctx, `
		SELECT s.org_id, s.page_url, s.locale, o.name, s.email, s.status
		FROM   marketing_subscriptions s JOIN organizations o ON o.id = s.org_id
		WHERE  s.unsubscribe_token = $1`, token).
		Scan(&u.OrgID, &u.PageURL, &u.Locale, &u.OrgName, &u.Email, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Unsubscribed{}, ErrTokenInvalid
	}
	u.Withdrawn = status == StatusWithdrawn
	return u, err
}

// Withdraw ends the consent the token names. A second withdrawal changes
// nothing and is not an error.
func Withdraw(ctx context.Context, db TxBeginner, token string, v Visit) (Unsubscribed, error) {
	token = strings.TrimSpace(token)
	if len(token) != 64 {
		return Unsubscribed{}, ErrTokenInvalid
	}
	now := v.Now.UTC()
	if v.Now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return Unsubscribed{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	u, err := LookupUnsubscribe(ctx, tx, token)
	if err != nil {
		return Unsubscribed{}, err
	}
	if u.Withdrawn {
		return u, nil
	}
	var (
		id      uuid.UUID
		version string
	)
	if err := tx.QueryRow(ctx, `
		UPDATE marketing_subscriptions
		SET    status = 'withdrawn', withdrawn_at = $2, confirm_token_hash = NULL, updated_at = $2
		WHERE  unsubscribe_token = $1 AND status <> 'withdrawn'
		RETURNING id, text_version`, token, now).Scan(&id, &version); err != nil {
		return Unsubscribed{}, err
	}
	if err := journal(ctx, tx, id, u.OrgID, "withdrawn", version, u.Locale, SourceWithdraw, nil, v.ClientIP, v.UserAgent, now); err != nil {
		return Unsubscribed{}, err
	}
	return u, tx.Commit(ctx)
}

// ForgetEmailTx is the GDPR erasure of one address in every organization:
// the subscription is withdrawn, its address and name are blanked, and the
// journal keeps only the facts (who, when, what text), without the client
// address and browser.
func ForgetEmailTx(ctx context.Context, tx DB, emailNormalized string, now time.Time) error {
	rows, err := tx.Query(ctx, `
		SELECT id, org_id, text_version, status FROM marketing_subscriptions WHERE email_normalized = $1`,
		strings.ToLower(strings.TrimSpace(emailNormalized)))
	if err != nil {
		return err
	}
	type sub struct {
		id, org         uuid.UUID
		version, status string
	}
	var subs []sub
	for rows.Next() {
		var s sub
		if err := rows.Scan(&s.id, &s.org, &s.version, &s.status); err != nil {
			rows.Close()
			return err
		}
		subs = append(subs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range subs {
		if s.status != StatusWithdrawn {
			if err := journal(ctx, tx, s.id, s.org, "withdrawn", s.version, "", SourceErasure, nil, "", "", now); err != nil {
				return err
			}
		}
		erased := "erased-" + s.id.String()
		if _, err := tx.Exec(ctx, `
			UPDATE marketing_subscriptions
			SET    status = 'withdrawn', withdrawn_at = COALESCE(withdrawn_at, $2), email = $3,
			       email_normalized = $3, buyer_name = NULL, page_url = '', confirm_token_hash = NULL, updated_at = $2
			WHERE  id = $1`, s.id, now, erased); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE marketing_consents SET client_ip = NULL, user_agent = NULL WHERE subscription_id = $1`, s.id); err != nil {
			return err
		}
	}
	return nil
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
