// Package eventbot is the Telegram event-center bot
// (08_architecture/28_telegram_event_center_bot_ru.md): organizers open it
// from an invitation deep link, and it shows their events and sales — and,
// in the next steps, creates and edits events — by calling arena-api as the
// linked user. It owns only the bot_* tables; every business read or write
// goes through the REST API.
package eventbot

import (
	"html"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// SupportedLocales are the languages the bot speaks; anything else falls
// back to en (spec 28 §8 — cs/he come with their toml translations later).
// es was added 2026-10-07 for the organizers in Spain.
var SupportedLocales = []string{"en", "ru", "es"}

// MessageKeys lists every bot.* key the bot renders. The locale test proves
// each one exists in every supported catalog, so a missing translation is a
// failing build rather than a key name shown to an organizer.
var MessageKeys = []string{
	"bot.cmd_start", "bot.cmd_events", "bot.cmd_new",
	"bot.cmd_team",
	"bot.btn_team",
	"bot.btn_team_add",
	"bot.team_title",
	"bot.team_member_line",
	"bot.team_linked_mark",
	"bot.team_pending_mark",
	"bot.team_not_linked_mark",
	"bot.team_self_mark",
	"bot.team_hint",
	"bot.team_invite_btn",
	"bot.team_remove_btn",
	"bot.team_remove_yes_btn",
	"bot.team_ask_email",
	"bot.team_dialog_expired",
	"bot.dialog_expired",
	"bot.too_many_messages",
	"bot.draft_reminder", "bot.draft_reminder_named", "bot.draft_deleted",
	"bot.team_ask_role",
	"bot.team_role_manager_btn",
	"bot.team_role_owner_btn",
	"bot.team_invited",
	"bot.team_invite_failed",
	"bot.team_confirm_remove",
	"bot.team_removed",
	"bot.team_remove_failed",
	"bot.team_owner_only", "bot.cmd_org", "bot.cmd_lang", "bot.cmd_help",
	"bot.not_invited", "bot.ask_email", "bot.ask_email_again",
	"bot.invite_accepted", "bot.invite_accepted_notice", "bot.invite_not_found", "bot.invite_email_mismatch",
	"bot.invite_already_linked", "bot.invite_failed", "bot.already_linked",
	"bot.about_description", "bot.about_short",
	"bot.role_owner", "bot.role_manager",
	"bot.menu_title", "bot.btn_events", "bot.btn_org", "bot.btn_lang", "bot.btn_help",
	"bot.btn_back", "bot.btn_home", "bot.btn_prev", "bot.btn_next", "bot.help",
	"bot.events_empty", "bot.events_title", "bot.events_upcoming", "bot.events_past",
	"bot.event_card", "bot.event_no_sessions", "bot.session_line", "bot.session_tickets_line",
	"bot.sample_btn", "bot.sample_caption", "bot.sample_failed",
	"bot.money_line", "bot.money_none",
	"bot.status_draft", "bot.status_published", "bot.status_archived", "bot.status_cancelled",
	"bot.session_cancelled",
	"bot.org_choose", "bot.org_switched", "bot.org_none",
	"bot.lang_choose", "bot.lang_set",
	"bot.error_generic", "bot.access_lost", "bot.unknown_input",
}

// NormalizeLocale maps a Telegram language_code (or a stored value) onto a
// supported locale: case-folded, region dropped, unknown → en.
func NormalizeLocale(raw string) string {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	for _, l := range SupportedLocales {
		if l == tag {
			return l
		}
	}
	return "en"
}

// Texts renders bot messages from the shared i18n bundle.
type Texts struct {
	bundle *i18n.Bundle
}

// NewTexts wraps the bundle. A nil bundle renders keys as themselves, which
// only unit tests without a catalog should ever see.
func NewTexts(bundle *i18n.Bundle) *Texts {
	return &Texts{bundle: bundle}
}

// T renders key in locale with the template data. Values in data are used
// verbatim: callers escape user-supplied strings with Esc before passing
// them, because every message is sent with HTML parse mode.
func (t *Texts) T(locale, key string, data map[string]any) string {
	if t == nil || t.bundle == nil {
		return key
	}
	loc := t.bundle.LocalizerFor(NormalizeLocale(locale))
	var td any
	if len(data) > 0 {
		td = data
	}
	msg, err := loc.Localize(&goi18n.LocalizeConfig{MessageID: key, TemplateData: td})
	if err != nil || msg == "" {
		return key
	}
	return msg
}

// Esc escapes a user-supplied string for Telegram's HTML parse mode.
func Esc(s string) string {
	return html.EscapeString(s)
}
