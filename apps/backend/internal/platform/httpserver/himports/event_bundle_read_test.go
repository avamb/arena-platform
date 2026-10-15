package himports

// event_bundle_read_test.go — the pure mapping of GET .../event-bundle
// (task 3-2): what is loaded is written in the shape the import reads.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

func ptr[T any](v T) *T { return &v }

// bundleFixtureSource is a published event with one general-admission
// session in Madrid: two categories, a price schedule and doors open.
func bundleFixtureSource(t *testing.T) (bundleSource, gen.SessionRow) {
	t.Helper()
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 4, 21, 30, 0, 0, madrid).UTC()
	venue := &gen.VenueRow{ID: uuid.New(), Name: "Sala Equis", Address: ptr("Calle Duque de Alba 4"), Timezone: ptr("Europe/Madrid")}
	session := gen.SessionRow{
		ID: uuid.New(), VenueID: venue.ID, StartAt: start, EndAt: start.Add(2 * time.Hour),
		Status: "scheduled", AdmissionMode: "general_admission", Currency: "EUR",
		SalesEndAt: start.Add(-time.Hour), DoorsOpenAt: ptr(start.Add(-30 * time.Minute)),
	}
	early := gen.TicketTierRow{ID: uuid.New(), Name: "Early", PricingMode: "fixed", PriceAmount: 1890,
		SaleWindowStart: ptr(start.Add(-48 * time.Hour)), SortOrder: 0}
	late := gen.TicketTierRow{ID: uuid.New(), Name: "Door", PricingMode: "fixed", PriceAmount: 2500, SortOrder: 1}
	src := bundleSource{
		Event: gen.EventRow{ID: uuid.New(), Name: "Noche de tango", Description: ptr("A night"),
			AgeRating: ptr("12+"), Status: "published", PosterMediaID: ptr(uuid.New())},
		ChannelIDs: []uuid.UUID{uuid.New()},
		Sessions: []bundleSessionSource{{
			Session: session, Venue: venue, Tiers: []gen.TicketTierRow{early, late},
			Stats: map[uuid.UUID]gaquota.Stats{early.ID: {Quantity: 60}, late.ID: {Quantity: 40}},
		}},
		Windows: map[uuid.UUID][]gen.TicketTierPriceRow{
			early.ID: {{TierID: early.ID, ValidFrom: start.Add(-48 * time.Hour), ValidTo: ptr(start.Add(-24 * time.Hour)), PriceAmount: 1500},
				{TierID: early.ID, ValidFrom: start.Add(-24 * time.Hour), PriceAmount: 1890}},
		},
	}
	return src, session
}

func TestBuildEventBundles_OneImportableBundlePerSession(t *testing.T) {
	src, session := bundleFixtureSource(t)
	out := buildEventBundles(src)
	if len(out.Bundles) != 1 || len(out.Skipped) != 0 || len(out.Warnings) != 0 {
		t.Fatalf("bundles %d skipped %d warnings %+v", len(out.Bundles), len(out.Skipped), out.Warnings)
	}
	b := out.Bundles[0]
	if b.Source != "arena" || b.ExternalRef != "" {
		t.Errorf("source %q externalRef %q: a bundle is source=arena and needs no key", b.Source, b.ExternalRef)
	}
	if b.Action.ArenaEventID != src.Event.ID.String() || b.ActionEvent.ArenaSessionID != session.ID.String() ||
		b.Venue.ArenaVenueID != src.Sessions[0].Venue.ID.String() {
		t.Errorf("ids are not filled in: %+v %+v %+v", b.Action, b.ActionEvent, b.Venue)
	}
	if b.Action.BigPosterURL != "" || out.PosterMediaID == nil {
		t.Errorf("the poster travels as poster_media_id, not as a URL")
	}
	if b.Action.PromoterID == nil || *b.Action.PromoterID != "" {
		t.Errorf("an event without a promoter says promoterId \"\", got %v", b.Action.PromoterID)
	}
	ae := b.ActionEvent
	if ae.Day != "04.07.2026" || ae.Time != "21:30" || ae.EndTime != "23:30" || ae.Currency != "EUR" {
		t.Errorf("wall clock in the venue's zone: %+v", ae)
	}
	if ae.SellEndTime != "2026-07-04T20:30:00+02:00" || ae.DoorsOpenTime == nil || *ae.DoorsOpenTime != "2026-07-04T21:00:00+02:00" {
		t.Errorf("sales end %q doors %v", ae.SellEndTime, ae.DoorsOpenTime)
	}
	if !b.Publish || len(b.ChannelIDs) != 1 {
		t.Errorf("publish %v channels %v", b.Publish, b.ChannelIDs)
	}
	if len(b.CategoryList) != 2 {
		t.Fatalf("categories: %+v", b.CategoryList)
	}
	e, d := b.CategoryList[0], b.CategoryList[1]
	if e.ArenaTierID == "" || e.CategoryPriceName != "Early" || e.Price != 18.9 || e.Availability != 60 {
		t.Errorf("Early: %+v", e)
	}
	if e.SellStartTime != "2026-07-02T21:30:00+02:00" || e.SellEndTime != "" {
		t.Errorf("Early window: %q..%q", e.SellStartTime, e.SellEndTime)
	}
	if e.NextCategoryIndex == nil || *e.NextCategoryIndex != -1 || e.SellLimit != nil {
		t.Errorf("an unlinked category says nextCategoryIndex -1 and no sellLimit: %+v", e)
	}
	if e.PriceSchedule == nil || len(*e.PriceSchedule) != 2 || (*e.PriceSchedule)[0].Price != 15 ||
		(*e.PriceSchedule)[0].ValidTo != "2026-07-03T21:30:00+02:00" || (*e.PriceSchedule)[1].ValidTo != "" {
		t.Errorf("Early schedule: %+v", e.PriceSchedule)
	}
	if d.PriceSchedule == nil || len(*d.PriceSchedule) != 0 {
		t.Errorf("a category without a schedule says an EMPTY list (it clears), got %v", d.PriceSchedule)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"bundles":[`, `"skipped":[]`, `"warnings":[]`, `"seatList":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("response lacks %s: %s", want, raw)
		}
	}
}

func TestBuildEventBundles_ChainHeadCarriesTheWholeHall(t *testing.T) {
	src, _ := bundleFixtureSource(t)
	ss := &src.Sessions[0]
	a, b, c := ss.Tiers[0], ss.Tiers[1], gen.TicketTierRow{ID: uuid.New(), Name: "Last", PricingMode: "fixed", PriceAmount: 3000, SortOrder: 2}
	ss.Tiers = append(ss.Tiers, c)
	ss.Chain = []gen.TierChainRow{
		{TierID: a.ID, NextTierID: b.ID, SellLimit: ptr(int32(10)), HandedOverAt: ptr(time.Now())},
		{TierID: b.ID, NextTierID: c.ID},
	}
	// The head handed everything over and owns nothing; the hall is 100.
	ss.Stats = map[uuid.UUID]gaquota.Stats{a.ID: {Quantity: 0}, b.ID: {Quantity: 70}, c.ID: {Quantity: 30}}

	cats := buildEventBundles(src).Bundles[0].CategoryList
	if cats[0].Availability != 100 || cats[1].Availability != 0 || cats[2].Availability != 0 {
		t.Errorf("availability head/middle/last = %d/%d/%d, want 100/0/0", cats[0].Availability, cats[1].Availability, cats[2].Availability)
	}
	if *cats[0].NextCategoryIndex != 1 || *cats[1].NextCategoryIndex != 2 || *cats[2].NextCategoryIndex != -1 {
		t.Errorf("links: %d %d %d", *cats[0].NextCategoryIndex, *cats[1].NextCategoryIndex, *cats[2].NextCategoryIndex)
	}
	if cats[0].SellLimit == nil || *cats[0].SellLimit != 10 || cats[1].SellLimit == nil || *cats[1].SellLimit != 0 || cats[2].SellLimit != nil {
		t.Errorf("sellLimit is written on linked categories only: %v %v %v", cats[0].SellLimit, cats[1].SellLimit, cats[2].SellLimit)
	}
}

func TestBuildEventBundles_SessionsABundleCannotCarryAreListed(t *testing.T) {
	src, _ := bundleFixtureSource(t)
	base := src.Sessions[0]
	clone := func(mutate func(*bundleSessionSource)) bundleSessionSource {
		s := base
		s.Session.ID = uuid.New()
		s.Tiers = append([]gen.TicketTierRow(nil), base.Tiers...)
		mutate(&s)
		return s
	}
	cancelled := clone(func(s *bundleSessionSource) { s.Session.Status = "cancelled" })
	seated := clone(func(s *bundleSessionSource) { s.Session.AdmissionMode = "assigned_seats" })
	planned := clone(func(s *bundleSessionSource) { s.Session.SeatingPlanVersionID = ptr(uuid.New()) })
	pwyw := clone(func(s *bundleSessionSource) { s.Tiers[1].PricingMode = "pwyw" })
	noVenue := clone(func(s *bundleSessionSource) { s.Venue = nil })
	src.Sessions = []bundleSessionSource{base, cancelled, seated, planned, pwyw, noVenue}

	out := buildEventBundles(src)
	if len(out.Bundles) != 1 || len(out.Skipped) != 5 {
		t.Fatalf("bundles %d skipped %+v", len(out.Bundles), out.Skipped)
	}
	want := []string{SkipSessionCancelled, SkipSessionSeated, SkipSessionSeated, SkipSessionPayWhatYouWant, SkipSessionVenueMissing}
	for i, w := range want {
		if out.Skipped[i].Code != w {
			t.Errorf("skipped[%d] = %s, want %s", i, out.Skipped[i].Code, w)
		}
	}
}

func TestBuildEventBundles_PromoterAndTimeNotes(t *testing.T) {
	src, _ := bundleFixtureSource(t)
	id := uuid.New()
	src.PromoterID, src.PromoterArchived = &id, true
	out := buildEventBundles(src)
	if out.Bundles[0].Action.PromoterID != nil || len(out.Warnings) != 1 || out.Warnings[0].Code != WarnBundlePromoterArchived {
		t.Fatalf("an archived promoter is left alone with a warning: %v %+v", out.Bundles[0].Action.PromoterID, out.Warnings)
	}
	src.PromoterArchived = false
	if p := buildEventBundles(src).Bundles[0].Action.PromoterID; p == nil || *p != id.String() {
		t.Fatalf("promoterId = %v, want %s", p, id)
	}

	// A session of 30 hours has no HH:MM end the importer would read back.
	src.Sessions[0].Session.EndAt = src.Sessions[0].Session.StartAt.Add(30 * time.Hour)
	out = buildEventBundles(src)
	if len(out.Warnings) != 1 || out.Warnings[0].Code != WarnBundleTimeNotRepresentable {
		t.Fatalf("warnings: %+v", out.Warnings)
	}

	// A venue with no zone is read in UTC, and says so.
	src.Sessions[0].Session.EndAt = src.Sessions[0].Session.StartAt.Add(2 * time.Hour)
	venue := *src.Sessions[0].Venue
	venue.Timezone = nil
	src.Sessions[0].Venue = &venue
	out = buildEventBundles(src)
	if len(out.Warnings) == 0 || out.Warnings[0].Code != WarnBundleVenueTimezone || out.Bundles[0].ActionEvent.Time != "19:30" {
		t.Fatalf("warnings %+v time %s", out.Warnings, out.Bundles[0].ActionEvent.Time)
	}
}

func TestBuildEventBundles_DraftEventDoesNotPublish(t *testing.T) {
	src, _ := bundleFixtureSource(t)
	src.Event.Status = "draft"
	b := buildEventBundles(src).Bundles[0]
	if b.Publish {
		t.Fatal("a draft event must not ask to publish")
	}
	if len(b.ChannelIDs) != 1 {
		t.Fatal("its channels still travel, so a later publish reaches them")
	}
}
