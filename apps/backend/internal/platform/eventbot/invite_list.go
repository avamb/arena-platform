package eventbot

// invite_list.go — the issued invitations and their annulling (EC-12, spec 35
// §6.3), the second half of the "invite" dialog of invite.go. The list is
// GET .../complimentary: five rows a page, newest first, each with a state
// chip - ✔ valid, ✕ annulled, ● used (scanned at the door). A row opens a
// card; a valid invitation has an "Annul" button that does NOT annul: it asks
// for the annul word, typed (invAnnulWord, localized; the English word is
// accepted in every language), because annulling is not undoable - the ticket
// and its barcode stop working at once and the place goes back on sale. The
// guest is not told by Arena; the card says so.
//
// A used invitation cannot be annulled (the API parks it for manual review and
// the screen says that). Presses: "iv:lp:<n>" page, "iv:lo:<i>" open row i,
// "iv:rv" ask for the word.

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// Invitation states the API reports (gen.ComplimentaryState*).
const (
	invStateValid   = "valid"
	invStateRevoked = "revoked"
	invStateUsed    = "used"

	invCodeAlready = "complimentary.already_revoked"
	invCodeScanned = "complimentary.scanned_ticket_requires_manual_review"
)

// inviteChip is the one-glyph state of a row.
func inviteChip(state string) string {
	switch state {
	case invStateValid:
		return "✔"
	case invStateRevoked:
		return "✕"
	case invStateUsed:
		return "●"
	}
	return "·"
}

// inviteGuest is who an invitation is for: the first ticket's name, else its
// e-mail, "×N" appended for an issuance of several tickets.
func inviteGuest(it openapi.ComplimentaryListItem) string {
	who := ""
	if len(it.Tickets) > 0 {
		t := it.Tickets[0]
		switch {
		case t.HolderName != nil && strings.TrimSpace(*t.HolderName) != "":
			who = strings.TrimSpace(*t.HolderName)
		case t.HolderEmail != nil:
			who = strings.TrimSpace(*t.HolderEmail)
		}
	}
	if who == "" && len(it.Recipients) > 0 {
		who = it.Recipients[0]
	}
	if who == "" {
		who = "—"
	}
	if it.TicketCount > 1 {
		who += " ×" + invNum(it.TicketCount)
	}
	return who
}

// inviteWhen is the date of an invitation's session in the venue's zone.
func inviteWhen(it openapi.ComplimentaryListItem) string {
	if it.SessionStartAt == nil {
		return ""
	}
	tz := ""
	if it.VenueTimezone != nil {
		tz = *it.VenueTimezone
	}
	return FormatWhen(*it.SessionStartAt, tz)
}

// inviteRowLabel is the text of a list row's button.
func inviteRowLabel(it openapi.ComplimentaryListItem) string {
	label := inviteChip(it.State) + " " + truncate(inviteGuest(it), 26)
	if when := inviteWhen(it); len(when) >= 16 {
		// "15.10.2026 20:00" -> "15.10 20:00"
		label += " · " + when[:5] + when[10:]
	}
	if it.TierName != nil && *it.TierName != "" {
		label += " · " + truncate(*it.TierName, 14)
	}
	return label
}

// ─── the list ─────────────────────────────────────────────────────────────────

func (b *Bot) showInviteList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, page int, note string) {
	loc := id.Locale()
	if page < 1 {
		page = 1
	}
	res, err := b.arena.ListInvitations(ctx, jwt, st.OrgID, listPageSize, (page-1)*listPageSize)
	if err == nil {
		if pages := PagesFor(res.Total, listPageSize); page > pages {
			page = pages
			res, err = b.arena.ListInvitations(ctx, jwt, st.OrgID, listPageSize, (page-1)*listPageSize)
		}
	}
	if err != nil {
		b.leaveInvite(ctx, id.Link.TelegramUserID)
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	pages := PagesFor(res.Total, listPageSize)
	st.ListPage, st.MsgID, st.ListIDs = page, msgOf(editMsgID), st.ListIDs[:0]
	b.saveInviteListIDs(&st, res.Issuances)
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepList)

	text := note + b.texts.T(loc, "bot.inv.list_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Total": res.Total, "Page": page, "Pages": pages,
		"Legend": b.texts.T(loc, "bot.inv.legend", nil),
	})
	if len(res.Issuances) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.inv.list_empty", nil)
	}
	kb := make([][]models.InlineKeyboardButton, 0, listPageSize+3)
	for i, it := range res.Issuances {
		kb = append(kb, []models.InlineKeyboardButton{{Text: inviteRowLabel(it), CallbackData: ItemCallback("iv:lo", i)}})
	}
	if nav := PagerRow(Pager{Prefix: "iv:lp", Page: page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		kb = append(kb, nav)
	}
	kb = append(kb, []models.InlineKeyboardButton{b.invButton(loc, "bot.inv.issue_btn", "iv:i")})
	kb = append(kb, b.invNav(loc, "iv:new"))
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.invMarkup(kb...))
}

func (b *Bot) saveInviteListIDs(st *inviteDialog, items []openapi.ComplimentaryListItem) {
	for _, it := range items {
		st.ListIDs = append(st.ListIDs, it.Id)
	}
}

// inviteListCallback handles the presses that belong to the list and the card:
// lp (page), lo (open a row), rv (ask for the annul word).
func (b *Bot) inviteListCallback(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, step, kind, arg string) {
	switch kind {
	case "lp":
		b.showInviteList(ctx, chatID, editMsgID, id, jwt, st, ParsePage(arg), "")
	case "lo":
		i, ok := ParseIndex(arg, len(st.ListIDs))
		if !ok || step != invStepList {
			b.showInviteList(ctx, chatID, editMsgID, id, jwt, st, st.ListPage, "")
			return
		}
		b.showInviteCard(ctx, chatID, editMsgID, id, jwt, st, st.ListIDs[i], "")
	case "rv":
		if step != invStepCard {
			b.showInviteList(ctx, chatID, editMsgID, id, jwt, st, st.ListPage, "")
			return
		}
		b.askInviteRevoke(ctx, chatID, editMsgID, id, jwt, st, "")
	}
}

// findInvitation re-reads the page the list was on and picks the invitation
// out of it, so a card always shows the current state.
func (b *Bot) findInvitation(ctx context.Context, jwt string, st inviteDialog, want uuid.UUID) (openapi.ComplimentaryListItem, bool, error) {
	res, err := b.arena.ListInvitations(ctx, jwt, st.OrgID, listPageSize, (max(st.ListPage, 1)-1)*listPageSize)
	if err != nil {
		return openapi.ComplimentaryListItem{}, false, err
	}
	for _, it := range res.Issuances {
		if it.Id == want {
			return it, true, nil
		}
	}
	return openapi.ComplimentaryListItem{}, false, nil
}

// ─── the card ─────────────────────────────────────────────────────────────────

func (b *Bot) showInviteCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, want uuid.UUID, note string) {
	loc := id.Locale()
	it, found, err := b.findInvitation(ctx, jwt, st, want)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	if !found {
		b.showInviteList(ctx, chatID, editMsgID, id, jwt, st, st.ListPage, "")
		return
	}
	st.Open, st.OpenGuest, st.MsgID = it.Id, inviteGuest(it), msgOf(editMsgID)
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepCard)

	var guests strings.Builder
	for i, t := range it.Tickets {
		if i > 0 {
			guests.WriteByte('\n')
		}
		if t.SystemTicketId > 0 {
			guests.WriteString("№" + invNum(t.SystemTicketId) + " · ")
		}
		r := InviteRecipient{}
		if t.HolderName != nil {
			r.Name = *t.HolderName
		}
		if t.HolderEmail != nil {
			r.Email = *t.HolderEmail
		}
		if r.Email == "" && r.Name == "" {
			guests.WriteString("—")
		} else if r.Email == "" {
			guests.WriteString(Esc(r.Name))
		} else {
			guests.WriteString(guestLine(r))
		}
	}
	tier := ""
	if it.TierName != nil {
		tier = *it.TierName
	}
	event := ""
	if it.EventName != nil {
		event = *it.EventName
	}
	text := note + b.texts.T(loc, "bot.inv.card", map[string]any{
		"Event": Esc(event), "When": Esc(inviteWhen(it)), "Tier": Esc(tier), "Guests": guests.String(),
		"State":  inviteChip(it.State) + " " + b.texts.T(loc, "bot.inv.state_"+it.State, nil),
		"Issued": Esc(FormatWhen(it.CreatedAt, inviteTZ(it))),
	})
	var kb [][]models.InlineKeyboardButton
	if it.State == invStateValid {
		kb = append(kb, []models.InlineKeyboardButton{b.invButton(loc, "bot.inv.revoke_btn", "iv:rv")})
	}
	kb = append(kb, b.invNav(loc, "iv:bk"))
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.invMarkup(kb...))
}

func inviteTZ(it openapi.ComplimentaryListItem) string {
	if it.VenueTimezone != nil {
		return *it.VenueTimezone
	}
	return ""
}

// ─── annulling ────────────────────────────────────────────────────────────────

// invAnnulWord is the word the person types to annul, in their language.
func (b *Bot) invAnnulWord(loc string) string { return b.texts.T(loc, "bot.inv.revoke_word", nil) }

// invIsAnnulWord reports whether the typed text is the explicit command: the
// word of the person's language, or the English one in any language.
func (b *Bot) invIsAnnulWord(loc, text string) bool {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "\"'«».!"))
	return strings.EqualFold(text, b.invAnnulWord(loc)) || strings.EqualFold(text, "annul")
}

// askInviteRevoke re-reads the card (the invitation may have been used or
// annulled meanwhile) and, if it is still valid, asks for the word.
func (b *Bot) askInviteRevoke(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, note string) {
	loc := id.Locale()
	it, found, err := b.findInvitation(ctx, jwt, st, st.Open)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	if !found || it.State != invStateValid {
		b.showInviteList(ctx, chatID, editMsgID, id, jwt, st, st.ListPage, "")
		return
	}
	st.MsgID = msgOf(editMsgID)
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepRevoke)
	event := ""
	if it.EventName != nil {
		event = *it.EventName
	}
	b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.inv.revoke_ask", map[string]any{
		"Guest": Esc(st.OpenGuest), "Event": Esc(event), "When": Esc(inviteWhen(it)), "Word": b.invAnnulWord(loc),
	}), b.invMarkup(b.invNav(loc, "iv:bk")))
}

// inviteRevokeWord takes the text typed while the annul word is awaited. A
// wrong word annuls nothing and asks again.
func (b *Bot) inviteRevokeWord(ctx context.Context, chatID int64, editMsgID *int, tg int64, id *Identity, jwt string, st inviteDialog, text string) {
	loc := id.Locale()
	if !b.invIsAnnulWord(loc, text) {
		b.askInviteRevoke(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, "bot.inv.revoke_wrong", map[string]any{"Word": b.invAnnulWord(loc)})+"\n\n")
		return
	}
	// The word is right: the dialog ends first, so a message sent twice cannot
	// annul twice, then the API decides.
	b.leaveInvite(ctx, tg)
	vars := map[string]any{"Guest": Esc(st.OpenGuest)}
	after := b.invMarkup(
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.list_btn", "iv:l")},
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.btn_home", "home")},
	)
	err := b.arena.RevokeInvitation(ctx, jwt, st.Open)
	switch {
	case err == nil:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.revoke_done", vars), after)
	case APIErrorCode(err) == invCodeAlready:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.revoke_already", vars), after)
	case APIErrorCode(err) == invCodeScanned:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.revoke_used", vars), after)
	case IsAPIError(err, http.StatusNotFound), IsAPIError(err, http.StatusForbidden):
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.not_found", nil), after)
	default:
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
	}
}
