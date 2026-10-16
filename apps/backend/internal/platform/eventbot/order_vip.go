package eventbot

// order_vip.go — the organizer's private VIP mark of a ticket on the order
// card (task V1, spec 40 section 5.V). The card shows "⭐ VIP" on a marked
// ticket and offers one button per active ticket that sets the mark the other
// way. The press carries the WANTED value, not "toggle" (`or:vip:1:<ticket>`
// marks, `or:vip:0:<ticket>` clears), so a stale button repeats a state and
// never flips it back. The mark never reaches the buyer: it is not in the PDF
// or in any letter.

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// maxVIPButtons bounds the ticket buttons of one card; a larger order marks
// the rest in the web event center.
const maxVIPButtons = 8

// SetTicketVIP sets or clears the VIP mark of one ticket
// (PATCH .../tickets/{ticket_id}/vip). The call is idempotent.
func (c *ArenaClient) SetTicketVIP(ctx context.Context, jwt string, orgID, ticketID uuid.UUID, vip bool) error {
	path := "/v1/organizations/" + orgID.String() + "/tickets/" + ticketID.String() + "/vip"
	return c.do(ctx, http.MethodPatch, path, jwt, map[string]bool{"vip": vip}, nil)
}

// vipOffered reports whether the card carries the VIP buttons: the roles that
// hold ticket.update (the same as "Resend tickets").
func vipOffered(id *Identity) bool { return canResend(id) }

// withVIPButtons adds one button per active ticket above the card's last row
// (Back / Home).
func (b *Bot) withVIPButtons(kb *models.InlineKeyboardMarkup, id *Identity, d openapi.OrderDetail) *models.InlineKeyboardMarkup {
	if kb == nil || !vipOffered(id) || len(kb.InlineKeyboard) == 0 {
		return kb
	}
	loc := id.Locale()
	var extra [][]models.InlineKeyboardButton
	for _, t := range d.Tickets {
		if t.Status != "active" || len(extra) >= maxVIPButtons {
			continue
		}
		num := invNum(t.SystemTicketId)
		if t.Vip {
			extra = append(extra, []models.InlineKeyboardButton{{
				Text: b.texts.T(loc, "bot.ord.vip_off_btn", map[string]any{"Num": num}), CallbackData: "or:vip:0:" + t.Id.String(),
			}})
		} else {
			extra = append(extra, []models.InlineKeyboardButton{{
				Text: b.texts.T(loc, "bot.ord.vip_on_btn", map[string]any{"Num": num}), CallbackData: "or:vip:1:" + t.Id.String(),
			}})
		}
	}
	if len(extra) == 0 {
		return kb
	}
	last := len(kb.InlineKeyboard) - 1
	rows := append([][]models.InlineKeyboardButton{}, kb.InlineKeyboard[:last]...)
	rows = append(rows, extra...)
	rows = append(rows, kb.InlineKeyboard[last])
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// vipPress handles `or:vip:<0|1>:<ticket>`: it sets the mark and redraws the
// card where it is. The order is the card on screen (the dialog's CardID); a
// press with no card behind it shows the list instead of guessing.
func (b *Bot) vipPress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, arg string) {
	want, ticket, ok := strings.Cut(arg, ":")
	ticketID, err := uuid.Parse(ticket)
	if !ok || err != nil || (want != "0" && want != "1") {
		return
	}
	loc := id.Locale()
	orgID := id.Current.OrgID
	st, _, expired := b.loadOrdersState(ctx, from.ID, orgID)
	if expired || st.CardID == nil {
		b.renderOrders(ctx, chatID, &msgID, id, jwt, newOrdersDialog(orgID), b.texts.T(loc, "bot.dialog_expired", nil)+"\n\n")
		return
	}
	if !vipOffered(id) {
		b.ecError(ctx, chatID, &msgID, id, &APIError{Status: http.StatusForbidden}, "or:b")
		return
	}
	if err := b.arena.SetTicketVIP(ctx, jwt, orgID, ticketID, want == "1"); err != nil {
		b.ecError(ctx, chatID, &msgID, id, err, "or:b")
		return
	}
	b.showOrderCard(ctx, chatID, &msgID, from, id, jwt, st, *st.CardID, "")
}
