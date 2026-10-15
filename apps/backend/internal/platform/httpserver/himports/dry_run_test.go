package himports

// dry_run_test.go — unit coverage of the event-bundle dry run (task 3-1)
// that needs no database: stage 1 collects EVERY field error of the bundle
// with its JSON pointer, while the import keeps answering the first one
// exactly as before.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// brokenArenaBundle is an arena bundle with one error in each of several
// independent fields.
func brokenArenaBundle() bil24compat.ImportSessionRequest {
	r := arenaPayload()
	r.Action.ActionName = ""                // action_name_required
	r.ActionEvent.Currency = "euro"         // invalid_currency
	r.ActionEvent.SellEndTime = "yesterday" // invalid_sell_end_time
	doors := "soon"
	r.ActionEvent.DoorsOpenTime = &doors // invalid_doors_open_time
	next, limit := 7, int32(-1)
	r.CategoryList = []bil24compat.ImportSessionCategory{
		{CategoryPriceName: "A", Price: -5, Availability: 10},                                   // invalid_price
		{CategoryPriceName: "B", Price: 10, Availability: 10, SellStartTime: "x"},               // invalid_sale_window
		{CategoryPriceName: "C", Price: 10, Availability: 10, NextCategoryIndex: &next},         // invalid_next_category
		{CategoryPriceName: "D", Price: 10, Availability: 10, SellLimit: &limit},                // invalid_sell_limit
		{CategoryPriceName: "E", Price: 0, PriceSchedule: &[]bil24compat.ImportPriceWindow{{}}}, // invalid_price_schedule
	}
	return r
}

func TestDryRun_CollectsEveryFieldErrorWithItsPointer(t *testing.T) {
	h := newTestHandler()
	issues := newIssueSink(importMode{DryRun: true})
	prep := h.prepareImport(context.Background(), uuid.New(), brokenArenaBundle(), "", importMode{DryRun: true},
		newWarningSink(), issues)
	if !prep.sourceOK {
		t.Fatal("source should resolve")
	}
	got := map[string]string{}
	for _, is := range issues.list() {
		got[is.Field] = is.Code
		if is.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", is.Field, is.Status)
		}
	}
	want := map[string]string{
		"/action/actionName":                "import.action_name_required",
		"/actionEvent/currency":             "import.invalid_currency",
		"/actionEvent/sellEndTime":          "import.invalid_sell_end_time",
		"/actionEvent/doorsOpenTime":        "import.invalid_doors_open_time",
		"/categoryList/0/price":             "import.invalid_price",
		"/categoryList/1/sellStartTime":     "import.invalid_sale_window",
		"/categoryList/2/nextCategoryIndex": "import.invalid_next_category",
		"/categoryList/3/sellLimit":         "import.invalid_sell_limit",
		"/categoryList/4/priceSchedule":     "import.invalid_price_schedule",
	}
	for field, code := range want {
		if got[field] != code {
			t.Errorf("field %s: got %q, want %q (all: %v)", field, got[field], code, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d errors, want %d: %v", len(got), len(want), got)
	}
}

func TestDryRun_ImportStillStopsAtTheFirstError(t *testing.T) {
	h := newTestHandler()
	issues := newIssueSink(importMode{})
	h.prepareImport(context.Background(), uuid.New(), brokenArenaBundle(), "", importMode{}, newWarningSink(), issues)
	if len(issues.items) != 1 {
		t.Fatalf("an import records %d errors, want only the first", len(issues.items))
	}
	var ie *importError
	if err := issues.first(); err == nil || !errors.As(err, &ie) || ie.code != "import.action_name_required" {
		t.Fatalf("first error = %v, want import.action_name_required", err)
	}
	// The import's own answer is the unchanged first-error envelope.
	rec := httptest.NewRecorder()
	h.HandleEventBundle(rec, importRequest(t, uuid.New(), brokenArenaBundle()))
	if rec.Code != http.StatusUnprocessableEntity || decodeError(t, rec) != "import.action_name_required" {
		t.Fatalf("import answered %d %s", rec.Code, rec.Body.String())
	}
}

func TestDryRun_AllIDViolationsReported(t *testing.T) {
	r := arenaPayload()
	r.Action.ActionID = 5
	r.Venue.VenueID = 7
	r.CategoryList[0].CategoryPriceID = 9
	issues := newIssueSink(importMode{DryRun: true})
	newTestHandler().prepareImport(context.Background(), uuid.New(), r, "", importMode{DryRun: true}, newWarningSink(), issues)
	fields := []string{}
	for _, is := range issues.list() {
		if is.Code == "import.arena_id_out_of_range" {
			fields = append(fields, is.Field)
		}
	}
	if strings.Join(fields, ",") != "/action/actionId,/venue/venueId,/categoryList/0/categoryPriceId" {
		t.Fatalf("id violations = %v", fields)
	}
}

func TestDryRun_UnknownSourceAnswersAReportWithoutTouchingTheDatabase(t *testing.T) {
	r := arenaPayload()
	r.Source = "wordpress"
	req := importRequest(t, uuid.New(), r)
	req.URL.RawQuery = "dry_run=true"
	rec := httptest.NewRecorder()
	// failingTxStarter would turn any transaction into a 500: a 200 proves
	// none was opened.
	newTestHandler().HandleEventBundle(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out ImportDryRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.DryRun || out.Valid || len(out.Errors) != 1 || out.Errors[0].Field != "/source" ||
		out.Errors[0].Code != "import.source_invalid" {
		t.Fatalf("report = %+v", out)
	}
}

func TestDryRun_BadQueryValueIs400(t *testing.T) {
	req := importRequest(t, uuid.New(), arenaPayload())
	req.URL.RawQuery = "dry_run=maybe"
	rec := httptest.NewRecorder()
	newTestHandler().HandleEventBundle(rec, req)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec) != "import.invalid_dry_run" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestDryRun_NormalizeBundleTrimsWithoutInventing(t *testing.T) {
	r := arenaPayload()
	r.ExternalRef = "  ref-1  "
	r.ActionEvent.Currency = " czk "
	r.Venue.VenueName = "  Hall  "
	r.CategoryList[0].CategoryPriceName = " Standard "
	h := newTestHandler()
	issues := newIssueSink(importMode{DryRun: true})
	prep := h.prepareImport(context.Background(), uuid.New(), r, "", importMode{DryRun: true}, newWarningSink(), issues)
	n := normalizeBundle(prep)
	if n.ExternalRef != "ref-1" || n.ActionEvent.Currency != "CZK" || n.Venue.VenueName != "Hall" ||
		n.CategoryList[0].CategoryPriceName != "Standard" || n.Source != bil24compat.SourceArena {
		t.Fatalf("normalized = %+v", n)
	}
	if r.CategoryList[0].CategoryPriceName != " Standard " {
		t.Fatal("normalizeBundle changed the caller's request")
	}
	if n.ActionEvent.SellEndTime != "" {
		t.Fatal("a default leaked into the normalized bundle")
	}
}

func TestDottedToPointer(t *testing.T) {
	for in, want := range map[string]string{
		"actionEvent.actionEventId":       "/actionEvent/actionEventId",
		"categoryList[2].categoryPriceId": "/categoryList/2/categoryPriceId",
		"venue.venueId":                   "/venue/venueId",
	} {
		if got := dottedToPointer(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
