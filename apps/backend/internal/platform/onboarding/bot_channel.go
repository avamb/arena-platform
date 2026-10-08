package onboarding

// bot_channel.go — what the Telegram bot needs from the application flow
// (08_architecture/34_onboarding_applications_ru.md §10). The bot is a thin
// client: it keeps no answers, every call names the Telegram account, and the
// HTTP layer in front of these methods is guarded by BOT_SERVICE_TOKEN.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const (
	emailCodeTTL        = 15 * time.Minute
	emailCodeMaxGuesses = 5
	maxCodesPerHour     = 3
)

// ErrCodeInvalid means the typed code is wrong, expired or used up.
var ErrCodeInvalid = errors.New("onboarding: the code is wrong or expired")

// BotStartInput is the first screen of the bot's form.
type BotStartInput struct {
	TelegramUserID   int64
	TelegramUsername string
	FirstName        string
	LastName         string
	// Phone comes from Telegram's own contact button, so the number belongs to
	// the account that sent it.
	Phone  string
	Email  string
	Locale string
}

// BotStart returns the person's open application, or creates one. Asking
// twice never opens a second draft.
func (s *Service) BotStart(ctx context.Context, in BotStartInput) (*Application, bool, error) {
	if in.TelegramUserID <= 0 {
		return nil, false, &FieldsError{Code: "invalid_field", Fields: FieldErrors{"telegram_user_id": "required"}}
	}
	if app, err := s.BotOpen(ctx, in.TelegramUserID); err == nil {
		return app, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	tg := in.TelegramUserID
	started, err := s.Start(ctx, StartInput{
		FirstName: in.FirstName, LastName: in.LastName, Email: in.Email, Phone: in.Phone,
		Locale: in.Locale, Source: SourceTelegram, TelegramUserID: &tg,
	})
	if err != nil {
		return nil, false, err
	}
	if u := strings.TrimPrefix(strings.TrimSpace(in.TelegramUsername), "@"); u != "" {
		if f, _ := LookupField("telegram_username"); f.Key != "" {
			if v, reason := NormalizeValue(f, u); reason == "" && v != nil {
				_, _ = s.saveAnswers(ctx, telegramKey(started.App.ID, tg), map[string]any{"telegram_username": v})
			}
		}
	}
	app, err := s.get(ctx, telegramKey(started.App.ID, tg))
	return app, true, err
}

// BotOpen returns the newest application of a Telegram account that is still
// in play (draft, details requested, waiting or expired-but-not-erased).
func (s *Service) BotOpen(ctx context.Context, tg int64) (*Application, error) {
	app, err := scanApplication(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM onboarding_applications
WHERE telegram_user_id = $1 AND purged_at IS NULL AND status IN ('draft', 'info_requested', 'pending_approval', 'expired')
ORDER BY created_at DESC LIMIT 1`, tg))
	return orNotFound(app, err)
}

// BotGet returns one of the account's applications.
func (s *Service) BotGet(ctx context.Context, id uuid.UUID, tg int64) (*Application, error) {
	return s.get(ctx, telegramKey(id, tg))
}

// BotSaveAnswers is SaveAnswers for the bot.
func (s *Service) BotSaveAnswers(ctx context.Context, id uuid.UUID, tg int64, patch map[string]any) (*Application, error) {
	return s.saveAnswers(ctx, telegramKey(id, tg), patch)
}

// BotSubmit is Submit for the bot.
func (s *Service) BotSubmit(ctx context.Context, id uuid.UUID, tg int64) (*Application, error) {
	return s.submit(ctx, telegramKey(id, tg))
}

// newEmailCode returns a uniformly random six-digit code.
func newEmailCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func codeHash(id uuid.UUID, code string) string {
	return users.TokenHash(id.String() + ":" + code)
}

// issueEmailCode stores a fresh code and queues its e-mail on tx.
func (s *Service) issueEmailCode(ctx context.Context, tx pgx.Tx, app *Application) error {
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE onboarding_applications
SET email_code_hash = $2, email_code_expires_at = $3, email_code_attempts = 0 WHERE id = $1`,
		app.ID, codeHash(app.ID, code), now.Add(emailCodeTTL)); err != nil {
		return err
	}
	name := ""
	if app.Answers != nil {
		name = app.Answers.String("first_name")
	}
	if err := s.queueRaw(ctx, tx, authemail.OnboardingEmailPayload{
		Kind: authemail.OnboardingKindCode, ApplicationID: app.ID.String(), Email: app.Email,
		FirstName: name, Locale: app.Locale, Code: code,
	}); err != nil {
		return err
	}
	return addEvent(ctx, tx, app.ID, "code_sent", ActorApplicant, map[string]any{"channel": "telegram"}, nil)
}

// BotSendCode e-mails a new confirmation code, at most three an hour.
func (s *Service) BotSendCode(ctx context.Context, id uuid.UUID, tg int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	app, err := telegramKey(id, tg).lock(ctx, tx)
	if err != nil {
		return err
	}
	if app.EmailConfirmedAt != nil || app.Status != StatusDraft && app.Status != StatusExpired {
		return ErrWrongState
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM onboarding_application_events
WHERE application_id = $1 AND kind = 'code_sent' AND created_at > now() - interval '1 hour'`, id).Scan(&recent); err != nil {
		return err
	}
	if recent >= maxCodesPerHour {
		return ErrRateLimited
	}
	if err := s.issueEmailCode(ctx, tx, app); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BotConfirmEmail checks the typed code. A wrong guess is counted and, after
// five, the code is burnt: a new one has to be requested.
func (s *Service) BotConfirmEmail(ctx context.Context, id uuid.UUID, tg int64, code string) (*Application, error) {
	code = strings.TrimSpace(code)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	app, err := telegramKey(id, tg).lock(ctx, tx)
	if err != nil {
		return nil, err
	}
	if app.EmailConfirmedAt != nil {
		return app, nil
	}
	var hash *string
	var expires *time.Time
	var attempts int16
	if err := tx.QueryRow(ctx, `SELECT email_code_hash, email_code_expires_at, email_code_attempts
FROM onboarding_applications WHERE id = $1`, id).Scan(&hash, &expires, &attempts); err != nil {
		return nil, err
	}
	now := s.now()
	if hash == nil || expires == nil || now.After(*expires) || int(attempts) >= emailCodeMaxGuesses {
		return nil, ErrCodeInvalid
	}
	if codeHash(id, code) != *hash {
		if _, err := tx.Exec(ctx, `UPDATE onboarding_applications SET email_code_attempts = email_code_attempts + 1 WHERE id = $1`, id); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrCodeInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE onboarding_applications
SET email_confirmed_at = $2, email_code_hash = NULL, email_code_expires_at = NULL, email_code_attempts = 0,
    last_activity_at = $2, updated_at = $2 WHERE id = $1`, id, now); err != nil {
		return nil, err
	}
	if err := addEvent(ctx, tx, id, "email_confirmed", ActorApplicant, map[string]any{"channel": "telegram"}, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.get(ctx, telegramKey(id, tg))
}

// BotSiteLink rotates the continue-link token and returns the website URL
// that opens this application in a browser. The person is already known to
// the bot, so the link is handed over in the chat, not by e-mail.
func (s *Service) BotSiteLink(ctx context.Context, id uuid.UUID, tg int64) (string, error) {
	if s.siteURL == "" {
		return "", ErrWrongState
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	app, err := telegramKey(id, tg).lock(ctx, tx)
	if err != nil {
		return "", err
	}
	if app.Status == StatusApproved || app.Status == StatusRejected || app.EmailConfirmedAt == nil {
		return "", ErrWrongState
	}
	resume, hash, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE onboarding_applications SET resume_token_hash = $2, resume_token_expires_at = $3 WHERE id = $1`,
		id, hash, s.now().Add(resumeLinkTTL)); err != nil {
		return "", err
	}
	if err := addEvent(ctx, tx, id, "link_sent", ActorApplicant, map[string]any{"channel": "telegram"}, nil); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return s.siteURL + "/start/confirm?token=" + url.QueryEscape(resume) + "&lang=" + url.QueryEscape(app.Locale), nil
}

// SiteLinks returns the website's terms and privacy pages for the consent screen.
func (s *Service) SiteLinks() (terms, privacy string) {
	if s.siteURL == "" {
		return "", ""
	}
	return s.siteURL + "/terms", s.siteURL + "/privacy"
}

// BotNotice is one decision the bot has to tell an applicant about.
type BotNotice struct {
	ApplicationID  uuid.UUID `json:"application_id"`
	TelegramUserID int64     `json:"telegram_user_id"`
	Status         string    `json:"status"`
	Locale         string    `json:"locale"`
	OrgName        string    `json:"org_name"`
	Message        string    `json:"message"`
}

// BotClaimNotices hands the bot the decisions it has not announced yet and
// marks them announced, so each is delivered once (at-most-once: a crash
// between the claim and the send loses one message rather than repeating it).
func (s *Service) BotClaimNotices(ctx context.Context, limit int) ([]BotNotice, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
WITH due AS (
  SELECT id FROM onboarding_applications
  WHERE telegram_user_id IS NOT NULL AND purged_at IS NULL
    AND status IN ('approved', 'rejected', 'info_requested')
    AND telegram_notified_status IS DISTINCT FROM status
  ORDER BY updated_at LIMIT $1 FOR UPDATE SKIP LOCKED)
UPDATE onboarding_applications a SET telegram_notified_status = a.status
FROM due WHERE a.id = due.id
RETURNING a.id, a.telegram_user_id, a.status, a.locale, coalesce(a.org_name, ''), coalesce(a.info_request_message, '')`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BotNotice{}
	for rows.Next() {
		var n BotNotice
		if err := rows.Scan(&n.ApplicationID, &n.TelegramUserID, &n.Status, &n.Locale, &n.OrgName, &n.Message); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
