package eventbot

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func archivedTestTexts(t *testing.T) *Texts {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	return NewTexts(bundle)
}

func TestArenaClient_MeInfoReadsArchivedFlag(t *testing.T) {
	live, gone := uuid.New(), uuid.New()
	c := newStubArena(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"id":"u"},"roles":["organizer"],"organization_memberships":[
			{"org_id":"` + live.String() + `","org_name":"Live","role":"org_admin","status":"active","archived":false},
			{"org_id":"` + gone.String() + `","org_name":"Gone","role":"org_admin","status":"active","archived":true}]}`))
	})
	me, err := c.MeInfo(context.Background(), "jwt")
	if err != nil {
		t.Fatalf("MeInfo: %v", err)
	}
	if len(me.Memberships) != 2 || me.Memberships[0].Archived || !me.Memberships[1].Archived {
		t.Fatalf("archived flag not read: %+v", me.Memberships)
	}
}

func TestBuildIdentity_ArchivedOrganizationIsNeverCurrent(t *testing.T) {
	live, gone := uuid.New(), uuid.New()
	liveM := Membership{OrgID: live, OrgName: "Live", Role: membershipRoleOwner}
	goneM := Membership{OrgID: gone, OrgName: "Gone", Role: membershipRoleOwner, Archived: true}

	// The remembered organization was archived: it falls back to the only
	// working one, which is remembered from now on.
	link := gen.BotTelegramLinkRow{CurrentOrgID: &gone}
	id, remember := buildIdentity(link, []Membership{goneM, liveM}, false)
	if id.Current == nil || id.Current.OrgID != live {
		t.Fatalf("current should fall back to the working organization, got %+v", id.Current)
	}
	if remember == nil || *remember != live {
		t.Fatalf("the working organization should be remembered, got %v", remember)
	}
	if len(id.Memberships) != 1 || len(id.Archived) != 1 || id.Archived[0].OrgID != gone {
		t.Fatalf("split wrong: memberships=%+v archived=%+v", id.Memberships, id.Archived)
	}

	// A remembered, still-working organization stays current, nothing to store.
	link = gen.BotTelegramLinkRow{CurrentOrgID: &live}
	id, remember = buildIdentity(link, []Membership{goneM, liveM}, false)
	if id.Current == nil || id.Current.OrgID != live || remember != nil {
		t.Fatalf("remembered working org should stay current: %+v %v", id.Current, remember)
	}

	// Only archived organizations: nothing is current, nothing to remember.
	link = gen.BotTelegramLinkRow{CurrentOrgID: &gone}
	id, remember = buildIdentity(link, []Membership{goneM}, false)
	if id.Current != nil || remember != nil || len(id.Memberships) != 0 {
		t.Fatalf("an all-archived person has no current organization: %+v %v", id.Current, remember)
	}

	// Several working organizations and a stale current one: the caller asks.
	other := Membership{OrgID: uuid.New(), OrgName: "Other", Role: membershipRoleManager}
	link = gen.BotTelegramLinkRow{CurrentOrgID: &gone}
	id, remember = buildIdentity(link, []Membership{goneM, liveM, other}, false)
	if id.Current != nil || remember != nil {
		t.Fatalf("with two working organizations the person chooses: %+v", id.Current)
	}
}

func TestOrgChooserView_ArchivedOrganizationIsNotAButton(t *testing.T) {
	b := &Bot{texts: archivedTestTexts(t)}
	live, gone, other := uuid.New(), uuid.New(), uuid.New()
	for _, loc := range SupportedLocales {
		link := gen.BotTelegramLinkRow{Locale: loc}
		id, _ := buildIdentity(link, []Membership{
			{OrgID: gone, OrgName: "Arena <Test> Promotions", Role: membershipRoleOwner, Archived: true},
			{OrgID: live, OrgName: "Live", Role: membershipRoleOwner},
			{OrgID: other, OrgName: "Other", Role: membershipRoleManager},
		}, false)
		text, kb := b.orgChooserView(id)
		if kb == nil || len(kb.InlineKeyboard) != 2 {
			t.Fatalf("%s: expected one button per working organization, got %+v", loc, kb)
		}
		for _, row := range kb.InlineKeyboard {
			if strings.Contains(row[0].CallbackData, gone.String()) || strings.Contains(row[0].Text, "Arena") {
				t.Fatalf("%s: archived organization offered as a button: %+v", loc, row[0])
			}
		}
		if !strings.Contains(text, "Arena &lt;Test&gt; Promotions") {
			t.Fatalf("%s: the archived organization should be named (escaped) in the text, got %q", loc, text)
		}
		if got := b.archivedNote(id); got == "" || !strings.Contains(text, strings.TrimSpace(got)) {
			t.Fatalf("%s: chooser should carry the archived note, got %q", loc, text)
		}
	}
}

func TestOrgChooserView_EveryOrganizationArchived(t *testing.T) {
	b := &Bot{texts: archivedTestTexts(t)}
	id, _ := buildIdentity(gen.BotTelegramLinkRow{Locale: "ru"}, []Membership{
		{OrgID: uuid.New(), OrgName: "Arena Test Promotions", Role: membershipRoleOwner, Archived: true},
	}, false)
	text, kb := b.orgChooserView(id)
	if kb != nil {
		t.Fatalf("an all-archived person gets no buttons, got %+v", kb)
	}
	if !strings.Contains(text, "Arena Test Promotions") || !strings.Contains(text, "в архиве") {
		t.Fatalf("ru text should name the organization and say it is archived: %q", text)
	}
	none, _ := b.orgChooserView(&Identity{Link: gen.BotTelegramLinkRow{Locale: "ru"}})
	if none == text {
		t.Fatalf("no organizations at all and all archived must read differently")
	}
}

func TestArchivedNote_EmptyWithoutArchivedOrganizations(t *testing.T) {
	b := &Bot{texts: archivedTestTexts(t)}
	if got := b.archivedNote(&Identity{}); got != "" {
		t.Fatalf("no archived organizations, no note, got %q", got)
	}
	if got := b.archivedNote(nil); got != "" {
		t.Fatalf("nil identity, no note, got %q", got)
	}
}
