package eventbot

// promoters.go — the "Promoters" screens of the bot (EC-14, spec 35 §6.5): a
// paged list of the organization's promoters, the card of one with a button
// per field, the one-question dialog behind each button and the archive
// confirmation. Reachable from the main menu and from the event card's
// promoter line. The rules (name unique among active promoters, the page
// address' alphabet and its platform-wide uniqueness, the length limits) are
// arena-api's: the bot validates only what saves a round trip and shows the
// API's refusal in plain words.
//
// State lives in ONE bot_dialogs row of kind "promoter" (dialogs.go): the
// list's page and the ids on screen, the promoter whose card is open, the
// field being asked and the page address awaiting its confirmation, so a bot
// restart in the middle of a question loses nothing. Presses are
// "pr:<kind>:<arg>"; a row of the list is named by its INDEX on the page
// (paging.go), anything that changes a promoter carries its UUID:
//
//	pr:l             the list (fresh)       pr:p:<n>      a page       pr:b   back to the list
//	pr:o:<i>         open row i             pr:v:<id>     the card
//	pr:ev:<event>    the event's promoter   pr:e:<f>:<id> ask field f  pr:x:<f>:<id>  clear it
//	pr:ok:<id>       confirm a new page address
//	pr:ar:<id>       archive (asks)         pr:ay:<id>    confirm the archive
//
// Changing a page address breaks the links already handed out, so it is
// confirmed with one press; so is the archive. Neither is a typed word: both
// can be undone through the API.

import (
	"context"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const promoterDialogKind = "promoter"

// The dialog's steps.
const (
	prStepList    = "list"
	prStepCard    = "card"
	prStepAsk     = "ask"     // a field's question is open, waiting for text
	prStepSlug    = "slug"    // a new page address waits for its confirmation press
	prStepArchive = "archive" // the archive question is open
)

// promoterDialog is the screens' state, stored as the bot_dialogs row's JSON.
type promoterDialog struct {
	OrgID uuid.UUID `json:"org_id"`
	MsgID int       `json:"msg_id,omitempty"`
	// Page/IDs are the list on screen.
	Page int         `json:"page"`
	IDs  []uuid.UUID `json:"ids,omitempty"`
	// CardID names the promoter of the open card, question or confirmation.
	CardID *uuid.UUID `json:"card_id,omitempty"`
	// Field is the code of the field being asked; Pending the page address
	// waiting for its confirmation.
	Field   string `json:"field,omitempty"`
	Pending string `json:"pending,omitempty"`
	// BackEvent is the event whose card opened the promoter, so the card can
	// offer the way back to it.
	BackEvent *uuid.UUID `json:"back_event,omitempty"`
}

func newPromoterDialog(orgID uuid.UUID) promoterDialog { return promoterDialog{OrgID: orgID, Page: 1} }

// promoterField is one editable field of the card.
type promoterField struct {
	Code      string // the callback code
	API       string // the key in PATCH .../promoters/{id}
	Key       string // the suffix of the bot.prom.* texts
	Clearable bool
}

// The card's fields, in the order of its buttons. The name cannot be cleared
// (the API refuses a blank one) and the page address is changed, not cleared:
// a promoter without a page has no link to hand out, and every event link is
// built from it.
var promoterFields = []promoterField{
	{"n", "name", "name", false},
	{"a", "address", "address", true},
	{"t", "legal_id", "tax", true},
	{"p", "phone", "phone", true},
	{"m", "email", "email", true},
	{"w", "website", "website", true},
	{"s", "slug", "page", false},
}

func promoterFieldByCode(code string) (promoterField, bool) {
	for _, f := range promoterFields {
		if f.Code == code {
			return f, true
		}
	}
	return promoterField{}, false
}

// promoterValue is what the promoter holds in the field now ("" = not set).
func promoterValue(p openapi.Promoter, f promoterField) string {
	var v *string
	switch f.API {
	case "name":
		return p.Name
	case "address":
		v = p.Address
	case "legal_id":
		v = p.LegalId
	case "phone":
		v = p.Phone
	case "email":
		v = p.Email
	case "website":
		v = p.Website
	case "slug":
		v = p.Slug
	}
	if v == nil {
		return ""
	}
	return *v
}

// canPromoters reports whether the person may manage promoters: the owner,
// the manager and the platform operator hold promoter.manage (the agent role
// holds none). The API still decides every call.
func canPromoters(id *Identity) bool { return canViewSales(id) }

// ─── parsing ──────────────────────────────────────────────────────────────────

// The limits the bot enforces before the API is asked.
const (
	maxPromoterNameRunes  = 120
	maxPromoterTextRunes  = 300 // address and website, as the API
	maxPromoterTaxRunes   = 40
	maxPromoterEmailRunes = 120
)

// parsePromoterField reads what the person typed for a field. The second
// result is the message key of the reason when the text is refused. The name,
// address and tax id are free text; the phone, e-mail and website are checked
// for shape only; the page address is folded to lower case and a pasted link
// is cut down to its last segment, the rest being the API's call.
func parsePromoterField(f promoterField, raw string) (string, string) {
	switch f.API {
	case "name":
		s := strings.Join(strings.Fields(raw), " ")
		if s == "" {
			return "", "bot.prom.err_name_empty"
		}
		if utf8.RuneCountInString(s) > maxPromoterNameRunes {
			return "", "bot.prom.err_name_long"
		}
		return s, ""
	case "address":
		s := strings.Join(strings.Fields(raw), " ")
		if s == "" || utf8.RuneCountInString(s) > maxPromoterTextRunes {
			return "", "bot.prom.err_address_long"
		}
		return s, ""
	case "legal_id":
		s := strings.TrimSpace(raw)
		if s == "" || utf8.RuneCountInString(s) > maxPromoterTaxRunes || strings.ContainsAny(s, "\r\n") {
			return "", "bot.prom.err_tax"
		}
		return s, ""
	case "phone":
		s := strings.TrimSpace(raw)
		digits := 0
		for _, r := range s {
			switch {
			case unicode.IsDigit(r):
				digits++
			case r == '+' || r == ' ' || r == '(' || r == ')' || r == '-' || r == '.':
			default:
				return "", "bot.prom.err_phone"
			}
		}
		if digits < 6 || digits > 15 {
			return "", "bot.prom.err_phone"
		}
		return s, ""
	case "email":
		s := strings.ToLower(strings.TrimSpace(raw))
		if !looksLikeEmail(s) || utf8.RuneCountInString(s) > maxPromoterEmailRunes {
			return "", "bot.prom.err_email"
		}
		return s, ""
	case "website":
		s := strings.TrimSpace(raw)
		if s == "" || utf8.RuneCountInString(s) > maxPromoterTextRunes || strings.ContainsAny(s, " \t\r\n") || !strings.Contains(s, ".") {
			return "", "bot.prom.err_website"
		}
		return s, ""
	case "slug":
		s := promoterSlugFromInput(raw)
		if !validPromoterSlug(s) {
			return "", "bot.prom.err_slug_invalid"
		}
		return s, ""
	}
	return "", "bot.error_generic"
}

// promoterSlugFromInput folds what was typed into a candidate page address:
// lower case, and when a whole link was pasted ("https://host/some-slug/")
// only its last path segment. The alphabet is checked by the API.
func promoterSlugFromInput(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(s, "/") {
		parts := strings.Split(strings.TrimRight(s, "/"), "/")
		s = parts[len(parts)-1]
	}
	return strings.TrimSpace(s)
}

// validPromoterSlug mirrors the API's rule (hcatalog.NormalizePromoterSlug):
// 2-64 characters of a-z and 0-9 with single inner hyphens. It is checked here
// as well so that a malformed address is refused BEFORE the person is asked to
// confirm replacing a working one; the API still decides.
func validPromoterSlug(s string) bool {
	if len(s) < 2 || len(s) > 64 {
		return false
	}
	prevHyphen := true
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			prevHyphen = false
		case c == '-':
			if prevHyphen || i == len(s)-1 {
				return false
			}
			prevHyphen = true
		default:
			return false
		}
	}
	return true
}

// promoterAPIErrKey maps a refusal of the PATCH route to the message that says
// plainly what to change. "" means the API's answer has no words of its own.
func promoterAPIErrKey(err error) string {
	switch APIErrorCode(err) {
	case promoterCodeDuplicateName:
		return "bot.prom.err_name_taken"
	case promoterCodeDuplicateSlug:
		return "bot.prom.err_slug_taken"
	case promoterCodeInvalidSlug:
		return "bot.prom.err_slug_invalid"
	case promoterCodeInvalidName:
		return "bot.prom.err_name_empty"
	case promoterCodeInvalidWeb:
		return "bot.prom.err_website"
	case promoterCodeInvalidAddr:
		return "bot.prom.err_address_long"
	}
	return ""
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadPromoterState reads the dialog, or starts an empty one when there is
// none, it ran out (expired is then true, once) or it belongs to another
// organization.
func (b *Bot) loadPromoterState(ctx context.Context, tg int64, orgID uuid.UUID) (st promoterDialog, step string, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, promoterDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: promoter dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return newPromoterDialog(orgID), "", false
	}
	if !found || st.OrgID != orgID {
		return newPromoterDialog(orgID), "", expired
	}
	if st.Page < 1 {
		st.Page = 1
	}
	return st, step, false
}

func (b *Bot) savePromoterState(ctx context.Context, tg int64, st promoterDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, promoterDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: promoter dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leavePromoter ends the dialog: whoever opens another screen is no longer
// answering a promoter question, so a text typed next is not taken for one.
func (b *Bot) leavePromoter(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, promoterDialogKind); err != nil {
		b.logger.Warn("eventbot: promoter dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── entry points ─────────────────────────────────────────────────────────────

// promoterMenuRow is the main menu's button, for a role that may manage
// promoters.
func (b *Bot) promoterMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if !canPromoters(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.prom.btn", nil), CallbackData: "pr:l"}}
}

// promoterEventLine is the event card's promoter line.
func (b *Bot) promoterEventLine(loc string, ev openapi.EventItem) string {
	if ev.PromoterName != nil && *ev.PromoterName != "" {
		return "\n" + b.texts.T(loc, "bot.prom.event_line", map[string]any{"Name": Esc(*ev.PromoterName)})
	}
	return "\n" + b.texts.T(loc, "bot.prom.event_line_org", nil)
}

// promoterEventRow is the event card's button: the promoter of that event.
func (b *Bot) promoterEventRow(loc string, id *Identity, eventID uuid.UUID) []models.InlineKeyboardButton {
	if !canPromoters(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.prom.event_btn", nil), CallbackData: "pr:ev:" + eventID.String()}}
}

// promoterPager is paging.go's row with the position button kept inside the
// "pr:" prefix: a bare "noop" press would end the dialog.
func (b *Bot) promoterPager(loc string, p Pager) []models.InlineKeyboardButton {
	row := PagerRow(p, b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil))
	for i := range row {
		if row[i].CallbackData == calNoop {
			row[i].CallbackData = "pr:noop"
		}
	}
	return row
}

// promoterCallback handles every "pr:<data>" press.
func (b *Bot) promoterCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canPromoters(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")

	switch kind {
	case "noop": // the position button of a pager
		return
	case "l":
		b.promoterRenderList(ctx, chatID, &msgID, id, jwt, newPromoterDialog(orgID), "")
		return
	case "ev":
		eventID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.promoterOfEvent(ctx, chatID, &msgID, id, jwt, eventID)
		return
	case "v", "e", "x", "ok", "ar", "ay":
		b.promoterCardPress(ctx, chatID, msgID, from, id, jwt, kind, arg)
		return
	}

	st, _, expired := b.loadPromoterState(ctx, from.ID, orgID)
	if expired {
		// The screen ran out while the person was away: say so once and show
		// the list, rather than act on a press made on stale rows.
		b.promoterRenderList(ctx, chatID, &msgID, id, jwt, newPromoterDialog(orgID), b.texts.T(loc, "bot.dialog_expired", nil)+"\n\n")
		return
	}
	switch kind {
	case "b":
		b.promoterRenderList(ctx, chatID, &msgID, id, jwt, st, "")
	case "p":
		st.Page = ParsePage(arg)
		b.promoterRenderList(ctx, chatID, &msgID, id, jwt, st, "")
	case "o":
		i, ok := ParseIndex(arg, len(st.IDs))
		if !ok {
			// The row is gone from the stored page (a stale message): show the
			// list as it is now instead of opening the wrong promoter.
			b.promoterRenderList(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.promoterShowCard(ctx, chatID, &msgID, id, jwt, st, st.IDs[i], "")
	}
}

// promoterOfEvent opens the card of the event's promoter, or the list with a
// word about it when the organization itself promotes the event.
func (b *Bot) promoterOfEvent(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, eventID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	for _, e := range events {
		if e.Id != eventID {
			continue
		}
		st := newPromoterDialog(orgID)
		if e.PromoterId == nil {
			b.promoterRenderList(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.prom.event_org_note", map[string]any{"Name": Esc(e.Name)})+"\n\n")
			return
		}
		st.BackEvent = &eventID
		b.promoterShowCard(ctx, chatID, msgID, id, jwt, st, *e.PromoterId, "")
		return
	}
	b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.ec.not_found", nil), b.backKeyboard(loc, "home"))
}

// promoterFind reads the organization's active promoters and picks one.
func (b *Bot) promoterFind(ctx context.Context, jwt string, orgID, promoterID uuid.UUID) (openapi.Promoter, bool, error) {
	list, err := b.arena.PromoterList(ctx, jwt, orgID)
	if err != nil {
		return openapi.Promoter{}, false, err
	}
	for _, p := range list {
		if p.Id == promoterID {
			return p, true, nil
		}
	}
	return openapi.Promoter{}, false, nil
}

// promoterCardPress handles the presses that name a promoter by its UUID.
func (b *Bot) promoterCardPress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, kind, arg string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	var fieldCode, idPart string
	switch kind {
	case "e", "x": // pr:e:<field>:<id>
		fieldCode, idPart, _ = strings.Cut(arg, ":")
	default:
		idPart = arg
	}
	promoterID, err := uuid.Parse(idPart)
	if err != nil {
		return
	}
	// A press that names its promoter works after the dialog ran out too; only
	// the two confirmations below need the question the dialog remembers.
	st, step, _ := b.loadPromoterState(ctx, from.ID, orgID)
	p, found, err := b.promoterFind(ctx, jwt, orgID, promoterID)
	if err != nil {
		b.ecError(ctx, chatID, &msgID, id, err, "pr:l")
		return
	}
	if !found {
		b.promoterRenderList(ctx, chatID, &msgID, id, jwt, newPromoterDialog(orgID), b.texts.T(loc, "bot.prom.gone", nil)+"\n\n")
		return
	}
	st.MsgID = msgID

	switch kind {
	case "v":
		b.promoterRenderCard(ctx, chatID, &msgID, id, st, p, "")
	case "e":
		f, ok := promoterFieldByCode(fieldCode)
		if !ok {
			return
		}
		st.CardID, st.Field, st.Pending = &p.Id, f.Code, ""
		b.promoterRenderAsk(ctx, chatID, &msgID, id, st, p, f, "")
	case "x":
		f, ok := promoterFieldByCode(fieldCode)
		if !ok || !f.Clearable {
			return
		}
		b.promoterApply(ctx, chatID, &msgID, id, jwt, st, p, f, "")
	case "ok":
		// Only the confirmation the dialog is waiting for, for THIS promoter.
		if step != prStepSlug || st.CardID == nil || *st.CardID != p.Id || st.Pending == "" {
			b.promoterRenderCard(ctx, chatID, &msgID, id, st, p, "")
			return
		}
		f, _ := promoterFieldByCode("s")
		b.promoterApply(ctx, chatID, &msgID, id, jwt, st, p, f, st.Pending)
	case "ar":
		st.CardID, st.Field, st.Pending = &p.Id, "", ""
		b.savePromoterState(ctx, from.ID, st, prStepArchive)
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.prom.archive_yes_btn", nil), CallbackData: "pr:ay:" + p.Id.String()}},
			{{Text: b.texts.T(loc, "bot.prom.cancel_btn", nil), CallbackData: "pr:v:" + p.Id.String()}},
		}}
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.prom.archive_ask", map[string]any{"Name": Esc(p.Name)}), kb)
	case "ay":
		// The archive needs its own question open, for THIS promoter.
		if step != prStepArchive || st.CardID == nil || *st.CardID != p.Id {
			b.promoterRenderCard(ctx, chatID, &msgID, id, st, p, "")
			return
		}
		if _, err := b.arena.PatchPromoter(ctx, jwt, orgID, p.Id, map[string]any{"archived": true}); err != nil {
			b.ecError(ctx, chatID, &msgID, id, err, "pr:l")
			return
		}
		b.promoterRenderList(ctx, chatID, &msgID, id, jwt, newPromoterDialog(orgID), b.texts.T(loc, "bot.prom.archived_done", map[string]any{"Name": Esc(p.Name)})+"\n\n")
	}
}

// promoterApply sends one field's new value (value "" clears it) and, when
// the API accepts it, shows the card; a refusal the bot has words for keeps
// the question open with the reason above it.
func (b *Bot) promoterApply(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoterDialog, p openapi.Promoter, f promoterField, value string) {
	loc := id.Locale()
	var sent any
	if value != "" {
		sent = value
	}
	updated, err := b.arena.PatchPromoter(ctx, jwt, id.Current.OrgID, p.Id, map[string]any{f.API: sent})
	if err != nil {
		if key := promoterAPIErrKey(err); key != "" {
			st.CardID, st.Field, st.Pending = &p.Id, f.Code, ""
			b.promoterRenderAsk(ctx, chatID, editMsgID, id, st, p, f, b.texts.T(loc, key, nil)+"\n\n")
			return
		}
		b.ecError(ctx, chatID, editMsgID, id, err, "pr:l")
		return
	}
	note := b.texts.T(loc, "bot.prom.saved", nil) + "\n\n"
	if value == "" {
		note = b.texts.T(loc, "bot.prom.cleared", nil) + "\n\n"
	}
	if f.API == "slug" && updated.Slug != nil {
		note = b.texts.T(loc, "bot.prom.saved_page", map[string]any{"URL": Esc(b.promoterPageURL(*updated.Slug))}) + "\n\n"
	}
	b.promoterRenderCard(ctx, chatID, editMsgID, id, st, updated, note)
}

// promoterPageURL is the promoter's public page: <tickets base>/<slug>, or the
// bare "/<slug>" when the bot does not know the storefront's address.
func (b *Bot) promoterPageURL(slug string) string {
	return strings.TrimRight(b.ticketsBaseURL, "/") + "/" + slug
}

// ─── the list ─────────────────────────────────────────────────────────────────

// promoterRenderList loads the active promoters, draws one page and stores the
// ids of the rows it shows.
func (b *Bot) promoterRenderList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoterDialog, note string) {
	loc := id.Locale()
	all, err := b.arena.PromoterList(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	total := int64(len(all))
	pages := PagesFor(total, listPageSize)
	if st.Page < 1 {
		st.Page = 1
	}
	if st.Page > pages {
		st.Page = pages
	}
	start := (st.Page - 1) * listPageSize
	end := start + listPageSize
	if end > len(all) {
		end = len(all)
	}
	pageItems := all[start:end]
	st.IDs = st.IDs[:0]
	for _, p := range pageItems {
		st.IDs = append(st.IDs, p.Id)
	}
	st.CardID, st.Field, st.Pending, st.BackEvent = nil, "", "", nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoterState(ctx, id.Link.TelegramUserID, st, prStepList)

	text := note + b.texts.T(loc, "bot.prom.list_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Total": total, "Page": st.Page, "Pages": pages,
	})
	if len(pageItems) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.prom.empty", nil)
	} else {
		text += "\n\n" + b.texts.T(loc, "bot.prom.list_hint", nil)
	}
	var rows [][]models.InlineKeyboardButton
	for i, p := range pageItems {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: truncate(p.Name, 48), CallbackData: ItemCallback("pr:o", i),
		}})
	}
	if nav := b.promoterPager(loc, Pager{Prefix: "pr:p", Page: st.Page, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── the card ─────────────────────────────────────────────────────────────────

// promoterShowCard reads the promoter afresh and draws its card.
func (b *Bot) promoterShowCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoterDialog, promoterID uuid.UUID, note string) {
	p, found, err := b.promoterFind(ctx, jwt, id.Current.OrgID, promoterID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "pr:l")
		return
	}
	if !found {
		b.promoterRenderList(ctx, chatID, editMsgID, id, jwt, newPromoterDialog(id.Current.OrgID), b.texts.T(id.Locale(), "bot.prom.gone", nil)+"\n\n")
		return
	}
	b.promoterRenderCard(ctx, chatID, editMsgID, id, st, p, note)
}

// promoterCardText is the card: the name and one line per field, "not set"
// where nothing is stored.
func (b *Bot) promoterCardText(loc string, p openapi.Promoter) string {
	none := b.texts.T(loc, "bot.prom.none", nil)
	line := func(key, value string) string {
		if strings.TrimSpace(value) == "" {
			value = none
		} else {
			value = Esc(value)
		}
		return "\n" + b.texts.T(loc, key, map[string]any{"Value": value})
	}
	page := none
	if p.Slug != nil && *p.Slug != "" {
		page = Esc(b.promoterPageURL(*p.Slug))
	}
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.prom.card", map[string]any{"Name": Esc(p.Name)}))
	sb.WriteString(line("bot.prom.line_address", promoterValue(p, promoterFields[1])))
	sb.WriteString(line("bot.prom.line_tax", promoterValue(p, promoterFields[2])))
	sb.WriteString(line("bot.prom.line_phone", promoterValue(p, promoterFields[3])))
	sb.WriteString(line("bot.prom.line_email", promoterValue(p, promoterFields[4])))
	sb.WriteString(line("bot.prom.line_website", promoterValue(p, promoterFields[5])))
	sb.WriteString("\n" + b.texts.T(loc, "bot.prom.line_page", map[string]any{"Value": page}))
	return sb.String()
}

// promoterCardRows is the card's buttons: a button per field, the archive and
// the way back (to the list, and to the event the card was opened from).
func (b *Bot) promoterCardRows(loc string, st promoterDialog, p openapi.Promoter) [][]models.InlineKeyboardButton {
	pid := p.Id.String()
	btn := func(code string) models.InlineKeyboardButton {
		f, _ := promoterFieldByCode(code)
		return models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.prom.btn_"+f.Key, nil), CallbackData: "pr:e:" + f.Code + ":" + pid}
	}
	rows := [][]models.InlineKeyboardButton{
		{btn("n"), btn("a")},
		{btn("t"), btn("p")},
		{btn("m"), btn("w")},
		{btn("s")},
		{{Text: b.texts.T(loc, "bot.prom.btn_archive", nil), CallbackData: "pr:ar:" + pid}},
	}
	back := []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "pr:b"}}
	if st.BackEvent != nil {
		back = append(back, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.prom.event_back_btn", nil), CallbackData: "ec:o:" + st.BackEvent.String()})
	}
	back = append(back, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"})
	return append(rows, back)
}

func (b *Bot) promoterRenderCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st promoterDialog, p openapi.Promoter, note string) {
	loc := id.Locale()
	st.CardID, st.Field, st.Pending = &p.Id, "", ""
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoterState(ctx, id.Link.TelegramUserID, st, prStepCard)
	b.reply(ctx, chatID, editMsgID, clipMessage(note+b.promoterCardText(loc, p), maxMessageRunes),
		&models.InlineKeyboardMarkup{InlineKeyboard: b.promoterCardRows(loc, st, p)})
}

// ─── one question ─────────────────────────────────────────────────────────────

// promoterRenderAsk opens the question of one field and waits for the text.
func (b *Bot) promoterRenderAsk(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st promoterDialog, p openapi.Promoter, f promoterField, prefix string) {
	loc := id.Locale()
	st.CardID, st.Field, st.Pending = &p.Id, f.Code, ""
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoterState(ctx, id.Link.TelegramUserID, st, prStepAsk)

	cur := promoterValue(p, f)
	now := b.texts.T(loc, "bot.prom.none", nil)
	if cur != "" {
		now = Esc(cur)
		if f.API == "slug" {
			now = Esc(b.promoterPageURL(cur))
		}
	}
	text := prefix + b.texts.T(loc, "bot.prom.q_"+f.Key, map[string]any{
		"URL": Esc(strings.TrimRight(b.ticketsBaseURL, "/") + "/"),
	}) + "\n\n" + b.texts.T(loc, "bot.prom.now", map[string]any{"Value": now})
	if f.API == "email" {
		text += "\n\n" + b.texts.T(loc, "bot.prom.email_note", nil)
	}
	var rows [][]models.InlineKeyboardButton
	if f.Clearable && cur != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.prom.clear_btn", nil), CallbackData: "pr:x:" + f.Code + ":" + p.Id.String()}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.prom.cancel_btn", nil), CallbackData: "pr:v:" + p.Id.String()}})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// promoterText consumes the text of a question in progress. It reports whether
// the text was taken.
func (b *Bot) promoterText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st promoterDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, promoterDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: promoter dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.prom.btn", nil), CallbackData: "pr:l"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || (step != prStepAsk && step != prStepSlug) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canPromoters(id) || st.CardID == nil {
		b.leavePromoter(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	f, ok := promoterFieldByCode(st.Field)
	if step == prStepSlug {
		// A page address waits for its press; a text now is a new attempt.
		f, ok = promoterFieldByCode("s")
	}
	if !ok {
		b.leavePromoter(ctx, from.ID)
		return false
	}
	p, found, err := b.promoterFind(ctx, jwt, st.OrgID, *st.CardID)
	if err != nil {
		b.ecError(ctx, chatID, edit, id, err, "pr:l")
		return true
	}
	if !found {
		b.promoterRenderList(ctx, chatID, edit, id, jwt, newPromoterDialog(st.OrgID), b.texts.T(loc, "bot.prom.gone", nil)+"\n\n")
		return true
	}
	value, errKey := parsePromoterField(f, text)
	if errKey != "" {
		b.promoterRenderAsk(ctx, chatID, edit, id, st, p, f, b.texts.T(loc, errKey, nil)+"\n\n")
		return true
	}
	if f.API == "slug" && p.Slug != nil && strings.EqualFold(*p.Slug, value) {
		b.promoterRenderCard(ctx, chatID, edit, id, st, p, "")
		return true
	}
	if f.API == "slug" && p.Slug != nil && *p.Slug != "" {
		// A changed address breaks the links already handed out: say what the
		// new one is, and save only on the press.
		st.CardID, st.Field, st.Pending = &p.Id, f.Code, value
		b.savePromoterState(ctx, from.ID, st, prStepSlug)
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.prom.slug_save_btn", nil), CallbackData: "pr:ok:" + p.Id.String()}},
			{{Text: b.texts.T(loc, "bot.prom.cancel_btn", nil), CallbackData: "pr:v:" + p.Id.String()}},
		}}
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.prom.slug_confirm", map[string]any{
			"Old": Esc(b.promoterPageURL(*p.Slug)), "New": Esc(b.promoterPageURL(value)),
		}), kb)
		return true
	}
	b.promoterApply(ctx, chatID, edit, id, jwt, st, p, f, value)
	return true
}
