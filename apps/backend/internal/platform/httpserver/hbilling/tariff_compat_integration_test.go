//go:build integration

// tariff_compat_integration_test.go - GET /v1/billing/tariffs/active against a
// real migrated database (task B1-4). Skips without DATABASE_URL; run it on a
// FRESH migrated database (AGENTS.md).
package hbilling

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func openCompatPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func getActive(t *testing.T, h *Handler, orgID uuid.UUID) (int, map[string]any) {
	t.Helper()
	url := "/v1/billing/tariffs/active"
	if orgID != uuid.Nil {
		url += "?org_id=" + orgID.String()
	}
	w := httptest.NewRecorder()
	h.HandleGetActiveTariff(w, httptest.NewRequest(http.MethodGet, url, nil))
	var body map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &body)
	}
	return w.Code, body
}

// An organization with an individual rate sees it in `active`; another
// organization on the price list sees the list's rate; the guard keeps a
// refused caller out of an organization's answer.
func TestTariffsActive_Integration_IndividualRateIsVisible(t *testing.T) {
	pool := openCompatPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	mkOrg := func(tag string) uuid.UUID {
		id := uuid.New()
		sfx := id.String()[:8]
		if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			id, "Tariff B1-4 "+tag+" "+sfx, "tariff-b14-"+tag+"-"+sfx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			bg := context.Background()
			_, _ = pool.Exec(bg, `DELETE FROM org_rate_overrides WHERE org_id = $1`, id)
			_, _ = pool.Exec(bg, `DELETE FROM billing_accounts WHERE org_id = $1`, id)
			_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, id)
		})
		if _, err := billing.EnsureAccount(ctx, q, id, "ES"); err != nil {
			t.Fatal(err)
		}
		return id
	}
	special, plain := mkOrg("special"), mkOrg("plain")

	var overrideID uuid.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO org_rate_overrides (org_id, currency, unseated_minor, seated_minor, effective_from, note, created_by)
VALUES ($1, 'EUR', 50, 100, '2026-01-01T00:00:00Z', 'test', 'test') RETURNING id`, special).Scan(&overrideID); err != nil {
		t.Fatal(err)
	}

	allow := func(denyOrg uuid.UUID) RowOrgAccess {
		return func(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, code, msg string) bool {
			if orgID == denyOrg {
				http.Error(w, msg, http.StatusNotFound)
				return false
			}
			return true
		}
	}
	h := New(q, nil, nil, slog.Default()).WithRowOrgAccess(allow(uuid.Nil))

	code, body := getActive(t, h, special)
	if code != http.StatusOK || body["per_ticket_fee_minor"] != float64(50) || body["plan_name"] != "individual" ||
		body["id"] != overrideID.String() || body["currency"] != "EUR" || body["org_id"] != special.String() {
		t.Fatalf("special org: %d %+v", code, body)
	}

	code, body = getActive(t, h, plain)
	if code != http.StatusOK || body["per_ticket_fee_minor"] != float64(59) || body["plan_name"] != "standard-2026" {
		t.Fatalf("plain org: %d %+v", code, body)
	}

	code, body = getActive(t, h, uuid.Nil)
	if code != http.StatusOK || body["per_ticket_fee_minor"] != float64(59) || body["org_id"] != nil {
		t.Fatalf("default: %d %+v", code, body)
	}

	// A caller outside the organization gets the route's own 404 and never
	// the individual rate.
	hDeny := New(q, nil, nil, slog.Default()).WithRowOrgAccess(allow(special))
	if code, _ = getActive(t, hDeny, special); code != http.StatusNotFound {
		t.Fatalf("outside caller: %d, want 404", code)
	}

	// No guard configured fails closed for an organization, not for the default.
	hOpen := New(q, nil, nil, slog.Default())
	if code, _ = getActive(t, hOpen, special); code != http.StatusServiceUnavailable {
		t.Fatalf("unguarded org read: %d, want 503", code)
	}
	if code, _ = getActive(t, hOpen, uuid.Nil); code != http.StatusOK {
		t.Fatalf("unguarded default read: %d, want 200", code)
	}
}
