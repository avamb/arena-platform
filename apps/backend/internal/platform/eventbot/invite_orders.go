package eventbot

// invite_orders.go — "Invitations" on the staff-invitation routes of spec 46
// (INV-05). Three parts, all inside the "invite" dialog of invite.go:
//
//   - a SEATED date: the seats are picked on the map, so the entry is a
//     Telegram Mini App button (BOT_MINIAPP_INVITES_ENABLED) that opens
//     <EVENT_CENTER_PUBLIC_URL>/tg/invitations?session=<id>&event=<id>#code=<code>,
//     with the same one-time code as the description editor; with the flag
//     off (or for a platform superadmin) the screen is a text with a link to
//     the web screen /o/<org>/events/<event>/sessions/<session>/invitations.
//     The code is drawn when the button is pressed ("iv:ma"), never when the
//     screen is drawn.
//   - the list: GET /v1/organizations/{org_id}/invitations, every date of the
//     organization at once, newest first, five to a page, pages by cursor
//     (the dialog remembers the cursor of every page it has shown). A row
//     opens a card; "Annul" asks for the annul word, typed, and then
//     cancels the whole order (POST .../invitations/{order_id}/cancel). The
//     guest is told by e-mail when the invitation had an address.
//   - the old issuances (complimentary_issuances, before spec 46) stay
//     reachable read-only from the list ("iv:lg") with their own annul, so
//     nothing already issued becomes unmanageable.
//
// Presses: iv:ma  the Mini App button      iv:np:<n>  list page
//
//	iv:no:<i>  open row i                     iv:nrv     ask for the annul word
//	iv:lg      the old issuances

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const invCodeNotIssued = "invitation.tickets_not_issued"
const invCodeTicketScanned = "invitation.ticket_scanned"

// maxStaffPlaceLines bounds the places printed on a card.
const maxStaffPlaceLines = 15

// staffGuestName is the name printed on the ticket: the API requires one, so
// a guest typed as an e-mail only is printed under the address.
func staffGuestName(r InviteRecipient) string {
	if n := strings.TrimSpace(r.Name); n != "" {
		return n
	}
	return r.Email
}

// ─── seated date ──────────────────────────────────────────────────────────────

// invitationsWebURL is the web event center screen of one date's invitations.
func invitationsWebURL(base string, orgID, eventID, sessionID uuid.UUID) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	return base + "/o/" + orgID.String() + "/events/" + eventID.String() + "/sessions/" + sessionID.String() + "/invitations"
}

// miniappInvitesOffered reports whether the seated entry is a Mini App button.
func (b *Bot) miniappInvitesOffered(chatID int64, id *Identity) bool {
	return b.miniappInvites && chatID > 0 && id != nil && !id.Superadmin
}

// showInviteSeated is the "Invitations" screen of a date with a seating plan.
func (b *Bot) showInviteSeated(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st inviteDialog, back string) {
	loc := id.Locale()
	vars := map[string]any{"Name": Esc(st.EventName), "When": Esc(st.When)}
	var rows [][]models.InlineKeyboardButton
	key := "bot.inv.seated_text"
	switch link := invitationsWebURL(b.eventCenterURL, st.OrgID, st.EventID, st.SessionID); {
	case b.miniappInvitesOffered(chatID, id) && link != "":
		rows = append(rows, []models.InlineKeyboardButton{b.invButton(loc, "bot.inv.seated_btn", "iv:ma")})
	case link != "":
		key = "bot.inv.seated_web"
		vars["Link"] = Esc(link)
	default:
		key = "bot.inv.seated_nolink"
	}
	rows = append(rows, b.invNav(loc, back))
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, vars), b.invMarkup(rows...))
}

// openInvitationsMiniapp handles "iv:ma": it draws the code and turns the
// pressed button into a web_app button that holds the link.
func (b *Bot) openInvitationsMiniapp(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, st inviteDialog) {
	loc := id.Locale()
	if !b.miniappInvitesOffered(chatID, id) {
		// The flag was turned off after the screen was drawn.
		b.showInviteSeated(ctx, chatID, &msgID, id, st, "iv:bk")
		return
	}
	link, err := b.arena.MiniappInvitationsLink(ctx, from.ID, st.EventID, st.SessionID)
	if err != nil {
		b.logger.Warn("eventbot: invitations mini app link failed", "error", routeOnly(err))
		b.send(ctx, chatID, b.texts.T(loc, "bot.inv.seated_failed", nil), nil)
		return
	}
	if !miniappURLOK(link.URL) {
		b.logger.Warn("eventbot: invitations mini app link refused by the bot", "length", len(link.URL))
		b.send(ctx, chatID, b.texts.T(loc, "bot.inv.seated_failed", nil), nil)
		return
	}
	b.miniappOpenLabeled(ctx, chatID, msgID, loc, "iv:ma", link.URL, "bot.inv.seated_btn")
}

// ─── the list ─────────────────────────────────────────────────────────────────

// staffInvChip is the one-glyph state of a row: ✕ once every ticket is
// annulled, ✔ otherwise (also while the tickets are still being issued).
func staffInvChip(it openapi.StaffInvitationOrgListItem) string {
	if it.TicketsTotal > 0 && it.TicketsActive == 0 {
		return "✕"
	}
	if staffInvLetterFailed(it) {
		return "⚠"
	}
	return "✔"
}

// staffInvLetterFailed reports a live invitation whose letter did not go out:
// the row is marked so the organizer sees it without opening the card.
func staffInvLetterFailed(it openapi.StaffInvitationOrgListItem) bool {
	return it.Letter != nil && it.Letter.Status == "failed" && it.TicketsActive > 0
}

// staffInvLetterLine is the card line about the guest's letter, with its
// leading newline, or "" when there is nothing to say: no letter was queued
// (a PDF-only invitation, tickets not issued yet) or every ticket is annulled.
func (b *Bot) staffInvLetterLine(loc string, it openapi.StaffInvitationOrgListItem) string {
	if it.Letter == nil || (it.TicketsTotal > 0 && it.TicketsActive == 0) {
		return ""
	}
	var key string
	vars := map[string]any{}
	switch it.Letter.Status {
	case "pending":
		key = "bot.inv.letter_pending"
	case "sent":
		key = "bot.inv.letter_sent"
		vars["When"] = ""
		if it.Letter.SentAt != nil {
			vars["When"] = Esc(FormatWhen(*it.Letter.SentAt, it.Session.Timezone))
		}
	case "failed":
		key = "bot.inv.letter_failed"
	case "skipped":
		key = "bot.inv.letter_skipped"
	case "disabled":
		key = "bot.inv.letter_disabled"
	default:
		return ""
	}
	return "\n" + b.texts.T(loc, key, vars)
}

// staffInvGuest is who an invitation is for, "×N" appended for several places.
func staffInvGuest(it openapi.StaffInvitationOrgListItem) string {
	who := "—"
	if it.GuestName != nil && strings.TrimSpace(*it.GuestName) != "" {
		who = strings.TrimSpace(*it.GuestName)
	}
	if len(it.Places) > 0 && it.Places[0].Vip {
		who = "⭐ " + who
	}
	if n := len(it.Places); n > 1 {
		who += " ×" + invNum(int64(n))
	}
	return who
}

// staffInvWhen is the date of an invitation's session in the venue's zone.
func staffInvWhen(it openapi.StaffInvitationOrgListItem) string {
	return FormatWhen(it.Session.StartAt, it.Session.Timezone)
}

// staffInvRowLabel is the text of a list row's button.
func staffInvRowLabel(it openapi.StaffInvitationOrgListItem) string {
	label := staffInvChip(it) + " " + truncate(staffInvGuest(it), 26)
	if when := staffInvWhen(it); len(when) >= 16 {
		// "15.10.2026 20:00" -> "15.10 20:00"
		label += " · " + when[:5] + when[10:]
	}
	return label + " · " + truncate(it.Session.EventName, 14)
}

// showStaffInvList shows one page of the organization's staff invitations. The
// pages follow each other by cursor, so the dialog keeps the cursor of every
// page it has shown (NCursors[i] fetches page i+1) and a page can only be
// reached from its neighbour.
func (b *Bot) showStaffInvList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, page int) {
	loc := id.Locale()
	if page < 1 || page > len(st.NCursors) {
		page = 1
	}
	cursor := ""
	if page > 1 {
		cursor = st.NCursors[page-1]
	}
	res, err := b.arena.ListOrgInvitations(ctx, jwt, st.OrgID, cursor, listPageSize)
	if err != nil {
		b.leaveInvite(ctx, id.Link.TelegramUserID)
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	cursors := append([]string(nil), st.NCursors...)
	if page == 1 || len(cursors) < page {
		cursors = []string{""}
	}
	cursors = cursors[:page]
	pages := page
	if res.NextCursor != nil && *res.NextCursor != "" {
		cursors = append(cursors, *res.NextCursor)
		pages = page + 1
	}
	st.NPage, st.NCursors, st.NOrderIDs, st.MsgID = page, cursors, st.NOrderIDs[:0], msgOf(editMsgID)
	for _, it := range res.Items {
		st.NOrderIDs = append(st.NOrderIDs, it.OrderId)
	}
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepNList)

	text := b.texts.T(loc, "bot.inv.nlist_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Page": page, "Legend": b.texts.T(loc, "bot.inv.nlegend", nil),
	})
	if len(res.Items) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.inv.list_empty", nil)
	}
	kb := make([][]models.InlineKeyboardButton, 0, listPageSize+4)
	for i, it := range res.Items {
		kb = append(kb, []models.InlineKeyboardButton{{Text: staffInvRowLabel(it), CallbackData: ItemCallback("iv:no", i)}})
	}
	if nav := PagerRow(Pager{Prefix: "iv:np", Page: page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		kb = append(kb, nav)
	}
	kb = append(kb, []models.InlineKeyboardButton{b.invButton(loc, "bot.inv.issue_btn", "iv:i")})
	// Invitations issued before spec 46 live in another table: a button only
	// when there are some.
	if old, lerr := b.arena.ListInvitations(ctx, jwt, st.OrgID, 1, 0); lerr == nil && old.Total > 0 {
		kb = append(kb, []models.InlineKeyboardButton{b.invButton(loc, "bot.inv.legacy_btn", "iv:lg")})
	}
	kb = append(kb, b.invNav(loc, "iv:new"))
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.invMarkup(kb...))
}

// showLegacyInviteList is the list of the old issuances, read-only except for
// their own annul.
func (b *Bot) showLegacyInviteList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, page int) {
	note := b.texts.T(id.Locale(), "bot.inv.legacy_note", nil) + "\n\n"
	b.showInviteList(ctx, chatID, editMsgID, id, jwt, st, page, note)
}

// staffInvCallback handles the presses of the new list and its card: np
// (page), no (open a row), nrv (ask for the annul word). It reports whether
// the press was one of them.
func (b *Bot) staffInvCallback(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, step, kind, arg string) bool {
	switch kind {
	case "np":
		b.showStaffInvList(ctx, chatID, editMsgID, id, jwt, st, ParsePage(arg))
	case "no":
		i, ok := ParseIndex(arg, len(st.NOrderIDs))
		if !ok || step != invStepNList {
			b.showStaffInvList(ctx, chatID, editMsgID, id, jwt, st, st.NPage)
			return true
		}
		b.showStaffInvCard(ctx, chatID, editMsgID, id, jwt, st, st.NOrderIDs[i])
	case "nrv":
		if step != invStepNCard {
			b.showStaffInvList(ctx, chatID, editMsgID, id, jwt, st, st.NPage)
			return true
		}
		b.askStaffInvRevoke(ctx, chatID, editMsgID, id, jwt, st, "")
	default:
		return false
	}
	return true
}

// findStaffInv re-reads the page the list was on and picks the invitation out of
// it, so a card always shows the current state.
func (b *Bot) findStaffInv(ctx context.Context, jwt string, st inviteDialog, want uuid.UUID) (openapi.StaffInvitationOrgListItem, bool, error) {
	cursor := ""
	if st.NPage > 1 && st.NPage <= len(st.NCursors) {
		cursor = st.NCursors[st.NPage-1]
	}
	res, err := b.arena.ListOrgInvitations(ctx, jwt, st.OrgID, cursor, listPageSize)
	if err != nil {
		return openapi.StaffInvitationOrgListItem{}, false, err
	}
	for _, it := range res.Items {
		if it.OrderId == want {
			return it, true, nil
		}
	}
	return openapi.StaffInvitationOrgListItem{}, false, nil
}

// staffPlaceLine is one place of a card: a seat as "sector · row · seat", a
// general-admission place by its category; ✕ marks an annulled ticket.
func staffPlaceLine(p openapi.StaffInvitationPlace) string {
	var s string
	if p.SeatKey != nil && p.Kind != nil && *p.Kind == "seat" {
		parts := make([]string, 0, 3)
		for _, v := range []*string{p.Sector, p.Row, p.Seat} {
			if v != nil && strings.TrimSpace(*v) != "" {
				parts = append(parts, strings.TrimSpace(*v))
			}
		}
		s = "🪑 " + Esc(strings.Join(parts, " · ")) + " · " + Esc(truncate(p.TierName, 20))
	} else {
		s = "🎟 " + Esc(truncate(p.TierName, 30))
	}
	if p.Vip {
		s = "⭐ VIP · " + s
	}
	if p.TicketStatus != nil && *p.TicketStatus == "cancelled" {
		s = "✕ " + s
	}
	return s
}

func staffPlaceLines(ps []openapi.StaffInvitationPlace) string {
	var sb strings.Builder
	for i, p := range ps {
		if i >= maxStaffPlaceLines {
			sb.WriteString("\n…")
			break
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(staffPlaceLine(p))
	}
	return sb.String()
}

// staffInvIssuer names who issued the invitation.
func staffInvIssuer(it openapi.StaffInvitationOrgListItem) string {
	var parts []string
	if it.IssuedBy.FirstName != nil && strings.TrimSpace(*it.IssuedBy.FirstName) != "" {
		parts = append(parts, strings.TrimSpace(*it.IssuedBy.FirstName))
	}
	if it.IssuedBy.LastName != nil && strings.TrimSpace(*it.IssuedBy.LastName) != "" {
		parts = append(parts, strings.TrimSpace(*it.IssuedBy.LastName))
	}
	name := strings.Join(parts, " ")
	if it.IssuedBy.Email != nil && *it.IssuedBy.Email != "" {
		if name != "" {
			return name + " · " + *it.IssuedBy.Email
		}
		return *it.IssuedBy.Email
	}
	if name == "" {
		return "—"
	}
	return name
}

func (b *Bot) showStaffInvCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, want uuid.UUID) {
	loc := id.Locale()
	it, found, err := b.findStaffInv(ctx, jwt, st, want)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	if !found {
		b.showStaffInvList(ctx, chatID, editMsgID, id, jwt, st, st.NPage)
		return
	}
	st.NOpen, st.NOpenGuest, st.MsgID = it.OrderId, staffInvGuest(it), msgOf(editMsgID)
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepNCard)

	state := "valid"
	switch {
	case it.TicketsTotal == 0:
		state = "issuing"
	case it.TicketsActive == 0:
		state = "revoked"
	}
	guest := "—"
	if it.GuestName != nil && strings.TrimSpace(*it.GuestName) != "" {
		guest = Esc(strings.TrimSpace(*it.GuestName))
	}
	text := b.texts.T(loc, "bot.inv.ncard", map[string]any{
		"Event": Esc(it.Session.EventName), "When": Esc(staffInvWhen(it)), "Guest": guest,
		"Places": staffPlaceLines(it.Places), "By": Esc(staffInvIssuer(it)),
		"State":  staffInvChip(it) + " " + b.texts.T(loc, "bot.inv.state_"+state, nil),
		"Issued": Esc(FormatWhen(it.CreatedAt, it.Session.Timezone)),
		"Letter": b.staffInvLetterLine(loc, it),
	})
	var kb [][]models.InlineKeyboardButton
	if it.TicketsActive > 0 {
		kb = append(kb, []models.InlineKeyboardButton{b.invButton(loc, "bot.inv.revoke_btn", "iv:nrv")})
	}
	kb = append(kb, b.invNav(loc, "iv:bk"))
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.invMarkup(kb...))
}

// askStaffInvRevoke re-reads the card (the invitation may have been annulled
// meanwhile) and, if some ticket is still active, asks for the annul word.
// Annulling is not undoable: the whole order's tickets stop working at once
// and its places go back where they came from, so a button alone never does it.
func (b *Bot) askStaffInvRevoke(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, note string) {
	loc := id.Locale()
	it, found, err := b.findStaffInv(ctx, jwt, st, st.NOpen)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	if !found || it.TicketsActive == 0 {
		b.showStaffInvList(ctx, chatID, editMsgID, id, jwt, st, st.NPage)
		return
	}
	st.MsgID = msgOf(editMsgID)
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepNRevoke)
	mail := "bot.inv.nrevoke_nomail"
	if it.Delivery == "guest_email" {
		mail = "bot.inv.nrevoke_mail"
	}
	b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.inv.nrevoke_ask", map[string]any{
		"Guest": Esc(st.NOpenGuest), "Event": Esc(it.Session.EventName), "When": Esc(staffInvWhen(it)),
		"N": it.TicketsActive, "Mail": b.texts.T(loc, mail, nil), "Word": b.invAnnulWord(loc),
	}), b.invMarkup(b.invNav(loc, "iv:bk")))
}

// inviteOrderRevokeWord takes the text typed while the annul word is awaited.
// A wrong word annuls nothing and asks again.
func (b *Bot) inviteOrderRevokeWord(ctx context.Context, chatID int64, editMsgID *int, tg int64, id *Identity, jwt string, st inviteDialog, text string) {
	loc := id.Locale()
	if !b.invIsAnnulWord(loc, text) {
		b.askStaffInvRevoke(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(loc, "bot.inv.revoke_wrong", map[string]any{"Word": b.invAnnulWord(loc)})+"\n\n")
		return
	}
	// The word is right: the dialog ends first, so a message sent twice
	// cannot annul twice, then the API decides.
	b.leaveInvite(ctx, tg)
	vars := map[string]any{"Guest": Esc(st.NOpenGuest)}
	after := b.invMarkup(
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.list_btn", "iv:l")},
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.btn_home", "home")},
	)
	res, err := b.arena.CancelStaffInvitation(ctx, jwt, st.OrgID, st.NOpen)
	switch {
	case err == nil && len(res.CancelledTicketIDs) == 0 && len(res.SkippedTicketIDs) == 0:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.revoke_already", vars), after)
	case err == nil:
		vars["N"] = len(res.CancelledTicketIDs)
		msg := b.texts.T(loc, "bot.inv.nrevoke_done", vars)
		if len(res.SkippedTicketIDs) > 0 {
			msg += "\n\n" + b.texts.T(loc, "bot.inv.nrevoke_skipped", map[string]any{"N": len(res.SkippedTicketIDs)})
		}
		b.reply(ctx, chatID, editMsgID, msg, after)
	case APIErrorCode(err) == invCodeTicketScanned:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.nrevoke_used", vars), after)
	case APIErrorCode(err) == invCodeNotIssued:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.nrevoke_notyet", vars), after)
	case IsAPIError(err, http.StatusNotFound), IsAPIError(err, http.StatusForbidden):
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.not_found", nil), after)
	default:
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
	}
}
