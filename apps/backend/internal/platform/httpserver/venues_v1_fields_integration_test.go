//go:build integration

// venues_v1_fields_integration_test.go — live-PostgreSQL coverage for bug
// B-2/B-3 (found 2026-09-18): HandleCreateVenue/HandleUpdateVenue accepted
// every V-1 extended field (address_line1/2, postal_code, country, geo_lat/
// geo_lng, timezone, contact_phone, contact_email, website_url, status) on
// the wire but silently dropped all of them — only name/city_id/address/
// capacity_default ever reached InsertVenue/UpdateVenue. The admin form
// (apps/admin-web/src/routes/venues.tsx) sent the full set and showed
// "saved" while the database kept the old values.
//
// This test drives the REAL hcatalog handlers against a live PostgreSQL, per
// AGENTS.md ("Integration tests must use real handlers + real dispatcher"),
// mirroring channels_ttl_integration_test.go's pattern for the analogous
// reservation_ttl_override tri-state defect.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_venues?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ \
//	    -run TestVenueV1FieldsIntegration
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

// venueV1IntegrationPool connects to DATABASE_URL, skipping the test when
// unset (mirrors channelTTLIntegrationPool).
func venueV1IntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// venueV1Request builds an authenticated, admin-reasoned request with chi
// URL params pre-populated so it can be handed directly to an
// hcatalog.Handler method (same pattern as channelTTLRequest).
func venueV1Request(method, orgID, venueID string, body []byte) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, "/", bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, "/", nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "venue V-1 fields integration test")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID)
	if venueID != "" {
		rctx.URLParams.Add("id", venueID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	ctx = auth.WithSuperadminOrgAccess(ctx)
	return req.WithContext(ctx)
}

type venueV1Envelope struct {
	Venue hcatalog.VenueResponse `json:"venue"`
}

func decodeVenueV1Envelope(t *testing.T, w *httptest.ResponseRecorder) hcatalog.VenueResponse {
	t.Helper()
	var env venueV1Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode venue envelope: %v (body: %s)", err, w.Body.String())
	}
	return env.Venue
}

func venueV1ErrorCodeFromBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode error envelope: %v (body: %s)", err, w.Body.String())
	}
	return venueErrorCode(m)
}

func newVenueV1Handler(pool *pgxpool.Pool) *hcatalog.Handler {
	return hcatalog.New(nil, gen.New(pool), nil, nil, nil, nil, nil, pool,
		audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool))
}

// TestVenueV1FieldsIntegration_CreateAndUpdatePersistEveryField is the bug
// B-2 regression test: create a venue with the full V-1 field set, verify
// every one of them round-trips through GET, then PATCH each of the
// tri-state fields (set, clear, keep-on-unrelated-PATCH) and verify the DB
// actually changed.
func TestVenueV1FieldsIntegration_CreateAndUpdatePersistEveryField(t *testing.T) {
	pool := venueV1IntegrationPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	org, err := q.InsertOrganization(ctx, "Venue V1 Fields Test Org", "venue-v1-fields-test-org-"+uuid.NewString(), "DE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM venues WHERE org_id = $1`, org.ID); err != nil {
			t.Logf("cleanup: delete venues: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org.ID); err != nil {
			t.Logf("cleanup: delete organizations: %v", err)
		}
	})

	h := newVenueV1Handler(pool)
	orgID := org.ID.String()

	// 1. CREATE without a timezone must fail 422 venue.timezone_required and
	// write nothing (bug B-3).
	w := httptest.NewRecorder()
	h.HandleCreateVenue(w, venueV1Request(http.MethodPost, orgID, "", []byte(`{"name":"No Timezone Venue"}`)))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create without timezone: got %d, want 422 (body: %s)", w.Code, w.Body.String())
	}
	if code := venueV1ErrorCodeFromBody(t, w); code != "venue.timezone_required" {
		t.Fatalf("create without timezone: code = %q, want venue.timezone_required", code)
	}

	// 2. CREATE with the full V-1 field set.
	createBody, _ := json.Marshal(map[string]any{
		"name":             "Full Fields Venue",
		"address":          "Legacy Address 1",
		"address_line1":    "Rothschild Blvd 101",
		"address_line2":    "Suite 4B",
		"postal_code":      "6688101",
		"country":          "DE",
		"geo_lat":          52.52,
		"geo_lng":          13.405,
		"timezone":         "Europe/Berlin",
		"contact_phone":    "+49-30-555-0100",
		"contact_email":    "info@venue-v1-fields.example",
		"website_url":      "https://venue-v1-fields.example",
		"status":           "draft",
		"capacity_default": 500,
	})
	w = httptest.NewRecorder()
	h.HandleCreateVenue(w, venueV1Request(http.MethodPost, orgID, "", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("create with full fields: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	v := decodeVenueV1Envelope(t, w)
	venueID := v.ID

	assertVenueStringField(t, "create AddressLine1", v.AddressLine1, "Rothschild Blvd 101")
	assertVenueStringField(t, "create AddressLine2", v.AddressLine2, "Suite 4B")
	assertVenueStringField(t, "create PostalCode", v.PostalCode, "6688101")
	assertVenueStringField(t, "create Country", v.Country, "DE")
	assertVenueStringField(t, "create Timezone", v.Timezone, "Europe/Berlin")
	assertVenueStringField(t, "create ContactPhone", v.ContactPhone, "+49-30-555-0100")
	assertVenueStringField(t, "create ContactEmail", v.ContactEmail, "info@venue-v1-fields.example")
	assertVenueStringField(t, "create WebsiteUrl", v.WebsiteUrl, "https://venue-v1-fields.example")
	if v.Status != "draft" {
		t.Fatalf("create Status = %q, want draft", v.Status)
	}
	if v.GeoLat == nil || *v.GeoLat != 52.52 {
		t.Fatalf("create GeoLat = %v, want 52.52", v.GeoLat)
	}
	if v.GeoLng == nil || *v.GeoLng != 13.405 {
		t.Fatalf("create GeoLng = %v, want 13.405", v.GeoLng)
	}
	if v.CapacityDefault == nil || *v.CapacityDefault != 500 {
		t.Fatalf("create CapacityDefault = %v, want 500", v.CapacityDefault)
	}

	// 3. Re-fetch via GET to prove it is really in the database, not just
	// echoed back from the request.
	w = httptest.NewRecorder()
	h.HandleGetVenue(w, venueV1Request(http.MethodGet, "", venueID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get after create: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	v = decodeVenueV1Envelope(t, w)
	assertVenueStringField(t, "get after create Timezone", v.Timezone, "Europe/Berlin")
	assertVenueStringField(t, "get after create Country", v.Country, "DE")

	// 4. PATCH name only — every other V-1 field must be UNCHANGED (this is
	// exactly the bug: the old UpdateVenue call signature had no way to
	// carry these fields at all, so a name-only PATCH looked identical to
	// one that wiped them).
	w = httptest.NewRecorder()
	h.HandleUpdateVenue(w, venueV1Request(http.MethodPatch, orgID, venueID, []byte(`{"name":"Renamed Venue"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch name only: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	v = decodeVenueV1Envelope(t, w)
	if v.Name != "Renamed Venue" {
		t.Fatalf("patch name only: Name = %q, want Renamed Venue", v.Name)
	}
	assertVenueStringField(t, "patch name only AddressLine1", v.AddressLine1, "Rothschild Blvd 101")
	assertVenueStringField(t, "patch name only Country", v.Country, "DE")
	assertVenueStringField(t, "patch name only Timezone", v.Timezone, "Europe/Berlin")
	assertVenueStringField(t, "patch name only ContactEmail", v.ContactEmail, "info@venue-v1-fields.example")
	if v.Status != "draft" {
		t.Fatalf("patch name only: Status = %q, want draft (unchanged)", v.Status)
	}
	if v.GeoLat == nil || *v.GeoLat != 52.52 {
		t.Fatalf("patch name only: GeoLat = %v, want 52.52 (unchanged)", v.GeoLat)
	}

	// 5. PATCH explicit null for address_line2/contact_phone — must clear
	// to NULL (tri-state semantics).
	w = httptest.NewRecorder()
	h.HandleUpdateVenue(w, venueV1Request(http.MethodPatch, orgID, venueID, []byte(`{"address_line2":null,"contact_phone":null}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch clear address_line2/contact_phone: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	v = decodeVenueV1Envelope(t, w)
	if v.AddressLine2 != nil {
		t.Fatalf("patch clear: AddressLine2 = %v, want nil", *v.AddressLine2)
	}
	if v.ContactPhone != nil {
		t.Fatalf("patch clear: ContactPhone = %v, want nil", *v.ContactPhone)
	}
	// AddressLine1 must be untouched by clearing a sibling field.
	assertVenueStringField(t, "patch clear AddressLine1 unaffected", v.AddressLine1, "Rothschild Blvd 101")

	// 6. PATCH capacity_default: null — must clear (bug: a bare *int32
	// request field could never distinguish "omitted" from "explicit null",
	// so this used to silently keep the old value).
	w = httptest.NewRecorder()
	h.HandleUpdateVenue(w, venueV1Request(http.MethodPatch, orgID, venueID, []byte(`{"capacity_default":null}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch clear capacity_default: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	v = decodeVenueV1Envelope(t, w)
	if v.CapacityDefault != nil {
		t.Fatalf("patch clear capacity_default: got %v, want nil", *v.CapacityDefault)
	}

	// 7. PATCH timezone: null — must be REJECTED (bug B-3: forbid clearing)
	// and must leave the stored timezone untouched.
	w = httptest.NewRecorder()
	h.HandleUpdateVenue(w, venueV1Request(http.MethodPatch, orgID, venueID, []byte(`{"timezone":null}`)))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("patch clear timezone: got %d, want 422 (body: %s)", w.Code, w.Body.String())
	}
	if code := venueV1ErrorCodeFromBody(t, w); code != "venue.timezone_required" {
		t.Fatalf("patch clear timezone: code = %q, want venue.timezone_required", code)
	}
	w = httptest.NewRecorder()
	h.HandleGetVenue(w, venueV1Request(http.MethodGet, "", venueID, nil))
	v = decodeVenueV1Envelope(t, w)
	assertVenueStringField(t, "timezone unchanged after rejected clear", v.Timezone, "Europe/Berlin")

	// 8. PATCH timezone to a new valid IANA zone — must succeed (replacing
	// is allowed, only clearing is forbidden).
	w = httptest.NewRecorder()
	h.HandleUpdateVenue(w, venueV1Request(http.MethodPatch, orgID, venueID, []byte(`{"timezone":"Europe/Prague"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch timezone to new value: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	v = decodeVenueV1Envelope(t, w)
	assertVenueStringField(t, "patch timezone new value", v.Timezone, "Europe/Prague")

	// 9. PATCH an invalid IANA zone — must be rejected 422 venue.invalid_timezone.
	w = httptest.NewRecorder()
	h.HandleUpdateVenue(w, venueV1Request(http.MethodPatch, orgID, venueID, []byte(`{"timezone":"Not/AZone"}`)))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("patch invalid timezone: got %d, want 422 (body: %s)", w.Code, w.Body.String())
	}
	if code := venueV1ErrorCodeFromBody(t, w); code != "venue.invalid_timezone" {
		t.Fatalf("patch invalid timezone: code = %q, want venue.invalid_timezone", code)
	}
}

func assertVenueStringField(t *testing.T, label string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil, want %q", label, want)
	}
	if *got != want {
		t.Fatalf("%s: got %q, want %q", label, *got, want)
	}
}
