// public_schema_test.go — SEAT-B3 (feature #307) contract tests for the
// small/medium-venue public seating endpoints implemented in
// public_schema.go.
//
// The tests exercise the ETag negotiation, the seat_key → category_index
// projection, and the empty-queries 503 gate. Database-backed integration
// coverage lives under the migrations / testcontainers harness; these
// unit tests are stdlib-only so they run without a live PostgreSQL.
package hseating

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// TestSeatB3_MatchesETag pins every branch of the strong-ETag validator
// used by /schema:
//   - empty header ⇒ no match
//   - wildcard "*" ⇒ match
//   - exact strong match anywhere in a comma-separated list ⇒ match
//   - weak validators W/"…" ⇒ never match a strong tag (RFC 7232 §2.3.2)
//   - unrelated tag ⇒ no match
func TestSeatB3_MatchesETag(t *testing.T) {
	t.Parallel()

	strong := `"abc123"`
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"empty header", "", false},
		{"wildcard", "*", true},
		{"exact match", `"abc123"`, true},
		{"match in list", `"other", "abc123"`, true},
		{"match with surrounding OWS", `  "abc123"  `, true},
		{"weak validator never matches", `W/"abc123"`, false},
		{"unrelated tag", `"different"`, false},
		{"mixed weak + unrelated", `W/"abc123", "different"`, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchesETag(tc.header, strong)
			if got != tc.want {
				t.Fatalf("matchesETag(%q, %q) = %v, want %v", tc.header, strong, got, tc.want)
			}
		})
	}
}

// TestSeatB3_SeatKeyIndex confirms the shared geometry walker maps every
// seat.key to its Seat (carrying the category_index), and falls back to a
// synthesised "<section>|<row>|<number>" key when the imported version
// omits seat.key (e.g. hand-authored geometry payloads that predate the
// canonicaliser).
func TestSeatB3_SeatKeyIndex(t *testing.T) {
	t.Parallel()

	g := seating.Geometry{
		Categories: []seating.Category{
			{Index: 1, Name: "First"},
			{Index: 2, Name: "Second"},
		},
		Sections: []seating.Section{
			{
				Key:  "parter",
				Name: "Parter",
				Rows: []seating.Row{{
					Key:  "1",
					Name: "1",
					Seats: []seating.Seat{
						{Key: "parter|1|5", Number: "5", CategoryIndex: 1},
						// fallback: no explicit key — should synth
						{Key: "", Number: "7", CategoryIndex: 2},
					},
				}},
			},
		},
	}
	idx := seatKeyIndex(g)
	if got, want := idx["parter|1|5"].CategoryIndex, 1; got != want {
		t.Fatalf("explicit seat.key mapping = %d, want %d", got, want)
	}
	if got, want := idx["parter|1|7"].CategoryIndex, 2; got != want {
		t.Fatalf("fallback seat.key mapping = %d, want %d", got, want)
	}
	if len(idx) != 2 {
		t.Fatalf("index size = %d, want 2 (entries: %v)", len(idx), idx)
	}
}

// TestSeatB3_HandlersReturn503WhenQueriesUnwired verifies the shared
// dependency-unavailable gate on both public endpoints. This is the same
// self-gate mount_seating.go relies on to keep the route registered even
// when the seating pool is unavailable, so the contract must not
// silently degrade to a 500 or a panic.
func TestSeatB3_HandlersReturn503WhenQueriesUnwired(t *testing.T) {
	t.Parallel()

	h := New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := chi.NewRouter()
	r.Get("/v1/event-sessions/{id}/schema", h.HandleGetPublicSessionSchema)
	r.Get("/v1/event-sessions/{id}/seat-status", h.HandleGetPublicSessionSeatStatus)

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	// Any well-formed UUID — the queries gate fires before the path
	// parameter is even parsed.
	const someID = "019f4d67-0000-7000-8000-000000000000"
	for _, path := range []string{
		"/v1/event-sessions/" + someID + "/schema",
		"/v1/event-sessions/" + someID + "/seat-status",
		"/v1/event-sessions/" + someID + "/seat-status?since_version=5",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status = %d, want 503; body=%s", path, resp.StatusCode, string(body))
		}
		if !strings.Contains(string(body), "dependency.database_unavailable") {
			t.Errorf("GET %s: body missing dependency.database_unavailable envelope; got %s", path, string(body))
		}
	}
}

// TestSeatB3_SchemaCacheHeaders pins the header contract expected by
// downstream caches. The values are exported as consts so tests that
// exercise the on-wire responses (in the migration harness) can share
// them with clients; this guardrail catches accidental changes.
func TestSeatB3_SchemaCacheHeaders(t *testing.T) {
	t.Parallel()

	if schemaCacheControl != "public, max-age=86400, immutable" {
		t.Errorf("schemaCacheControl = %q; SEAT-B3 requires 'public, max-age=86400, immutable'", schemaCacheControl)
	}
	if seatStatusCacheControl != "no-cache" {
		t.Errorf("seatStatusCacheControl = %q; SEAT-B3 requires 'no-cache' for delta correctness", seatStatusCacheControl)
	}
}
