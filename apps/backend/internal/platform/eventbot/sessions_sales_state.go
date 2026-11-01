package eventbot

// sessions_sales_state.go — the sales state of an EXISTING session (migration
// 0183), switched from its card in the "Sessions" dialog: "open", "coming
// soon" or "sold out" for every channel at once (widget, tickets page, the
// sites). It is reversible and emails nobody, so one confirmation press
// writes it; there is no typed word as for a cancellation. The confirm screen
// reads the state first, so the organizer sees the free places, the unpaid
// bookings and the partner quotas the switch cannot close. After a change
// that the WordPress sites must hear about, the card offers a button that
// asks whether they did.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

// sesStepSalesState is the confirm screen of a sales state switch.
const sesStepSalesState = "sales_state"

func salesStatePath(orgID, sessionID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/sales-state"
}

// GetSessionSalesState reads a session's sales state, its places and the
// delivery of its latest change to the sites.
func (c *ArenaClient) GetSessionSalesState(ctx context.Context, jwt string, orgID, sessionID uuid.UUID) (openapi.SessionSalesState, error) {
	var out openapi.SessionSalesState
	err := c.do(ctx, http.MethodGet, salesStatePath(orgID, sessionID), jwt, nil, &out)
	return out, err
}

// SetSessionSalesState sets "soon" or "sold_out", or lifts the state with
// "open". The bot's X-Client-Channel puts telegram_bot into the journal.
func (c *ArenaClient) SetSessionSalesState(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, state string) (openapi.SessionSalesState, error) {
	var out openapi.SessionSalesState
	err := c.do(ctx, http.MethodPut, salesStatePath(orgID, sessionID), jwt, map[string]any{"state": state}, &out)
	return out, err
}

// sesSalesStates are the three choices, in the order the card shows them.
var sesSalesStates = []string{salesstate.Open, salesstate.Soon, salesstate.SoldOut}

// sesSalesStateButtons are the choices the card offers for a session whose
// organizer state is manual ("" when none is set): every state but the one
// already set. Nothing is offered once the sale is over or the session is
// cancelled.
func sesSalesStateButtons(manual, effective string) []string {
	if effective == salesstate.Ended || effective == salesstate.Cancelled {
		return nil
	}
	current := manual
	if current == "" {
		current = salesstate.Open
	}
	out := make([]string, 0, 2)
	for _, s := range sesSalesStates {
		if s != current {
			out = append(out, s)
		}
	}
	return out
}

func sesStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// sesSalesStateName is the state as the card names it.
func (b *Bot) sesSalesStateName(loc, state string) string {
	switch state {
	case salesstate.Open, salesstate.Soon, salesstate.SoldOut, salesstate.Ended, salesstate.Cancelled:
		return b.sesT(loc, "bot.ses.ss_name_"+state, nil)
	}
	return state
}

// sesSalesStateLine is the card's line with the state buyers see.
func (b *Bot) sesSalesStateLine(loc string, it sesItem) string {
	effective, manual := sesStr(it.Raw.SalesState), sesStr(it.Raw.ManualSalesState)
	if effective == "" {
		return ""
	}
	key := "bot.ses.ss_line"
	if manual != "" {
		key = "bot.ses.ss_line_manual"
	}
	return b.sesT(loc, key, map[string]any{"State": b.sesSalesStateName(loc, effective)})
}

// sesSalesStateRow is the card's row of state buttons ("ss:<state>").
func (b *Bot) sesSalesStateRow(loc string, it sesItem) []Button {
	var row []Button
	for _, s := range sesSalesStateButtons(sesStr(it.Raw.ManualSalesState), sesStr(it.Raw.SalesState)) {
		row = append(row, b.sesBtn(loc, "bot.ses.ss_btn_"+s, "ss:"+s, nil))
	}
	return row
}

// sesShowSalesState is the confirm screen of the switch to dlg.SalesTo.
func (b *Bot) sesShowSalesState(ctx context.Context, chatID int64, msgID *int, jwt, loc string, dlg *sessionDialog, prefix string) {
	if !b.sesCurOpen(dlg) || dlg.SalesTo == "" {
		dlg.Step = sesStepCard
		b.sesShowCard(ctx, chatID, msgID, jwt, loc, dlg, prefix)
		return
	}
	it := dlg.Items[dlg.Cur]
	text := prefix + b.sesT(loc, "bot.ses.ss_confirm_"+dlg.SalesTo, map[string]any{
		"Name": Esc(dlg.EventName), "When": Esc(FormatWhen(it.Start, it.Tz)),
	})
	if st, err := b.arena.GetSessionSalesState(ctx, jwt, dlg.OrgID, it.ID); err == nil {
		if st.FreePlaces+st.HeldPlaces > 0 {
			text += "\n\n" + b.sesT(loc, "bot.ses.ss_places", map[string]any{"Free": st.FreePlaces, "Held": st.HeldPlaces})
		}
		if st.ExternalAllocations > 0 && dlg.SalesTo != salesstate.Open {
			text += "\n\n" + b.sesT(loc, "bot.ses.ss_external", map[string]any{"Count": st.ExternalAllocations})
		}
	} else {
		b.logger.Warn("eventbot: sales state read failed", "session_id", it.ID.String(), "error", err.Error())
	}
	b.sesReply(ctx, chatID, msgID, dlg, text, [][]Button{
		{b.sesBtn(loc, "bot.ses.ss_go_btn", "ssgo", nil)},
		b.sesNav(loc, "card"), b.sesHomeRow(loc),
	})
}

// sesSiteNote says how far the latest change got to the WordPress sites; ""
// when no site sells the event.
func (b *Bot) sesSiteNote(loc string, sync openapi.SessionSalesStateSiteSync) string {
	switch sync.Status {
	case salesstate.SitePending:
		return b.sesT(loc, "bot.ses.ss_site_pending", map[string]any{"Sites": sync.Sites})
	case salesstate.SiteRetrying:
		return b.sesT(loc, "bot.ses.ss_site_retrying", map[string]any{"Attempts": sync.Attempts})
	case salesstate.SiteDelivered:
		return b.sesT(loc, "bot.ses.ss_site_delivered", nil)
	case salesstate.SiteFailed:
		return b.sesT(loc, "bot.ses.ss_site_failed", nil)
	}
	return ""
}

// sesSalesStateGo writes the switch and returns to the card with the result.
func (b *Bot) sesSalesStateGo(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, dlg *sessionDialog) {
	loc := id.Locale()
	if dlg.Step != sesStepSalesState || dlg.SalesTo == "" || !b.sesCurOpen(dlg) {
		// A second press, or a button of an older screen.
		b.sesShow(ctx, chatID, msgID, id, jwt, nil, dlg, "")
		return
	}
	it := dlg.Items[dlg.Cur]
	asked := dlg.SalesTo
	st, err := b.arena.SetSessionSalesState(ctx, jwt, dlg.OrgID, it.ID, asked)
	if err != nil {
		b.sesFail(ctx, chatID, msgID, id, dlg, err)
		return
	}
	if items, lerr := b.loadSesItems(ctx, jwt, dlg.OrgID, dlg.EventID); lerr == nil {
		dlg.Items = items
	}
	dlg.Step, dlg.SalesTo = sesStepCard, ""
	note := b.sesT(loc, "bot.ses.ss_done", map[string]any{"State": b.sesSalesStateName(loc, st.Effective)})
	if st.Changed != nil && !*st.Changed {
		note = b.sesT(loc, "bot.ses.ss_no_change", nil)
	}
	if asked == salesstate.Open && st.Effective != salesstate.Open {
		note += "\n" + b.sesT(loc, "bot.ses.ss_auto_note", map[string]any{"State": b.sesSalesStateName(loc, st.Effective)})
	}
	if st.Effective == salesstate.SoldOut && !st.OrgSalesChat {
		// Owner decision 2026-10-31: without a sales chat the sell-out goes
		// by e-mail only, and the organizer is told how to get it here.
		note += "\n" + b.sesT(loc, "bot.ses.ss_no_chat", nil)
	}
	dlg.SalesCheck = st.SiteSync.Sites > 0
	if site := b.sesSiteNote(loc, st.SiteSync); site != "" {
		note += "\n" + site
	}
	b.sesShow(ctx, chatID, msgID, id, jwt, nil, dlg, note)
}

// sesSalesStateCheck asks again whether the sites heard about the change.
func (b *Bot) sesSalesStateCheck(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, dlg *sessionDialog) {
	if dlg.Cur < 0 || dlg.Cur >= len(dlg.Items) {
		return
	}
	st, err := b.arena.GetSessionSalesState(ctx, jwt, dlg.OrgID, dlg.Items[dlg.Cur].ID)
	if err != nil {
		b.sesFail(ctx, chatID, msgID, id, dlg, err)
		return
	}
	note := b.sesSiteNote(id.Locale(), st.SiteSync)
	if st.SiteSync.Status == salesstate.SiteDelivered || st.SiteSync.Status == salesstate.SiteNone {
		dlg.SalesCheck = false
	}
	dlg.Step = sesStepCard
	b.sesShow(ctx, chatID, msgID, id, jwt, nil, dlg, note)
}
