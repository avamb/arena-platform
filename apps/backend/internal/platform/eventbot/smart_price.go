package eventbot

// smart_price.go — the "Smart price" screen of a category card (spec
// 08_architecture/51_4_2_smart_price_ru.md). It lives in the categories
// dialog (kind "category"), so every press keeps the "ct:" prefix:
//
//	ct:sp:<tier>   the screen: where an active preset stands, or the three presets
//	ct:ss:<tier>   rise every N tickets sold    ct:sd:<tier>  rise every N days
//	ct:sl:<tier>   higher in the last days      (each shows the server's preview)
//	ct:sn:<tier>   type other numbers           ct:se:<tier>  turn the previewed preset on
//	ct:sx:<tier>   ask to stop the rise         ct:sz:<tier>  stop it
//
// The bot computes no price: the steps, the windows and where a preset stands
// come from arena-api. A preset is turned on only by the press under the
// preview the person saw (step "smart_confirm" for THAT category), and
// stopped only from its own question (step "smart_stop").

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The steps of the smart price screens.
const (
	catStepSmartOK   = "smart_confirm" // a preview waits for its "turn on" press
	catStepSmartNums = "smart_nums"    // the preset's numbers are awaited as text
	catStepSmartStop = "smart_stop"    // the stop question is on screen
)

// smartKindOf maps a press to the preset it picks.
var smartKindOf = map[string]string{"ss": "by_sales", "sd": "by_days", "sl": "last_days"}

// smartPresses are the presses of this screen; each names a category.
var smartPresses = []string{"sp", "ss", "sd", "sl", "sn", "se", "sx", "sz"}

func isSmartPress(kind string) bool {
	for _, k := range smartPresses {
		if k == kind {
			return true
		}
	}
	return false
}

// smartDefaults are the proposed numbers of a preset: the person sees their
// table and may type others before anything is written.
func smartDefaults(kind string) smartNums {
	switch kind {
	case "by_days":
		return smartNums{Step: 10, Every: 7, Cap: 50}
	case "last_days":
		return smartNums{Step: 20, Every: 3}
	}
	return smartNums{Step: 10, Every: 50, Cap: 50}
}

// smartNums are the preset's numbers kept in the dialog between the preview
// and the press that turns it on.
type smartNums struct {
	Step  int `json:"step"`
	Every int `json:"every"`
	Cap   int `json:"cap,omitempty"`
}

// parseSmartNums reads "10 50 50" (step %, every, cap %); the last days take
// two numbers. Only whole positive numbers; the server checks the ranges.
func parseSmartNums(kind, text string) (smartNums, bool) {
	fields := strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == ',' || r == ';' || r == '/' || r == '\t' })
	want := 3
	if kind == "last_days" {
		want = 2
	}
	if len(fields) != want {
		return smartNums{}, false
	}
	vals := make([]int, want)
	for i, f := range fields {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(f, "+"), "%"))
		if err != nil || n < 1 || n > 100000 {
			return smartNums{}, false
		}
		vals[i] = n
	}
	out := smartNums{Step: vals[0], Every: vals[1]}
	if want == 3 {
		out.Cap = vals[2]
	}
	return out, true
}

func smartRequest(kind string, tierID uuid.UUID, n smartNums) openapi.SmartPriceRequest {
	req := openapi.SmartPriceRequest{TierIds: []uuid.UUID{tierID}, Kind: kind, StepPercent: n.Step, Every: n.Every}
	if kind != "last_days" {
		capPct := n.Cap
		req.CapPercent = &capPct
	}
	return req
}

// smartRuleFor finds the active preset that drives t (it is the preset's
// first step or one of its later steps), and the latest ended one of t.
func smartRuleFor(rules []openapi.SmartPriceRule, tierID uuid.UUID) (active, ended *openapi.SmartPriceRule) {
	for i := range rules {
		r := &rules[i]
		mine := r.TierId == tierID
		for _, s := range r.StepTierIds {
			if s == tierID {
				mine = true
			}
		}
		if !mine {
			continue
		}
		if r.Status == "active" {
			if active == nil {
				active = r
			}
		} else if ended == nil && r.TierId == tierID {
			ended = r
		}
	}
	return active, ended
}

// smartErrKey maps a refused preset to the message that says what to do.
// "" means the answer has no words of its own.
func smartErrKey(err error) string {
	switch APIErrorCode(err) {
	case "smart_price.already_on", "smart_price.category_in_chain":
		return "bot.sp.err_on"
	case "smart_price.changed":
		return "bot.sp.err_changed"
	case "smart_price.schedule_exists":
		return "bot.sp.err_schedule"
	case "smart_price.not_enough_places":
		return "bot.sp.err_places"
	case "smart_price.no_room":
		return "bot.sp.err_no_room"
	case "smart_price.too_late":
		return "bot.sp.err_too_late"
	case "smart_price.seated_by_sales":
		return "bot.sp.err_seated"
	case "smart_price.free_category":
		return "bot.sp.err_free"
	case "smart_price.not_active", "smart_price.not_found":
		return "bot.sp.err_off"
	case "smart_price.invalid_step", "smart_price.invalid_every", "smart_price.invalid_cap",
		"smart_price.invalid_start", "smart_price.invalid_kind", "smart_price.invalid_body":
		return "bot.sp.err_input"
	}
	return ""
}

// smartFollows reports whether a hand-set price of the category would turn an
// active preset off. A failed read says nothing (the API still decides).
func (b *Bot) smartFollows(ctx context.Context, jwt string, st categoryDialog, tierID uuid.UUID) bool {
	rules, err := b.arena.SmartPriceRules(ctx, jwt, st.OrgID, st.SessionID)
	if err != nil {
		return false
	}
	active, _ := smartRuleFor(rules, tierID)
	return active != nil
}

// smartRuleText says the preset in one line: "+10% every 50 tickets sold, at most +50%".
func (b *Bot) smartRuleText(loc string, r openapi.SmartPriceRule) string {
	val := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	return b.texts.T(loc, "bot.sp.rule_"+r.Kind, map[string]any{
		"Step": val(r.Params.StepPercent), "Every": val(r.Params.Every), "Cap": val(r.Params.CapPercent),
	})
}

// smartPress handles every smart price press of one category.
func (b *Bot) smartPress(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, step, kind string, t openapi.TicketTierItem, tiers []openapi.TicketTierItem) {
	loc := id.Locale()
	caps := categoryCapsFor(t, tiers)
	if !caps.Price {
		b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
		return
	}
	switch kind {
	case "sp":
		b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, "")
	case "ss", "sd", "sl":
		pk := smartKindOf[kind]
		if pk == "by_sales" && caps.Seated {
			b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, b.texts.T(loc, "bot.sp.err_seated", nil)+"\n\n")
			return
		}
		st.SmartKind, st.SmartNums = pk, smartDefaults(pk)
		b.smartPreview(ctx, chatID, msgID, id, jwt, st, t, tiers)
	case "sn":
		if st.SmartKind == "" || st.CardID == nil || *st.CardID != t.Id {
			b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, "")
			return
		}
		b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepSmartNums)
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.sp.q_nums_"+st.SmartKind, nil), ctKeyboard([][]Button{b.ctCardBack(loc, t.Id)}))
	case "se":
		// Only the preview on screen, for THIS category, is turned on.
		if step != catStepSmartOK || st.SmartKind == "" || st.CardID == nil || *st.CardID != t.Id {
			b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, "")
			return
		}
		if _, err := b.arena.SmartPriceEnable(ctx, jwt, st.OrgID, st.SessionID, smartRequest(st.SmartKind, t.Id, st.SmartNums)); err != nil {
			b.smartFail(ctx, chatID, msgID, id, jwt, st, t, tiers, err)
			return
		}
		st.SmartKind = ""
		b.categoryShowCard(ctx, chatID, msgID, id, jwt, st, t.Id, b.texts.T(loc, "bot.sp.enabled", nil)+"\n\n")
	case "sx":
		rules, err := b.arena.SmartPriceRules(ctx, jwt, st.OrgID, st.SessionID)
		if err != nil {
			b.ecError(ctx, chatID, msgID, id, err, "events:1")
			return
		}
		active, _ := smartRuleFor(rules, t.Id)
		if active == nil {
			b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, "")
			return
		}
		price := active.BasePrice
		if active.Now != nil {
			price = active.Now.Price
		}
		st.CardID = &t.Id
		b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepSmartStop)
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.sp.stop_confirm", map[string]any{
			"Name": Esc(t.Name), "Price": Esc(FormatMoney(price, active.Currency, loc)),
		}), ctKeyboard([][]Button{
			{b.ctBtn(loc, "bot.sp.stop_yes_btn", "sz:"+t.Id.String(), nil)},
			b.ctCardBack(loc, t.Id),
		}))
	case "sz":
		if step != catStepSmartStop || st.CardID == nil || *st.CardID != t.Id {
			b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, "")
			return
		}
		rules, err := b.arena.SmartPriceRules(ctx, jwt, st.OrgID, st.SessionID)
		if err != nil {
			b.ecError(ctx, chatID, msgID, id, err, "events:1")
			return
		}
		active, _ := smartRuleFor(rules, t.Id)
		if active == nil {
			b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, b.texts.T(loc, "bot.sp.err_off", nil)+"\n\n")
			return
		}
		if err := b.arena.SmartPriceStop(ctx, jwt, st.OrgID, st.SessionID, active.Id); err != nil {
			b.smartFail(ctx, chatID, msgID, id, jwt, st, t, tiers, err)
			return
		}
		b.categoryShowCard(ctx, chatID, msgID, id, jwt, st, t.Id, b.texts.T(loc, "bot.sp.stopped", nil)+"\n\n")
	}
}

// smartScreen shows where an active preset stands, or offers the presets.
func (b *Bot) smartScreen(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, t openapi.TicketTierItem, tiers []openapi.TicketTierItem, note string) {
	loc := id.Locale()
	rules, err := b.arena.SmartPriceRules(ctx, jwt, st.OrgID, st.SessionID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "events:1")
		return
	}
	st.CardID, st.SmartKind = &t.Id, ""
	b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepCard)
	caps := categoryCapsFor(t, tiers)
	tid := t.Id.String()
	active, ended := smartRuleFor(rules, t.Id)

	var sb strings.Builder
	sb.WriteString(note)
	sb.WriteString(b.texts.T(loc, "bot.sp.title", map[string]any{"Name": Esc(t.Name)}))
	var rows [][]Button
	switch {
	case active != nil:
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sp.active", map[string]any{"Rule": Esc(b.smartRuleText(loc, *active))}))
		if n := active.Now; n != nil {
			sb.WriteString("\n" + b.texts.T(loc, "bot.sp.now", map[string]any{
				"Price": Esc(FormatMoney(n.Price, active.Currency, loc)), "Step": n.Step, "Steps": n.Steps,
			}))
			switch {
			case n.NextPrice == nil:
				sb.WriteString("\n" + b.texts.T(loc, "bot.sp.now_last", nil))
			case n.RemainingAtPrice != nil:
				sb.WriteString("\n" + b.texts.T(loc, "bot.sp.now_left", map[string]any{
					"Next": Esc(FormatMoney(*n.NextPrice, active.Currency, loc)), "Left": *n.RemainingAtPrice,
				}))
			case n.NextAt != nil:
				sb.WriteString("\n" + b.texts.T(loc, "bot.sp.now_at", map[string]any{
					"Next": Esc(FormatMoney(*n.NextPrice, active.Currency, loc)), "When": Esc(FormatWhen(*n.NextAt, st.Tz)),
				}))
			}
		}
		rows = append(rows, []Button{b.ctBtn(loc, "bot.sp.stop_btn", "sx:"+tid, nil)})
	case caps.Chain:
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sp.in_chain", nil))
	default:
		if ended != nil && ended.StoppedAt != nil {
			key := "bot.sp.ended_stopped"
			if ended.Status == "overridden" {
				key = "bot.sp.ended_overridden"
			}
			sb.WriteString("\n\n" + b.texts.T(loc, key, map[string]any{"When": Esc(FormatWhen(*ended.StoppedAt, st.Tz))}))
		}
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.sp.intro", nil))
		if !caps.Seated {
			rows = append(rows, []Button{b.ctBtn(loc, "bot.sp.kind_by_sales_btn", "ss:"+tid, nil)})
		}
		rows = append(rows,
			[]Button{b.ctBtn(loc, "bot.sp.kind_by_days_btn", "sd:"+tid, nil)},
			[]Button{b.ctBtn(loc, "bot.sp.kind_last_days_btn", "sl:"+tid, nil)})
	}
	rows = append(rows, b.ctCardBack(loc, t.Id))
	b.reply(ctx, chatID, msgID, clipMessage(sb.String(), maxMessageRunes), ctKeyboard(rows))
}

// smartPreview asks the server for the preset's table and shows it with the
// one press that turns it on.
func (b *Bot) smartPreview(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, t openapi.TicketTierItem, tiers []openapi.TicketTierItem) {
	loc := id.Locale()
	res, err := b.arena.SmartPricePreview(ctx, jwt, st.OrgID, st.SessionID, smartRequest(st.SmartKind, t.Id, st.SmartNums))
	if err != nil {
		b.smartFail(ctx, chatID, msgID, id, jwt, st, t, tiers, err)
		return
	}
	st.CardID = &t.Id
	b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepSmartOK)

	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.sp.preview_head", map[string]any{
		"Name": Esc(t.Name),
		"Rule": Esc(b.smartRuleText(loc, openapi.SmartPriceRule{Kind: st.SmartKind, Params: openapi.SmartPriceParams{
			StepPercent: &st.SmartNums.Step, Every: &st.SmartNums.Every, CapPercent: &st.SmartNums.Cap,
		}})),
	}))
	for _, c := range res.Categories {
		sb.WriteString("\n")
		if res.Kind != "by_sales" {
			sb.WriteString("\n" + b.texts.T(loc, "bot.sp.row_now", map[string]any{"Price": Esc(FormatMoney(c.BasePrice, c.Currency, loc))}))
		}
		for i, s := range c.Steps {
			switch {
			case res.Kind == "by_sales" && i == len(c.Steps)-1:
				sb.WriteString("\n" + b.texts.T(loc, "bot.sp.row_rest", map[string]any{"N": i + 1, "Places": intOr0(s.Places), "Price": Esc(FormatMoney(s.Price, c.Currency, loc))}))
			case res.Kind == "by_sales":
				sb.WriteString("\n" + b.texts.T(loc, "bot.sp.row_tickets", map[string]any{"N": i + 1, "Places": intOr0(s.Places), "Price": Esc(FormatMoney(s.Price, c.Currency, loc))}))
			case s.From != nil:
				sb.WriteString("\n" + b.texts.T(loc, "bot.sp.row_from", map[string]any{"When": Esc(FormatWhen(*s.From, st.Tz)), "Price": Esc(FormatMoney(s.Price, c.Currency, loc))}))
			}
		}
	}
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.sp.preview_note", nil))
	tid := t.Id.String()
	b.reply(ctx, chatID, msgID, clipMessage(sb.String(), maxMessageRunes), ctKeyboard([][]Button{
		{b.ctBtn(loc, "bot.sp.enable_btn", "se:"+tid, nil)},
		{b.ctBtn(loc, "bot.sp.nums_btn", "sn:"+tid, nil)},
		b.ctCardBack(loc, t.Id),
	}))
}

func intOr0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// smartFail answers a refused preset: a refusal the bot has words for keeps
// the person on the smart price screen with the reason above it.
func (b *Bot) smartFail(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, t openapi.TicketTierItem, tiers []openapi.TicketTierItem, err error) {
	key := smartErrKey(err)
	if key == "" {
		b.ecError(ctx, chatID, msgID, id, err, "events:1")
		return
	}
	note := b.texts.T(id.Locale(), key, nil) + "\n\n"
	switch key {
	case "bot.sp.err_input", "bot.sp.err_places", "bot.sp.err_no_room", "bot.sp.err_too_late":
		if st.SmartKind == "" {
			break
		}
		// Other numbers mend these: ask them again rather than drop the chosen preset.
		st.CardID = &t.Id
		b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepSmartNums)
		b.reply(ctx, chatID, msgID, note+b.texts.T(id.Locale(), "bot.sp.q_nums_"+st.SmartKind, nil), ctKeyboard([][]Button{b.ctCardBack(id.Locale(), t.Id)}))
		return
	}
	b.smartScreen(ctx, chatID, msgID, id, jwt, st, t, tiers, note)
}

// smartText takes the typed numbers of a preset.
func (b *Bot) smartText(ctx context.Context, chatID int64, edit *int, id *Identity, jwt string, st categoryDialog, t openapi.TicketTierItem, tiers []openapi.TicketTierItem, text string) {
	loc := id.Locale()
	n, ok := parseSmartNums(st.SmartKind, text)
	if !ok || st.SmartKind == "" {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.sp.err_nums", nil)+"\n\n"+b.texts.T(loc, "bot.sp.q_nums_"+st.SmartKind, nil),
			ctKeyboard([][]Button{b.ctCardBack(loc, t.Id)}))
		return
	}
	st.SmartNums = n
	b.smartPreview(ctx, chatID, edit, id, jwt, st, t, tiers)
}
