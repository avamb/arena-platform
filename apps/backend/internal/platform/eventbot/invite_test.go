package eventbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// ─── the recipient parser (pure) ──────────────────────────────────────────────

func TestParseInviteRecipients_Formats(t *testing.T) {
	t.Parallel()
	text := strings.Join([]string{
		"Anna Novak, Anna@Example.com",                // name, e-mail (case folded)
		"boris@example.com",                           // bare e-mail
		"",                                            // blank lines are skipped
		"Clara Ruiz <clara@example.com>",              // Name <e-mail>
		"dmitri@example.com; Dmitri Petrov",           // e-mail first, semicolon
		"3. Eva Kral\teva@example.com",                // list marker and a tab
		"- «Fred» fred@example.com,",                  // bullet, quotes, trailing comma
		"Gabriela Sanchez Lopez gabriela@example.org", // spaces only
	}, "\n")
	got, bad := ParseInviteRecipients(text, nil)
	if len(bad) != 0 {
		t.Fatalf("unexpected problems: %+v", bad)
	}
	want := []InviteRecipient{
		{"Anna Novak", "anna@example.com"},
		{"", "boris@example.com"},
		{"Clara Ruiz", "clara@example.com"},
		{"Dmitri Petrov", "dmitri@example.com"},
		{"Eva Kral", "eva@example.com"},
		{"Fred", "fred@example.com"},
		{"Gabriela Sanchez Lopez", "gabriela@example.org"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d guests %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("guest %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseInviteRecipients_CRLFAndNumbering(t *testing.T) {
	t.Parallel()
	got, bad := ParseInviteRecipients("1) a@example.com\r\n2) b@example.com\r\n", nil)
	if len(bad) != 0 || len(got) != 2 || got[0].Email != "a@example.com" || got[1].Email != "b@example.com" {
		t.Fatalf("got %+v bad %+v", got, bad)
	}
}

// One problem line refuses the whole message, and every problem is named with
// its line number (blank lines still count) and its reason.
func TestParseInviteRecipients_ProblemsRefuseTheMessage(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", inviteMaxNameRunes+1)
	text := strings.Join([]string{
		"ok@example.com",         // 1: fine
		"",                       // 2
		"Just a name",            // 3: no address
		"broken@",                // 4: not an address
		"a@example.com b@x.org",  // 5: two addresses
		long + ", n@example.org", // 6: name too long
		"OK@example.com",         // 7: repeats line 1
	}, "\n")
	got, bad := ParseInviteRecipients(text, nil)
	if got != nil {
		t.Fatalf("a message with problems must yield no guests, got %+v", got)
	}
	want := map[int]string{3: inviteReasonEmail, 4: inviteReasonEmail, 5: inviteReasonSeveral, 6: inviteReasonName, 7: inviteReasonRepeat}
	if len(bad) != len(want) {
		t.Fatalf("problems = %+v, want lines %v", bad, want)
	}
	for _, p := range bad {
		if want[p.Line] != p.Reason {
			t.Errorf("line %d reason = %q, want %q", p.Line, p.Reason, want[p.Line])
		}
		if p.Text == "" {
			t.Errorf("line %d carries no text", p.Line)
		}
	}
}

func TestParseInviteRecipients_RepeatsAgainstEarlierMessages(t *testing.T) {
	t.Parallel()
	existing := []InviteRecipient{{"Anna", "anna@example.com"}}
	got, bad := ParseInviteRecipients("ANNA@example.com\nnew@example.com", existing)
	if got != nil || len(bad) != 1 || bad[0].Line != 1 || bad[0].Reason != inviteReasonRepeat {
		t.Fatalf("got %+v bad %+v", got, bad)
	}
	got, bad = ParseInviteRecipients("new@example.com", existing)
	if len(bad) != 0 || len(got) != 1 {
		t.Fatalf("got %+v bad %+v", got, bad)
	}
}

func TestParseInviteRecipients_Empty(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "  \n\n  ", "\r\n"} {
		if got, bad := ParseInviteRecipients(in, nil); len(got) != 0 || len(bad) != 0 {
			t.Errorf("%q: got %+v bad %+v", in, got, bad)
		}
	}
}

func TestParseInviteQty(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"1": 1, " 12 ": 12, "50": 50, "51": 51, "000003": 3} {
		if n, ok := ParseInviteQty(in); !ok || n != want {
			t.Errorf("ParseInviteQty(%q) = %d %v, want %d", in, n, ok, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "3.5", "four", "1 2", "1234567", "٣"} {
		if _, ok := ParseInviteQty(in); ok {
			t.Errorf("ParseInviteQty(%q) must fail", in)
		}
	}
}

// ─── who sees it, how it looks ────────────────────────────────────────────────

func TestInviteRoles(t *testing.T) {
	t.Parallel()
	for role, want := range map[string]bool{"org_admin": true, "organizer": true, "agent": false} {
		if got := canInvite(resendIdentity(role, false)); got != want {
			t.Errorf("canInvite(%s) = %v, want %v", role, got, want)
		}
	}
	if !canInvite(resendIdentity("agent", true)) {
		t.Error("the platform operator may invite")
	}
	if canInvite(&Identity{}) || canInvite(nil) {
		t.Error("nobody without an organization may invite")
	}
}

func TestInviteRowLabelsAndGuest(t *testing.T) {
	t.Parallel()
	raw := `{"id":"` + uuid.NewString() + `","org_id":"` + uuid.NewString() + `","session_id":"` + uuid.NewString() + `","tier_id":null,
	"qty":1,"recipients":["anna@example.com"],"batch_id":"b","status":"issued","state":"valid","ticket_count":1,
	"event_name":"Swan Lake","session_start_at":"2099-10-15T18:00:00Z","venue_timezone":"Europe/Madrid","tier_name":"VIP",
	"tickets":[{"id":"` + uuid.NewString() + `","system_ticket_id":4711,"holder_email":"anna@example.com","holder_name":"Anna Novak","status":"active","used":false}],
	"created_at":"2099-01-01T00:00:00Z","updated_at":"2099-01-01T00:00:00Z"}`
	var it openapi.ComplimentaryListItem
	if err := json.Unmarshal([]byte(raw), &it); err != nil {
		t.Fatal(err)
	}
	if got := inviteGuest(it); got != "Anna Novak" {
		t.Errorf("guest = %q", got)
	}
	label := inviteRowLabel(it)
	for _, w := range []string{"✔", "Anna Novak", "15.10 20:00", "VIP"} {
		if !strings.Contains(label, w) {
			t.Errorf("label %q lacks %q", label, w)
		}
	}
	it.State = invStateRevoked
	if !strings.HasPrefix(inviteRowLabel(it), "✕") {
		t.Error("an annulled row carries ✕")
	}
	it.State = invStateUsed
	if !strings.HasPrefix(inviteRowLabel(it), "●") {
		t.Error("a used row carries ●")
	}
	// no name: the e-mail; several tickets: ×N
	name := ""
	it.Tickets[0].HolderName = &name
	it.TicketCount = 3
	if got := inviteGuest(it); got != "anna@example.com ×3" {
		t.Errorf("guest = %q", got)
	}
	if len([]rune(inviteRowLabel(it))) > 64 {
		t.Errorf("label is too long for a button: %q", inviteRowLabel(it))
	}
}

// ─── texts ────────────────────────────────────────────────────────────────────

func TestInviteTexts_RenderInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	vars := map[string]any{
		"Org": "Org", "Max": 50, "Page": 1, "Pages": 2, "Name": "Swan Lake", "When": "15.10.2026 20:00", "Tier": "VIP", "N": 4,
		"Left": 2, "Got": 2, "Sent": 3, "List": "1. Anna", "Line": 3, "Text": "x", "Reason": "r", "Done": 1, "Why": "w",
		"Total": 7, "Legend": "l", "Event": "e", "Guests": "g", "Guest": "Anna", "State": "s", "Issued": "d", "Word": "ANNUL",
	}
	for _, loc := range SupportedLocales {
		for _, key := range inviteKeys {
			got := b.texts.T(loc, key, vars)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s %s rendered %q", loc, key, got)
			}
		}
		// The numbers the screens promise are in the words.
		for key, need := range map[string]string{
			"bot.inv.hub": "50", "bot.inv.qty_ask": "50", "bot.inv.qty_over_max": "50", "bot.inv.qty_over_free": "4",
			"bot.inv.rcpt_ask": "4", "bot.inv.rcpt_too_many": "2", "bot.inv.confirm": "4", "bot.inv.done": "4",
			"bot.inv.partial": "4", "bot.inv.revoke_ask": "ANNUL", "bot.inv.revoke_wrong": "ANNUL", "bot.inv.bad_line": "3",
		} {
			if got := b.texts.T(loc, key, vars); !strings.Contains(got, need) {
				t.Errorf("%s %s lacks %q: %q", loc, key, need, got)
			}
		}
		if loc != "en" {
			for _, key := range []string{"bot.inv.hub", "bot.inv.confirm", "bot.inv.revoke_ask", "bot.inv.revoke_word", "bot.inv.rcpt_ask"} {
				if b.texts.T(loc, key, vars) == b.texts.T("en", key, vars) {
					t.Errorf("%s %s is a copy of English", loc, key)
				}
			}
		}
		// The annul word is a real, different word per language and never empty.
		if w := b.invAnnulWord(loc); w == "" || w == "bot.inv.revoke_word" {
			t.Errorf("%s annul word = %q", loc, w)
		}
	}
}

func TestInviteAnnulWord(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for loc, word := range map[string]string{"en": "ANNUL", "ru": "АННУЛИРОВАТЬ", "es": "ANULAR"} {
		if !b.invIsAnnulWord(loc, word) || !b.invIsAnnulWord(loc, strings.ToLower(word)) || !b.invIsAnnulWord(loc, " «"+word+"». ") {
			t.Errorf("%s: %q must be accepted", loc, word)
		}
		if !b.invIsAnnulWord(loc, "annul") {
			t.Errorf("%s: the English word is accepted everywhere", loc)
		}
		for _, no := range []string{"", "yes", "ok", "cancel", "annulment please", "да"} {
			if b.invIsAnnulWord(loc, no) {
				t.Errorf("%s: %q must not annul", loc, no)
			}
		}
	}
}

// Presses stay inside Telegram's 64-byte callback_data.
func TestInviteCallbacks_FitTelegram(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		"iv:new", "iv:i", "iv:l", "iv:go", "iv:bk", "iv:rv", "iv:e:" + uuid.NewString(),
		"iv:ep:99", "iv:eo:4", "iv:sp:99", "iv:so:4", "iv:to:7", "iv:q:50", "iv:lp:99", "iv:lo:4",
	} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
	}
}

// The batch id of each call is fixed by the operation and the guest's place in
// it, so a retry replays the same ids.
func TestInviteOutcome(t *testing.T) {
	t.Parallel()
	st := inviteDialog{
		Rcpt: []InviteRecipient{{"A", "a@x.org"}, {"B", "b@x.org"}, {"C", "c@x.org"}},
		Done: []int64{101, 0, 103},
	}
	issued, left := inviteOutcome(st)
	if len(issued) != 2 || issued[0].Num != 101 || issued[1].Num != 103 || len(left) != 1 || left[0].Email != "b@x.org" {
		t.Fatalf("issued %+v left %+v", issued, left)
	}
	// A Done shorter than Rcpt (a dialog saved before any call) leaves everyone.
	_, left = inviteOutcome(inviteDialog{Rcpt: st.Rcpt})
	if len(left) != 3 {
		t.Fatalf("left %+v", left)
	}
}

func TestInviteIssuedLinesCapAndEscape(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	var many []issuedGuest
	for i := 0; i < 30; i++ {
		many = append(many, issuedGuest{Num: int64(1000 + i), Guest: InviteRecipient{Name: "<b>Name</b>", Email: "x@example.com"}})
	}
	out := b.issuedLines(many)
	if strings.Contains(out, "<b>Name") || !strings.Contains(out, "&lt;b&gt;") {
		t.Errorf("names must be escaped: %q", out[:80])
	}
	if strings.Count(out, "\n") > 26 || !strings.HasSuffix(out, "…") {
		t.Errorf("a long list is cut with an ellipsis, got %d lines", strings.Count(out, "\n")+1)
	}
}
