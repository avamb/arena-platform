package eventbot

// onboarding_dialog.go — the Telegram channel of the organizer application
// (08_architecture/34_onboarding_applications_ru.md §10), behind
// BOT_SELF_ONBOARDING_ENABLED.
//
// The bot is a thin client. The application lives on the server and the next
// question is always derived from it (the form schema plus the application's
// own missing_fields), so a bot restart or an expired in-memory dialog loses
// nothing but the first four answers, which are asked before an application
// exists. The in-memory state is only that, the half-ticked multiselect and
// which requested fields were re-answered in an info round.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	onbNoticeEvery  = 45 * time.Second
	onbFormCacheTTL = 10 * time.Minute
)

type onbStep int

const (
	onbIdle onbStep = iota
	onbFirst
	onbLast
	onbPhone
	onbEmail
)

type onbDialog struct {
	step     onbStep
	first    string
	last     string
	phone    string
	locale   string
	sel      map[string]map[string]bool
	infoDone map[string]bool
	// dropped is set by clear(); a dropped dialog is not written back.
	dropped bool
	// loadFailed marks a dialog that is EMPTY because the store could not be
	// read, not because there was nothing stored: writing it back would
	// replace the person's real answers with blanks.
	loadFailed bool
}

// onbDialogJSON is the stored form of onbDialog (the fields are unexported).
type onbDialogJSON struct {
	Step     onbStep                    `json:"step"`
	First    string                     `json:"first,omitempty"`
	Last     string                     `json:"last,omitempty"`
	Phone    string                     `json:"phone,omitempty"`
	Locale   string                     `json:"locale,omitempty"`
	Sel      map[string]map[string]bool `json:"sel,omitempty"`
	InfoDone map[string]bool            `json:"info_done,omitempty"`
}

func (d onbDialog) MarshalJSON() ([]byte, error) {
	return json.Marshal(onbDialogJSON{d.step, d.first, d.last, d.phone, d.locale, d.sel, d.infoDone})
}

func (d *onbDialog) UnmarshalJSON(raw []byte) error {
	var j onbDialogJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return err
	}
	*d = onbDialog{step: j.Step, first: j.First, last: j.Last, phone: j.Phone, locale: j.Locale, sel: j.Sel, infoDone: j.InfoDone}
	return nil
}

func newOnbDialog() *onbDialog {
	return &onbDialog{sel: map[string]map[string]bool{}, infoDone: map[string]bool{}}
}

// onbDialogKind is the bot_dialogs kind of the application dialog (EC-40).
const onbDialogKind = "onboarding"

// onbDialogs keeps the first four answers of an application (name, phone,
// e-mail, language) and the half-ticked multiselect in bot_dialogs, so a bot
// restart loses nothing. Everything after the first four answers lives on the
// server. Entry points read the dialog with get, write it back before each
// reply (saveBeforeReply) and once more with save when they are done, unless
// they cleared it.
type onbDialogs struct {
	store DialogStore
}

func newOnbDialogs(store DialogStore) *onbDialogs { return &onbDialogs{store: store} }

// get returns the live dialog of an account, or an empty one when there is
// none or it expired (an applicant who comes back after 30 minutes starts the
// first four answers again; the application itself is on the server).
func (d *onbDialogs) get(ctx context.Context, tg int64) *onbDialog {
	cur := newOnbDialog()
	if _, found, _, err := d.store.Load(ctx, tg, onbDialogKind, cur); err != nil || !found {
		if err != nil {
			slog.Default().Error("onboarding dialog: load failed", "error", err.Error())
			failed := newOnbDialog()
			failed.loadFailed = true
			return failed
		}
		return newOnbDialog()
	}
	if cur.sel == nil {
		cur.sel = map[string]map[string]bool{}
	}
	if cur.infoDone == nil {
		cur.infoDone = map[string]bool{}
	}
	return cur
}

// save writes the dialog back and slides its expiry.
func (d *onbDialogs) save(ctx context.Context, tg int64, cur *onbDialog) {
	if cur == nil || cur.dropped || cur.loadFailed {
		return
	}
	if err := d.store.Save(ctx, tg, nil, onbDialogKind, "open", cur, dialogTTL); err != nil {
		slog.Default().Error("onboarding dialog: save failed", "error", err.Error())
	}
}

// saveBeforeReply makes every Telegram request sent on the returned ctx write
// cur back first (reply_hook.go): the handlers change cur and then reply, and
// the step the reply asks about must already be stored when the applicant
// sees it. The deferred save still runs at the end for a handler that changes
// cur after its last reply, or does not reply at all.
func (d *onbDialogs) saveBeforeReply(ctx context.Context, tg int64, cur *onbDialog) context.Context {
	return withSaveBeforeReply(ctx, func() { d.save(ctx, tg, cur) })
}

func (d *onbDialogs) clear(ctx context.Context, tg int64, cur *onbDialog) {
	if cur != nil {
		cur.dropped = true
	}
	if err := d.store.Delete(ctx, tg, onbDialogKind); err != nil {
		slog.Default().Error("onboarding dialog: delete failed", "error", err.Error())
	}
}

// onbFormCache keeps the form definition per language for a few minutes.
type onbFormCache struct {
	mu sync.Mutex
	m  map[string]onbFormEntry
}

type onbFormEntry struct {
	form OnbForm
	at   time.Time
}

func (b *Bot) onbForm(ctx context.Context, locale string) (OnbForm, error) {
	b.onbForms.mu.Lock()
	if e, ok := b.onbForms.m[locale]; ok && time.Since(e.at) < onbFormCacheTTL {
		b.onbForms.mu.Unlock()
		return e.form, nil
	}
	b.onbForms.mu.Unlock()
	form, err := b.arena.OnboardingForm(ctx, locale)
	if err != nil {
		return form, err
	}
	b.onbForms.mu.Lock()
	b.onbForms.m[locale] = onbFormEntry{form: form, at: time.Now()}
	b.onbForms.mu.Unlock()
	return form, nil
}

func (b *Bot) onbOn() bool { return b.selfOnboarding }

// popularCountries are offered as buttons when the settings accept every country.
var popularCountries = []string{"ES", "CZ", "IL", "DE", "FR", "IT", "PT", "MX", "AR", "CL", "CO", "BR", "US", "GB", "PL", "CH", "AT", "NL"}

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

func flagOf(cc string) string {
	if len(cc) != 2 {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(cc) {
		if r < 'A' || r > 'Z' {
			return ""
		}
		b.WriteRune(0x1F1E6 + (r - 'A'))
	}
	return b.String()
}

func (b *Bot) onbLocale(from *models.User, d *onbDialog, app *OnbApplication) string {
	switch {
	case app != nil && app.Locale != "":
		return NormalizeLocale(app.Locale)
	case d != nil && d.locale != "":
		return d.locale
	}
	return NormalizeLocale(from.LanguageCode)
}

func inline(rows ...[]models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func (b *Bot) sendMarkup(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) {
	if _, err := b.post(ctx, chatID, 0, text, markup); err != nil {
		b.logger.Warn("eventbot: sendMessage failed", slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
	}
}

// ─── entry points ────────────────────────────────────────────────────────────

// onbWelcome is what a stranger sees instead of "you need an invitation": the
// way in for an invited person and the way to apply for everybody else. An
// open application takes them straight back to where they stopped.
func (b *Bot) onbWelcome(ctx context.Context, chatID int64, from *models.User) {
	d := b.onb.get(ctx, from.ID)
	defer b.onb.save(ctx, from.ID, d)
	ctx = b.onb.saveBeforeReply(ctx, from.ID, d)
	b.onbWelcomeWith(ctx, chatID, from, d)
}

// onbWelcomeWith is onbWelcome for a caller that already holds the dialog: a
// second load would read the stored copy, which lacks what the caller just
// changed (the language button), and the two write-backs would fight.
func (b *Bot) onbWelcomeWith(ctx context.Context, chatID int64, from *models.User, d *onbDialog) {
	if app, err := b.arena.OnboardingCurrent(ctx, from.ID); err == nil {
		b.onbRoute(ctx, chatID, from, d, &app)
		return
	} else if !IsAPIError(err, http.StatusNotFound) {
		b.logger.Error("eventbot: onboarding lookup failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(NormalizeLocale(from.LanguageCode), "bot.not_invited", nil), nil)
		return
	}
	loc := b.onbLocale(from, d, nil)
	b.send(ctx, chatID, b.texts.T(loc, "bot.onb.welcome", nil), inline(
		[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.apply_btn", nil), "onb:start")},
		[]models.InlineKeyboardButton{btn("English", "onb:lang:en"), btn("Русский", "onb:lang:ru"), btn("Español", "onb:lang:es")},
	))
}

// onbApplyDirect serves the link t.me/<bot>?start=apply: a stranger who already
// decided to apply gets a one-line intro and the first question at once, with
// no extra "press Apply" step. An open application takes them back to where
// they stopped, as the welcome screen does.
func (b *Bot) onbApplyDirect(ctx context.Context, chatID int64, from *models.User) {
	d := b.onb.get(ctx, from.ID)
	defer b.onb.save(ctx, from.ID, d)
	ctx = b.onb.saveBeforeReply(ctx, from.ID, d)
	if app, err := b.arena.OnboardingCurrent(ctx, from.ID); err == nil {
		b.onbRoute(ctx, chatID, from, d, &app)
		return
	} else if !IsAPIError(err, http.StatusNotFound) {
		b.logger.Error("eventbot: onboarding lookup failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(NormalizeLocale(from.LanguageCode), "bot.not_invited", nil), nil)
		return
	}
	loc := b.onbLocale(from, d, nil)
	d.step = onbFirst
	b.send(ctx, chatID, b.texts.T(loc, "bot.onb.apply_intro", nil), nil)
	b.send(ctx, chatID, b.texts.T(loc, "bot.onb.ask_first", nil), nil)
}

// onbMessage handles a text or a contact from somebody who is not linked.
func (b *Bot) onbMessage(ctx context.Context, chatID int64, from *models.User, m *models.Message) {
	d := b.onb.get(ctx, from.ID)
	defer b.onb.save(ctx, from.ID, d)
	ctx = b.onb.saveBeforeReply(ctx, from.ID, d)
	text := strings.TrimSpace(m.Text)
	loc := b.onbLocale(from, d, nil)

	if d.step != onbIdle {
		b.onbCollect(ctx, chatID, from, d, m, text)
		return
	}
	app, err := b.arena.OnboardingCurrent(ctx, from.ID)
	if err != nil {
		if IsAPIError(err, http.StatusNotFound) {
			b.onbWelcomeWith(ctx, chatID, from, d)
			return
		}
		b.logger.Error("eventbot: onboarding lookup failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	b.onbText(ctx, chatID, from, d, &app, text)
}

// onbCollect gathers the four answers that open an application.
func (b *Bot) onbCollect(ctx context.Context, chatID int64, from *models.User, d *onbDialog, m *models.Message, text string) {
	loc := b.onbLocale(from, d, nil)
	switch d.step {
	case onbFirst:
		if text == "" || len([]rune(text)) > 80 {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.ask_first", nil), nil)
			return
		}
		d.first, d.step = text, onbLast
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.ask_last", nil), nil)
	case onbLast:
		if text == "" || len([]rune(text)) > 80 {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.ask_last", nil), nil)
			return
		}
		d.last, d.step = text, onbPhone
		b.askPhone(ctx, chatID, loc)
	case onbPhone:
		if m.Contact == nil {
			b.askPhone(ctx, chatID, loc)
			return
		}
		if m.Contact.UserID != from.ID {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.phone_not_yours", nil), nil)
			return
		}
		phone := strings.TrimSpace(m.Contact.PhoneNumber)
		if !strings.HasPrefix(phone, "+") {
			phone = "+" + phone
		}
		d.phone, d.step = phone, onbEmail
		b.sendMarkup(ctx, chatID, b.texts.T(loc, "bot.onb.ask_email", nil), &models.ReplyKeyboardRemove{RemoveKeyboard: true})
		b.menuKB.set(chatID, false) // the keyboard is gone: a later menu sends it again
	case onbEmail:
		if !looksLikeEmail(text) {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.email_invalid", nil), nil)
			return
		}
		app, err := b.arena.OnboardingStart(ctx, OnbStartRequest{
			TelegramUserID: from.ID, TelegramUsername: from.Username,
			FirstName: d.first, LastName: d.last, Phone: d.phone, Email: text, Locale: loc,
		})
		if err != nil {
			b.onbStartFailed(ctx, chatID, loc, d, err)
			return
		}
		// The application now holds the name, phone and e-mail: the dialog
		// must not keep a stranger's personal data (nor have it end up in the
		// database backups) for the rest of its lifetime.
		d.step, d.first, d.last, d.phone = onbIdle, "", "", ""
		b.onbRoute(ctx, chatID, from, d, &app)
		b.onb.clear(ctx, from.ID, d)
	}
}

func (b *Bot) askPhone(ctx context.Context, chatID int64, loc string) {
	b.menuKB.set(chatID, false) // the contact button replaces any other reply keyboard
	b.sendMarkup(ctx, chatID, b.texts.T(loc, "bot.onb.ask_phone", nil), &models.ReplyKeyboardMarkup{
		Keyboard:        [][]models.KeyboardButton{{{Text: b.texts.T(loc, "bot.onb.share_phone_btn", nil), RequestContact: true}}},
		ResizeKeyboard:  true,
		OneTimeKeyboard: true,
	})
}

func (b *Bot) onbStartFailed(ctx context.Context, chatID int64, loc string, d *onbDialog, err error) {
	var ae *APIError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
		switch {
		case ae.Fields["email"] != "":
			d.step = onbEmail
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.email_invalid", nil), nil)
		case ae.Fields["phone"] != "":
			d.step = onbPhone
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.phone_invalid", nil), nil)
			b.askPhone(ctx, chatID, loc)
		default:
			d.step = onbFirst
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.ask_first", nil), nil)
		}
	case IsAPIError(err, http.StatusTooManyRequests):
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.rate_limited", nil), nil)
	default:
		b.logger.Error("eventbot: onboarding start failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
	}
}

// ─── routing by the application's state ──────────────────────────────────────

// onbRoute says what the application needs next.
func (b *Bot) onbRoute(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication) {
	loc := b.onbLocale(from, d, app)
	switch {
	case app.Status == "pending_approval":
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.pending", nil), b.onbSiteRow(loc))
	case !app.EmailConfirmed:
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.code_sent", map[string]any{"Email": Esc(app.Email)}), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.code_resend_btn", nil), "onb:resend")}))
	case app.Status == "info_requested":
		b.onbInfoNext(ctx, chatID, from, d, app)
	default:
		b.onbNext(ctx, chatID, from, d, app)
	}
}

var consentKeys = map[string]bool{"accept_terms": true, "accept_privacy": true, "confirm_authority": true}

// onbNext asks the first required answer still missing, or shows the review.
func (b *Bot) onbNext(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication) {
	if len(app.MissingFields) == 0 {
		b.onbReview(ctx, chatID, from, d, app)
		return
	}
	key := app.MissingFields[0]
	if consentKeys[key] {
		b.onbConsent(ctx, chatID, from, d, app)
		return
	}
	// A field that defaults from an answer already given (the postal country
	// from the registration country) is filled in, not asked.
	if form, err := b.onbForm(ctx, b.onbLocale(from, d, app)); err == nil {
		if f, ok := form.Schema.Field(key); ok && f.DefaultFrom != "" {
			if v, have := app.Answers[f.DefaultFrom]; have {
				b.onbSave(ctx, chatID, from, d, app, key, v)
				return
			}
		}
	}
	b.onbAsk(ctx, chatID, from, d, app, key)
}

// onbInfoNext walks the fields the operator asked about, one by one.
func (b *Bot) onbInfoNext(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication) {
	loc := b.onbLocale(from, d, app)
	for _, key := range app.RequestedFields {
		if !d.infoDone[key] {
			if len(d.infoDone) == 0 && app.InfoRequestMessage != "" {
				b.send(ctx, chatID, b.texts.T(loc, "bot.onb.info_requested", map[string]any{"Message": Esc(app.InfoRequestMessage)}), nil)
			}
			b.onbAsk(ctx, chatID, from, d, app, key)
			return
		}
	}
	b.onbReview(ctx, chatID, from, d, app)
}

// onbAsk renders one question from the form definition.
func (b *Bot) onbAsk(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication, key string) {
	loc := b.onbLocale(from, d, app)
	form, err := b.onbForm(ctx, loc)
	if err != nil {
		b.logger.Error("eventbot: onboarding form failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	f, ok := form.Schema.Field(key)
	if !ok {
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	head := b.texts.T(loc, "bot.onb.progress", map[string]any{"Pct": app.Progress}) + "\n\n<b>" + Esc(f.Label) + "</b>"
	if f.Hint != "" {
		head += "\n" + Esc(f.Hint)
	}
	switch f.Type {
	case "select":
		rows := [][]models.InlineKeyboardButton{}
		for i, o := range f.Options {
			rows = append(rows, []models.InlineKeyboardButton{btn(o.Label, fmt.Sprintf("onb:s:%s:%d", key, i))})
		}
		b.send(ctx, chatID, head, inline(rows...))
	case "multiselect":
		b.send(ctx, chatID, head, b.multiselectKeyboard(loc, d, f))
	case "bool":
		b.send(ctx, chatID, head, inline([]models.InlineKeyboardButton{
			btn(b.texts.T(loc, "bot.onb.yes_btn", nil), "onb:b:"+key+":1"),
			btn(b.texts.T(loc, "bot.onb.no_btn", nil), "onb:b:"+key+":0"),
		}))
	case "country":
		codes := form.Schema.AcceptedCountries
		if len(codes) == 0 {
			codes = popularCountries
		}
		rows := [][]models.InlineKeyboardButton{}
		var row []models.InlineKeyboardButton
		for _, cc := range codes {
			row = append(row, btn(flagOf(cc)+" "+cc, "onb:c:"+key+":"+cc))
			if len(row) == 3 {
				rows = append(rows, row)
				row = nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
		b.send(ctx, chatID, head+"\n\n"+b.texts.T(loc, "bot.onb.country_hint", nil), inline(rows...))
	default:
		b.send(ctx, chatID, head, nil)
	}
}

func (b *Bot) multiselectKeyboard(loc string, d *onbDialog, f OnbSchemaField) *models.InlineKeyboardMarkup {
	chosen := d.sel[f.Key]
	rows := [][]models.InlineKeyboardButton{}
	for i, o := range f.Options {
		mark := "▫️ "
		if chosen[o.Value] {
			mark = "✅ "
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+o.Label, fmt.Sprintf("onb:m:%s:%d", f.Key, i))})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.done_btn", nil), "onb:md:"+f.Key)})
	return inline(rows...)
}

func (b *Bot) onbConsent(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication) {
	loc := b.onbLocale(from, d, app)
	links := ""
	if form, err := b.onbForm(ctx, loc); err == nil {
		if form.TermsURL != "" {
			links += `<a href="` + Esc(form.TermsURL) + `">` + b.texts.T(loc, "bot.onb.terms_link", nil) + "</a>\n"
		}
		if form.PrivacyURL != "" {
			links += `<a href="` + Esc(form.PrivacyURL) + `">` + b.texts.T(loc, "bot.onb.privacy_link", nil) + "</a>\n"
		}
	}
	b.send(ctx, chatID, b.texts.T(loc, "bot.onb.consent_text", map[string]any{"Links": links}), b.consentKeyboard(loc, d))
}

// consentOrder is the order of the consent ticks on the screen; each is a
// separate statement the applicant must tick, never one bundled button.
var consentOrder = []struct{ key, text string }{
	{"accept_terms", "bot.onb.consent_terms"},
	{"accept_privacy", "bot.onb.consent_privacy"},
	{"confirm_authority", "bot.onb.consent_authority"},
}

func (b *Bot) consentKeyboard(loc string, d *onbDialog) *models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{}
	for _, c := range consentOrder {
		mark := "▫️ "
		if d.sel["consent"][c.key] {
			mark = "✅ "
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+b.texts.T(loc, c.text, nil), "onb:ct:"+c.key)})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.consent_btn", nil), "onb:consent")})
	return inline(rows...)
}

// onbConsentTick flips one consent tick and redraws the buttons in place.
func (b *Bot) onbConsentTick(ctx context.Context, chatID int64, msgID int, loc string, d *onbDialog, key string) {
	known := false
	for _, c := range consentOrder {
		known = known || c.key == key
	}
	if !known {
		return
	}
	if d.sel["consent"] == nil {
		d.sel["consent"] = map[string]bool{}
	}
	d.sel["consent"][key] = !d.sel["consent"][key]
	_, _ = b.tg.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{ChatID: chatID, MessageID: msgID, ReplyMarkup: b.consentKeyboard(loc, d)})
}

// onbReview shows what will be sent and the submit button.
func (b *Bot) onbReview(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication) {
	loc := b.onbLocale(from, d, app)
	form, err := b.onbForm(ctx, loc)
	if err != nil {
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.onb.review_title", nil))
	sb.WriteString("\n\n")
	for _, st := range form.Schema.Steps {
		for _, f := range st.Fields {
			if f.Type == "bool" || consentKeys[f.Key] {
				continue
			}
			v, ok := app.Answers[f.Key]
			if !ok {
				continue
			}
			if val := onbDisplay(f, v); val != "" {
				sb.WriteString("• " + Esc(f.Label) + ": <b>" + Esc(val) + "</b>\n")
			}
		}
	}
	sb.WriteString("\n" + b.texts.T(loc, "bot.onb.review_hint", nil))
	rows := [][]models.InlineKeyboardButton{{btn(b.texts.T(loc, "bot.onb.submit_btn", nil), "onb:submit")}}
	if b.siteButton {
		rows = append(rows, []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.site_btn", nil), "onb:site")})
	}
	b.send(ctx, chatID, sb.String(), inline(rows...))
}

// onbSiteRow is the "open on the website" button, or no keyboard while the
// website's continue page is not live (BOT_ONBOARDING_SITE_BUTTON).
func (b *Bot) onbSiteRow(loc string) *models.InlineKeyboardMarkup {
	if !b.siteButton {
		return nil
	}
	return inline([]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.site_btn", nil), "onb:site")})
}

// onbDisplay renders an answer for the review screen.
func onbDisplay(f OnbSchemaField, v any) string {
	label := func(value string) string {
		for _, o := range f.Options {
			if o.Value == value {
				return o.Label
			}
		}
		return value
	}
	switch x := v.(type) {
	case string:
		return label(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				parts = append(parts, label(s))
			}
		}
		return strings.Join(parts, ", ")
	case float64:
		return fmt.Sprint(x)
	}
	return ""
}

// ─── answers ─────────────────────────────────────────────────────────────────

// onbText takes a typed message as the answer to the pending question.
func (b *Bot) onbText(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication, text string) {
	loc := b.onbLocale(from, d, app)
	if text == "" {
		b.onbRoute(ctx, chatID, from, d, app)
		return
	}
	if app.Status == "pending_approval" {
		b.onbRoute(ctx, chatID, from, d, app)
		return
	}
	if !app.EmailConfirmed {
		code := strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) {
				return r
			}
			return -1
		}, text)
		if !sixDigits.MatchString(code) {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.ask_code", nil), inline(
				[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.code_resend_btn", nil), "onb:resend")}))
			return
		}
		confirmed, err := b.arena.OnboardingConfirmEmail(ctx, app.ID, from.ID, code)
		if err != nil {
			if IsAPIError(err, http.StatusUnprocessableEntity) {
				b.send(ctx, chatID, b.texts.T(loc, "bot.onb.code_wrong", nil), inline(
					[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.code_resend_btn", nil), "onb:resend")}))
				return
			}
			b.logger.Error("eventbot: confirm email failed", slog.String("error", err.Error()))
			b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
			return
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.email_confirmed", nil), nil)
		b.onbRoute(ctx, chatID, from, d, &confirmed)
		return
	}
	key := b.onbPendingKey(d, app)
	if key == "" || consentKeys[key] {
		b.onbRoute(ctx, chatID, from, d, app)
		return
	}
	form, err := b.onbForm(ctx, loc)
	if err != nil {
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		return
	}
	f, ok := form.Schema.Field(key)
	if !ok {
		b.onbRoute(ctx, chatID, from, d, app)
		return
	}
	switch f.Type {
	case "select", "multiselect", "bool":
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.use_buttons", nil), nil)
		b.onbAsk(ctx, chatID, from, d, app, key)
		return
	case "country":
		text = strings.ToUpper(text)
	}
	b.onbSave(ctx, chatID, from, d, app, key, text)
}

// onbPendingKey is the field the next typed answer belongs to.
func (b *Bot) onbPendingKey(d *onbDialog, app *OnbApplication) string {
	if app.Status == "info_requested" {
		for _, k := range app.RequestedFields {
			if !d.infoDone[k] {
				return k
			}
		}
		return ""
	}
	if len(app.MissingFields) > 0 {
		return app.MissingFields[0]
	}
	return ""
}

// onbSave stores one answer and moves on.
func (b *Bot) onbSave(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication, key string, value any) {
	b.onbSaveAll(ctx, chatID, from, d, app, key, map[string]any{key: value})
}

// onbSaveAll stores several answers in one call (nothing is saved unless all
// are valid); key is the question reported on a rejection.
func (b *Bot) onbSaveAll(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication, key string, answers map[string]any) {
	updated, err := b.arena.OnboardingSave(ctx, app.ID, from.ID, answers)
	if err != nil {
		b.onbSaveFailed(ctx, chatID, from, d, app, key, err)
		return
	}
	for k := range answers {
		if app.Status == "info_requested" {
			d.infoDone[k] = true
		}
		delete(d.sel, k)
	}
	b.onbRoute(ctx, chatID, from, d, &updated)
}

func (b *Bot) onbSaveFailed(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication, key string, err error) {
	loc := b.onbLocale(from, d, app)
	var ae *APIError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
		label := key
		if form, ferr := b.onbForm(ctx, loc); ferr == nil {
			if f, ok := form.Schema.Field(key); ok {
				label = f.Label
			}
		}
		textKey := "bot.onb.invalid_field"
		if ae.Fields[key] == "not_allowed" {
			textKey = "bot.onb.not_allowed"
		}
		b.send(ctx, chatID, b.texts.T(loc, textKey, map[string]any{"Label": Esc(label)}), nil)
		b.onbAsk(ctx, chatID, from, d, app, key)
	case IsAPIError(err, http.StatusConflict):
		b.onbWelcomeWith(ctx, chatID, from, d)
	default:
		b.logger.Error("eventbot: onboarding save failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
	}
}

// ─── callbacks ───────────────────────────────────────────────────────────────

// onbCallback handles every "onb:*" button.
func (b *Bot) onbCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	if !b.onbOn() {
		return
	}
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return
	}
	d := b.onb.get(ctx, from.ID)
	defer b.onb.save(ctx, from.ID, d)
	ctx = b.onb.saveBeforeReply(ctx, from.ID, d)
	loc := b.onbLocale(from, d, nil)

	switch parts[1] {
	case "lang":
		if len(parts) > 2 {
			d.locale = NormalizeLocale(parts[2])
		}
		b.onbWelcomeWith(ctx, chatID, from, d)
		return
	case "start":
		if app, err := b.arena.OnboardingCurrent(ctx, from.ID); err == nil {
			b.onbRoute(ctx, chatID, from, d, &app)
			return
		}
		d.step = onbFirst
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.ask_first", nil), nil)
		return
	}

	app, err := b.arena.OnboardingCurrent(ctx, from.ID)
	if err != nil {
		b.onbWelcomeWith(ctx, chatID, from, d)
		return
	}
	loc = b.onbLocale(from, d, &app)
	switch parts[1] {
	case "resend":
		if err := b.arena.OnboardingSendCode(ctx, app.ID, from.ID); err != nil {
			if IsAPIError(err, http.StatusTooManyRequests) {
				b.send(ctx, chatID, b.texts.T(loc, "bot.onb.rate_limited", nil), nil)
				return
			}
			b.logger.Error("eventbot: resend code failed", slog.String("error", err.Error()))
			b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
			return
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.code_resent", map[string]any{"Email": Esc(app.Email)}), nil)
	case "go":
		b.onbRoute(ctx, chatID, from, d, &app)
	case "site":
		if !b.siteButton {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.unavailable", nil), nil)
			return
		}
		link, err := b.arena.OnboardingSiteLink(ctx, app.ID, from.ID)
		if err != nil {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.unavailable", nil), nil)
			return
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.onb.site_link", map[string]any{"Url": Esc(link)}), nil)
	case "consent":
		// All three ticks are needed; the press then records them together.
		for _, c := range consentOrder {
			if !d.sel["consent"][c.key] {
				b.send(ctx, chatID, b.texts.T(loc, "bot.onb.consent_need_all", nil), nil)
				return
			}
		}
		b.onbSaveAll(ctx, chatID, from, d, &app, "accept_terms", map[string]any{
			"accept_terms": true, "accept_privacy": true, "confirm_authority": true,
		})
	case "ct":
		if len(parts) > 2 {
			b.onbConsentTick(ctx, chatID, msgID, loc, d, parts[2])
		}
	case "submit":
		b.onbSubmit(ctx, chatID, from, d, &app)
	case "s", "c", "b":
		if len(parts) < 4 {
			return
		}
		b.onbChoice(ctx, chatID, from, d, &app, parts[1], parts[2], parts[3])
	case "m", "md":
		if len(parts) < 3 {
			return
		}
		b.onbMulti(ctx, chatID, msgID, from, d, &app, parts)
	}
}

// onbChoice applies a select, country or yes/no button.
func (b *Bot) onbChoice(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication, kind, key, arg string) {
	loc := b.onbLocale(from, d, app)
	switch kind {
	case "s":
		form, err := b.onbForm(ctx, loc)
		if err != nil {
			return
		}
		f, ok := form.Schema.Field(key)
		var idx int
		if _, scanErr := fmt.Sscanf(arg, "%d", &idx); !ok || scanErr != nil || idx < 0 || idx >= len(f.Options) {
			return
		}
		b.onbSave(ctx, chatID, from, d, app, key, f.Options[idx].Value)
	case "c":
		b.onbSave(ctx, chatID, from, d, app, key, arg)
	case "b":
		b.onbSave(ctx, chatID, from, d, app, key, arg == "1")
	}
}

// onbMulti toggles an option or finishes the choice.
func (b *Bot) onbMulti(ctx context.Context, chatID int64, msgID int, from *models.User, d *onbDialog, app *OnbApplication, parts []string) {
	loc := b.onbLocale(from, d, app)
	key := parts[2]
	form, err := b.onbForm(ctx, loc)
	if err != nil {
		return
	}
	f, ok := form.Schema.Field(key)
	if !ok {
		return
	}
	if parts[1] == "md" {
		var values []string
		for _, o := range f.Options {
			if d.sel[key][o.Value] {
				values = append(values, o.Value)
			}
		}
		if len(values) == 0 {
			b.send(ctx, chatID, b.texts.T(loc, "bot.onb.use_buttons", nil), nil)
			return
		}
		b.onbSave(ctx, chatID, from, d, app, key, values)
		return
	}
	var idx int
	if len(parts) < 4 {
		return
	}
	if _, err := fmt.Sscanf(parts[3], "%d", &idx); err != nil || idx < 0 || idx >= len(f.Options) {
		return
	}
	if d.sel[key] == nil {
		d.sel[key] = map[string]bool{}
	}
	v := f.Options[idx].Value
	d.sel[key][v] = !d.sel[key][v]
	kb := b.multiselectKeyboard(loc, d, f)
	_, _ = b.tg.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{ChatID: chatID, MessageID: msgID, ReplyMarkup: kb})
}

func (b *Bot) onbSubmit(ctx context.Context, chatID int64, from *models.User, d *onbDialog, app *OnbApplication) {
	loc := b.onbLocale(from, d, app)
	if _, err := b.arena.OnboardingSubmit(ctx, app.ID, from.ID); err != nil {
		var ae *APIError
		switch {
		case errors.As(err, &ae) && ae.Code == "onboarding.email_not_confirmed":
			b.onbRoute(ctx, chatID, from, d, app)
		case errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
			if fresh, cerr := b.arena.OnboardingCurrent(ctx, from.ID); cerr == nil {
				b.onbRoute(ctx, chatID, from, d, &fresh)
				return
			}
			b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		case IsAPIError(err, http.StatusConflict):
			b.onbWelcomeWith(ctx, chatID, from, d)
		default:
			b.logger.Error("eventbot: onboarding submit failed", slog.String("error", err.Error()))
			b.send(ctx, chatID, b.texts.T(loc, "bot.error_generic", nil), nil)
		}
		return
	}
	b.onb.clear(ctx, from.ID, d)
	b.send(ctx, chatID, b.texts.T(loc, "bot.onb.submitted", nil), nil)
}

// ─── decisions ───────────────────────────────────────────────────────────────

// onbNoticeLoop polls for decisions on applications that came through the
// bot and tells each applicant, once.
func (b *Bot) onbNoticeLoop(ctx context.Context) {
	every := b.noticeEvery
	if every <= 0 {
		every = onbNoticeEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.onbAnnounce(ctx)
		}
	}
}

func (b *Bot) onbAnnounce(ctx context.Context) {
	notices, err := b.arena.OnboardingClaimNotices(ctx)
	if err != nil {
		b.logger.Warn("eventbot: onboarding notices failed", slog.String("error", err.Error()))
		return
	}
	for _, n := range notices {
		loc := NormalizeLocale(n.Locale)
		switch n.Status {
		case "approved":
			b.send(ctx, n.TelegramUserID, b.texts.T(loc, "bot.onb.approved", map[string]any{"Org": Esc(n.OrgName)}), inline(
				[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.btn_home", nil), "home")},
				b.scannerRow(loc)))
		case "rejected":
			b.send(ctx, n.TelegramUserID, b.texts.T(loc, "bot.onb.rejected", nil), nil)
		case "info_requested":
			b.send(ctx, n.TelegramUserID, b.texts.T(loc, "bot.onb.info_requested", map[string]any{"Message": Esc(n.Message)}), inline(
				[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.onb.info_go_btn", nil), "onb:go")}))
		}
	}
}
