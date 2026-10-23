package eventbot

// session_not_entered.go — the "Not entered" list of a date (task M2): the
// active tickets nobody has entered with, a page at a time, from the card of a
// date in the Sessions dialog. The screen is stateless like the date's
// summary: its buttons are `ses:ne:<page>` and the date is the dialog's
// current one. The buyer's name is printed in a private chat only, like the
// order card's contacts.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// notEnteredPageSize is how many tickets one page of the list holds.
const notEnteredPageSize = 10

// SessionTickets returns one page of a date's tickets with their entry at the
// door (GET .../sessions/{id}/tickets). entered: nil = every ticket, false =
// active tickets nobody entered with. The page holds buyers' names and
// e-mails: nothing here logs it.
func (c *ArenaClient) SessionTickets(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, entered *bool, limit, offset int) (openapi.SessionTicketList, error) {
	var out openapi.SessionTicketList
	q := "?limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(offset)
	if entered != nil {
		q += "&entered=" + strconv.FormatBool(*entered)
	}
	path := "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/tickets" + q
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// showNotEntered renders page (1-based) of the date's not-entered tickets.
func (b *Bot) showNotEntered(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, session uuid.UUID, scope string, page int) {
	loc := id.Locale()
	if !canViewSales(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "ses:card"))
		return
	}
	if page < 1 {
		page = 1
	}
	no := false
	list, err := b.arena.SessionTickets(ctx, jwt, id.Current.OrgID, session, &no, notEnteredPageSize, (page-1)*notEnteredPageSize)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "ses:card")
		return
	}
	// A page past the end (tickets entered while the person was reading):
	// show the last one that exists.
	if len(list.Items) == 0 && list.Total > 0 && page > 1 {
		b.showNotEntered(ctx, chatID, editMsgID, from, id, jwt, session, scope, notEnteredPages(list.Total))
		return
	}
	private := chatID == from.ID
	tz := ""
	if list.VenueTimezone != nil {
		tz = *list.VenueTimezone
	}
	var text strings.Builder
	text.WriteString(b.texts.T(loc, "bot.ses.not_entered_head", map[string]any{
		"Scope": scope, "Count": list.Total,
	}))
	if list.Total == 0 {
		text.WriteString("\n\n" + b.texts.T(loc, "bot.ses.not_entered_empty", nil))
	}
	for _, it := range list.Items {
		text.WriteString("\n" + b.notEnteredLine(loc, it, tz, private))
	}
	if list.Total > 0 && !private {
		text.WriteString(b.texts.T(loc, "bot.ord.contacts_private", nil))
	}
	var rows [][]models.InlineKeyboardButton
	if row := PagerRow(Pager{Prefix: "ses:ne", Page: page, Pages: notEnteredPages(list.Total)},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); row != nil {
		rows = append(rows, row)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ses:card"},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	b.reply(ctx, chatID, editMsgID, clipMessage(text.String(), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// notEnteredPages is the number of pages n tickets fill (at least 1).
func notEnteredPages(n int64) int {
	p := int((n + notEnteredPageSize - 1) / notEnteredPageSize)
	if p < 1 {
		return 1
	}
	return p
}

// notEnteredLine is one ticket: its number, the order (or "invitation"), the
// category, the seat, the buyer's name in a private chat, and — when the gate
// turned it away — when and where.
func (b *Bot) notEnteredLine(loc string, it openapi.SessionTicketItem, tz string, private bool) string {
	order := b.texts.T(loc, "bot.ses.not_entered_invite", nil)
	if it.OrderNumber != nil {
		order = "№" + strconv.FormatInt(*it.OrderNumber, 10)
	}
	tier := ""
	if it.TierName != nil && strings.TrimSpace(*it.TierName) != "" {
		tier = " · " + Esc(*it.TierName)
	}
	seat := ""
	if it.SeatLabel != nil && strings.TrimSpace(*it.SeatLabel) != "" {
		seat = b.texts.T(loc, "bot.ord.seat", map[string]any{"Label": Esc(*it.SeatLabel)})
	}
	buyer := ""
	if private && it.BuyerName != nil && strings.TrimSpace(*it.BuyerName) != "" {
		buyer = " · " + Esc(strings.TrimSpace(*it.BuyerName))
	}
	scan := ""
	if s := it.LastScan; s != nil && s.Result != scanAdmitted {
		scan = b.texts.T(loc, "bot.ord.scan_denied", map[string]any{"When": Esc(FormatWhen(s.ScannedAt, tz))}) +
			b.scanDetail(loc, s.Source, s.Gate, s.ScannerLabel)
	}
	return fmt.Sprintf("• <code>%d</code> · %s%s%s%s%s", it.SystemTicketId, Esc(order), tier, seat, buyer, scan)
}
