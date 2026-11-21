//go:build integration

// seating_template_reset_integration_test.go — a verified template never
// changes without a new verification (spec 08_architecture/64 section 3.4),
// also when the Bil24 import adds a version to it.
package himports

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

func TestImportSeatingPlanVersion_ResetsAVerifiedTemplate(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	q := gen.New(pool)
	org, err := q.InsertOrganization(ctx, "Import template reset "+uuid.NewString()[:8], "import-tpl-"+uuid.NewString(), "ES", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	venueID := uuid.New()
	t.Cleanup(func() {
		c := context.Background()
		for _, stmt := range []string{
			`UPDATE seating_plans SET current_version_id = NULL WHERE owner_org_id = $1`,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
			`DELETE FROM seating_plans WHERE owner_org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(c, stmt, org.ID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Import template hall', 'Europe/Madrid')`,
		venueID, org.ID); err != nil {
		t.Fatalf("venue: %v", err)
	}

	geometry := seating.Canonicalize(sbtPlanWithSeats(t, 7001, 3).Geometry)
	checksum, err := seating.Checksum(geometry)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	raw, err := seating.CanonicalJSON(geometry)
	if err != nil {
		t.Fatalf("geometry: %v", err)
	}
	const name = "Bil24 template hall"
	plan, err := q.InsertSeatingPlan(ctx, venueID, org.ID, name, "assigned_seats", "private", "active", nil)
	if err != nil {
		t.Fatalf("InsertSeatingPlan: %v", err)
	}
	v1, err := q.InsertSeatingPlanVersion(ctx, plan.ID, 1, raw, checksum, nil, 3, 0)
	if err != nil {
		t.Fatalf("InsertSeatingPlanVersion: %v", err)
	}
	if _, err := q.SetSeatingPlanCurrentVersion(ctx, plan.ID, org.ID, &v1.ID); err != nil {
		t.Fatalf("SetSeatingPlanCurrentVersion: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE seating_plans SET visibility = 'operator_verified', verification_state = 'verified', verified_at = now()
	                             WHERE id = $1`, plan.ID); err != nil {
		t.Fatalf("verify fixture: %v", err)
	}

	imp := importPlan{OrgID: org.ID, Request: bil24compat.ImportSessionRequest{
		ActionEvent: bil24compat.ImportSessionActionEvent{SeatingPlanName: name},
	}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	h := &Handler{}
	versionID, err := h.upsertSeatingPlanVersion(ctx, q.WithTx(tx), imp, venueID, "assigned_seats", geometry, checksum)
	if err != nil {
		t.Fatalf("upsertSeatingPlanVersion: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var visibility string
	var state *string
	var current uuid.UUID
	var verifiedAt any
	if err := pool.QueryRow(ctx, `SELECT visibility, verification_state, current_version_id, verified_at FROM seating_plans WHERE id = $1`, plan.ID).
		Scan(&visibility, &state, &current, &verifiedAt); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if current != versionID {
		t.Fatalf("the import added its version to another plan: current %s, imported %s", current, versionID)
	}
	if visibility != "private" || state == nil || *state != "unverified" || verifiedAt != nil {
		t.Errorf("after the import: visibility %s, state %v, verified_at %v; want private, unverified, NULL", visibility, state, verifiedAt)
	}
}
