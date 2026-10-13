package eventbot

// org_data.go — the owner's "Organization data" screens (HP-4a): the legal
// data the sales page prints in its seller block (also printed in ticket
// letters and on the PDF ticket), and for each channel that runs a hosted
// sales page its public contact and the "show seller data on the page"
// switch. Owner only (org.update; the platform operator too): the manager
// does not see the menu button and is refused on a press.
//
// The rules are arena-api's (sellerinfo): the tax-number formats, the shape
// of a public e-mail and phone, what must be filled before the switch may be
// turned on. The bot checks the shape before the call to save a round trip,
// and shows the API's refusal in plain words.
//
// State lives in ONE bot_dialogs row of kind "orgdata": the field being
// asked, the tax scheme chosen for it and the channel of a contact question,
// so a bot restart in the middle of a question loses nothing. Presses are
// "og:<kind>[:<arg>]"; a press on a channel carries its UUID:
//
//	og:h            the card                og:e:<f>    ask field f   og:x:<f>  clear it
//	og:t            choose the tax scheme   og:ts:<s>   ask the number of scheme s
//	og:tx           clear the tax number    og:k:<CC>   set the address country
//	og:cv:<ch>      a channel's card        og:ce:<ch>  ask its e-mail  og:cp:<ch> ask its phone
//	og:cxe:<ch>     clear the e-mail        og:cxp:<ch> clear the phone
//	og:on:<ch>      show the seller block   og:off:<ch> hide it

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

const orgDataDialogKind = "orgdata"

// The dialog's steps.
const (
	ogStepCard = "card"
	ogStepAsk  = "ask" // a question is open, waiting for text
)

// orgDataDialog is the screens' state, stored as the bot_dialogs row's JSON.
type orgDataDialog struct {
	OrgID uuid.UUID `json:"org_id"`
	MsgID int       `json:"msg_id,omitempty"`
	// Field is the code of the question being asked: an orgFields code,
	// "tax", or "ce"/"cp" for a channel's e-mail/phone.
	Field string `json:"field,omitempty"`
	// Scheme is the tax scheme the number is asked for.
	Scheme string `json:"scheme,omitempty"`
	// ChannelID is the channel of a contact question or of the open card.
	ChannelID *uuid.UUID `json:"channel_id,omitempty"`
}

// orgField is one editable legal field of the organization.
type orgField struct {
	Code      string // the callback code
	API       string // the key of PATCH /v1/organizations/{id}
	Key       string // the suffix of the bot.og.* texts
	Clearable bool
	Max       int // runes
}

// The card's legal fields, in the order of its buttons. The legal name and
// the address are required for the seller block, so they are changed, never
// cleared. The country is offered with the organization's own country as a
// button.
var orgFields = []orgField{
	{"n", "legal_name", "legal_name", false, 200},
	{"f", "legal_form", "legal_form", true, sellerinfo.MaxLegalFormRunes},
	{"r", "registration_number", "reg", true, 60},
	{"a", "legal_address_line1", "address", false, 200},
	{"c", "legal_address_city", "city", false, 100},
	{"z", "legal_address_postal_code", "postcode", false, 20},
	{"k", "legal_address_country", "country", true, 2},
}

func orgFieldByCode(code string) (orgField, bool) {
	for _, f := range orgFields {
		if f.Code == code {
			return f, true
		}
	}
	return orgField{}, false
}

// orgFieldValue is what the profile holds in the field now ("" = not set).
func orgFieldValue(p openapi.SellerProfile, f orgField) string {
	var v *string
	switch f.API {
	case "legal_name":
		v = p.LegalName
	case "legal_form":
		v = p.LegalForm
	case "registration_number":
		v = p.RegistrationNumber
	case "legal_address_line1":
		v = p.LegalAddressLine1
	case "legal_address_city":
		v = p.LegalAddressCity
	case "legal_address_postal_code":
		v = p.LegalAddressPostalCode
	case "legal_address_country":
		v = p.LegalAddressCountry
	}
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// canOrgData reports whether the person may edit the organization's data:
// the owner (org.update) and the platform operator. The API still decides.
func canOrgData(id *Identity) bool {
	return id != nil && id.Current != nil && (isOwner(id) || id.Superadmin)
}

// ─── parsing ──────────────────────────────────────────────────────────────────

// parseOrgField reads what the person typed for a legal field. The second
// result is the message key of the reason when the text is refused.
func parseOrgField(f orgField, raw string) (string, string) {
	s := strings.Join(strings.Fields(raw), " ")
	if f.API == "legal_address_country" {
		s = strings.ToUpper(s)
		if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
			return "", "bot.og.err_country"
		}
		return s, ""
	}
	if s == "" || utf8.RuneCountInString(s) > f.Max {
		return "", "bot.og.err_" + f.Key
	}
	return s, ""
}

// parseOrgTax folds a typed tax number into the scheme's stored form and
// checks it. The second result is the message key of the reason.
func parseOrgTax(scheme, raw string) (string, string) {
	s := sellerinfo.NormalizeTaxID(scheme, raw)
	if !sellerinfo.ValidTaxID(scheme, s) {
		return "", "bot.og.err_tax_" + scheme
	}
	return s, ""
}

// orgAPIErrKey maps a refusal of the API to the message that says plainly
// what to change. "" means the answer has no words of its own.
func orgAPIErrKey(err error) string {
	switch APIErrorCode(err) {
	case "org.invalid_tax_id", "org.invalid_tax_id_scheme":
		return "bot.og.err_tax_api"
	case "org.invalid_legal_address_country":
		return "bot.og.err_country"
	case "org.invalid_legal_form":
		return "bot.og.err_legal_form"
	case "seller.invalid_email":
		return "bot.og.err_email"
	case "seller.invalid_phone":
		return "bot.og.err_phone"
	}
	return ""
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

func (b *Bot) loadOrgData(ctx context.Context, tg int64, orgID uuid.UUID) (st orgDataDialog, step string, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, orgDataDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: org data dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return orgDataDialog{OrgID: orgID}, "", false
	}
	if !found || st.OrgID != orgID {
		return orgDataDialog{OrgID: orgID}, "", expired
	}
	return st, step, false
}

func (b *Bot) saveOrgData(ctx context.Context, tg int64, st orgDataDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, orgDataDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: org data dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveOrgData ends the dialog: whoever opens another screen is no longer
// answering an organization question, so a text typed next is not taken for one.
func (b *Bot) leaveOrgData(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, orgDataDialogKind); err != nil {
		b.logger.Warn("eventbot: org data dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── entry points ─────────────────────────────────────────────────────────────

// orgDataMenuRow is the main menu's button, for the owner.
func (b *Bot) orgDataMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if !canOrgData(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.btn", nil), CallbackData: "og:h"}}
}

// orgDataCallback handles every "og:<data>" press.
func (b *Bot) orgDataCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canOrgData(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.og.owner_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")
	if kind == "noop" {
		return
	}
	st, _, _ := b.loadOrgData(ctx, from.ID, orgID)
	st.MsgID = msgID
	st.Field, st.Scheme = "", ""

	p, err := b.arena.SellerProfile(ctx, jwt, orgID)
	if err != nil {
		b.ecError(ctx, chatID, &msgID, id, err, "home")
		return
	}

	switch kind {
	case "h":
		st.ChannelID = nil
		b.orgRenderCard(ctx, chatID, &msgID, id, st, p, "")
	case "e":
		f, ok := orgFieldByCode(arg)
		if !ok {
			return
		}
		st.Field, st.ChannelID = f.Code, nil
		b.orgRenderAskField(ctx, chatID, &msgID, id, st, p, f, "")
	case "x":
		f, ok := orgFieldByCode(arg)
		if !ok || !f.Clearable {
			return
		}
		b.orgApplyLegal(ctx, chatID, &msgID, id, jwt, st, map[string]any{f.API: nil}, "bot.og.cleared")
	case "k":
		f, _ := orgFieldByCode("k")
		value, errKey := parseOrgField(f, arg)
		if errKey != "" {
			return
		}
		b.orgApplyLegal(ctx, chatID, &msgID, id, jwt, st, map[string]any{f.API: value}, "bot.og.saved")
	case "t":
		b.orgRenderSchemes(ctx, chatID, &msgID, id, st, p)
	case "ts":
		if !sellerinfo.KnownScheme(arg) {
			return
		}
		st.Field, st.Scheme, st.ChannelID = "tax", arg, nil
		b.orgRenderAskTax(ctx, chatID, &msgID, id, st, "")
	case "tx":
		b.orgApplyLegal(ctx, chatID, &msgID, id, jwt, st, map[string]any{"tax_id": nil, "tax_id_scheme": nil}, "bot.og.cleared")
	case "tg":
		if arg != "1" && arg != "0" {
			return
		}
		b.orgApplyFormTelegram(ctx, chatID, &msgID, id, jwt, st, arg == "1")
	case "cv", "ce", "cp", "cxe", "cxp", "on", "off":
		chID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		ch, found := orgChannel(p, chID)
		if !found {
			// Another organization's channel, or one that no longer runs a
			// sales page: show the card as it is now.
			b.orgRenderCard(ctx, chatID, &msgID, id, st, p, b.texts.T(loc, "bot.og.channel_gone", nil)+"\n\n")
			return
		}
		st.ChannelID = &ch.Id
		b.orgChannelPress(ctx, chatID, &msgID, id, jwt, st, ch, kind)
	}
}

// orgChannel finds a channel of the profile by id.
func orgChannel(p openapi.SellerProfile, id uuid.UUID) (openapi.SellerProfileChannel, bool) {
	for _, c := range p.Channels {
		if c.Id == id {
			return c, true
		}
	}
	return openapi.SellerProfileChannel{}, false
}

func (b *Bot) orgChannelPress(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st orgDataDialog, ch openapi.SellerProfileChannel, kind string) {
	loc := id.Locale()
	switch kind {
	case "cv":
		b.orgRenderChannel(ctx, chatID, msgID, id, st, ch, "")
	case "ce", "cp":
		st.Field = kind
		b.orgRenderAskContact(ctx, chatID, msgID, id, st, ch, "")
	case "cxe":
		b.orgApplyDisplay(ctx, chatID, msgID, id, jwt, st, ch, map[string]any{"public_contact": map[string]any{"email": nil}}, "bot.og.cleared")
	case "cxp":
		b.orgApplyDisplay(ctx, chatID, msgID, id, jwt, st, ch, map[string]any{"public_contact": map[string]any{"phone": nil}}, "bot.og.cleared")
	case "on":
		if len(ch.Missing) > 0 {
			// Say exactly what is missing; nothing is sent.
			b.orgRenderChannel(ctx, chatID, msgID, id, st, ch, b.orgMissingText(loc, ch.Missing)+"\n\n")
			return
		}
		b.orgApplyDisplay(ctx, chatID, msgID, id, jwt, st, ch, map[string]any{"show_seller": true}, "bot.og.shown_done")
	case "off":
		b.orgApplyDisplay(ctx, chatID, msgID, id, jwt, st, ch, map[string]any{"show_seller": false}, "bot.og.hidden_done")
	}
}

// ─── writes ───────────────────────────────────────────────────────────────────

// orgApplyLegal sends legal fields to PATCH /v1/organizations/{id} and shows
// the card; a refusal the bot has words for keeps the open question.
func (b *Bot) orgApplyLegal(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st orgDataDialog, body map[string]any, doneKey string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	hidden, err := b.arena.PatchOrganization(ctx, jwt, orgID, body)
	if err != nil {
		if key := orgAPIErrKey(err); key != "" && st.Field != "" {
			p, perr := b.arena.SellerProfile(ctx, jwt, orgID)
			if perr == nil {
				if st.Field == "tax" {
					b.orgRenderAskTax(ctx, chatID, msgID, id, st, b.texts.T(loc, key, nil)+"\n\n")
					return
				}
				if f, ok := orgFieldByCode(st.Field); ok {
					b.orgRenderAskField(ctx, chatID, msgID, id, st, p, f, b.texts.T(loc, key, nil)+"\n\n")
					return
				}
			}
		}
		b.ecError(ctx, chatID, msgID, id, err, "og:h")
		return
	}
	p, err := b.arena.SellerProfile(ctx, jwt, orgID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	st.Field, st.Scheme, st.ChannelID = "", "", nil
	note := b.texts.T(loc, doneKey, nil) + "\n\n"
	if hidden > 0 {
		note += b.texts.T(loc, "bot.og.block_hidden", map[string]any{"N": hidden}) + "\n\n"
	}
	b.orgRenderCard(ctx, chatID, msgID, id, st, p, note)
}

// orgApplyFormTelegram switches whether buyers' messages from the sales page's
// contact form also go to the organization's sales chat.
func (b *Bot) orgApplyFormTelegram(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st orgDataDialog, on bool) {
	loc := id.Locale()
	p, err := b.arena.PatchSellerProfile(ctx, jwt, id.Current.OrgID, on)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "og:h")
		return
	}
	done := "bot.og.form_tg_saved_off"
	if on {
		done = "bot.og.form_tg_saved_on"
	}
	st.Field, st.Scheme, st.ChannelID = "", "", nil
	b.orgRenderCard(ctx, chatID, msgID, id, st, p, b.texts.T(loc, done, nil)+"\n\n")
}

// orgFormToTelegram reads the profile's switch; an older server that does not
// answer it means the default, on.
func orgFormToTelegram(p openapi.SellerProfile) bool {
	return p.ContactFormToTelegram == nil || *p.ContactFormToTelegram
}

// orgApplyDisplay sends a channel change to PATCH .../seller-display and
// shows the channel's card.
func (b *Bot) orgApplyDisplay(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st orgDataDialog, ch openapi.SellerProfileChannel, body map[string]any, doneKey string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	p, err := b.arena.PatchSellerDisplay(ctx, jwt, orgID, ch.Id, body)
	if err != nil {
		if APIErrorCode(err) == "seller.incomplete" {
			// The data changed since the card was drawn: read it again and say
			// what is missing now.
			if fresh, ferr := b.arena.SellerProfile(ctx, jwt, orgID); ferr == nil {
				if c, ok := orgChannel(fresh, ch.Id); ok {
					st.Field = ""
					b.orgRenderChannel(ctx, chatID, msgID, id, st, c, b.orgMissingText(loc, c.Missing)+"\n\n")
					return
				}
			}
		}
		if key := orgAPIErrKey(err); key != "" && (st.Field == "ce" || st.Field == "cp") {
			b.orgRenderAskContact(ctx, chatID, msgID, id, st, ch, b.texts.T(loc, key, nil)+"\n\n")
			return
		}
		b.ecError(ctx, chatID, msgID, id, err, "og:h")
		return
	}
	c, ok := orgChannel(p, ch.Id)
	if !ok {
		b.orgRenderCard(ctx, chatID, msgID, id, st, p, b.texts.T(loc, "bot.og.channel_gone", nil)+"\n\n")
		return
	}
	note := b.texts.T(loc, doneKey, nil) + "\n\n"
	if st.Field == "ce" && c.EmailLooksPersonal {
		note += b.texts.T(loc, "bot.og.personal_email", nil) + "\n\n"
	}
	st.Field = ""
	b.orgRenderChannel(ctx, chatID, msgID, id, st, c, note)
}

// ─── rendering ────────────────────────────────────────────────────────────────

// orgMissingText lists the missing fields in words.
func (b *Bot) orgMissingText(loc string, missing []string) string {
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.og.missing_title", nil))
	for _, m := range missing {
		sb.WriteString("\n• " + b.texts.T(loc, "bot.og.m_"+m, nil))
	}
	return sb.String()
}

func (b *Bot) orgValue(loc, v string) string {
	if strings.TrimSpace(v) == "" {
		return b.texts.T(loc, "bot.og.none", nil)
	}
	return Esc(v)
}

// orgCardText is the organization's card: one sentence on where buyers see
// the data, one line per field, the sales pages, and what is still missing.
func (b *Bot) orgCardText(loc string, p openapi.SellerProfile) string {
	val := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	line := func(key, value string) string {
		return "\n" + b.texts.T(loc, key, map[string]any{"Value": b.orgValue(loc, value)})
	}
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.og.card", map[string]any{"Org": Esc(p.OrgName)}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.og.card_where", nil) + "\n")
	sb.WriteString(line("bot.og.line_legal_name", val(p.LegalName)))
	sb.WriteString(line("bot.og.line_legal_form", val(p.LegalForm)))
	sb.WriteString(line("bot.og.line_reg", val(p.RegistrationNumber)))
	tax := ""
	if p.TaxId != nil && *p.TaxId != "" {
		tax = *p.TaxId
		if p.TaxIdScheme != nil && *p.TaxIdScheme != "" {
			tax += " (" + b.texts.T(loc, "bot.og.scheme_"+*p.TaxIdScheme, nil) + ")"
		}
	}
	sb.WriteString(line("bot.og.line_tax", tax))
	addr := []string{}
	for _, part := range []string{val(p.LegalAddressLine1), strings.TrimSpace(val(p.LegalAddressPostalCode) + " " + val(p.LegalAddressCity))} {
		if strings.TrimSpace(part) != "" {
			addr = append(addr, part)
		}
	}
	country := val(p.LegalAddressCountry)
	if country == "" {
		country = p.Country
	}
	if len(addr) > 0 {
		addr = append(addr, country)
	}
	sb.WriteString(line("bot.og.line_address", strings.Join(addr, ", ")))
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.og.pages_title", nil))
	if len(p.Channels) == 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.og.pages_none", nil))
	}
	for _, c := range p.Channels {
		state := b.texts.T(loc, "bot.og.state_hidden", nil)
		if c.ShowSeller {
			state = b.texts.T(loc, "bot.og.state_shown", nil)
		}
		sb.WriteString("\n• " + Esc(c.Name) + ": " + state)
	}
	if len(p.Channels) > 0 {
		tg := b.texts.T(loc, "bot.og.form_tg_off", nil)
		if orgFormToTelegram(p) {
			tg = b.texts.T(loc, "bot.og.form_tg_on", nil)
		}
		sb.WriteString("\n" + b.texts.T(loc, "bot.og.line_form_tg", map[string]any{"Value": tg}))
	}
	if len(p.Missing) > 0 {
		sb.WriteString("\n\n" + b.orgMissingText(loc, p.Missing))
	}
	return sb.String()
}

func (b *Bot) orgCardRows(loc string, p openapi.SellerProfile) [][]models.InlineKeyboardButton {
	btn := func(code string) models.InlineKeyboardButton {
		f, _ := orgFieldByCode(code)
		return models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.og.btn_"+f.Key, nil), CallbackData: "og:e:" + f.Code}
	}
	rows := [][]models.InlineKeyboardButton{
		{btn("n"), btn("f")},
		{btn("r"), {Text: b.texts.T(loc, "bot.og.btn_tax", nil), CallbackData: "og:t"}},
		{btn("a"), btn("c")},
		{btn("z"), btn("k")},
	}
	for _, c := range p.Channels {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         b.texts.T(loc, "bot.og.btn_page", map[string]any{"Name": truncate(c.Name, 40)}),
			CallbackData: "og:cv:" + c.Id.String(),
		}})
	}
	if len(p.Channels) > 0 {
		if orgFormToTelegram(p) {
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.btn_form_tg_off", nil), CallbackData: "og:tg:0"}})
		} else {
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.btn_form_tg_on", nil), CallbackData: "og:tg:1"}})
		}
	}
	return append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
}

func (b *Bot) orgRenderCard(ctx context.Context, chatID int64, msgID *int, id *Identity, st orgDataDialog, p openapi.SellerProfile, note string) {
	loc := id.Locale()
	st.Field, st.Scheme = "", ""
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveOrgData(ctx, id.Link.TelegramUserID, st, ogStepCard)
	b.reply(ctx, chatID, msgID, clipMessage(note+b.orgCardText(loc, p), maxMessageRunes),
		&models.InlineKeyboardMarkup{InlineKeyboard: b.orgCardRows(loc, p)})
}

// orgRenderChannel is a sales page's card: its contact, the switch, and what
// is missing before the switch may be turned on.
func (b *Bot) orgRenderChannel(ctx context.Context, chatID int64, msgID *int, id *Identity, st orgDataDialog, ch openapi.SellerProfileChannel, note string) {
	loc := id.Locale()
	st.Field, st.Scheme, st.ChannelID = "", "", &ch.Id
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveOrgData(ctx, id.Link.TelegramUserID, st, ogStepCard)
	email, phone := "", ""
	if ch.PublicContact != nil {
		if ch.PublicContact.Email != nil {
			email = *ch.PublicContact.Email
		}
		if ch.PublicContact.Phone != nil {
			phone = *ch.PublicContact.Phone
		}
	}
	state := b.texts.T(loc, "bot.og.state_hidden", nil)
	if ch.ShowSeller {
		state = b.texts.T(loc, "bot.og.state_shown", nil)
	}
	var sb strings.Builder
	sb.WriteString(note)
	sb.WriteString(b.texts.T(loc, "bot.og.page_card", map[string]any{"Name": Esc(ch.Name)}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.og.page_where", nil) + "\n")
	sb.WriteString("\n" + b.texts.T(loc, "bot.og.line_email", map[string]any{"Value": b.orgValue(loc, email)}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.og.line_phone", map[string]any{"Value": b.orgValue(loc, phone)}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.og.line_state", map[string]any{"Value": state}))
	if ch.EmailLooksPersonal && !strings.Contains(note, b.texts.T(loc, "bot.og.personal_email", nil)) {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.og.personal_email", nil))
	}
	if len(ch.Missing) > 0 && !ch.ShowSeller && note == "" {
		sb.WriteString("\n\n" + b.orgMissingText(loc, ch.Missing))
	}
	cid := ch.Id.String()
	rows := [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.og.btn_email", nil), CallbackData: "og:ce:" + cid}, {Text: b.texts.T(loc, "bot.og.btn_phone", nil), CallbackData: "og:cp:" + cid}},
	}
	if ch.ShowSeller {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.btn_hide", nil), CallbackData: "og:off:" + cid}})
	} else {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.btn_show", nil), CallbackData: "og:on:" + cid}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "og:h"},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	b.reply(ctx, chatID, msgID, clipMessage(sb.String(), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// orgRenderAskField opens the question of one legal field.
func (b *Bot) orgRenderAskField(ctx context.Context, chatID int64, msgID *int, id *Identity, st orgDataDialog, p openapi.SellerProfile, f orgField, prefix string) {
	loc := id.Locale()
	st.Field, st.ChannelID = f.Code, nil
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveOrgData(ctx, id.Link.TelegramUserID, st, ogStepAsk)
	cur := orgFieldValue(p, f)
	text := prefix + b.texts.T(loc, "bot.og.q_"+f.Key, nil) + "\n\n" +
		b.texts.T(loc, "bot.og.now", map[string]any{"Value": b.orgValue(loc, cur)})
	var rows [][]models.InlineKeyboardButton
	if f.API == "legal_address_country" && p.Country != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: p.Country, CallbackData: "og:k:" + p.Country}})
	}
	if f.Clearable && cur != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.clear_btn", nil), CallbackData: "og:x:" + f.Code}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.cancel_btn", nil), CallbackData: "og:h"}})
	b.reply(ctx, chatID, msgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// orgRenderSchemes asks which kind of tax number the organization has.
func (b *Bot) orgRenderSchemes(ctx context.Context, chatID int64, msgID *int, id *Identity, st orgDataDialog, p openapi.SellerProfile) {
	loc := id.Locale()
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveOrgData(ctx, id.Link.TelegramUserID, st, ogStepCard)
	cur := ""
	if p.TaxId != nil {
		cur = *p.TaxId
	}
	text := b.texts.T(loc, "bot.og.q_scheme", nil) + "\n\n" +
		b.texts.T(loc, "bot.og.now", map[string]any{"Value": b.orgValue(loc, cur)})
	var rows [][]models.InlineKeyboardButton
	for _, s := range sellerinfo.Schemes {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.scheme_"+s, nil), CallbackData: "og:ts:" + s}})
	}
	if cur != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.clear_btn", nil), CallbackData: "og:tx"}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.cancel_btn", nil), CallbackData: "og:h"}})
	b.reply(ctx, chatID, msgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// orgRenderAskTax asks for the number of the chosen scheme.
func (b *Bot) orgRenderAskTax(ctx context.Context, chatID int64, msgID *int, id *Identity, st orgDataDialog, prefix string) {
	loc := id.Locale()
	st.Field, st.ChannelID = "tax", nil
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveOrgData(ctx, id.Link.TelegramUserID, st, ogStepAsk)
	text := prefix + b.texts.T(loc, "bot.og.q_tax_"+st.Scheme, nil)
	rows := [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.og.other_scheme_btn", nil), CallbackData: "og:t"}},
		{{Text: b.texts.T(loc, "bot.og.cancel_btn", nil), CallbackData: "og:h"}},
	}
	b.reply(ctx, chatID, msgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// orgRenderAskContact asks for a channel's public e-mail or phone.
func (b *Bot) orgRenderAskContact(ctx context.Context, chatID int64, msgID *int, id *Identity, st orgDataDialog, ch openapi.SellerProfileChannel, prefix string) {
	loc := id.Locale()
	st.ChannelID = &ch.Id
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveOrgData(ctx, id.Link.TelegramUserID, st, ogStepAsk)
	cur, key, clear := "", "bot.og.q_email", "og:cxe:"
	if st.Field == "cp" {
		key, clear = "bot.og.q_phone", "og:cxp:"
	}
	if ch.PublicContact != nil {
		if st.Field == "ce" && ch.PublicContact.Email != nil {
			cur = *ch.PublicContact.Email
		}
		if st.Field == "cp" && ch.PublicContact.Phone != nil {
			cur = *ch.PublicContact.Phone
		}
	}
	text := prefix + b.texts.T(loc, key, nil) + "\n\n" + b.texts.T(loc, "bot.og.now", map[string]any{"Value": b.orgValue(loc, cur)})
	var rows [][]models.InlineKeyboardButton
	if cur != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.clear_btn", nil), CallbackData: clear + ch.Id.String()}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.og.cancel_btn", nil), CallbackData: "og:cv:" + ch.Id.String()}})
	b.reply(ctx, chatID, msgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── typed answers ────────────────────────────────────────────────────────────

// orgDataText consumes the text of a question in progress. It reports whether
// the text was taken.
func (b *Bot) orgDataText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st orgDataDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, orgDataDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: org data dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.og.btn", nil), CallbackData: "og:h"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || step != ogStepAsk || st.Field == "" {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canOrgData(id) {
		b.leaveOrgData(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	p, err := b.arena.SellerProfile(ctx, jwt, st.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, edit, id, err, "home")
		return true
	}
	switch st.Field {
	case "tax":
		value, errKey := parseOrgTax(st.Scheme, text)
		if errKey != "" {
			b.orgRenderAskTax(ctx, chatID, edit, id, st, b.texts.T(loc, errKey, nil)+"\n\n")
			return true
		}
		b.orgApplyLegal(ctx, chatID, edit, id, jwt, st, map[string]any{"tax_id_scheme": st.Scheme, "tax_id": value}, "bot.og.saved")
	case "ce", "cp":
		if st.ChannelID == nil {
			b.leaveOrgData(ctx, from.ID)
			return false
		}
		ch, ok := orgChannel(p, *st.ChannelID)
		if !ok {
			b.orgRenderCard(ctx, chatID, edit, id, st, p, b.texts.T(loc, "bot.og.channel_gone", nil)+"\n\n")
			return true
		}
		if st.Field == "ce" {
			value, ok := sellerinfo.NormalizeEmail(text)
			if !ok {
				b.orgRenderAskContact(ctx, chatID, edit, id, st, ch, b.texts.T(loc, "bot.og.err_email", nil)+"\n\n")
				return true
			}
			b.orgApplyDisplay(ctx, chatID, edit, id, jwt, st, ch, map[string]any{"public_contact": map[string]any{"email": value}}, "bot.og.saved")
			return true
		}
		value, ok := sellerinfo.NormalizePhone(text)
		if !ok {
			b.orgRenderAskContact(ctx, chatID, edit, id, st, ch, b.texts.T(loc, "bot.og.err_phone", nil)+"\n\n")
			return true
		}
		b.orgApplyDisplay(ctx, chatID, edit, id, jwt, st, ch, map[string]any{"public_contact": map[string]any{"phone": value}}, "bot.og.saved")
	default:
		f, ok := orgFieldByCode(st.Field)
		if !ok {
			b.leaveOrgData(ctx, from.ID)
			return false
		}
		value, errKey := parseOrgField(f, text)
		if errKey != "" {
			b.orgRenderAskField(ctx, chatID, edit, id, st, p, f, b.texts.T(loc, errKey, nil)+"\n\n")
			return true
		}
		b.orgApplyLegal(ctx, chatID, edit, id, jwt, st, map[string]any{f.API: value}, "bot.og.saved")
	}
	return true
}
