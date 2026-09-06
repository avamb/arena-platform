// bil24_session_517_test.go — unit coverage for the Bil24 session import
// (feature #517, W1-C3c; spec §13.2).
//
// Everything asserted here is reachable WITHOUT a database: the handler
// validates the payload (spec §13.2 step 1 plus the currency/day/time
// preconditions) before it opens a transaction, so a *gen.Queries bound to an
// always-failing DBTX is enough to prove the ordering. The database half of
// the algorithm (steps 2-5, 7-8, idempotency) is covered by
// httpserver/imports_bil24_517_integration_test.go against a live PostgreSQL.
package himports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test doubles
// ─────────────────────────────────────────────────────────────────────────────

var errNoDB = errors.New("db unavailable in unit test")

// emptyDBTX satisfies gen.DBTX and behaves like a reachable but EMPTY database:
// single-row lookups answer pgx.ErrNoRows, everything else fails. That is what
// the pre-transaction ladder actually needs — the venue lookup must resolve
// "unknown venue" so the timezone rules are reached — while a write attempt
// still cannot succeed, proving no data is touched before BeginTx.
type emptyDBTX struct{}

func (emptyDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errNoDB
}
func (emptyDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, errNoDB }
func (emptyDBTX) QueryRow(context.Context, string, ...any) pgx.Row        { return noRow{} }

type noRow struct{}

func (noRow) Scan(...any) error { return pgx.ErrNoRows }

// failingTxStarter satisfies TxStarter and never yields a transaction.
type failingTxStarter struct{}

func (failingTxStarter) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errNoDB
}

// newTestHandler builds a Handler whose database is present but unusable, and
// whose membership check is disabled (membershipQueries stays nil), so the
// tests exercise exactly the validation ladder.
func newTestHandler() *Handler {
	return New(gen.New(emptyDBTX{}), failingTxStarter{}, nil, nil)
}

// importRequest builds a POST carrying body with the org_id path param bound.
func importRequest(t *testing.T, orgID uuid.UUID, body any) *http.Request {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case string:
		raw = []byte(v)
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/imports/bil24-session", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// validPayload is a minimal, fully valid general-admission import body.
func validPayload() bil24compat.ImportSessionRequest {
	return bil24compat.ImportSessionRequest{
		Action: bil24compat.ImportSessionAction{
			ActionID:   267271,
			ActionName: "Vino & Co Tasting",
		},
		ActionEvent: bil24compat.ImportSessionActionEvent{
			ActionEventID: 703872,
			Day:           "26.04.2026",
			Time:          "17:00",
			Currency:      "EUR",
		},
		Venue: bil24compat.ImportSessionVenue{
			VenueID:   9619,
			VenueName: "Test Hall",
			Timezone:  "Europe/Madrid",
		},
		CategoryList: []bil24compat.ImportSessionCategory{
			{CategoryPriceID: 12345, CategoryPriceName: "Parter", Price: 25, Availability: 100},
		},
	}
}

// decodeError pulls the machine code out of the standard ErrorEnvelope.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	return env.Error.Code
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler-level validation (spec §13.2 step 1 and the preconditions)
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleBil24Session_SelfGatesWithoutDatabase proves the standard
// dependency self-gate: a handler constructed without queries/pool answers 503
// rather than panicking, matching every other httpserver sub-package.
func TestHandleBil24Session_SelfGatesWithoutDatabase(t *testing.T) {
	h := New(nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	h.HandleBil24Session(rec, importRequest(t, uuid.New(), validPayload()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec); got != "dependency.database_unavailable" {
		t.Fatalf("code = %q, want dependency.database_unavailable", got)
	}
}

func TestHandleBil24Session_ValidationLadder(t *testing.T) {
	orgID := uuid.New()

	tests := []struct {
		name     string
		mutate   func(*bil24compat.ImportSessionRequest)
		rawBody  string
		wantCode int
		wantErr  string
	}{
		{
			name:     "malformed json",
			rawBody:  `{"action":`,
			wantCode: http.StatusBadRequest,
			wantErr:  "import.invalid_body",
		},
		{
			// Spec §13.2 step 1: an id at or above the 1e9 compat ceiling is
			// an arena system id echoed back at us.
			name:     "action id at the compat ceiling",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.Action.ActionID = 1_000_000_000 },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "compat.external_id_out_of_range",
		},
		{
			name:     "action event id above the compat ceiling",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.ActionEventID = 7_038_720_000 },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "compat.external_id_out_of_range",
		},
		{
			name:     "category price id out of range",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.CategoryList[0].CategoryPriceID = 0 },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "compat.external_id_out_of_range",
		},
		{
			name:     "empty category list",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.CategoryList = nil },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.categories_required",
		},
		{
			name: "no action name at all",
			mutate: func(r *bil24compat.ImportSessionRequest) {
				r.Action.ActionName = ""
				r.Action.FullActionName = ""
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.action_name_required",
		},
		{
			name:     "missing currency",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.Currency = "" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.invalid_currency",
		},
		{
			name:     "currency is not alpha-3",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.Currency = "EURO" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.invalid_currency",
		},
		{
			// Spec §13.2 step 2: a venue arena does not know yet MUST bring a
			// timezone, otherwise the session start instant is a guess.
			name:     "unknown venue without timezone",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.Venue.Timezone = "" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "venue.timezone_required",
		},
		{
			name:     "timezone is not an IANA zone",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.Venue.Timezone = "Mars/Olympus_Mons" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "venue.timezone_required",
		},
		{
			name:     "day is not DD.MM.YYYY",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.Day = "2026-04-26" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.invalid_start_time",
		},
		{
			name:     "sell end time is not RFC3339",
			mutate:   func(r *bil24compat.ImportSessionRequest) { r.ActionEvent.SellEndTime = "26.04.2026 16:00" },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "import.invalid_sell_end_time",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler()
			var req *http.Request
			if tc.rawBody != "" {
				req = importRequest(t, orgID, tc.rawBody)
			} else {
				payload := validPayload()
				tc.mutate(&payload)
				req = importRequest(t, orgID, payload)
			}
			rec := httptest.NewRecorder()
			h.HandleBil24Session(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := decodeError(t, rec); got != tc.wantErr {
				t.Fatalf("code = %q, want %q; body=%s", got, tc.wantErr, rec.Body.String())
			}
		})
	}
}

// TestHandleBil24Session_UnknownFieldsTolerated proves the deliberate decoding
// choice documented in the handler: Bil24 adds payload fields without notice
// and arena must not break every operator over a field it ignores. The request
// must therefore travel past decoding and fail later, at the database.
func TestHandleBil24Session_UnknownFieldsTolerated(t *testing.T) {
	payload := validPayload()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	asMap["somethingBil24AddedLastWeek"] = map[string]any{"nested": true}

	h := newTestHandler()
	rec := httptest.NewRecorder()
	h.HandleBil24Session(rec, importRequest(t, uuid.New(), asMap))

	if rec.Code == http.StatusBadRequest {
		t.Fatalf("unknown field rejected at decode time: %s", rec.Body.String())
	}
	// The failing DBTX surfaces as the transaction failure of the venue
	// timezone lookup — proof the payload passed validation.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (db down after successful validation); body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleBil24Session_RejectsNonUUIDOrg guards the path parameter.
func TestHandleBil24Session_RejectsNonUUIDOrg(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/not-a-uuid/imports/bil24-session", strings.NewReader("{}"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	newTestHandler().HandleBil24Session(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Warnings
// ─────────────────────────────────────────────────────────────────────────────

// TestWarningSink_DeduplicatesAndPreservesOrder pins the response contract:
// warnings is never null and never repeats a code.
func TestWarningSink_DeduplicatesAndPreservesOrder(t *testing.T) {
	s := newWarningSink()
	if got := s.list(); got == nil || len(got) != 0 {
		t.Fatalf("empty sink list() = %#v, want an empty non-nil slice", got)
	}
	s.add(WarnPosterSkipped, "first")
	s.add(WarnCountryUnresolved, "second")
	s.add(WarnPosterSkipped, "duplicate, must be dropped")

	got := s.list()
	if len(got) != 2 {
		t.Fatalf("len(list()) = %d, want 2: %#v", len(got), got)
	}
	if got[0].Code != WarnPosterSkipped || got[0].Message != "first" {
		t.Fatalf("first warning = %#v", got[0])
	}
	if got[1].Code != WarnCountryUnresolved {
		t.Fatalf("second warning = %#v", got[1])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestNormalizeCurrency(t *testing.T) {
	if got, err := normalizeCurrency(" eur "); err != nil || got != "EUR" {
		t.Fatalf("normalizeCurrency(\" eur \") = %q, %v; want EUR, nil", got, err)
	}
	for _, bad := range []string{"", "EU", "EURO", "E1R"} {
		if _, err := normalizeCurrency(bad); err == nil {
			t.Fatalf("normalizeCurrency(%q) accepted an invalid code", bad)
		}
	}
}

// TestSlugify covers the transliteration table: without it a Cyrillic Bil24
// city name would slugify to the empty string and silently drop the city.
func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Madrid":          "madrid",
		"  Sant Cugat  ":  "sant-cugat",
		"Székesfehérvár":  "szekesfehervar",
		"Москва":          "moskva",
		"Санкт-Петербург": "sankt-peterburg",
		"":                "",
		"---":             "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Fatalf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExternalIDStringMatchesTierIDKeys(t *testing.T) {
	if got := externalIDString(12345); got != "12345" {
		t.Fatalf("externalIDString(12345) = %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Wire payload (internal/adapters/bil24compat)
// ─────────────────────────────────────────────────────────────────────────────

// TestParseLocalStart_UsesVenueTimezone proves the day/time pair is read as
// WALL-CLOCK time in the venue zone, not as UTC — the single most damaging
// possible mistake in this endpoint (every imported session would be off by
// the zone offset).
func TestParseLocalStart_UsesVenueTimezone(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	ae := bil24compat.ImportSessionActionEvent{Day: "26.04.2026", Time: "17:00"}
	got, err := ae.ParseLocalStart(madrid)
	if err != nil {
		t.Fatalf("ParseLocalStart: %v", err)
	}
	// 26 April is inside CEST (UTC+2), so 17:00 local is 15:00Z.
	if want := time.Date(2026, 4, 26, 15, 0, 0, 0, time.UTC); !got.UTC().Equal(want) {
		t.Fatalf("ParseLocalStart = %s, want %s", got.UTC(), want)
	}

	// An absent time component defaults to midnight rather than failing.
	midnight, err := bil24compat.ImportSessionActionEvent{Day: "26.04.2026"}.ParseLocalStart(time.UTC)
	if err != nil {
		t.Fatalf("ParseLocalStart without time: %v", err)
	}
	if midnight.Hour() != 0 || midnight.Minute() != 0 {
		t.Fatalf("default start = %s, want midnight", midnight)
	}
}

// TestPriceMinorUnits guards the float→minor-unit conversion against the
// classic 24.999999 → 2499 truncation artefact.
func TestPriceMinorUnits(t *testing.T) {
	cases := map[float64]int64{25: 2500, 24.99: 2499, 0: 0, 1312.5: 131250, 0.1: 10}
	for in, want := range cases {
		got := bil24compat.ImportSessionCategory{Price: in}.PriceMinorUnits()
		if got != want {
			t.Fatalf("PriceMinorUnits(%v) = %d, want %d", in, got, want)
		}
	}
}

// TestValidateExternalIDs_SeatIDsAreExempt pins the deliberate carve-out: Bil24
// seat ids legitimately exceed 1e9 (the spec example carries 2873098559) since
// they land in session_seats.system_seat_id, not in the compat mapping table.
func TestValidateExternalIDs_SeatIDsAreExempt(t *testing.T) {
	req := validPayload()
	req.SeatList = []bil24compat.ImportSessionSeat{{SeatID: 2873098559, CategoryPriceID: 12345}}
	if err := req.ValidateExternalIDs(); err != nil {
		t.Fatalf("ValidateExternalIDs rejected a legitimate large seat id: %v", err)
	}
}

// TestTotalAvailabilityAndPlacement covers the two payload-shape predicates the
// session upsert depends on.
func TestTotalAvailabilityAndPlacement(t *testing.T) {
	req := validPayload()
	req.CategoryList = append(req.CategoryList, bil24compat.ImportSessionCategory{
		CategoryPriceID: 12346, Availability: 50,
	})
	if got := req.TotalAvailability(); got != 150 {
		t.Fatalf("TotalAvailability = %d, want 150", got)
	}
	if req.HasPlacement() {
		t.Fatalf("HasPlacement = true for a general-admission payload")
	}
	req.CategoryList[1].Placement = true
	if !req.HasPlacement() {
		t.Fatalf("HasPlacement = false despite a placement category")
	}
}
