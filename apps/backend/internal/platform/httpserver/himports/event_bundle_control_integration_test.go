//go:build integration

// event_bundle_control_integration_test.go — MA-40 (migration 0228): an
// event-bundle import never touches a control_only session or the event that
// holds one. Addressed by arenaSessionId the bundle answers 409
// session.sales_mode_locked; addressed by the event's id it answers 409
// event.control_only; both as a real import and as a dry run, and neither
// writes a tier, a place, a ledger row or anything else.
package himports

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestEventBundle_ControlSession_IsNeverImportedInto(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	// An ordinary event first, so the import has something real to compare
	// with (and the venue the bundle names exists).
	rec, ordinary := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("baseline import: %d %s", rec.Code, rec.Body.String())
	}

	// The control container: a draft event with one control_only session.
	venueID, eventID, sessionID := uuid.New(), uuid.New(), uuid.New()
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venueID, f.orgID, "Ctl Hall " + venueID.String()[:8]}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'Ctl container', 'draft', 'private')`, []any{eventID, f.orgID}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source, sales_mode)
		  VALUES ($1, $2, $3, $4, $5, 80, 'scheduled', 'general_admission', 'EUR', 'override', 'control_only')`,
			[]any{sessionID, eventID, venueID, time.Now().UTC().Add(20 * 24 * time.Hour), time.Now().UTC().Add(20*24*time.Hour + 3*time.Hour)}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture (%.60s): %v", s.sql, err)
		}
	}

	post := func(dry bool, body bil24compat.ImportSessionRequest) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		url := "/v1/organizations/" + f.orgID.String() + "/imports/event-bundle"
		if dry {
			url += "?dry_run=true"
		}
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("org_id", f.orgID.String())
		c := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		c = auth.WithActor(c, auth.Actor{ID: f.userID.String(), Type: auth.ActorTypeUser})
		rec := httptest.NewRecorder()
		h.HandleEventBundle(rec, req.WithContext(c))
		return rec
	}

	bySession := func() bil24compat.ImportSessionRequest {
		b := f.payload()
		b.ExternalRef = ""
		b.Action.ArenaEventID = eventID.String()
		b.ActionEvent.ArenaSessionID = sessionID.String()
		return b
	}
	byEvent := func() bil24compat.ImportSessionRequest {
		b := f.payload()
		b.ExternalRef = f.externalRef + ":on-control-event"
		b.Action.ArenaEventID = eventID.String()
		return b
	}

	for _, tc := range []struct {
		name     string
		body     func() bil24compat.ImportSessionRequest
		wantCode string
	}{
		{"addressed by arenaSessionId", bySession, "session.sales_mode_locked"},
		{"addressed by the event id", byEvent, "event.control_only"},
	} {
		for _, dry := range []bool{false, true} {
			name := tc.name + " (real)"
			if dry {
				name = tc.name + " (dry run)"
			}
			t.Run(name, func(t *testing.T) {
				before := orgFootprint(ctx, t, pool, f.orgID)
				rec := post(dry, tc.body())
				if dry {
					// A dry run answers 200 and reports the refusal it would
					// give: valid false, one error carrying the 409 and the code.
					var out struct {
						Valid  bool `json:"valid"`
						Errors []struct {
							Code   string `json:"code"`
							Status int    `json:"status"`
						} `json:"errors"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
						t.Fatalf("decode dry run: %v; body=%s", err, rec.Body.String())
					}
					if rec.Code != http.StatusOK || out.Valid || len(out.Errors) != 1 ||
						out.Errors[0].Status != http.StatusConflict || out.Errors[0].Code != tc.wantCode {
						t.Fatalf("dry run: status %d body %s, want valid false with one 409 %s", rec.Code, rec.Body.String(), tc.wantCode)
					}
				} else if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), tc.wantCode) {
					t.Fatalf("status %d body %s, want 409 %s", rec.Code, rec.Body.String(), tc.wantCode)
				}
				assertSameFootprint(t, name, before, orgFootprint(ctx, t, pool, f.orgID))
				var tiers, ledger, seats int
				if err := pool.QueryRow(ctx, `SELECT
				    (SELECT count(*) FROM ticket_tiers WHERE session_id = $1),
				    (SELECT count(*) FROM inventory_ledger WHERE session_id = $1),
				    (SELECT count(*) FROM session_seats WHERE session_id = $1)`, sessionID).Scan(&tiers, &ledger, &seats); err != nil {
					t.Fatal(err)
				}
				if tiers+ledger+seats != 0 {
					t.Errorf("the control session got %d tiers, %d ledger rows, %d places", tiers, ledger, seats)
				}
				var mode string
				var live int
				_ = pool.QueryRow(ctx, `SELECT sales_mode FROM sessions WHERE id = $1`, sessionID).Scan(&mode)
				_ = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE event_id = $1 AND deleted_at IS NULL`, eventID).Scan(&live)
				if mode != "control_only" || live != 1 {
					t.Errorf("the container changed: sales_mode %q, %d live sessions", mode, live)
				}
			})
		}
	}

	// Regression: the ordinary event is still importable by its own ids.
	edit := f.payload()
	edit.ExternalRef = ""
	edit.Action.ArenaEventID = ordinary.EventID.String()
	edit.ActionEvent.ArenaSessionID = ordinary.SessionID.String()
	if rec := post(false, edit); rec.Code != http.StatusOK {
		t.Errorf("edit of the ordinary session: %d %s", rec.Code, rec.Body.String())
	}
}
