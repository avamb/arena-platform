// event_bundle_fixture_526_test.go — feature #526, W1-E1d: proves the
// committed contract fixture testdata/wp/event_bundle/arena_ga_lampyris.json
// (event-bundle spec §3, the staging Lampyris example body verbatim) decodes
// into bil24compat.ImportSessionRequest and clears the pre-transaction
// validation ladder for source=arena. This is the PHP-facing contract: the
// site-side "Мероприятия (arena)" module (spec §10) is expected to assemble
// exactly this shape.
//
// No database and no build tag required — everything asserted here runs
// before the handler would open a transaction (mirrors
// himports/event_bundle_524_test.go one package over).
package compat_bil24_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// eventBundleFixturePath is the contract fixture for the arena-native event
// bundle (event-bundle spec §9 / §3), used by both this decode test and (per
// the spec) as the reference body for the PHP-side integration.
const eventBundleFixturePath = "testdata/wp/event_bundle/arena_ga_lampyris.json"

func TestEventBundleFixture_DecodesAndValidates(t *testing.T) {
	raw, err := os.ReadFile(eventBundleFixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", eventBundleFixturePath, err)
	}

	var req bil24compat.ImportSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode fixture into ImportSessionRequest: %v", err)
	}

	// Top-level source/externalRef selection (event-bundle spec §2, §3).
	if req.Source != bil24compat.SourceArena {
		t.Errorf("source = %q, want %q", req.Source, bil24compat.SourceArena)
	}
	if !bil24compat.KnownImportSource(req.Source) {
		t.Errorf("source %q is not a known import source", req.Source)
	}
	if got, want := req.NormalizedExternalRef(), "wp:lampyris-staging:product:4711"; got != want {
		t.Errorf("externalRef = %q, want %q", got, want)
	}
	if len(req.ExternalRef) > bil24compat.MaxExternalRefLength {
		t.Errorf("externalRef length %d exceeds MaxExternalRefLength %d", len(req.ExternalRef), bil24compat.MaxExternalRefLength)
	}

	// Every *Id in the fixture is null (arena mints them) — ValidateArenaIDs
	// must accept the payload as-is (event-bundle spec §3.1).
	if err := req.ValidateArenaIDs(); err != nil {
		t.Errorf("ValidateArenaIDs() = %v, want nil (every id omitted)", err)
	}
	if req.Action.ActionID != 0 || req.ActionEvent.ActionEventID != 0 || req.Venue.VenueID != 0 {
		t.Errorf("fixture compat ids must be omitted (0/null) for source=arena: action=%d actionEvent=%d venue=%d",
			req.Action.ActionID, req.ActionEvent.ActionEventID, req.Venue.VenueID)
	}
	for i, c := range req.CategoryList {
		if c.CategoryPriceID != 0 {
			t.Errorf("categoryList[%d].categoryPriceId = %d, want 0 (omitted)", i, c.CategoryPriceID)
		}
	}

	// Action / actionEvent / venue / categoryList content (spec §3 table).
	if req.Action.Name() == "" {
		t.Error("action carries no usable name (actionName / fullActionName)")
	}
	if req.Venue.Timezone != "Europe/Prague" {
		t.Errorf("venue.timezone = %q, want Europe/Prague", req.Venue.Timezone)
	}
	if req.ActionEvent.Day != "26.10.2026" || req.ActionEvent.Time != "19:00" {
		t.Errorf("actionEvent day/time = %q/%q, want 26.10.2026/19:00", req.ActionEvent.Day, req.ActionEvent.Time)
	}
	if req.ActionEvent.EndTime != "22:00" {
		t.Errorf("actionEvent.endTime = %q, want 22:00", req.ActionEvent.EndTime)
	}
	if len(req.CategoryList) != 2 {
		t.Fatalf("categoryList has %d entries, want 2 (Standard + VIP)", len(req.CategoryList))
	}
	if !req.Publish {
		t.Error("publish = false, want true (fixture publishes on save)")
	}

	// Local start/end and sell-window parsing must all succeed against the
	// declared venue timezone (event-bundle spec §3 / §3.1).
	loc, err := time.LoadLocation(req.Venue.Timezone)
	if err != nil {
		t.Fatalf("load venue timezone %q: %v", req.Venue.Timezone, err)
	}
	start, err := req.ActionEvent.ParseLocalStart(loc)
	if err != nil {
		t.Fatalf("ParseLocalStart: %v", err)
	}
	end, err := req.ActionEvent.ParseLocalEnd(loc)
	if err != nil {
		t.Fatalf("ParseLocalEnd: %v", err)
	}
	if end == nil {
		t.Fatal("ParseLocalEnd returned nil, want a value (fixture carries endTime)")
	}
	if !end.After(start) {
		t.Errorf("end %v is not after start %v", end, start)
	}
	sellStart, err := req.ActionEvent.ParseSellStart()
	if err != nil {
		t.Fatalf("ParseSellStart: %v", err)
	}
	if sellStart == nil {
		t.Fatal("ParseSellStart returned nil, want a value (fixture carries sellStartTime)")
	}
	sellEnd, err := req.ActionEvent.ParseSellEnd()
	if err != nil {
		t.Fatalf("ParseSellEnd: %v", err)
	}
	if sellEnd == nil || !sellStart.Before(*sellEnd) {
		t.Errorf("sellStartTime %v must be before sellEndTime %v", sellStart, sellEnd)
	}
}
