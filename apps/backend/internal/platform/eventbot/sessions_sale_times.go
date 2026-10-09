package eventbot

// sessions_sale_times.go — the sales end and doors-open time of an EXISTING
// session (migration 0128), changed from its card in the "Sessions" dialog.
// Neither is a buyer-visible change in the sessionchange sense: the PATCH
// writes no journal row and sends no letter, so one press saves.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// Dialog steps of the two questions.
const (
	sesStepSalesEnd = "sales_end"
	sesStepDoors    = "doors"
)

// SetSessionSaleTimes changes a session's sales end (nil keeps it) and, when
// setDoors is true, its doors-open time (nil clears it).
func (c *ArenaClient) SetSessionSaleTimes(ctx context.Context, jwt string, orgID, eventID, sessionID uuid.UUID, salesEnd, doors *time.Time, setDoors bool) (openapi.SessionEnvelope, error) {
	body := map[string]any{}
	if salesEnd != nil {
		body["sales_end_at"] = salesEnd.UTC().Format(time.RFC3339)
	}
	if setDoors {
		if doors != nil {
			body["doors_open_at"] = doors.UTC().Format(time.RFC3339)
		} else {
			body["doors_open_at"] = nil
		}
	}
	var out openapi.SessionEnvelope
	err := c.do(ctx, http.MethodPatch, sessionPath(orgID, eventID, sessionID), jwt, body, &out)
	return out, err
}

// sesClock prints an instant as the venue's wall clock.
func sesClock(t time.Time, tz string) string {
	loc := time.UTC
	if l, err := time.LoadLocation(tz); tz != "" && err == nil {
		loc = l
	}
	// allow:timeformat: clock time shown in a chat, not a wire timestamp
	return t.In(loc).Format("15:04")
}

// sesSalesEndLabel is the session's sales end as the card says it: "at the
// start" when it is the start, a clock time on the same day, the full date
// otherwise.
func (b *Bot) sesSalesEndLabel(loc string, it sesItem) string {
	end := it.Raw.SalesEndAt
	if end.IsZero() || end.Equal(it.Start) {
		return b.sesT(loc, "bot.wz.sales_end_at_start", map[string]any{"Time": it.localTime(it.Tz)})
	}
	if end.Sub(it.Start).Abs() < 12*time.Hour {
		return sesClock(end, it.Tz)
	}
	return FormatWhen(end, it.Tz)
}

// sesDoorsLabel is the doors time as the card says it.
func (b *Bot) sesDoorsLabel(loc string, it sesItem) string {
	if it.Raw.DoorsOpenAt == nil {
		return b.sesT(loc, "bot.wz.ack_doors_none", nil)
	}
	return sesClock(*it.Raw.DoorsOpenAt, it.Tz)
}

// sesSaleTimesLine is the card's line with both times.
func (b *Bot) sesSaleTimesLine(loc string, it sesItem) string {
	return b.sesT(loc, "bot.ses.sale_times_line", map[string]any{
		"SalesEnd": Esc(b.sesSalesEndLabel(loc, it)), "Doors": Esc(b.sesDoorsLabel(loc, it)),
	})
}

func (b *Bot) sesShowSalesEnd(ctx context.Context, chatID int64, msgID *int, loc string, dlg *sessionDialog, prefix string) {
	it := dlg.Items[dlg.Cur]
	start := it.localTime(it.Tz)
	b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.sales_end_q", map[string]any{
		"Now": Esc(b.sesSalesEndLabel(loc, it)), "Start": start,
	}), [][]Button{
		{b.sesBtn(loc, "bot.ses.at_start_btn", "se:0", map[string]any{"Time": start})},
		{
			b.sesBtn(loc, "bot.wz.sales_end_after_btn", "se:30", map[string]any{"Min": 30, "Time": clockAt(start, 30)}),
			b.sesBtn(loc, "bot.wz.sales_end_after_h_btn", "se:60", map[string]any{"Hours": 1, "Time": clockAt(start, 60)}),
		},
		b.sesNav(loc, "card"), b.sesHomeRow(loc),
	})
}

func (b *Bot) sesShowDoors(ctx context.Context, chatID int64, msgID *int, loc string, dlg *sessionDialog, prefix string) {
	it := dlg.Items[dlg.Cur]
	start := it.localTime(it.Tz)
	b.sesReply(ctx, chatID, msgID, dlg, prefix+b.sesT(loc, "bot.ses.doors_q", map[string]any{
		"Now": Esc(b.sesDoorsLabel(loc, it)), "Start": start,
	}), [][]Button{
		{
			b.sesBtn(loc, "bot.wz.doors_before_btn", "dr:30", map[string]any{"Min": 30, "Time": clockAt(start, -30)}),
			b.sesBtn(loc, "bot.wz.doors_before_h_btn", "dr:60", map[string]any{"Hours": 1, "Time": clockAt(start, -60)}),
		},
		{b.sesBtn(loc, "bot.ses.doors_clear_btn", "dr:0", nil)},
		b.sesNav(loc, "card"), b.sesHomeRow(loc),
	})
}

// sesSaleTimesInput applies a press ("se:<min>", "dr:<min>") or a typed
// "HH:MM" at one of the two questions, saves it and returns to the card.
func (b *Bot) sesSaleTimesInput(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, from *models.User, dlg *sessionDialog, text, data string) {
	loc := id.Locale()
	if !b.sesCurOpen(dlg) {
		return
	}
	it := dlg.Items[dlg.Cur]
	start := it.localTime(it.Tz)
	var (
		salesEnd, doors *time.Time
		setDoors        bool
	)
	switch dlg.Step {
	case sesStepSalesEnd:
		off, ok := parseOffsetData("se:", data)
		if !ok && text != "" {
			off, ok = salesEndOffset(start, text)
		}
		if !ok {
			b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.wz.err_sales_end", nil))
			return
		}
		v := it.Start.Add(time.Duration(off) * time.Minute)
		salesEnd = &v
	case sesStepDoors:
		before, ok := parseOffsetData("dr:", data)
		if !ok && text != "" {
			before, ok = doorsOffset(start, text)
		}
		if !ok || before < 0 || before > maxDoorsMin {
			b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.wz.err_doors", nil))
			return
		}
		setDoors = true
		if before > 0 {
			v := it.Start.Add(-time.Duration(before) * time.Minute)
			doors = &v
		}
	default:
		return
	}
	if _, err := b.arena.SetSessionSaleTimes(ctx, jwt, dlg.OrgID, dlg.EventID, it.ID, salesEnd, doors, setDoors); err != nil {
		if code := APIErrorCode(err); strings.HasPrefix(code, "session.") && IsAPIError(err, http.StatusBadRequest) {
			b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.wz.err_doors", nil))
			return
		}
		b.sesFail(ctx, chatID, msgID, id, dlg, err)
		return
	}
	if items, lerr := b.loadSesItems(ctx, jwt, dlg.OrgID, dlg.EventID); lerr == nil {
		dlg.Items = items
	}
	dlg.Step = sesStepCard
	b.sesShow(ctx, chatID, msgID, id, jwt, from, dlg, b.sesT(loc, "bot.ses.sale_times_saved", nil))
}
