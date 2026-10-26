package eventbot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// UXH-05: a sales page that is "not shown yet" must never be followed by "the
// seller data is complete": the page's own missing field (its public e-mail)
// is named on its line and in the list below.
func TestOrgCard_IncompleteChannelIsNeverCalledComplete(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	incomplete := openapi.SellerProfileChannel{Id: uuid.New(), Name: "lampyris.example", Missing: []string{"public_email"}}
	shown := openapi.SellerProfileChannel{Id: uuid.New(), Name: "shown.example", SellerShown: true}
	hidden := openapi.SellerProfileChannel{Id: uuid.New(), Name: "hidden.example", SellerHidden: true}

	for _, loc := range SupportedLocales {
		complete := b.texts.T(loc, "bot.og.status_complete", nil)
		email := b.texts.T(loc, "bot.og.m_public_email", nil)

		text := b.orgCardText(loc, openapi.SellerProfile{OrgName: "Org", Complete: true, Channels: []openapi.SellerProfileChannel{incomplete}})
		if strings.Contains(text, complete) {
			t.Errorf("%s: an incomplete sales page is followed by %q:\n%s", loc, complete, text)
		}
		if strings.Count(text, email) < 2 {
			t.Errorf("%s: the missing public e-mail must be named on the page line and in the list (%q):\n%s", loc, email, text)
		}
		if strings.Contains(text, "bot.og.") {
			t.Errorf("%s: raw key in %q", loc, text)
		}

		ok := b.orgCardText(loc, openapi.SellerProfile{OrgName: "Org", Complete: true, Channels: []openapi.SellerProfileChannel{shown, hidden}})
		if !strings.Contains(ok, complete) {
			t.Errorf("%s: a complete organization with shown/hidden pages must say it is complete:\n%s", loc, ok)
		}
	}
}

func TestOrgAllMissing_UnionWithoutDuplicates(t *testing.T) {
	t.Parallel()
	p := openapi.SellerProfile{
		Missing: []string{"legal_name", "public_email"},
		Channels: []openapi.SellerProfileChannel{
			{Missing: []string{"legal_name", "public_email"}},
			{Missing: []string{"tax_id"}, SellerHidden: true},
			{Missing: []string{"address_city"}},
		},
	}
	got := strings.Join(orgAllMissing(p), ",")
	if want := "legal_name,public_email,address_city"; got != want {
		t.Errorf("orgAllMissing = %q, want %q", got, want)
	}
}

// UXH-13: the list of promoters offers "+ Promoter", empty or not.
func TestPromoterListRows_AlwaysOfferAdd(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	for _, loc := range SupportedLocales {
		for _, items := range [][]openapi.Promoter{nil, {promoterFixture()}} {
			found := false
			for _, row := range b.promoterListRows(loc, items, 1, 1) {
				for _, btn := range row {
					if btn.CallbackData == "pr:n" {
						found = btn.Text != "" && !strings.HasPrefix(btn.Text, "bot.")
					}
				}
			}
			if !found {
				t.Errorf("%s: no \"+ Promoter\" button with %d promoters", loc, len(items))
			}
		}
		if empty := b.texts.T(loc, "bot.prom.empty", nil); !strings.Contains(empty, b.texts.T(loc, "bot.prom.add_btn", nil)) {
			t.Errorf("%s: the empty list does not point at the button: %q", loc, empty)
		}
	}
}

// UXH-12: a draft without a name has its own text, never «…».
func TestResumeWithoutName_HasItsOwnText(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	for _, loc := range SupportedLocales {
		got := b.texts.T(loc, "bot.wz.resume_noname", map[string]any{"Name": ""})
		if got == "" || strings.Contains(got, "…") || strings.HasPrefix(got, "bot.") {
			t.Errorf("%s: resume_noname = %q", loc, got)
		}
	}
}

// UXH-15: the "over 5 MB" refusal says how to get under the limit.
func TestPosterTooBig_SaysHow(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	for _, loc := range SupportedLocales {
		got := b.texts.T(loc, "bot.wz.poster_too_big", nil)
		if !strings.Contains(got, "JPEG") {
			t.Errorf("%s: %q does not say how to shrink the file", loc, got)
		}
	}
}

// dupVenueRefs answers every venue creation with the API's duplicate refusal.
type dupVenueRefs struct{ *fakeRefs }

func (dupVenueRefs) CreateVenue(context.Context, string, uuid.UUID, VenueCreate) (VenueRef, error) {
	return VenueRef{}, &APIError{Status: 409, Code: "venue.duplicate", Message: "a venue with that name already exists in this organization"}
}

// UXH-14: a refused duplicate venue offers the existing one as a button, and
// pressing it carries on with that venue.
func TestWizard_DuplicateVenueOffersTheExistingOne(t *testing.T) {
	r := newWizardRun(t, dupVenueRefs{newFakeRefs()})
	r.eventHead()
	r.text("20.03.2027", stSTime)
	r.text("21:00", stSCountry)
	r.press("country:cz", stSCity)
	r.press("city:prg", stSVenue)
	r.press("venue:new", stVName)
	r.text("palác akropolis", stVAddress)
	r.press("skip", stVCapacity)
	note := r.text("300", stSVenue)
	if !strings.Contains(note, "palác akropolis") {
		t.Errorf("the note does not name the venue: %q", note)
	}
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if len(screen.Buttons) == 0 || screen.Buttons[0][0].Data != "venue:akr" {
		t.Fatalf("the first button must pick the existing venue: %+v", screen.Buttons)
	}
	r.press("venue:akr", stSCapacity)
	if r.d.Scratch.DupVenueID != "" {
		t.Errorf("the duplicate hint must be dropped once the venue is chosen")
	}
	if s := r.d.Sessions[0]; s.VenueID != "akr" {
		t.Errorf("session venue = %q", s.VenueID)
	}
}
