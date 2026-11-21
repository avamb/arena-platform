package eventbot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// menuStub answers the few arena-api routes the menu reads.
type menuStub struct {
	steps    string // body of getting-started; "" answers 500
	disputes string // body of the disputes list; "" answers 500
	team     string // body of the team; "" answers an empty team
}

func (s menuStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(body string) {
			if body == "" {
				http.Error(w, `{"error":{"code":"internal"}}`, http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getting-started"):
			write(s.steps)
		case strings.HasSuffix(r.URL.Path, "/disputes"):
			write(s.disputes)
		case strings.HasSuffix(r.URL.Path, "/bot-team"):
			if s.team == "" {
				write(`{"members":[]}`)
				return
			}
			write(s.team)
		default:
			http.NotFound(w, r)
		}
	})
}

func menuTestBot(t *testing.T, s menuStub) *Bot {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return &Bot{
		texts:  NewTexts(bundle),
		arena:  NewArenaClient(srv.URL, "", nil),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func menuIdentity(role string, super bool, orgs int) *Identity {
	m := Membership{OrgID: uuid.New(), OrgName: "Org", Role: role}
	id := &Identity{Current: &m, Superadmin: super, Memberships: []Membership{m}}
	for i := 1; i < orgs; i++ {
		id.Memberships = append(id.Memberships, Membership{OrgID: uuid.New(), OrgName: "Other", Role: role})
	}
	return id
}

func rowData(rows [][]models.InlineKeyboardButton) []string {
	var out []string
	for _, r := range rows {
		for _, b := range r {
			d := b.CallbackData
			if d == "" {
				d = "url"
			}
			out = append(out, d)
		}
	}
	return out
}

func sameList(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestHomeKeyboard_RowsByRole(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	cases := []struct {
		name string
		id   *Identity
		want []string
	}{
		{"owner", menuIdentity("org_admin", false, 1), []string{"wz:new", "el:new", "mn:ord", "mn:pro", "mn:door", "mn:set", "web:open", "help"}},
		{"owner of two organizations", menuIdentity("org_admin", false, 2), []string{"wz:new", "el:new", "mn:ord", "mn:pro", "mn:door", "mn:set", "web:open", "help"}},
		{"manager", menuIdentity("organizer", false, 1), []string{"wz:new", "el:new", "mn:ord", "mn:pro", "mn:door", "mn:set", "web:open", "help"}},
		{"superadmin", menuIdentity("org_admin", true, 1), []string{"wz:new", "el:new", "mn:ord", "mn:pro", "mn:door", "mn:set", "help"}},
		{"agent", menuIdentity("agent", false, 1), []string{"wz:new", "el:new", "mn:door", "mn:set", "web:open", "help"}},
	}
	for _, c := range cases {
		got := rowData(b.homeKeyboard(context.Background(), c.id, "").InlineKeyboard)
		if !sameList(got, c.want) {
			t.Errorf("%s: home = %v, want %v", c.name, got, c.want)
		}
		for _, row := range b.homeKeyboard(context.Background(), c.id, "").InlineKeyboard {
			if len(row) != 1 {
				t.Errorf("%s: a home row has %d buttons", c.name, len(row))
			}
		}
	}
}

func TestSections_RowsByRole(t *testing.T) {
	t.Parallel()
	b := menuTestBot(t, menuStub{})
	ctx := context.Background()
	owner := menuIdentity("org_admin", false, 2)
	manager := menuIdentity("organizer", false, 1)
	super := menuIdentity("agent", true, 1) // the operator is no member of the organization

	check := func(name string, got [][]models.InlineKeyboardButton, want []string) {
		t.Helper()
		if d := rowData(got); !sameList(d, want) {
			t.Errorf("%s = %v, want %v", name, d, want)
		}
	}
	// Promotion
	check("owner promo", b.promoSectionRows("en", owner), []string{"pm:l", "iv:new", "ql:menu", "mn:mail"})
	check("super promo", b.promoSectionRows("en", super), []string{"pm:l", "iv:new", "ql:menu"}) // a superadmin gets no web link, so Messages to buyers is not offered
	check("agent promo", b.promoSectionRows("en", menuIdentity("agent", false, 1)), nil)
	// Entrance
	check("owner door", b.doorSectionRows("en", owner), []string{"scanner", "mn:gl"})
	check("agent door", b.doorSectionRows("en", menuIdentity("agent", false, 1)), []string{"scanner"})
	// Settings: the owner is alone in the stub team, so Team is the invite.
	check("owner settings", b.settingsSectionRows(ctx, "en", owner, "jwt"),
		[]string{"og:h", "mn:pay", "team:invite", "pr:l", "mn:ven", "ec:nt", "lang", "org"})
	check("manager settings", b.settingsSectionRows(ctx, "en", manager, "jwt"),
		[]string{"team", "pr:l", "mn:ven", "ec:nt", "lang"})
	check("super settings", b.settingsSectionRows(ctx, "en", super, "jwt"),
		[]string{"og:h", "mn:pay", "pr:l", "ec:nt", "lang"}) // no Venues for a superadmin
	// Orders: no dispute call without a token, so the plain button stays.
	check("owner orders", b.ordersSectionRows(ctx, "en", owner, ""), []string{"or:new", "rq:l:" + queueDefaultFilter + ":1", "dp:l:open:1"})
}

func TestSettings_TeamRowFollowsTheTeam(t *testing.T) {
	t.Parallel()
	id := menuIdentity("org_admin", false, 1)
	id.Link.UserID = uuid.New()
	alone := menuTestBot(t, menuStub{team: `{"members":[{"user_id":"` + id.Link.UserID.String() + `"}]}`})
	if d := rowData(alone.settingsSectionRows(context.Background(), "en", id, "jwt")); d[2] != "team:invite" {
		t.Errorf("owner alone: %v", d)
	}
	crowd := menuTestBot(t, menuStub{team: `{"members":[{"user_id":"` + id.Link.UserID.String() + `"},{"user_id":"` + uuid.NewString() + `"}]}`})
	if d := rowData(crowd.settingsSectionRows(context.Background(), "en", id, "jwt")); d[2] != "team" {
		t.Errorf("owner with a team: %v", d)
	}
}

func TestFirstSteps_BlockAndButtons(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner := menuIdentity("org_admin", false, 1)
	steps := func(done ...bool) string {
		keys := []string{"org_data", "payments", "event", "sales_link"}
		var parts []string
		for i, d := range done {
			v := "false"
			if d {
				v = "true"
			}
			parts = append(parts, `{"key":"`+keys[i]+`","done":`+v+`}`)
		}
		return `{"getting_started":{"complete":false,"steps":[` + strings.Join(parts, ",") + `]}}`
	}
	run := func(_ string, s menuStub, id *Identity) (string, []string) {
		b := menuTestBot(t, s)
		gs := b.firstSteps(ctx, id, "jwt")
		return b.firstStepsText("en", gs), rowData(b.homeKeyboardFor(id, gs).InlineKeyboard)
	}

	text, rows := run("four open", menuStub{steps: steps(false, false, false, false)}, owner)
	if !strings.Contains(text, "0 of 4 done") || strings.Count(text, "▫️") != 4 || strings.Contains(text, "✅") {
		t.Errorf("four open: text %q", text)
	}
	if !sameList(rows[:4], []string{"og:h", "mn:pay", "wz:new", "ql:menu"}) || len(rows) != 12 {
		t.Errorf("four open: rows %v", rows)
	}

	text, rows = run("partly", menuStub{steps: steps(true, false, true, false)}, owner)
	if !strings.Contains(text, "2 of 4 done") || strings.Count(text, "✅") != 2 {
		t.Errorf("partly: text %q", text)
	}
	if !sameList(rows[:2], []string{"mn:pay", "ql:menu"}) || len(rows) != 10 {
		t.Errorf("partly: rows %v", rows)
	}

	complete := `{"getting_started":{"complete":true,"steps":[{"key":"org_data","done":true}]}}`
	for name, s := range map[string]menuStub{"complete": {steps: complete}, "error": {}, "empty": {steps: `{"getting_started":{"complete":false,"steps":[]}}`}} {
		text, rows := run(name, s, owner)
		if text != "" || len(rows) != 8 || rows[0] != "wz:new" {
			t.Errorf("%s: text %q rows %v", name, text, rows)
		}
	}

	manager := menuIdentity("organizer", false, 1)
	text, rows = run("manager", menuStub{steps: `{"getting_started":{"complete":false,"steps":[{"key":"event","done":false},{"key":"sales_link","done":false}]}}`}, manager)
	if !strings.Contains(text, "0 of 2 done") || !sameList(rows[:2], []string{"wz:new", "ql:menu"}) {
		t.Errorf("manager: text %q rows %v", text, rows)
	}

	super := menuIdentity("org_admin", true, 1)
	if text, rows := run("superadmin", menuStub{steps: steps(false, false, false, false)}, super); text != "" || rows[0] != "wz:new" {
		t.Errorf("superadmin: text %q rows %v", text, rows)
	}
}

func TestDisputeButton_ByWhatTheOrganizationHas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	id := menuIdentity("org_admin", false, 1)
	cases := []struct {
		name     string
		disputes string
		hidden   bool
		counter  string
	}{
		{"none", `{"disputes":[],"total":0,"open_count":0}`, true, ""},
		{"some, none waiting", `{"disputes":[],"total":3,"open_count":0}`, false, "0"},
		{"some waiting", `{"disputes":[],"total":3,"open_count":2}`, false, "2"},
		{"error", ``, false, ""},
	}
	for _, c := range cases {
		b := menuTestBot(t, menuStub{disputes: c.disputes})
		row := b.disputeSectionRow(ctx, "en", id, "jwt")
		if c.hidden {
			if row != nil {
				t.Errorf("%s: the button must be hidden, got %+v", c.name, row)
			}
			continue
		}
		if row == nil || row[0].CallbackData != "dp:l:open:1" {
			t.Fatalf("%s: row %+v", c.name, row)
		}
		wantText := "⚠️ Payment disputes"
		if c.counter != "" {
			wantText += ": " + c.counter
		}
		if row[0].Text != wantText {
			t.Errorf("%s: text %q, want %q", c.name, row[0].Text, wantText)
		}
	}
}

// Every callback of the old flat menu is on the home screen or one press away.
func TestHome_EveryOldFunctionWithinTwoPresses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := menuTestBot(t, menuStub{disputes: `{"disputes":[],"total":1,"open_count":1}`})
	id := shopIdentityOf("org_admin", false, []string{"shop"}, "full")
	id.Memberships = []Membership{*id.Current, {OrgID: uuid.New(), OrgName: "Other", Role: "org_admin"}}

	reach := map[string]bool{}
	home := b.homeKeyboard(ctx, id, "").InlineKeyboard
	for _, d := range rowData(home) {
		reach[d] = true
	}
	level2 := map[string][][]models.InlineKeyboardButton{
		"mn:ord":  b.ordersSectionRows(ctx, "en", id, "jwt"),
		"mn:pro":  b.promoSectionRows("en", id),
		"mn:door": b.doorSectionRows("en", id),
		"mn:set":  b.settingsSectionRows(ctx, "en", id, "jwt"),
	}
	for from, rows := range level2 {
		if !reach[from] {
			t.Errorf("home has no %s", from)
		}
		for _, d := range rowData(rows) {
			reach[d] = true
		}
	}
	// The screens of the third level that only carry one more press.
	_, glRows := b.guestListSection(ctx, 1, "en", id, "")
	for _, d := range rowData(glRows) {
		reach[d] = true
	}
	for _, old := range []string{
		"wz:new", "el:new", "or:new", "sh:home", "rq:l:" + queueDefaultFilter + ":1", "dp:l:open:1", "iv:new", "pm:l", "pr:l",
		"ql:menu", "og:h", "org", "web:open", "ec:nt", "scanner", "lang", "help",
	} {
		if !reach[old] {
			t.Errorf("the old button %q is not reachable in two presses", old)
		}
	}
	// The team: the invite dialog for an owner who is alone, the screen for
	// an owner with a team and for the manager.
	aid := menuIdentity("org_admin", false, 1)
	aid.Link.UserID = uuid.New()
	alone := menuTestBot(t, menuStub{})
	if d := rowData(alone.settingsSectionRows(ctx, "en", aid, "jwt")); !strings.Contains(strings.Join(d, " "), "team:invite") {
		t.Errorf("team:invite is not reachable: %v", d)
	}
	crowd := menuTestBot(t, menuStub{team: `{"members":[{"user_id":"` + uuid.NewString() + `"}]}`})
	if d := rowData(crowd.settingsSectionRows(ctx, "en", aid, "jwt")); !strings.Contains(strings.Join(d, " "), " team ") {
		t.Errorf("team is not reachable: %v", d)
	}
}

// Every section screen has a title, only buttons with texts, and the way home.
func TestSections_EveryButtonHasATextInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := menuTestBot(t, menuStub{disputes: `{"disputes":[],"total":1,"open_count":2}`, steps: `{"getting_started":{"complete":false,"steps":[{"key":"org_data","done":false},{"key":"payments","done":true},{"key":"event","done":false},{"key":"sales_link","done":false}]}}`})
	ctx := context.Background()
	id := shopIdentityOf("org_admin", false, []string{"shop"}, "full")
	id.Memberships = []Membership{*id.Current, {OrgID: uuid.New(), Role: "org_admin"}}
	for _, loc := range SupportedLocales {
		id.Link.Locale = loc
		gs := b.firstSteps(ctx, id, "jwt")
		all := b.homeKeyboardFor(id, gs).InlineKeyboard
		all = append(all, b.ordersSectionRows(ctx, loc, id, "jwt")...)
		all = append(all, b.promoSectionRows(loc, id)...)
		all = append(all, b.doorSectionRows(loc, id)...)
		all = append(all, b.settingsSectionRows(ctx, loc, id, "jwt")...)
		for _, r := range all {
			for _, btn := range r {
				if btn.Text == "" || strings.HasPrefix(btn.Text, "bot.") {
					t.Errorf("%s: button %q has no text (%q)", loc, btn.CallbackData, btn.Text)
				}
			}
		}
		for _, key := range menuSectionKeys {
			if text := b.texts.T(loc, key, map[string]any{"Count": 1, "Done": 1, "Total": 2}); text == "" || text == key {
				t.Errorf("%s: %s has no text", loc, key)
			}
		}
	}
}
