package onboarding

// admin_notify.go — a new (or re-submitted) application reaches every platform
// superadmin personally: one Telegram message per linked Telegram account (sent
// by the event-center bot, EVENTS_TELEGRAM_BOT_TOKEN) and one e-mail. The ops
// chat is only the fallback when there is no superadmin to tell.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/adminnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

const (
	// JobTypeAdminTelegram is the LEGACY job type of one personal Telegram message
	// to a platform admin. New jobs use adminnotify.JobTypeTelegram; the worker
	// still handles this one (same payload) for rows queued before the move.
	JobTypeAdminTelegram = "onboarding.admin_telegram"
	// eventAdminsNotified marks that the admins of one submission round were
	// told, so a retried onboarding.notify job sends nothing twice.
	eventAdminsNotified = "admins_notified"
)

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
	admins, err := adminnotify.Recipients(ctx, tx)
	if err != nil {
		return false, err
	}
	if len(admins) == 0 {
		return false, nil
	}

	title, details := adminMessage(app, would, again)
	link := s.link(app.ID)
	text := adminnotify.TelegramText(adminnotify.Message{Title: title, Details: details, Link: link, WhatToDo: adminWhatToDo})
	org := ""
	if app.OrgName != nil {
		org = *app.OrgName
	}
	telegramSkipped := false
	for _, a := range admins {
		if _, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypeOnboardingEmail, authemail.OnboardingEmailPayload{
			Kind:          authemail.OnboardingKindAdminNewApplication,
			ApplicationID: app.ID.String(),
			Email:         a.Email,
			UserID:        a.UserID.String(),
			Locale:        "ru",
			OrgName:       org,
			Title:         title,
			Details:       details,
			Link:          link,
		}, emailJobAttempts); err != nil {
			return false, fmt.Errorf("onboarding: queue admin e-mail: %w", err)
		}
		for _, chat := range a.Chats {
			if !s.adminTelegram {
				telegramSkipped = true
				continue
			}
			if _, err := worker.EnqueueInTx(ctx, tx, adminnotify.JobTypeTelegram, adminnotify.TelegramPayload{ChatID: chat, Text: text}, emailJobAttempts); err != nil {
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
