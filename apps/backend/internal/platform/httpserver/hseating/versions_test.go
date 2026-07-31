// versions_test.go — AB-25 unit coverage for the seating_plan_versions HTTP
// surface.
//
// Like seats_test.go, these tests are stdlib + fake-DBTX only so they run in
// the Unit CI job, where DATABASE_URL points at an unmigrated schema. They
// cover the branches that resolve before any query is issued: dependency
// gating, path-parameter validation, and the row → response projection that
// the admin version-history table consumes.
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// errDBTXUnused fails loudly if a handler reaches the database in a test that
// is supposed to short-circuit before any query.
var errDBTXUnused = errors.New("hseating test: database was not expected to be used")

// unusedDBTX satisfies gen.DBTX without a live connection.
type unusedDBTX struct{ t *testing.T }

func (d unusedDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	d.t.Helper()
	d.t.Error(errDBTXUnused)
	return pgconn.CommandTag{}, errDBTXUnused
}

func (d unusedDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	d.t.Helper()
	d.t.Error(errDBTXUnused)
	return nil, errDBTXUnused
}

func (d unusedDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	d.t.Helper()
	d.t.Error(errDBTXUnused)
	return nil
}

// newVersionsRequest builds a request whose chi route context carries the
// supplied {id} path parameter, mirroring mount_seating.go's route pattern.
func newVersionsRequest(t *testing.T, planID string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/seating-plans/"+planID+"/versions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", planID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func decodeCode(t *testing.T, body io.Reader) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Code string `json:"code"`
	}
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Code != "" {
		return env.Error.Code
	}
	return env.Code
}

func TestAB25_ListSeatingPlanVersions_DatabaseUnavailable(t *testing.T) {
	t.Parallel()

	// New explicitly permits nil queries; the handler must answer 503 rather
	// than panic when the pool never came up.
	h := New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := httptest.NewRecorder()

	h.HandleListSeatingPlanVersions(w, newVersionsRequest(t, uuid.NewString()))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusServiceUnavailable)
	}
	if code := decodeCode(t, w.Body); code != "dependency.database_unavailable" {
		t.Errorf("error code = %q; want dependency.database_unavailable", code)
	}
}

func TestAB25_ListSeatingPlanVersions_RejectsNonUUIDPlanID(t *testing.T) {
	t.Parallel()

	// The path parameter is validated before any query runs, so the fake DBTX
	// must never be touched.
	h := New(
		gen.New(unusedDBTX{t: t}),
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	w := httptest.NewRecorder()

	h.HandleListSeatingPlanVersions(w, newVersionsRequest(t, "not-a-uuid"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusBadRequest)
	}
	if code := decodeCode(t, w.Body); code == "" {
		t.Error("error envelope carried no code")
	}
}

// TestAB25_SeatingPlanVersionFromRow_ProjectsHistoryColumns pins the JSON
// contract the admin version-history table reads: the marker fields
// (locked_at, svg_asset_media_id) must survive as null rather than as a zero
// UUID / zero time, and timestamps must be RFC3339 UTC.
func TestAB25_SeatingPlanVersionFromRow_ProjectsHistoryColumns(t *testing.T) {
	t.Parallel()

	planID := uuid.New()
	versionID := uuid.New()
	mediaID := uuid.New()
	created := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	locked := time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)

	t.Run("unlocked version without an SVG asset", func(t *testing.T) {
		t.Parallel()
		got := SeatingPlanVersionFromRow(gen.SeatingPlanVersionRow{
			ID:               versionID,
			SeatingPlanID:    planID,
			VersionNumber:    1,
			Geometry:         json.RawMessage(`{"schema_version":1}`),
			GeometryChecksum: "deadbeef",
			CapacitySeated:   450,
			CapacityStanding: 120,
			CreatedAt:        created,
		})
		if got.LockedAt != nil {
			t.Errorf("LockedAt = %v; want nil", *got.LockedAt)
		}
		if got.SvgAssetMediaID != nil {
			t.Errorf("SvgAssetMediaID = %v; want nil", *got.SvgAssetMediaID)
		}
		if got.CapacitySeated != 450 || got.CapacityStanding != 120 {
			t.Errorf("capacities = (%d, %d); want (450, 120)",
				got.CapacitySeated, got.CapacityStanding)
		}
		if got.CreatedAt != "2026-07-30T10:00:00Z" {
			t.Errorf("CreatedAt = %q; want RFC3339 UTC", got.CreatedAt)
		}
	})

	t.Run("locked version carrying an SVG asset", func(t *testing.T) {
		t.Parallel()
		got := SeatingPlanVersionFromRow(gen.SeatingPlanVersionRow{
			ID:               versionID,
			SeatingPlanID:    planID,
			VersionNumber:    3,
			Geometry:         json.RawMessage(`{}`),
			GeometryChecksum: "cafe",
			SvgAssetMediaID:  &mediaID,
			CapacitySeated:   10,
			LockedAt:         &locked,
			CreatedAt:        created,
		})
		if got.SvgAssetMediaID == nil || *got.SvgAssetMediaID != mediaID.String() {
			t.Errorf("SvgAssetMediaID = %v; want %s", got.SvgAssetMediaID, mediaID)
		}
		if got.LockedAt == nil || *got.LockedAt != "2026-07-31T09:30:00Z" {
			t.Errorf("LockedAt = %v; want 2026-07-31T09:30:00Z", got.LockedAt)
		}
	})
}

// TestAB25_VersionCreateFields_AcceptsSVGAssetAndStandingCapacity pins the
// accepted body keys for POST /versions. The admin drawer sends exactly
// {svg, svg_asset_media_id, capacity_standing?}; an unknown key is a 400, and
// capacity_seated is deliberately NOT accepted because the handler derives it
// from the imported geometry.
func TestAB25_VersionCreateFields_AcceptsSVGAssetAndStandingCapacity(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"svg", "geometry", "svg_asset_media_id", "capacity_standing"} {
		if !versionCreateFields[key] {
			t.Errorf("versionCreateFields is missing %q", key)
		}
	}
	if versionCreateFields["capacity_seated"] {
		t.Error("versionCreateFields accepts capacity_seated; it must be server-derived")
	}

	if _, code, _ := decodeBody([]byte(`{"capacity_seated":10}`), versionCreateFields); code != "seating_plan.unknown_field" {
		t.Errorf("decodeBody code = %q; want seating_plan.unknown_field", code)
	}
	fields, code, msg := decodeBody(
		[]byte(`{"svg":"<svg/>","svg_asset_media_id":"`+uuid.NewString()+`","capacity_standing":120}`),
		versionCreateFields,
	)
	if code != "" {
		t.Fatalf("decodeBody rejected the admin payload: %s (%s)", code, msg)
	}
	standing, present, ok := intField(fields, "capacity_standing")
	if !ok || !present || standing != 120 {
		t.Errorf("capacity_standing = (%d, %t, %t); want (120, true, true)", standing, present, ok)
	}
}
