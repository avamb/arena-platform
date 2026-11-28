package eventbot

// scanner_app_devices.go — "Control app" under the menu's Scanner screen (spec
// 69, MA-12): the link and the QR picture of the person's OWN phone (a code
// that works once, for 15 minutes), and the list of the person's own phones
// with a button to remove each. The presses are "sp:me", "sp:pc" and "sp:md",
// routed with the passes (scanner_passes.go).

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"
)

// scannerAppRow is the Scanner screen's button to the control app, only for
// a person who may pair a phone.
func (b *Bot) scannerAppRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if id == nil || id.Current == nil || !canScannerPasses(id) {
		return nil
	}
	return spBtn(b.texts.T(loc, "bot.scan.app_btn", nil), "sp:me")
}

// spShowApp draws the control app screen.
func (b *Bot) spShowApp(ctx context.Context, chatID int64, editMsgID *int, id *Identity, note string) {
	loc := id.Locale()
	b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.scan.app_text", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		spBtn(b.texts.T(loc, "bot.scan.app_pair_btn", nil), "sp:pc"),
		spBtn(b.texts.T(loc, "bot.scan.app_devs_btn", nil), "sp:md"),
		spBtn("« "+b.texts.T(loc, "bot.btn_back", nil), "scanner"),
		b.spHomeRow(loc),
	}})
}

// spPairMyPhone makes the code of the person's own phone and sends the link
// with its QR picture.
func (b *Bot) spPairMyPhone(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string) {
	loc := id.Locale()
	link, err := b.arena.CreateScannerPairingCode(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.spError(ctx, chatID, editMsgID, id, err, "sp:me")
		return
	}
	text := b.texts.T(loc, "bot.scan.app_code_text", map[string]any{"URL": Esc(link.Link), "Code": Esc(link.Code)})
	rows := [][]models.InlineKeyboardButton{
		spBtn(b.texts.T(loc, "bot.scan.app_devs_btn", nil), "sp:md"),
		spBtn("« "+b.texts.T(loc, "bot.btn_back", nil), "sp:me"),
		b.spHomeRow(loc),
	}
	if b.spSendQR(ctx, chatID, link.QrPng, "pair-"+link.Code+".png", text) {
		text = b.texts.T(loc, "bot.scan.app_code_sent", nil)
	}
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// spShowMyDevices lists the phones of this person: the organizer's own, not the
// controllers' (the API lists every phone to a person who manages passes).
func (b *Bot) spShowMyDevices(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt, note string) {
	loc := id.Locale()
	devices, err := b.arena.ListScannerDevices(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.spError(ctx, chatID, editMsgID, id, err, "sp:me")
		return
	}
	var text strings.Builder
	text.WriteString(note + b.texts.T(loc, "bot.scan.mydev_head", nil))
	var rows [][]models.InlineKeyboardButton
	mine := 0
	for _, d := range devices {
		if d.Kind != "organizer" || d.UserId == nil || *d.UserId != id.Link.UserID {
			continue
		}
		mine++
		name := b.spDeviceName(loc, d.ControllerName, d.Label, d.Platform)
		text.WriteString("\n" + b.texts.T(loc, "bot.scan.dev_line", map[string]any{"Name": Esc(name), "When": b.spLastSeen(loc, d.LastSeenAt, "")}))
		rows = append(rows, spBtn(b.texts.T(loc, "bot.scan.dev_remove_btn", map[string]any{"Name": truncate(name, 30)}), "sp:rv:"+d.Id.String()))
	}
	if mine == 0 {
		text.WriteString("\n\n" + b.texts.T(loc, "bot.scan.mydev_none", nil))
	}
	rows = append(rows, spBtn("« "+b.texts.T(loc, "bot.btn_back", nil), "sp:me"), b.spHomeRow(loc))
	b.reply(ctx, chatID, editMsgID, text.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}
