package onboarding

// admin_notify.go — a new (or re-submitted) application reaches every platform
// superadmin personally: one Telegram message per linked Telegram account (sent
// by the event-center bot, EVENTS_TELEGRAM_BOT_TOKEN) and one e-mail. The ops
// chat is only the fallback when there is no superadmin to tell.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

const (
	// JobTypeAdminTelegram is the worker_jobs.job_type of one personal Telegram
	// message to a platform admin.
	JobTypeAdminTelegram = "onboarding.admin_telegram"
	// eventAdminsNotified marks that the admins of one submission round were
	// told, so a retried onboarding.notify job sends nothing twice.
	eventAdminsNotified = "admins_notified"
)

// AdminTelegramPayload is the payload of an onboarding.admin_telegram job.
type AdminTelegramPayload struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// adminRecipient is one platform superadmin.
type adminRecipient struct {
	userID uuid.UUID
	email  string
	chats  []int64
}

// adminMessage returns the headline and the plain-text detail lines shared by
// the e-mail and the Telegram message.
func adminMessage(app *Application, would, again bool) (title string, details []string) {
	val := func(p *string) string {
		if p == nil || strings.TrimSpace(*p) == "" {
			return "—"
		}
		return *p
	}
	title = "Новая заявка на онбординг"
	if again {
		title = "Заявка отправлена повторно после уточнений"
	}
	verdict := "нет"
	if would {
		verdict = "да"
	}
	details = []string{
		"Организация: " + val(app.OrgName),
		"Страна: " + val(app.Country),
		"Заявитель: " + val(app.ApplicantName),
		"E-mail: " + app.Email,
		"Телефон: " + val(app.Phone),
		"Источник: " + app.Source,
		"Система одобрила бы: " + verdict,
	}
	return title, details
}

const adminWhatToDo = "Что делать: откройте заявку в админке, проверьте данные и одобрите её, отклоните или запросите уточнения."

// adminTelegramText renders the Telegram (HTML) message.
func adminTelegramText(title string, details []string, link string) string {
	e := opsalert.EscapeHTML
	lines := []string{"🆕 <b>" + e(title) + "</b>"}
	for _, d := range details {
		lines = append(lines, e(d))
	}
	lines = append(lines, "", e(adminWhatToDo))
	if link != "" {
		lines = append(lines, link)
	}
	return strings.Join(lines, "\n")
}

// admins lists the active platform superadmins with their live Telegram links.
func (s *Service) admins(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]adminRecipient, error) {
	rows, err := q.Query(ctx, `
SELECT u.id, u.email, coalesce((SELECT array_agg(l.telegram_user_id ORDER BY l.telegram_user_id)
                                FROM bot_telegram_links l WHERE l.user_id = u.id AND l.revoked_at IS NULL), '{}')
FROM users u
WHERE u.deactivated_at IS NULL
  AND (EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
               WHERE ur.user_id = u.id AND ur.org_id IS NULL AND r.org_id IS NULL AND r.name = 'platform_superadmin')
    OR EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = u.id AND m.role = 'platform_superadmin' AND m.status = 'active'))
ORDER BY u.email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adminRecipient
	for rows.Next() {
		var a adminRecipient
		if err := rows.Scan(&a.userID, &a.email, &a.chats); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// notifyAdmins tells every platform superadmin about a submission. It answers
// false when there is nobody to tell (the caller falls back to the ops chat).
// Everything is queued in ONE transaction together with the marker of the
// submission round, so a retry of the job sends nothing twice.
func (s *Service) notifyAdmins(ctx context.Context, app *Application, would, again bool) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Serialize concurrent runs of the same job.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM onboarding_applications WHERE id = $1 FOR UPDATE`, app.ID); err != nil {
		return false, err
	}
	round := ""
	err = tx.QueryRow(ctx, `SELECT id::text FROM onboarding_application_events
WHERE application_id = $1 AND kind IN ('submitted', 'resubmitted') ORDER BY created_at DESC, id DESC LIMIT 1`, app.ID).Scan(&round)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if round == "" && app.SubmittedAt != nil {
		round = app.SubmittedAt.UTC().Format(time.RFC3339Nano)
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM onboarding_application_events
WHERE application_id = $1 AND kind = $2 AND detail->>'round' = $3)`, app.ID, eventAdminsNotified, round).Scan(&done); err != nil {
		return false, err
	}
	if done {
		return true, nil
	}
	admins, err := s.admins(ctx, tx)
	if err != nil {
		return false, err
	}
	if len(admins) == 0 {
		return false, nil
	}

	title, details := adminMessage(app, would, again)
	link := s.link(app.ID)
	text := adminTelegramText(title, details, link)
	org := ""
	if app.OrgName != nil {
		org = *app.OrgName
	}
	telegramSkipped := false
	for _, a := range admins {
		if _, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypeOnboardingEmail, authemail.OnboardingEmailPayload{
			Kind:          authemail.OnboardingKindAdminNewApplication,
			ApplicationID: app.ID.String(),
			Email:         a.email,
			UserID:        a.userID.String(),
			Locale:        "ru",
			OrgName:       org,
			Title:         title,
			Details:       details,
			Link:          link,
		}, emailJobAttempts); err != nil {
			return false, fmt.Errorf("onboarding: queue admin e-mail: %w", err)
		}
		for _, chat := range a.chats {
			if !s.adminTelegram {
				telegramSkipped = true
				continue
			}
			if _, err := worker.EnqueueInTx(ctx, tx, JobTypeAdminTelegram, AdminTelegramPayload{ChatID: chat, Text: text}, emailJobAttempts); err != nil {
				return false, fmt.Errorf("onboarding: queue admin Telegram message: %w", err)
			}
		}
	}
	if telegramSkipped {
		s.logger.Warn("onboarding: EVENTS_TELEGRAM_BOT_TOKEN is not set on the worker, platform admins get the new application by e-mail only")
	}
	if err := addEvent(ctx, tx, app.ID, eventAdminsNotified, ActorSystem, map[string]any{"round": round}, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// AdminTelegramSender is the part of the Telegram client the handler needs.
type AdminTelegramSender interface {
	Send(ctx context.Context, chatID, text string) error
}

// NewAdminTelegramHandler is the worker handler of onboarding.admin_telegram.
// A nil sender (no EVENTS_TELEGRAM_BOT_TOKEN) drops the job with a warning.
func NewAdminTelegramHandler(sender AdminTelegramSender, logger *slog.Logger) func(context.Context, []byte) error {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, payload []byte) error {
		var p AdminTelegramPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("onboarding: decode admin Telegram payload: %w", err)
		}
		if p.ChatID == 0 || p.Text == "" {
			return fmt.Errorf("onboarding: admin Telegram payload missing chat or text")
		}
		if sender == nil {
			logger.Warn("onboarding: admin Telegram message dropped, EVENTS_TELEGRAM_BOT_TOKEN is not set")
			return nil
		}
		err := sender.Send(ctx, fmt.Sprint(p.ChatID), p.Text)
		var perm *salesnotify.PermanentError
		if errors.As(err, &perm) {
			// The admin blocked the bot or deleted the chat: retrying is pointless.
			logger.Warn("onboarding: admin Telegram message refused", slog.Any("error", err))
			return nil
		}
		return err
	}
}
