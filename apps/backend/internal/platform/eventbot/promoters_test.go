package eventbot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestParsePromoterField(t *testing.T) {
	t.Parallel()
	field := func(code string) promoterField {
		f, ok := promoterFieldByCode(code)
		if !ok {
			t.Fatalf("no field %q", code)
		}
		return f
	}
	cases := []struct {
		code, in, want, errKey string
	}{
		{"n", "  Partner   Agency  s.r.o. ", "Partner Agency s.r.o.", ""},
		{"n", "   ", "", "bot.prom.err_name_empty"},
		{"n", strings.Repeat("я", 121), "", "bot.prom.err_name_long"},
		{"n", strings.Repeat("я", 120), strings.Repeat("я", 120), ""},
		{"a", "Dlouhá 12,\n110 00 Praha", "Dlouhá 12, 110 00 Praha", ""},
		{"a", strings.Repeat("x", 301), "", "bot.prom.err_address_long"},
		{"t", " CZ12345678 ", "CZ12345678", ""},
		{"t", "12\n34", "", "bot.prom.err_tax"},
		{"t", strings.Repeat("1", 41), "", "bot.prom.err_tax"},
		{"p", "+420 123 456 789", "+420 123 456 789", ""},
		{"p", "(34) 600-111-222", "(34) 600-111-222", ""},
		{"p", "12345", "", "bot.prom.err_phone"},
		{"p", "call me", "", "bot.prom.err_phone"},
		{"p", "1234567890123456", "", "bot.prom.err_phone"},
		{"m", " Office@Partner.Example ", "office@partner.example", ""},
		{"m", "not an email", "", "bot.prom.err_email"},
		{"w", "partner.example", "partner.example", ""},
		{"w", "https://partner.example/a", "https://partner.example/a", ""},
		{"w", "partner example", "", "bot.prom.err_website"},
		{"w", "partner", "", "bot.prom.err_website"},
		{"s", " Teatr-Kolibel ", "teatr-kolibel", ""},
		{"s", "https://tickets.arenasoldout.com/Teatr-Kolibel/", "teatr-kolibel", ""},
		{"s", " / ", "", "bot.prom.err_slug_invalid"},
		{"s", "a", "", "bot.prom.err_slug_invalid"},
		{"s", "-ab", "", "bot.prom.err_slug_invalid"},
		{"s", "a--b", "", "bot.prom.err_slug_invalid"},
		{"s", "teatr_kolibel", "", "bot.prom.err_slug_invalid"},
	}
	for _, c := range cases {
		got, errKey := parsePromoterField(field(c.code), c.in)
		if got != c.want || errKey != c.errKey {
			t.Errorf("field %s %q = (%q, %q), want (%q, %q)", c.code, c.in, got, errKey, c.want, c.errKey)
		}
	}
}

func TestPromoterAPIErrKey(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]string{
		"promoter.duplicate_name":  "bot.prom.err_name_taken",
		"promoter.duplicate_slug":  "bot.prom.err_slug_taken",
		"promoter.invalid_slug":    "bot.prom.err_slug_invalid",
		"promoter.invalid_name":    "bot.prom.err_name_empty",
		"promoter.invalid_website": "bot.prom.err_website",
		"promoter.invalid_address": "bot.prom.err_address_long",
		"promoter.not_found":       "",
	} {
		if got := promoterAPIErrKey(&APIError{Status: 409, Code: code}); got != want {
			t.Errorf("%s -> %q, want %q", code, got, want)
		}
	}
	if promoterAPIErrKey(errors.New("boom")) != "" {
		t.Error("a transport error has no words of its own")
	}
}

func promoterFixture() openapi.Promoter {
	return openapi.Promoter{
		Id: uuid.New(), OrgId: uuid.New(), Name: "Partner <Agency>",
		LegalId: strp("12345678"), Phone: strp("+420 123 456 789"), Email: strp("office@partner.example"),
		Slug: strp("partner-agency"), Address: strp("Dlouhá 12"), Website: strp("https://partner.example"),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

// The card and the question render in every language: no key name, no missing
// value, the name escaped, the page link complete.
func TestPromoterScreens_RenderInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	b.ticketsBaseURL = "https://tickets.example/"
	full := promoterFixture()
	bare := openapi.Promoter{Id: uuid.New(), Name: "Bare"}
	for _, loc := range SupportedLocales {
		text := b.promoterCardText(loc, full)
		for _, want := range []string{"Partner &lt;Agency&gt;", "12345678", "+420 123 456 789", "office@partner.example", "https://partner.example", "https://tickets.example/partner-agency", "Dlouhá 12"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: the card lacks %q:\n%s", loc, want, text)
			}
		}
		if strings.Contains(text, "bot.prom.") || strings.Contains(text, "<no value>") {
			t.Errorf("%s: card = %q", loc, text)
		}
		none := b.texts.T(loc, "bot.prom.none", nil)
		if got := strings.Count(b.promoterCardText(loc, bare), none); got != 6 {
			t.Errorf("%s: a bare promoter shows %q %d times, want 6:\n%s", loc, none, got, b.promoterCardText(loc, bare))
		}
		for _, f := range promoterFields {
			q := b.texts.T(loc, "bot.prom.q_"+f.Key, map[string]any{"URL": "https://tickets.example/"})
			if strings.TrimSpace(q) == "" || q == "bot.prom.q_"+f.Key || strings.Contains(q, "<no value>") {
				t.Errorf("%s: question %s = %q", loc, f.Key, q)
			}
		}
		for _, r := range b.promoterCardRows(loc, newPromoterDialog(uuid.New()), full) {
			for _, btn := range r {
				if len(btn.CallbackData) > 64 || btn.CallbackData == "" || btn.Text == "" || strings.HasPrefix(btn.Text, "bot.") {
					t.Errorf("%s: bad button %+v", loc, btn)
				}
			}
		}
	}
}

func TestPromoterCallbacks_FitTelegram(t *testing.T) {
	t.Parallel()
	pid := uuid.NewString()
	for _, data := range []string{
		"pr:l", "pr:b", "pr:p:12", "pr:o:4", "pr:noop", "pr:ev:" + pid, "pr:v:" + pid, "pr:e:n:" + pid, "pr:e:s:" + pid,
		"pr:x:w:" + pid, "pr:ok:" + pid, "pr:ar:" + pid, "pr:ay:" + pid,
	} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
	}
}

func TestPromoterMenuAndEventButtons_OnlyForRolesThatManagePromoters(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	ev := uuid.New()
	for role, want := range map[string]bool{"org_admin": true, "organizer": true, "agent": false, "": false} {
		id := &Identity{Current: &Membership{Role: role}}
		if got := b.promoterMenuRow("en", id) != nil; got != want {
			t.Errorf("menu button for %q = %v, want %v", role, got, want)
		}
		row := b.promoterEventRow("en", id, ev)
		if (row != nil) != want {
			t.Errorf("event button for %q = %v, want %v", role, row != nil, want)
		}
		if row != nil && row[0].CallbackData != "pr:ev:"+ev.String() {
			t.Errorf("event button data = %q", row[0].CallbackData)
		}
	}
	if b.promoterMenuRow("en", &Identity{Current: &Membership{Role: "agent"}, Superadmin: true}) == nil {
		t.Error("the platform operator manages promoters too")
	}
}

func TestPromoterEventLine(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	for _, loc := range SupportedLocales {
		with := b.promoterEventLine(loc, openapi.EventItem{PromoterName: strp("A&B")})
		if !strings.Contains(with, "A&amp;B") || strings.Contains(with, "bot.prom.") {
			t.Errorf("%s: %q", loc, with)
		}
		org := b.promoterEventLine(loc, openapi.EventItem{})
		if strings.TrimSpace(org) == "" || strings.Contains(org, "bot.prom.") {
			t.Errorf("%s: %q", loc, org)
		}
	}
}

func TestPromoterDialog_JSONRoundTripKeepsTheQuestion(t *testing.T) {
	t.Parallel()
	card, ev := uuid.New(), uuid.New()
	st := promoterDialog{OrgID: uuid.New(), MsgID: 7, Page: 2, IDs: []uuid.UUID{card}, CardID: &card, Field: "s", Pending: "new-slug", BackEvent: &ev}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back promoterDialog
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.CardID == nil || *back.CardID != card || back.Field != "s" || back.Pending != "new-slug" || back.BackEvent == nil || *back.BackEvent != ev || back.Page != 2 || back.MsgID != 7 {
		t.Errorf("round trip lost state: %+v", back)
	}
}

// Every bot.prom.* key is registered and present in every language.
func TestPromoterKeys_AreRegisteredAndTranslated(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	registered := map[string]bool{}
	for _, k := range MessageKeys {
		registered[k] = true
	}
	vars := map[string]any{"Org": "Org", "Total": 3, "Page": 1, "Pages": 2, "Name": "N", "Value": "v", "URL": "https://x.test/", "Old": "o", "New": "n"}
	// The same in every language by nature (names of fields written the same way).
	sameOK := map[string]bool{"bot.prom.card": true, "bot.prom.line_email": true, "bot.prom.btn_email": true, "bot.prom.q_email": true}
	for _, key := range promoterKeys {
		if !registered[key] {
			t.Errorf("%s is not in MessageKeys", key)
		}
		en := b.texts.T("en", key, vars)
		for _, loc := range SupportedLocales {
			got := b.texts.T(loc, key, vars)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s %s rendered %q", loc, key, got)
			}
			if loc != "en" && got == en && !sameOK[key] {
				t.Errorf("%s %s is a copy of English: %q", loc, key, got)
			}
		}
	}
}
