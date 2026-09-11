// event_bundle_524_test.go — unit coverage for the event-bundle validation
// ladder (feature #524, W1-E1b; event-bundle spec §3, §3.1, §5).
//
// Like bil24_session_517_test.go, everything asserted here is reachable
// WITHOUT a database: source / externalRef / id-range / endTime /
// sellStartTime are all decided before the handler opens a transaction, and
// a source=arena bundle is answered 501 before the venue lookup. The
// execution half of source=arena lands with feature #525.
package himports

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// arenaPayload is a minimal, fully valid source=arena bundle: no compat ids at
// all (arena mints them), an externalRef, and one GA category.
func arenaPayload() bil24compat.ImportSessionRequest {
	return bil24compat.ImportSessionRequest{
		Source:      bil24compat.SourceArena,
		ExternalRef: "wp:lampyris-staging:product:4711",
		Action: bil24compat.ImportSessionAction{
			ActionName: "Lampyris Live",
		},
		ActionEvent: bil24compat.ImportSessionActionEvent{
			Day:      "26.10.2026",
			Time:     "19:00",
			EndTime:  "22:00",
			Currency: "CZK",
		},
		Venue: bil24compat.ImportSessionVenue{
			VenueName: "Palác Akropolis",
			Timezone:  "Europe/Prague",
		},
		CategoryList: []bil24compat.ImportSessionCategory{
			{CategoryPriceName: "Standard", Price: 450, Availability: 300},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Route ↔ source agreement (spec §2, §5)
// ─────────────────────────────────────────────────────────────────────────────

// TestLegacyRoute_SourceHandling pins the alias contract of
// /imports/bil24-session: source may be omitted (meaning bil24) or spelled
// bil24, but never arena.
func TestLegacyRoute_SourceHandling(t *testing.T) {
	orgID := uuid.New()

	t.Run("missing source still means bil24", func(t *testing.T) {
		payload := validPayload() // carries no source at all
		rec := httptest.NewRecorder()
		newTestHandler().HandleBil24Session(rec, importRequest(t, orgID, payload))
		// Validation passed; the unusable DBTX surfaces as a 500 at the venue
		// timezone lookup — exactly the pre-#524 behaviour.
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (validation passed, db down); body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("explicit source=bil24 accepted", func(t *testing.T) {
		payload := validPayload()
		payload.Source = bil24compat.SourceBil24
		rec := httptest.NewRecorder()
		newTestHandler().HandleBil24Session(rec, importRequest(t, orgID, payload))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (validation passed, db down); body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("source=arena rejected with source_mismatch", func(t *testing.T) {
		payload := validPayload()
		payload.Source = bil24compat.SourceArena
		rec := httptest.NewRecorder()
		newTestHandler().HandleBil24Session(rec, importRequest(t, orgID, payload))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got != "import.source_mismatch" {
			t.Fatalf("code = %q, want import.source_mismatch", got)
		}
	})

	t.Run("unknown source rejected with source_invalid", func(t *testing.T) {
		payload := validPayload()
		payload.Source = "woocommerce"
		rec := httptest.NewRecorder()
		newTestHandler().HandleBil24Session(rec, importRequest(t, orgID, payload))
		if got := decodeError(t, rec); got != "import.source_invalid" {
			t.Fatalf("code = %q, want import.source_invalid; body=%s", got, rec.Body.String())
		}
	})
}

// TestEventBundleRoute_SourceRequired proves the new route demands an explicit
// source: it serves both regimes, so a default would be a silent guess.
func TestEventBundleRoute_SourceRequired(t *testing.T) {
	orgID := uuid.New()

	for _, tc := range []struct {
		name    string
		source  string
		wantErr string
	}{
		{"missing", "", "import.source_invalid"},
		{"blank after trim", "   ", "import.source_invalid"},
		{"unknown value", "gsheets", "import.source_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := validPayload()
			payload.Source = tc.source
			rec := httptest.NewRecorder()
			newTestHandler().HandleEventBundle(rec, importRequest(t, orgID, payload))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
			}
			if got := decodeError(t, rec); got != tc.wantErr {
				t.Fatalf("code = %q, want %q", got, tc.wantErr)
			}
		})
	}

	t.Run("source=bil24 takes the legacy path", func(t *testing.T) {
		payload := validPayload()
		payload.Source = bil24compat.SourceBil24
		rec := httptest.NewRecorder()
		newTestHandler().HandleEventBundle(rec, importRequest(t, orgID, payload))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (validation passed, db down); body=%s", rec.Code, rec.Body.String())
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// The source=arena ladder (spec §3.1, §5)
// ─────────────────────────────────────────────────────────────────────────────

func TestEventBundle_ArenaValidationLadder(t *testing.T) {
	orgID := uuid.New()
	longRef := strings.Repeat("x", 201)

	tests := []struct {
		name     string
		mutate   func(*bil24compat.ImportSessionRequest)
		wantCode int
		wantErr  string
	}{
		{
			name:     "externalRef missing",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ExternalRef = "" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.external_ref_required",
		},
		{
			name:     "externalRef blank after trim",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ExternalRef = "   " },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.external_ref_invalid",
		},
		{
			name:     "externalRef longer than 200",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ExternalRef = longRef },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.external_ref_invalid",
		},
		{
			// §3.1: a Bil24-range id under source=arena would hijack a
			// foreign mapping row.
			name:     "actionId below the ceiling",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.Action.ActionID = 267271 },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.arena_id_out_of_range",
		},
		{
			name:     "actionEventId below the ceiling",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.ActionEventID = 703872 },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.arena_id_out_of_range",
		},
		{
			name:     "venueId below the ceiling",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.Venue.VenueID = 9619 },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.arena_id_out_of_range",
		},
		{
			name:     "categoryPriceId below the ceiling",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.CategoryList[0].CategoryPriceID = 12345 },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.arena_id_out_of_range",
		},
		{
			name:     "endTime is not HH:MM",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.EndTime = "22h00" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.end_time_invalid",
		},
		{
			name:     "sellStartTime is not RFC3339",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.SellStartTime = "15.09.2026 10:00" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.invalid_sell_start_time",
		},
		{
			name: "sellStartTime at or after sellEndTime",
			mutate: func(r *bil24compat.ImportSessionRequest) {
				r.ActionEvent.SellStartTime = "2026-10-26T18:00:00+01:00"
				r.ActionEvent.SellEndTime = "2026-10-26T18:00:00+01:00"
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.invalid_sell_start_time",
		},
		{
			// The shared rungs still apply to an arena bundle.
			name:     "no categories",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.CategoryList = nil },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.categories_required",
		},
		{
			name: "no action name",
			mutate: func(r *bil24compat.ImportSessionRequest) {
				r.Action.ActionName = ""
				r.Action.FullActionName = ""
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.action_name_required",
		},
		{
			name:     "bad currency",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.Currency = "CZKK" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.invalid_currency",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := arenaPayload()
			tc.mutate(&payload)
			rec := httptest.NewRecorder()
			newTestHandler().HandleEventBundle(rec, importRequest(t, orgID, payload))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := decodeError(t, rec); got != tc.wantErr {
				t.Fatalf("code = %q, want %q; body=%s", got, tc.wantErr, rec.Body.String())
			}
		})
	}
}

// TestEventBundle_ArenaAcceptsIDsAtOrAboveCeiling proves the positive half of
// §3.1: an id arena itself minted is welcome back on an edit.
func TestEventBundle_ArenaAcceptsIDsAtOrAboveCeiling(t *testing.T) {
	payload := arenaPayload()
	payload.Action.ActionID = 1_000_000_007
	payload.ActionEvent.ActionEventID = 1_000_000_008
	payload.Venue.VenueID = 1_000_000_003
	payload.CategoryList[0].CategoryPriceID = 1_000_000_012

	rec := httptest.NewRecorder()
	newTestHandler().HandleEventBundle(rec, importRequest(t, uuid.New(), payload))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (validation passed, executor lands with #525); body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestEventBundle_ArenaPassesValidationButIsNotExecutedYet documents the #524
// boundary: a well-formed arena bundle is answered honestly with 501 instead
// of being run through the Bil24 algorithm, which would mint wrong ids.
// Feature #525 replaces this branch with the real executor.
func TestEventBundle_ArenaPassesValidationButIsNotExecutedYet(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler().HandleEventBundle(rec, importRequest(t, uuid.New(), arenaPayload()))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec); got != "import.arena_source_not_implemented" {
		t.Fatalf("code = %q, want import.arena_source_not_implemented", got)
	}
}

// TestEventBundle_LegacySourceKeepsBil24IDRules proves the two id regimes did
// not get crossed: under source=bil24 the ids stay mandatory and below the
// ceiling, exactly as feature #517 defined them.
func TestEventBundle_LegacySourceKeepsBil24IDRules(t *testing.T) {
	orgID := uuid.New()

	t.Run("missing ids still rejected", func(t *testing.T) {
		payload := validPayload()
		payload.Source = bil24compat.SourceBil24
		payload.Action.ActionID = 0
		rec := httptest.NewRecorder()
		newTestHandler().HandleEventBundle(rec, importRequest(t, orgID, payload))
		if got := decodeError(t, rec); got != "compat.external_id_out_of_range" {
			t.Fatalf("code = %q, want compat.external_id_out_of_range; body=%s", got, rec.Body.String())
		}
	})

	t.Run("arena-range id still rejected", func(t *testing.T) {
		payload := validPayload()
		payload.Source = bil24compat.SourceBil24
		payload.ActionEvent.ActionEventID = 1_000_000_008
		rec := httptest.NewRecorder()
		newTestHandler().HandleEventBundle(rec, importRequest(t, orgID, payload))
		if got := decodeError(t, rec); got != "compat.external_id_out_of_range" {
			t.Fatalf("code = %q, want compat.external_id_out_of_range; body=%s", got, rec.Body.String())
		}
	})

	t.Run("optional externalRef accepted", func(t *testing.T) {
		payload := validPayload()
		payload.Source = bil24compat.SourceBil24
		payload.ExternalRef = "wp:vinoandco:product:12"
		rec := httptest.NewRecorder()
		newTestHandler().HandleEventBundle(rec, importRequest(t, orgID, payload))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (validation passed, db down); body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("over-long externalRef rejected for bil24 too", func(t *testing.T) {
		payload := validPayload()
		payload.Source = bil24compat.SourceBil24
		payload.ExternalRef = strings.Repeat("y", 201)
		rec := httptest.NewRecorder()
		newTestHandler().HandleEventBundle(rec, importRequest(t, orgID, payload))
		if got := decodeError(t, rec); got != "import.external_ref_invalid" {
			t.Fatalf("code = %q, want import.external_ref_invalid; body=%s", got, rec.Body.String())
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Warnings for fields that belong to the other source (spec §3, §5)
// ─────────────────────────────────────────────────────────────────────────────

func TestNoteArenaIgnoredFields(t *testing.T) {
	t.Run("clean bundle warns about nothing", func(t *testing.T) {
		sink := newWarningSink()
		noteArenaIgnoredFields(arenaPayload(), sink)
		if got := sink.list(); len(got) != 0 {
			t.Fatalf("warnings = %#v, want none", got)
		}
	})

	t.Run("fee and seating-plan metadata are ignored with a warning", func(t *testing.T) {
		payload := arenaPayload()
		payload.ActionEvent.ChargePercent = 5
		payload.ActionEvent.SeatingPlanID = 42
		payload.ActionEvent.SeatingPlanName = "Hall A"
		sink := newWarningSink()
		noteArenaIgnoredFields(payload, sink)

		got := sink.list()
		if len(got) != 1 || got[0].Code != WarnFieldIgnoredForSource {
			t.Fatalf("warnings = %#v, want a single %s", got, WarnFieldIgnoredForSource)
		}
		for _, field := range []string{"chargePercent", "seatingPlanId", "seatingPlanName"} {
			if !strings.Contains(got[0].Message, field) {
				t.Errorf("warning message %q does not name %s", got[0].Message, field)
			}
		}
	})

	t.Run("seating payload warns seating_not_imported", func(t *testing.T) {
		payload := arenaPayload()
		payload.SVG = "<svg/>"
		payload.SeatList = []bil24compat.ImportSessionSeat{{SeatID: 2873098559}}
		sink := newWarningSink()
		noteArenaIgnoredFields(payload, sink)

		got := sink.list()
		if len(got) != 1 || got[0].Code != WarnSeatingNotImported {
			t.Fatalf("warnings = %#v, want a single %s", got, WarnSeatingNotImported)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Parse helpers (spec §3)
// ─────────────────────────────────────────────────────────────────────────────

func TestParseLocalEnd(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	t.Run("absent endTime yields nil", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{Day: "26.10.2026", Time: "19:00"}
		got, err := ev.ParseLocalEnd(prague)
		if err != nil || got != nil {
			t.Fatalf("ParseLocalEnd = %v, %v; want nil, nil", got, err)
		}
	})

	t.Run("same-day end", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{Day: "26.10.2026", Time: "19:00", EndTime: "22:00"}
		got, err := ev.ParseLocalEnd(prague)
		if err != nil {
			t.Fatalf("ParseLocalEnd: %v", err)
		}
		want := time.Date(2026, 10, 26, 22, 0, 0, 0, prague).UTC()
		if !got.Equal(want) {
			t.Fatalf("ParseLocalEnd = %s, want %s", got, want)
		}
	})

	t.Run("endTime before start rolls to the next day", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{Day: "26.10.2026", Time: "22:00", EndTime: "01:00"}
		got, err := ev.ParseLocalEnd(prague)
		if err != nil {
			t.Fatalf("ParseLocalEnd: %v", err)
		}
		want := time.Date(2026, 10, 27, 1, 0, 0, 0, prague).UTC()
		if !got.Equal(want) {
			t.Fatalf("ParseLocalEnd = %s, want %s", got, want)
		}
	})

	t.Run("endTime equal to start rolls to the next day", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{Day: "26.10.2026", Time: "19:00", EndTime: "19:00"}
		got, err := ev.ParseLocalEnd(prague)
		if err != nil {
			t.Fatalf("ParseLocalEnd: %v", err)
		}
		want := time.Date(2026, 10, 27, 19, 0, 0, 0, prague).UTC()
		if !got.Equal(want) {
			t.Fatalf("ParseLocalEnd = %s, want %s", got, want)
		}
	})

	t.Run("malformed endTime is an error", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{Day: "26.10.2026", Time: "19:00", EndTime: "10 PM"}
		if _, err := ev.ParseLocalEnd(prague); err == nil {
			t.Fatalf("ParseLocalEnd(%q): want an error", ev.EndTime)
		}
	})
}

func TestParseSellStart(t *testing.T) {
	t.Run("absent yields nil", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{}
		got, err := ev.ParseSellStart()
		if err != nil || got != nil {
			t.Fatalf("ParseSellStart = %v, %v; want nil, nil", got, err)
		}
	})

	t.Run("RFC3339 is normalised to UTC", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{SellStartTime: "2026-09-15T10:00:00+02:00"}
		got, err := ev.ParseSellStart()
		if err != nil {
			t.Fatalf("ParseSellStart: %v", err)
		}
		want := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("ParseSellStart = %s, want %s", got, want)
		}
	})

	t.Run("must be before sellEndTime", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{
			SellStartTime: "2026-10-26T19:00:00+01:00",
			SellEndTime:   "2026-10-26T18:00:00+01:00",
		}
		if _, err := ev.ParseSellStart(); err == nil {
			t.Fatalf("ParseSellStart: want an error for start after end")
		}
	})

	t.Run("equal to sellEndTime is rejected", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{
			SellStartTime: "2026-10-26T18:00:00+01:00",
			SellEndTime:   "2026-10-26T18:00:00+01:00",
		}
		if _, err := ev.ParseSellStart(); err == nil {
			t.Fatalf("ParseSellStart: want an error for start == end")
		}
	})

	t.Run("not RFC3339", func(t *testing.T) {
		ev := bil24compat.ImportSessionActionEvent{SellStartTime: "15.09.2026"}
		if _, err := ev.ParseSellStart(); err == nil {
			t.Fatalf("ParseSellStart: want an error")
		}
	})
}
