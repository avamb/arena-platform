package eventbot

// translations.go — "Translations" on the edit group of the event card
// (lane L1). The organizer sees the languages of the event page with the state
// of each (none, written by hand, machine, outdated), opens one, reads the
// text the buyers of that language get (or the original when there is no
// translation yet) and sends their own translation of the description as a
// message. The message is stored with PUT .../translations/{locale}: the
// translation is then the organizer's own and the machine never replaces it.
// "Translate by machine" asks for the languages that have no translation at
// all, when the platform has the model switched on.
//
// Presses are "tr:<kind>:<event>[:<locale>]":
//
//	tr:l:<event>            the list of languages (also ends the typed step)
//	tr:o:<event>:<locale>   one language: the text, and the wait for the translation
//	tr:m:<event>            machine translation of the languages without one
//
// The typed step lives in bot_dialogs, kind "translations", 30 minutes, and
// ends on any press that is not a "tr:" one and on every command
// (dialog_owners.go). The owner, the manager and the operator may use it, like
// the letter text.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

const (
	trDialogKind = "translations"
	// trStepAsk waits for the typed translation of one language.
	trStepAsk = "ask"

	// trShownRunes bounds the text shown on a language screen.
	trShownRunes = 2500
)

// trDialog is the state of the typed step.
type trDialog struct {
	OrgID   uuid.UUID `json:"org_id"`
	EventID uuid.UUID `json:"event_id"`
	Locale  string    `json:"locale"`
}

// trLangNames are the languages' own names: they are shown the same in every
// bot language, so people find theirs at once.
var trLangNames = map[string]string{
	"cs": "Čeština", "de": "Deutsch", "en": "English", "es": "Español", "et": "Eesti",
	"fi": "Suomi", "fr": "Français", "he": "עברית", "hu": "Magyar", "it": "Italiano",
	"lt": "Lietuvių", "lv": "Latviešu", "pl": "Polski", "ru": "Русский", "uk": "Українська",
}

func trLangName(locale string) string {
	if n, ok := trLangNames[locale]; ok {
		return n
	}
	return strings.ToUpper(locale)
}

var trKeys = []string{
	"bot.tr.btn", "bot.tr.list_title", "bot.tr.list_about", "bot.tr.lang_btn", "bot.tr.lang_label",
	"bot.tr.state_none", "bot.tr.state_manual", "bot.tr.state_machine", "bot.tr.state_manual_stale", "bot.tr.state_machine_stale",
	"bot.tr.machine_btn", "bot.tr.machine_queued", "bot.tr.machine_none", "bot.tr.machine_off", "bot.tr.machine_failed",
	"bot.tr.lang_title", "bot.tr.lang_current", "bot.tr.lang_original", "bot.tr.lang_empty", "bot.tr.lang_ask",
	"bot.tr.to_list_btn", "bot.tr.saved", "bot.tr.save_failed", "bot.tr.expired",
}

func init() { MessageKeys = append(MessageKeys, trKeys...) }

func trData(kind string, eventID uuid.UUID, rest ...string) string {
	return strings.Join(append([]string{"tr", kind, eventID.String()}, rest...), ":")
}

// trLocales are the languages a translation may be made for: the supported
// ones except the language of the original.
func trLocales(list openapi.ContentTranslationList) []string {
	out := make([]string, 0, len(list.SupportedLocales))
	for _, l := range list.SupportedLocales {
		if l != list.ContentLocale {
			out = append(out, l)
		}
	}
	return out
}

func trFind(list openapi.ContentTranslationList, locale string) *openapi.ContentTranslationItem {
	for i := range list.Translations {
		if list.Translations[i].Locale == locale {
			return &list.Translations[i]
		}
	}
	return nil
}

// trState is the key of the state text of one language.
func trStateKey(t *openapi.ContentTranslationItem) string {
	switch {
	case t == nil:
		return "bot.tr.state_none"
	case t.Source == "manual" && t.TranslationStale:
		return "bot.tr.state_manual_stale"
	case t.Source == "manual":
		return "bot.tr.state_manual"
	case t.TranslationStale:
		return "bot.tr.state_machine_stale"
	}
	return "bot.tr.state_machine"
}

// trItems is the list of controls of the languages screen: the keyboard and
// the help are both made from it.
func (b *Bot) trItems(loc string, eventID uuid.UUID, list openapi.ContentTranslationList) screenItems {
	var items screenItems
	var rows [][]button
	var row []button
	for _, l := range trLocales(list) {
		row = append(row, button{
			Text: b.texts.T(loc, "bot.tr.lang_btn", map[string]any{
				"Lang": trLangName(l), "State": b.texts.T(loc, trStateKey(trFind(list, l)), nil),
			}),
			CallbackData: trData("o", eventID, l),
		})
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		items = append(items, screenItem{ID: "tr.lang", Rows: rows, Label: b.texts.T(loc, "bot.tr.lang_label", nil)})
	}
	if list.MachineAvailable && len(rows) > 0 {
		items.add("tr.machine", b.mnBtn(loc, "bot.tr.machine_btn", trData("m", eventID)))
	}
	return items
}

func trBack(eventID uuid.UUID) string { return "ec:g:" + eventID.String() + ":" + cardGroupEdit }

// trListScreen draws the list of languages. prefix is a line of the action that
// led here.
func (b *Bot) trListScreen(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID uuid.UUID, prefix string) {
	loc := id.Locale()
	list, err := b.arena.ListTranslations(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, trBack(eventID))
		return
	}
	text := prefix + b.texts.T(loc, "bot.tr.list_title", map[string]any{"Name": Esc(list.Original.Name)}) + "\n\n" + b.texts.T(loc, "bot.tr.list_about", nil)
	rows := b.trItems(loc, eventID, list).rows()
	rows = append(rows, b.helpRow(loc, hpTrans, eventID.String()))
	rows = append(rows, []button{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: trBack(eventID)},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// trShown is the Telegram text of a stored description, cut so the message
// stays under the Telegram limit.
func trShown(html string) string {
	if len([]rune(html)) <= trShownRunes {
		return richtext.ToTelegramHTML(html)
	}
	return Esc(truncate(richtext.PlainText(html), trShownRunes))
}

func trHas(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }

// trLangScreen draws one language and starts the wait for the typed text.
func (b *Bot) trLangScreen(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, eventID uuid.UUID, locale string) {
	loc := id.Locale()
	list, err := b.arena.ListTranslations(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, trBack(eventID))
		return
	}
	found := false
	for _, l := range trLocales(list) {
		found = found || l == locale
	}
	if !found {
		b.trListScreen(ctx, chatID, editMsgID, id, jwt, eventID, "")
		return
	}
	dlg := trDialog{OrgID: id.Current.OrgID, EventID: eventID, Locale: locale}
	if err := b.dialogs.Save(ctx, from.ID, &dlg.OrgID, trDialogKind, trStepAsk, dlg, dialogTTL); err != nil {
		b.logger.Error("eventbot: translations dialog save failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	text := b.texts.T(loc, "bot.tr.lang_title", map[string]any{"Lang": Esc(trLangName(locale)), "Name": Esc(list.Original.Name)}) + "\n\n"
	switch cur := trFind(list, locale); {
	case cur != nil && trHas(cur.DescriptionHtml):
		text += b.texts.T(loc, "bot.tr.lang_current", map[string]any{"Text": trShown(*cur.DescriptionHtml)})
	case trHas(list.Original.DescriptionHtml):
		text += b.texts.T(loc, "bot.tr.lang_original", map[string]any{"Text": trShown(*list.Original.DescriptionHtml)})
	default:
		text += b.texts.T(loc, "bot.tr.lang_empty", nil)
	}
	text += "\n\n" + b.texts.T(loc, "bot.tr.lang_ask", nil)
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]button{
		{{Text: "« " + b.texts.T(loc, "bot.tr.to_list_btn", nil), CallbackData: trData("l", eventID)}},
	}})
}

// trMachine asks for the machine translation of the languages that have no
// translation at all. A translation written by hand is never in that set, and
// the server's job leaves it alone as well.
func (b *Bot) trMachine(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	list, err := b.arena.ListTranslations(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, trBack(eventID))
		return
	}
	if !list.MachineAvailable {
		b.trListScreen(ctx, chatID, editMsgID, id, jwt, eventID, b.texts.T(loc, "bot.tr.machine_off", nil)+"\n\n")
		return
	}
	var missing, names []string
	for _, l := range trLocales(list) {
		if trFind(list, l) == nil {
			missing = append(missing, l)
			names = append(names, trLangName(l))
		}
	}
	if len(missing) == 0 {
		b.trListScreen(ctx, chatID, editMsgID, id, jwt, eventID, b.texts.T(loc, "bot.tr.machine_none", nil)+"\n\n")
		return
	}
	if err := b.arena.MachineTranslate(ctx, jwt, orgID, eventID, missing); err != nil {
		switch {
		case IsAPIError(err, http.StatusServiceUnavailable):
			b.trListScreen(ctx, chatID, editMsgID, id, jwt, eventID, b.texts.T(loc, "bot.tr.machine_off", nil)+"\n\n")
		case IsAPIError(err, http.StatusForbidden) || IsAPIError(err, http.StatusNotFound):
			b.ecError(ctx, chatID, editMsgID, id, err, trBack(eventID))
		default:
			b.logger.Error("eventbot: machine translation failed", slog.String("error", routeOnly(err)))
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.tr.machine_failed", nil), b.backKeyboard(loc, trData("l", eventID)))
		}
		return
	}
	b.trListScreen(ctx, chatID, editMsgID, id, jwt, eventID, b.texts.T(loc, "bot.tr.machine_queued", map[string]any{"Langs": Esc(strings.Join(names, ", "))})+"\n\n")
}

// trCallback handles every "tr:<data>" press.
func (b *Bot) trCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	b.trPress(ctx, chatID, msgID, from, id, jwt, strings.Split(data, ":"))
}

// trPress is trCallback for a known person.
func (b *Bot) trPress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, parts []string) {
	loc := id.Locale()
	if len(parts) < 2 {
		return
	}
	eventID, err := uuid.Parse(parts[1])
	if err != nil {
		return
	}
	if !canViewSales(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	switch parts[0] {
	case "l":
		b.clearTranslationsDialog(ctx, from.ID)
		b.trListScreen(ctx, chatID, &msgID, id, jwt, eventID, "")
	case "o":
		if len(parts) < 3 {
			return
		}
		b.trLangScreen(ctx, chatID, &msgID, from, id, jwt, eventID, parts[2])
	case "m":
		b.clearTranslationsDialog(ctx, from.ID)
		b.trMachine(ctx, chatID, &msgID, id, jwt, eventID)
	}
}

// clearTranslationsDialog ends the typed step, if any.
func (b *Bot) clearTranslationsDialog(ctx context.Context, tg int64) {
	if b.dialogs == nil {
		return
	}
	if err := b.dialogs.Delete(ctx, tg, trDialogKind); err != nil {
		b.logger.Warn("eventbot: translations dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// translationsText takes the typed translation. It reports whether it took the
// text.
func (b *Bot) translationsText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	if b.dialogs == nil {
		return false
	}
	var dlg trDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, trDialogKind, &dlg)
	if err != nil {
		b.logger.Error("eventbot: translations dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired && step == trStepAsk {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.tr.expired", nil), nil)
		return true
	}
	if !found || step != trStepAsk {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || dlg.OrgID != id.Current.OrgID {
		b.clearTranslationsDialog(ctx, from.ID) // another organization now: not theirs to answer
		return false
	}
	b.trTyped(ctx, chatID, from, id, jwt, dlg, text)
	return true
}

// trTyped stores the typed translation of the description. The other texts of
// the row are kept as they are, because the route replaces the row as a whole.
func (b *Bot) trTyped(ctx context.Context, chatID int64, from *models.User, id *Identity, jwt string, dlg trDialog, text string) {
	loc := id.Locale()
	if !canViewSales(id) {
		b.clearTranslationsDialog(ctx, from.ID)
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	_, plain, ref := tfCheck(text, tfKindDescription)
	if ref != nil {
		b.send(ctx, chatID, b.tfRefusalText(loc, ref), nil) // the dialog stays: the person types again
		return
	}
	orgID := id.Current.OrgID
	list, err := b.arena.ListTranslations(ctx, jwt, orgID, dlg.EventID)
	if err != nil {
		b.logger.Warn("eventbot: translations not read", slog.String("error", routeOnly(err)))
		b.send(ctx, chatID, b.texts.T(loc, "bot.tr.save_failed", nil), nil)
		return
	}
	body := openapi.PutContentTranslationRequest{DescriptionHtml: &plain}
	if cur := trFind(list, dlg.Locale); cur != nil {
		body.Name, body.ShortDescription, body.ReportTextHtml = cur.Name, cur.ShortDescription, cur.ReportTextHtml
	}
	if _, err := b.arena.PutTranslation(ctx, jwt, orgID, dlg.EventID, dlg.Locale, body); err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Status == http.StatusUnprocessableEntity {
			ref := tfRefusalForReason(api.Reason, tfKindDescription)
			b.send(ctx, chatID, b.tfRefusalText(loc, &ref), nil)
			return
		}
		b.logger.Error("eventbot: translation save failed", slog.String("error", routeOnly(err)))
		b.send(ctx, chatID, b.texts.T(loc, "bot.tr.save_failed", nil), nil)
		return
	}
	b.clearTranslationsDialog(ctx, from.ID)
	b.trListScreen(ctx, chatID, nil, id, jwt, dlg.EventID, b.texts.T(loc, "bot.tr.saved", map[string]any{"Lang": Esc(trLangName(dlg.Locale))})+"\n\n")
}
