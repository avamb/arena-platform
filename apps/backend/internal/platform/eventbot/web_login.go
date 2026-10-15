package eventbot

// web_login.go — "Open in browser" (spec 08_architecture/37 section 4.3,
// task WEB-AUTH-2, owner decision 2026-10-14: for every role, according to
// its rights). The bot asks arena-api for a one-time link under its service
// token and sends it to the private chat only. After the link is used, the
// bot tells the person and offers "Sign out everywhere", which ends every
// web session of that person.
//
// Presses: web:open (a new link), web:out (sign out everywhere).

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/go-telegram/bot/models"
)

// webNoticeEvery is how often the bot asks for sign-ins to report.
const webNoticeEvery = 30 * time.Second

// webMenuRow is the main menu's "Open in browser" row. A platform
// superadmin works in admin-web and gets no link (spec 37 section 1).
func (b *Bot) webMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if id == nil || id.Superadmin {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.web.btn", nil), CallbackData: "web:open"}}
}

func (b *Bot) webCallback(ctx context.Context, chatID int64, from *models.User, rest string) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	loc := id.Locale()
	home := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}}
	// The link signs in as this person: never into a group.
	if chatID != from.ID {
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.private_only", nil), nil)
		return
	}
	switch rest {
	case "open":
		if id.Superadmin {
			b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.superadmin", nil), inline(home))
			return
		}
		link, err := b.arena.WebLoginLink(ctx, from.ID)
		if err != nil {
			b.reply(ctx, chatID, nil, b.texts.T(loc, webLinkErrorKey(err), nil), inline(home))
			return
		}
		// "Sign out everywhere" also lives here, so it stays one press away
		// after the sign-in notice has lost its buttons.
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.link", nil), inline(
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.web.open_btn", nil), URL: link.URL}},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.web.signout_btn", nil), CallbackData: "web:out"}},
			home))
	case "out":
		if err := b.arena.RevokeWebSessions(ctx, jwt); err != nil {
			b.logger.Warn("eventbot: revoke web sessions failed", slog.String("error", err.Error()))
			b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.signout_failed", nil), inline(
				[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.web.signout_btn", nil), CallbackData: "web:out"}},
				home))
			return
		}
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.web.signed_out", nil), inline(home))
	default:
		b.showHome(ctx, chatID, nil, from, "")
	}
}

// webLinkErrorKey turns a refusal of POST /v1/bot/web-login-codes into the
// text the person sees.
func webLinkErrorKey(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "bot.web_login_not_configured":
			return "bot.web.not_configured"
		case "bot.web_login_superadmin":
			return "bot.web.superadmin"
		}
	}
	return "bot.web.failed"
}

// webNoticeLoop reports sign-ins through "Open in browser" links, each once.
func (b *Bot) webNoticeLoop(ctx context.Context) {
	every := b.noticeEvery
	if every <= 0 {
		every = webNoticeEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.webAnnounce(ctx)
		}
	}
}

func (b *Bot) webAnnounce(ctx context.Context) {
	notices, err := b.arena.ClaimWebLoginNotices(ctx)
	if err != nil {
		b.logger.Warn("eventbot: web login notices failed", slog.String("error", err.Error()))
		return
	}
	for _, n := range notices {
		loc := "en"
		if link, lerr := b.queries.GetBotTelegramLink(ctx, n.TelegramUserID); lerr == nil {
			if link.RevokedAt != nil {
				continue
			}
			loc = NormalizeLocale(link.Locale)
		}
		text := b.texts.T(loc, "bot.web.signed_in", map[string]any{
			"Time": n.SignedInAt.UTC().Format("2006-01-02 15:04"), // allow:timeformat
		})
		b.send(ctx, n.TelegramUserID, text, inline(
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.web.signout_btn", nil), CallbackData: "web:out"}},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}}))
	}
}
