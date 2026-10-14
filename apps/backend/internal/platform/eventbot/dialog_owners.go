package eventbot

// dialog_owners.go — which dialog a press or a typed text belongs to.
//
// A dialog (a bot_dialogs row) lives while its screens are on: a press of one
// of its own buttons keeps it, and a press of any other button ends it
// (leaveForeignDialogs), as does every command (leaveAllDialogs). Only the
// page counter of a list ("noop") belongs to everybody.
//
// A typed text is offered to the open dialogs newest first and goes to the
// first one that takes text at its step (routeDialogText). A dialog opened
// earlier never sees a word typed for a newer one.
//
// A NEW dialog kind needs an entry in both tables below
// (TestDialogOwners_EveryDialogKindIsRouted).

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot/models"
)

// dialogOwner tells which presses belong to a dialog and how it ends.
type dialogOwner struct {
	owns  func(data string) bool
	leave func(b *Bot, ctx context.Context, tg int64)
}

func prefixIs(p string) func(string) bool {
	return func(data string) bool {
		head, _, _ := strings.Cut(data, ":")
		return head == p
	}
}

func dataHasPrefix(p string) func(string) bool {
	return func(data string) bool { return strings.HasPrefix(data, p) }
}

// dialogOwners maps every dialog kind a linked user can have open to its
// buttons. The organizer application (onbDialogKind) belongs to a stranger
// and is never ended by a press.
var dialogOwners = map[string]dialogOwner{
	eventsDialogKind: {
		owns:  func(data string) bool { head, _, _ := strings.Cut(data, ":"); return isEventsCallback(head) },
		leave: (*Bot).leaveEvents,
	},
	ordersDialogKind: {
		owns:  func(data string) bool { head, _, _ := strings.Cut(data, ":"); return isOrdersCallback(head) },
		leave: (*Bot).leaveOrders,
	},
	// A refund already with the server, or one that may have started, is
	// kept or parked with its key by leaveIdleRefund, never dropped.
	refundDialogKind:   {owns: dataHasPrefix("or:f:"), leave: (*Bot).leaveIdleRefund},
	scanExcDialogKind:  {owns: dataHasPrefix("or:f:x"), leave: (*Bot).leaveScanExc},
	resendDialogKind:   {owns: dataHasPrefix("or:r:"), leave: (*Bot).leaveResend},
	evsDialogKind:      {owns: prefixIs("ec"), leave: (*Bot).leaveEvStatus},
	inviteDialogKind:   {owns: prefixIs("iv"), leave: (*Bot).leaveInvite},
	promoDialogKind:    {owns: prefixIs("pm"), leave: (*Bot).leavePromo},
	promoterDialogKind: {owns: prefixIs("pr"), leave: (*Bot).leavePromoter},
	categoryDialogKind: {owns: prefixIs("ct"), leave: (*Bot).leaveCategories},
	orgDataDialogKind:  {owns: prefixIs("og"), leave: (*Bot).leaveOrgData},
	sesDialogKind:      {owns: prefixIs("ses"), leave: (*Bot).leaveSessions},
	teamDialogKind:     {owns: prefixIs("team"), leave: (*Bot).clearTeamDialog},
}

// dialogTexts maps every dialog kind that takes typed text to its handler.
// A handler reports whether it took the text.
var dialogTexts = map[string]func(b *Bot, ctx context.Context, chatID int64, from *models.User, text string) bool{
	eventsDialogKind:   (*Bot).eventsText,
	ordersDialogKind:   (*Bot).ordersText,
	refundDialogKind:   (*Bot).refundText,
	scanExcDialogKind:  (*Bot).scanExcText,
	resendDialogKind:   (*Bot).resendText,
	evsDialogKind:      (*Bot).evStatusText,
	inviteDialogKind:   (*Bot).inviteText,
	promoDialogKind:    (*Bot).promoText,
	promoterDialogKind: (*Bot).promoterText,
	categoryDialogKind: (*Bot).categoryText,
	orgDataDialogKind:  (*Bot).orgDataText,
	sesDialogKind:      (*Bot).sessionsText,
	teamDialogKind:     (*Bot).teamText,
}

// leaveForeignDialogs ends every dialog the press does not belong to. A press
// of a list's page counter ("noop") ends nothing.
func (b *Bot) leaveForeignDialogs(ctx context.Context, tg int64, data string) {
	if data == calNoop {
		return
	}
	for _, kind := range dialogOwnerOrder {
		if o := dialogOwners[kind]; !o.owns(data) {
			o.leave(b, ctx, tg)
		}
	}
}

// dialogOwnerOrder is the fixed order dialogs are ended in (a map has none).
var dialogOwnerOrder = []string{
	eventsDialogKind, ordersDialogKind, refundDialogKind, scanExcDialogKind,
	resendDialogKind, evsDialogKind, inviteDialogKind, promoDialogKind,
	promoterDialogKind, categoryDialogKind, orgDataDialogKind, sesDialogKind,
	teamDialogKind,
}

// leaveSessions ends the "Sessions" dialog (move, cancel, contact question).
func (b *Bot) leaveSessions(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, sesDialogKind); err != nil {
		b.logger.Warn("eventbot: sessions dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// routeDialogText offers a typed text to the open dialogs, newest first, and
// reports whether one took it. An older dialog gets the text only when every
// newer one does not take text at its step.
func (b *Bot) routeDialogText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	kinds, err := b.dialogs.Recent(ctx, from.ID)
	if err != nil {
		b.logger.Warn("eventbot: dialogs not listed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	return offerText(kinds, func(kind string) bool {
		handle := dialogTexts[kind]
		return handle != nil && handle(b, ctx, chatID, from, text)
	})
}

// offerText offers a text to the dialogs in the order given (newest first)
// and stops at the first one that takes it.
func offerText(kinds []string, take func(kind string) bool) bool {
	for _, kind := range kinds {
		if take(kind) {
			return true
		}
	}
	return false
}
