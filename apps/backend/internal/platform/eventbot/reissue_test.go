package eventbot

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The button is on the card exactly where "Resend tickets" is: a paid order
// with an active ticket, owner or manager, in the private chat.
func TestReissueButton_OnlyWhereItCanBeUsed(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	has := func(id *Identity, status string, tickets int, private bool) (bool, string) {
		d := orderDetail(t, status, "", tickets, "")
		kb := b.withReissueButton(b.orderCardKeyboard("ru", d), id, d, private)
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				if strings.HasPrefix(btn.CallbackData, "or:x:s:") {
					if btn.Text != "🔁 Заменить билеты" {
						t.Errorf("label = %q", btn.Text)
					}
					return true, btn.CallbackData
				}
			}
		}
		return false, ""
	}
	for _, id := range []*Identity{resendIdentity("org_admin", false), resendIdentity("organizer", false), resendIdentity("agent", true)} {
		id.Link.Locale = "ru"
		if ok, data := has(id, "paid", 2, true); !ok || len(data) > 64 {
			t.Errorf("%+v: a paid order must offer the button: %v %q", id.Current, ok, data)
		}
	}
	mgr := resendIdentity("organizer", false)
	mgr.Link.Locale = "ru"
	if ok, _ := has(mgr, "partially_refunded", 1, true); !ok {
		t.Error("a partially refunded order must offer the button")
	}
	for name, c := range map[string]struct {
		id      *Identity
		status  string
		tickets int
		private bool
	}{
		"agent":      {resendIdentity("agent", false), "paid", 2, true},
		"unpaid":     {mgr, "pending_payment", 1, true},
		"no tickets": {mgr, "paid", 0, true},
		"group":      {mgr, "paid", 2, false},
	} {
		if ok, _ := has(c.id, c.status, c.tickets, c.private); ok {
			t.Errorf("%s: the button must not be offered", name)
		}
	}
}

func reissueOpts(route string, tickets ...openapi.ReissueTicketOption) openapi.ReissueOptions {
	return openapi.ReissueOptions{Route: route, OrderNumber: 1000000500, OrderStatus: "paid", OrderEmail: "anna@example.test", Tickets: tickets}
}

func rxTicket(n int64, reissuable bool, reason string) openapi.ReissueTicketOption {
	t := openapi.ReissueTicketOption{TicketId: uuid.New(), Number: n, Status: "active", Reissuable: reissuable}
	if reason != "" {
		t.Reason = strp(reason)
	}
	if reason == "not_active" {
		t.Status = "transferred"
	}
	return t
}

// The first press decides the screen from the server's options alone.
func TestReissueStartScreen(t *testing.T) {
	t.Parallel()
	ok := rxTicket(101, true, "")
	type startCase struct {
		name        string
		opts        openapi.ReissueOptions
		screen, key string
	}
	cases := []startCase{
		{"open", reissueOpts("arena", ok), "case", ""},
		{"site", reissueOpts("seller_site", ok), "site", "bot.reissue.site"},
		{"nothing", reissueOpts("arena", rxTicket(1, false, "not_active")), "blocked", "bot.reissue.err_nothing"},
	}
	for _, c := range []struct{ code, key string }{
		{"order.not_paid", "bot.reissue.err_not_paid"},
		{"order.seller_site_order", "bot.reissue.site"},
		{"reissue.session_closed", "bot.reissue.err_closed"},
		{"reissue.nothing_to_reissue", "bot.reissue.err_nothing"},
		{"reissue.in_progress", "bot.reissue.err_in_progress"},
	} {
		o := reissueOpts("arena", ok)
		o.Blocked = strp(c.code)
		cases = append(cases, startCase{c.code, o, "blocked", c.key})
	}
	review := reissueOpts("arena", ok, rxTicket(102, false, "review_hold"))
	review.Blocked = strp("reissue.ticket_not_reissuable")
	refund := reissueOpts("arena", rxTicket(103, false, "refund_in_progress"), ok)
	refund.Blocked = strp("reissue.ticket_not_reissuable")
	pending := reissueOpts("arena", ok)
	pending.Blocked = strp("reissue.in_progress")
	pending.PendingReissue = &openapi.ReissuePending{Id: uuid.New(), State: "pending", NewEmail: "real@example.org"}
	cases = append(cases,
		startCase{"review hold", review, "blocked", "bot.reissue.err_ticket_review"},
		startCase{"refund in progress", refund, "blocked", "bot.reissue.err_ticket_refund"},
		startCase{"pending", pending, "pending", ""},
	)
	for _, c := range cases {
		screen, key := reissueStartScreen(c.opts)
		if screen != c.screen || key != c.key {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, screen, key, c.screen, c.key)
		}
	}
	if tk := reissueBlockingTicket(review.Tickets); tk == nil || tk.Number != 102 {
		t.Errorf("the blocking ticket is not named: %+v", tk)
	}
	if n := reissuableCount(reissueOpts("arena", ok, rxTicket(2, false, "not_active"), rxTicket(3, true, "")).Tickets); n != 2 {
		t.Errorf("reissuable = %d, want 2", n)
	}
}

// Every API refusal has its own polite text that renders in every language
// and names the order.
func TestReissueRefusalKey_EveryCodeHasItsText(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	codes := []string{
		"order.not_paid", "order.seller_site_order", "reissue.nothing_to_reissue", "reissue.session_closed",
		"reissue.in_progress", "reissue.ticket_not_reissuable", "reissue.idempotency_key_reused",
		"reissue.already_applied", "reissue.invalid_email", "reissue.invalid_reason", "reissue.note_too_long",
		"reissue.invalid_body", "reissue.idempotency_key_required", "something.new",
	}
	seen := map[string]string{}
	for _, code := range codes {
		key := reissueRefusalKey(code, "")
		if prev, dup := seen[key]; dup && key != "bot.reissue.err_refused" && key != "bot.reissue.site" {
			t.Errorf("%s and %s share the text %s", code, prev, key)
		}
		seen[key] = code
		for _, loc := range SupportedLocales {
			got := b.texts.T(loc, key, map[string]any{"Num": int64(1000000500), "Ticket": "№7"})
			if got == key || got == "" || strings.Contains(got, "<no value>") {
				t.Errorf("%s/%s: %q", loc, code, got)
			}
		}
	}
	if reissueRefusalKey("reissue.ticket_not_reissuable", "refund_in_progress") != "bot.reissue.err_ticket_refund" ||
		reissueRefusalKey("reissue.ticket_not_reissuable", "review_hold") != "bot.reissue.err_ticket_review" {
		t.Error("a ticket refusal must name its reason")
	}
	// A refusal never blames: it says what to do next.
	if got := b.texts.T("ru", "bot.reissue.err_ticket_refund", map[string]any{"Num": 1, "Ticket": "№7"}); !strings.Contains(got, "№7") || !strings.Contains(got, "Ничего не изменилось") {
		t.Errorf("ru ticket refusal: %q", got)
	}
}

func TestReissueReasonsAndLocks(t *testing.T) {
	t.Parallel()
	for arg, want := range map[string]string{"1": "sent_to_wrong_address", "2": "forwarded_to_stranger", "3": "other"} {
		if got, ok := reissueReasonFor(arg); !ok || got != want {
			t.Errorf("reason %s = %q %v", arg, got, ok)
		}
	}
	for _, bad := range []string{"", "0", "4", "x", "-1"} {
		if _, ok := reissueReasonFor(bad); ok {
			t.Errorf("reason %q must be refused", bad)
		}
	}
	idle := reissueDialog{}
	for _, k := range []string{"b", "fo", "fn", "r", "nn", "ok", "no"} {
		if reissuePressLocked(idle, k) {
			t.Errorf("an idle dialog locks %q", k)
		}
	}
	tried := reissueDialog{Attempted: true}
	for _, k := range []string{"b", "fo", "fn", "r", "nn"} {
		if !reissuePressLocked(tried, k) {
			t.Errorf("an operation that may be done must lock %q", k)
		}
	}
	for _, k := range []string{"ok", "no", "rs"} {
		if reissuePressLocked(tried, k) {
			t.Errorf("%q must stay open", k)
		}
	}
	if !reissueOutcomeUnknown(errors.New("dial tcp: timeout")) || !reissueOutcomeUnknown(&APIError{Status: 500}) {
		t.Error("no answer or a 500 is an unknown outcome")
	}
	for _, st := range []int{400, 403, 404, 409, 422, http.StatusServiceUnavailable} {
		if reissueOutcomeUnknown(&APIError{Status: st}) {
			t.Errorf("%d is a known outcome", st)
		}
	}
	if validReissueNote("") || validReissueNote(strings.Repeat("я", 501)) || !validReissueNote(strings.Repeat("я", 500)) {
		t.Error("the note rule is the API's: 1..500 characters")
	}
}

// The reissue word confirms a reissue only, and no other word confirms it.
func TestTypedWords_ReissueHasItsOwnPhrase(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	others := []string{ordCancelWordKey, sesCancelWordKey, "bot.ref.word", "bot.evs.delete_word", "bot.inv.revoke_word", "bot.promo.delete_word", disputeSubmitWordKey, disputeAcceptWordKey}
	for _, loc := range SupportedLocales {
		w := b.reissueWord(loc)
		if w == "" || w == reissueWordKey {
			t.Fatalf("%s: the reissue word is missing", loc)
		}
		if !b.isReissueWord(loc, w) || !b.isReissueWord(loc, "  reissue   tickets. ") || !b.isReissueWord(loc, "«"+strings.ToLower(w)+"»") {
			t.Errorf("%s: %q and the English phrase must confirm", loc, w)
		}
		for _, k := range others {
			for _, l2 := range SupportedLocales {
				o := b.texts.T(l2, k, nil)
				if b.isReissueWord(loc, o) {
					t.Errorf("%s: %q (%s/%s) confirms a reissue", loc, o, l2, k)
				}
				if normalizePhrase(o) == normalizePhrase(w) {
					t.Errorf("%s: the reissue word equals %s/%s", loc, l2, k)
				}
			}
		}
		for _, typed := range []string{"", "да", "yes", "заменить", "REISSUE", "tickets", "ЗАМЕНИТЬ БИЛЕТ"} {
			if b.isReissueWord(loc, typed) {
				t.Errorf("%s: %q confirms a reissue", loc, typed)
			}
		}
	}
	if !b.isReissueWord("es", "reemitir entradas") || !b.isReissueWord("ru", "заменить билеты") {
		t.Error("each language's own phrase must confirm")
	}
}

// The screens render in every language: the check screen hides the new
// address, warns about a used code and about a provider that keeps the old
// address; the result lists the new numbers.
func TestReissueScreens_RenderInEveryLocale(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	st := reissueDialog{
		Num: 1000000500, Tickets: 2, Masked: "a***@e******.test", Buyer: "Anna <B>", Phone: "+34 600",
		Email: "real.buyer@example.org", Reason: "forwarded_to_stranger", Note: "Called <us>",
		Scanned:  []reissueScanned{{Number: 102, When: "01.02.2099 19:20"}},
		Provider: "AllPay", ProviderKeeps: true,
	}
	res := openapi.ReissueTicketsResponse{RecipientMasked: "r*********@e******.org", Tickets: []openapi.ReissuePair{
		{OldNumber: 101, NewNumber: 201, Seat: "A-3-12"}, {OldNumber: 102, NewNumber: 202},
	}}
	for _, loc := range SupportedLocales {
		check := b.reissueCheckText(loc, st)
		for _, want := range []string{"1000000500", "№102", "AllPay", maskEmail(st.Email), "Called &lt;us&gt;", b.texts.T(loc, "bot.reissue.reason_forwarded_to_stranger", nil)} {
			if !strings.Contains(check, want) {
				t.Errorf("%s: check lacks %q:\n%s", loc, want, check)
			}
		}
		if strings.Contains(check, st.Email) {
			t.Errorf("%s: the check screen must mask the new address:\n%s", loc, check)
		}
		ask := b.reissueEmailAsk(loc, st)
		for _, want := range []string{"Anna &lt;B&gt;", "+34 600", st.Masked} {
			if !strings.Contains(ask, want) {
				t.Errorf("%s: the address question lacks %q:\n%s", loc, want, ask)
			}
		}
		done := b.reissueResultText(loc, st, res)
		for _, want := range []string{"№101 → №201 · A-3-12", "№102 → №202", res.RecipientMasked} {
			if !strings.Contains(done, want) {
				t.Errorf("%s: the result lacks %q:\n%s", loc, want, done)
			}
		}
		for _, s := range []string{check, ask, done} {
			if strings.Contains(s, "<no value>") || strings.Contains(s, "bot.reissue.") {
				t.Errorf("%s: a text did not render:\n%s", loc, s)
			}
		}
		res.Replayed = true
		if again := b.reissueResultText(loc, st, res); !strings.HasPrefix(again, b.texts.T(loc, "bot.reissue.replayed", nil)) {
			t.Errorf("%s: a replay must say so", loc)
		}
		res.Replayed = false
	}
	// Without a provider note and a used code there is no warning line.
	plain := b.reissueCheckText("ru", reissueDialog{Num: 1, Tickets: 1, Email: "x@y.zz", Reason: "other"})
	if strings.Contains(plain, "ℹ️") || strings.Count(plain, "⚠️") != 1 || strings.Contains(plain, "Заметка") {
		t.Errorf("plain check screen:\n%s", plain)
	}
}

// "or:x" alone is the orders list's "clear the search": it ends a reissue
// dialog like any foreign press, while every "or:x:" press is the dialog's own.
func TestReissueDialog_OwnsOnlyItsPresses(t *testing.T) {
	t.Parallel()
	o := dialogOwners[reissueDialogKind]
	for _, d := range []string{"or:x:s:" + uuid.NewString(), "or:x:fo", "or:x:fn", "or:x:r:1", "or:x:nn", "or:x:ok", "or:x:b", "or:x:no", "or:x:rs"} {
		if !o.owns(d) {
			t.Errorf("%q must belong to the reissue dialog", d)
		}
	}
	for _, d := range []string{"or:x", "or:r:a", "or:f:x:1", "or:v:" + uuid.NewString(), "home"} {
		if o.owns(d) {
			t.Errorf("%q must end the reissue dialog", d)
		}
	}
}

// An invitation issued before spec 46 has no case question and speaks of
// an invitation: every text with an order number or an order address has its
// invitation wording, and the refusal of a revoked invitation has its own.
func TestReissueInvitation_ScreensAndWording(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	ok := rxTicket(301, true, "")
	open := openapi.InvitationReissueOptions{InvitationId: uuid.New(), InvitationStatus: "issued", GuestEmail: "g@example.test", Tickets: []openapi.ReissueTicketOption{ok}}
	if screen, key := invitationStartScreen(open); screen != "email" || key != "" {
		t.Errorf("an open invitation starts at the address: %q %q", screen, key)
	}
	revoked := open
	revoked.Blocked = strp("reissue.invitation_not_issued")
	if screen, key := invitationStartScreen(revoked); screen != "blocked" || key != "bot.reissue.inv_err_not_issued" {
		t.Errorf("a revoked invitation: %q %q", screen, key)
	}
	pending := open
	pending.PendingReissue = &openapi.ReissuePending{Id: uuid.New(), State: "send_failed"}
	if screen, _ := invitationStartScreen(pending); screen != "pending" {
		t.Errorf("a pending reissue: %q", screen)
	}

	for name := range reissueInvVariants {
		if got := rxKey(true, "bot.reissue."+name); got != "bot.reissue.inv_"+name {
			t.Errorf("rxKey(%s) = %q", name, got)
		}
		if got := rxKey(false, "bot.reissue."+name); got != "bot.reissue."+name {
			t.Errorf("an order keeps its wording for %s, got %q", name, got)
		}
	}
	if rxKey(true, "bot.reissue.word_wrong") != "bot.reissue.word_wrong" {
		t.Error("a text without an order in it stays shared")
	}

	st := reissueDialog{InvitationID: uuid.New(), Tickets: 1, Masked: "g*@e******.test", Email: "guest.real@example.org", Reason: "other",
		Scanned: []reissueScanned{{Number: 301, When: "01.02.2099 19:20 UTC"}}}
	res := openapi.ReissueTicketsResponse{RecipientMasked: "g*********@e******.org", Tickets: []openapi.ReissuePair{{OldNumber: 301, NewNumber: 401}}}
	for _, loc := range SupportedLocales {
		for _, s := range []string{b.reissueCheckText(loc, st), b.reissueEmailAsk(loc, st), b.reissueResultText(loc, st, res),
			b.rxT(loc, st, "bot.reissue.word_ask", map[string]any{"N": 1, "Word": b.reissueWord(loc)})} {
			if strings.Contains(s, "№0") || strings.Contains(s, "<no value>") || strings.Contains(s, "bot.reissue.") {
				t.Errorf("%s: an invitation text names an order or did not render:\n%s", loc, s)
			}
			if strings.Contains(s, st.Email) {
				t.Errorf("%s: the new address must be masked:\n%s", loc, s)
			}
		}
		if done := b.reissueResultText(loc, st, res); !strings.Contains(done, "№301 → №401") {
			t.Errorf("%s: the result lacks the new number:\n%s", loc, done)
		}
	}
	if st.backTo() != "iv:lg" || (reissueDialog{OrderID: uuid.New()}).backTo()[:5] != "or:v:" {
		t.Error("Back leads to the earlier invitations or to the order card")
	}
}

// The button is on an earlier invitation's card only while it is issued with
// an active ticket, for the owner or a manager, in the private chat.
func TestReissueInvitation_ButtonOnlyWhereItCanBeUsed(t *testing.T) {
	t.Parallel()
	item := func(status string, ticket string) openapi.ComplimentaryListItem {
		return openapi.ComplimentaryListItem{Id: uuid.New(), Status: status, Tickets: []openapi.ComplimentaryTicketItem{{Id: uuid.New(), Status: ticket}}}
	}
	owner, agent := resendIdentity("org_admin", false), resendIdentity("agent", false)
	if !invitationReissueOffered(owner, item("issued", "active"), true) || !invitationReissueOffered(resendIdentity("organizer", false), item("issued", "active"), true) {
		t.Error("an issued invitation with an active ticket must offer the button")
	}
	for name, c := range map[string]struct {
		id      *Identity
		it      openapi.ComplimentaryListItem
		private bool
	}{
		"agent":         {agent, item("issued", "active"), true},
		"revoked":       {owner, item("revoked", "cancelled"), true},
		"manual review": {owner, item("manual_review", "active"), true},
		"no active":     {owner, item("issued", "transferred"), true},
		"group":         {owner, item("issued", "active"), false},
	} {
		if invitationReissueOffered(c.id, c.it, c.private) {
			t.Errorf("%s: the button must not be offered", name)
		}
	}
	if len("or:x:si:"+uuid.NewString()) > 64 {
		t.Error("callback data over 64 bytes")
	}
}
