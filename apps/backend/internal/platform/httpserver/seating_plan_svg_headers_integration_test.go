//go:build integration

// seating_plan_svg_headers_integration_test.go — the two seating-plan SVG
// answers (GET /v1/event-sessions/{id}/layout.svg and GET /compat/bil24/image)
// carry a Content-Security-Policy and nosniff, and serve the stored decor
// through the decor writer: drawing only. The JSON schema of the session
// serves the same decor. Every request goes through the real router.
//
//	DATABASE_URL=... go test -tags integration -count=1 \
//	    ./apps/backend/internal/platform/httpserver/ -run SeatingPlanSVG
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

const planSVGWantCSP = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; sandbox"

func TestSeatingPlanSVG_HeadersAndDecor(t *testing.T) {
	f := newPlanExtraFixture(t)
	ctx := context.Background()

	// Decor stored with content the decor writer leaves out.
	stored := `<rect x="1" y="1" width="9" height="9" fill="#eee" onclick="x()"/><script>x()</script><a href="https://example.com/">t</a>`
	if _, err := f.pool.Exec(ctx,
		`UPDATE seating_plan_versions SET geometry = jsonb_set(geometry, '{decor_svg}', to_jsonb($2::text)) WHERE id = $1`,
		f.versionID, stored); err != nil {
		t.Fatalf("store decor: %v", err)
	}

	three := 3
	bundle := bil24compat.ImportSessionRequest{
		Source:      bil24compat.SourceArena,
		ExternalRef: "plan-svg-" + f.suffix,
		Action:      bil24compat.ImportSessionAction{ActionName: "Plan SVG " + f.suffix},
		ActionEvent: bil24compat.ImportSessionActionEvent{
			Day: time.Now().UTC().Add(60 * 24 * time.Hour).Format("02.01.2006"), Time: "19:00", Currency: "EUR",
			ArenaSeatingPlanVersionID: f.versionID.String(),
		},
		Venue: bil24compat.ImportSessionVenue{ArenaVenueID: f.venueID.String()},
		CategoryList: []bil24compat.ImportSessionCategory{
			{CategoryPriceName: "Parter", Price: 40},
			{CategoryPriceName: "Balcony", Price: 25},
			{CategoryPriceName: "Standing", Price: 15, PlanCategoryIndex: &three, Availability: 5},
		},
	}
	st, out, text := f.request(http.MethodPost, "/v1/organizations/"+f.orgID.String()+"/imports/event-bundle", f.key, bundle)
	if st != http.StatusOK {
		t.Fatalf("import: %d %s", st, text)
	}
	sid, _ := out["session_id"].(string)
	sessionID, err := uuid.Parse(sid)
	if err != nil {
		t.Fatalf("import answer: %s", text)
	}
	eventID := f.eventOf(sessionID)
	for _, sql := range []string{
		`UPDATE events SET status = 'published' WHERE id = $1`,
		`UPDATE sessions SET status = 'scheduled' WHERE event_id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, eventID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.q.PublishEvent(ctx, eventID, f.feedID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	actionEventID, err := compatids.Ensure(ctx, f.pool, compatids.KindActionEvent, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	wantDecor := `<rect x="1" y="1" width="9" height="9" fill="#eee"/><a>t</a>`
	checkSVG := func(t *testing.T, path string) {
		t.Helper()
		resp := integDoRequest(t, f.ts.Client(), http.MethodGet, f.ts.URL+path, "", "")
		body := integReadBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
			t.Errorf("Content-Type = %q", ct)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); csp != planSVGWantCSP {
			t.Errorf("Content-Security-Policy = %q, want %q", csp, planSVGWantCSP)
		}
		if v := resp.Header.Get("X-Content-Type-Options"); v != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", v)
		}
		if !strings.Contains(body, wantDecor) {
			t.Errorf("body has no drawing-only decor %q:\n%s", wantDecor, body)
		}
		for _, bad := range []string{"<script", "onclick", "example.com"} {
			if strings.Contains(body, bad) {
				t.Errorf("body holds %q:\n%s", bad, body)
			}
		}
	}

	t.Run("layout.svg", func(t *testing.T) {
		checkSVG(t, "/v1/event-sessions/"+sessionID.String()+"/layout.svg")
	})
	t.Run("bil24 image", func(t *testing.T) {
		checkSVG(t, "/compat/bil24/image?type=seatingPlan&actionEventId="+strconv.FormatInt(actionEventID, 10)+
			"&userId=0&fid="+strconv.FormatInt(f.fid, 10))
	})
	t.Run("schema JSON", func(t *testing.T) {
		st, _, text := f.request(http.MethodGet, "/v1/event-sessions/"+sessionID.String()+"/schema", "", nil)
		if st != http.StatusOK {
			t.Fatalf("schema: %d %s", st, text)
		}
		var env struct {
			Geometry struct {
				DecorSVG string `json:"decor_svg"`
			} `json:"geometry"`
		}
		if err := json.Unmarshal([]byte(text), &env); err != nil {
			t.Fatalf("decode schema: %v", err)
		}
		if env.Geometry.DecorSVG != wantDecor {
			t.Fatalf("schema decor_svg = %q, want %q", env.Geometry.DecorSVG, wantDecor)
		}
	})
}
