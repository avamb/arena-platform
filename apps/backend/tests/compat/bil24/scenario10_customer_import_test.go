//go:build integration

// scenario10_customer_import_test.go — spec §15.3 scenario 10, the
// platform.superadmin customer-imports admin surface (feature #520, W1-C7b,
// epic #468, spec §12.4): create an import against the real
// bil24_orders_pseudonymized.json fixture, dry-run it, apply it, and prove
// the apply is idempotent — all driven through the real HTTP handlers
// (customerimport.RunImport itself, feature #519, is exercised directly by
// TestRunImport_LiveDB in the customerimport package; this scenario proves
// the HTTP wiring on top of it).
package compat_bil24_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// sc10FixturePath is the real, pseudonymized WooCommerce/Bil24 export used
// across the compat suite (also the fixture behind
// customerimport.TestParseBil24OrdersJSON_Fixture, which pins it at exactly
// 68 orders / rows). Its single frontend is
// "https://www.einatwinery.com/" — see testdata/wp/README.md.
const sc10FixturePath = "testdata/wp/bil24_orders_pseudonymized.json"

// sc10FixtureFrontend is the sole frontend URL present in the fixture file.
const sc10FixtureFrontend = "https://www.einatwinery.com/"

// sc10FixtureRows is TestParseBil24OrdersJSON_Fixture's pinned row count for
// sc10FixturePath — kept here as a named constant so a future fixture edit
// only needs updating once per package.
const sc10FixtureRows = 68

// runScenario10CustomerImport is the body of the
// 10_customer_import_c7_dry_run_then_apply sub-test.
func runScenario10CustomerImport(t *testing.T, st *harnessState) {
	t.Helper()
	ctx := context.Background()
	base := startHarnessServer(t, st)

	// ── fixture: a dedicated organization + platform_superadmin user ───────
	// A fresh org (rather than st.OrgID) keeps this scenario's customer/
	// org-link rollups isolated from every other scenario's fixtures.
	orgID := uuid.New()
	suffix := orgID.String()[:8]
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "W1-520 Import Org "+suffix, "w1-520-"+suffix,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
			t.Logf("cleanup org: %v", err)
		}
	})

	userID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, email_verified_at)
		 VALUES ($1, $2, 'x', now())`,
		userID, "harness-520-admin-"+suffix+"@example.test",
	); err != nil {
		t.Fatalf("seed superadmin user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup superadmin user: %v", err)
		}
	})

	// ── mint the platform_superadmin JWT ────────────────────────────────────
	// DBChecker.Check (rbac_checker.go) trusts the JWT's Roles claim
	// directly and migration 0034 grants platform_superadmin the
	// superadmin.read permission — no user_roles row required.
	stub := harnessStubAuth(t)
	adminJWT, _, err := stub.IssueToken(ctx, auth.IssueRequest{
		ActorID: userID.String(),
		Roles:   []string{"platform_superadmin"},
		TTL:     time.Hour,
	})
	if err != nil {
		t.Fatalf("mint platform_superadmin jwt: %v", err)
	}
	adminHeaders := map[string]string{"X-Admin-Reason": "feature #520 scenario 10 harness"}

	// ── seed the media_objects row the import references ────────────────────
	fileBytes, err := os.ReadFile(filepath.Join(sc10FixturePath))
	if err != nil {
		t.Fatalf("read fixture %s: %v", sc10FixturePath, err)
	}
	key := "customer-imports/" + uuid.NewString() + ".json"
	if _, err := st.Media.Storage().Put(ctx, storage.PutInput{
		Key:         key,
		ContentType: "application/json",
		Size:        int64(len(fileBytes)),
		Body:        bytes.NewReader(fileBytes),
	}); err != nil {
		t.Fatalf("put fixture file: %v", err)
	}
	// owner_type has no dedicated value for a raw customer/order export;
	// media_objects_owner_type_check enumerates only UI-asset kinds, and
	// mediastore.Repo.Insert itself does not validate against
	// mediastore.AllowedOwnerTypes (that allowlist only gates POST
	// /v1/media) — org_logo is reused as a harmless DB-valid placeholder,
	// matching customerimport/job_integration_test.go's fixture.
	sum := sha256.Sum256(fileBytes)
	mediaObj, err := st.Media.Insert(ctx, mediastore.InsertInput{
		OrgID:          &orgID,
		OwnerType:      "org_logo",
		StorageBackend: "local",
		StorageKey:     key,
		ContentType:    "application/json",
		ByteSize:       int64(len(fileBytes)),
		ChecksumSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("insert media object: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM media_objects WHERE id = $1`, mediaObj.ID); err != nil {
			t.Logf("cleanup media object: %v", err)
		}
	})

	// ── POST /v1/admin/customer-imports ──────────────────────────────────────
	mapping := map[string]any{
		"frontends": map[string]any{
			sc10FixtureFrontend: orgID.String(),
		},
	}
	createStatus, createResp := restJSON(t, base, "POST", "/v1/admin/customer-imports", adminJWT, adminHeaders,
		map[string]any{
			"org_id":        orgID.String(),
			"source_label":  "bil24_orders_json",
			"file_media_id": mediaObj.ID.String(),
			"mapping":       mapping,
			"legal_basis":   "organizer_contract",
		})
	if createStatus != 201 {
		t.Fatalf("POST admin/customer-imports status = %d, want 201 (body %v)", createStatus, createResp)
	}
	importIDStr, _ := createResp["id"].(string)
	if importIDStr == "" {
		t.Fatalf("POST admin/customer-imports response missing id: %v", createResp)
	}
	if got, _ := createResp["status"].(string); got != "uploaded" {
		t.Fatalf("POST admin/customer-imports status field = %q, want uploaded (body %v)", got, createResp)
	}
	// customer_import_rows FKs import_id ON DELETE CASCADE, but consents/
	// org-links do not cascade from the import — they must be cleared
	// before the org itself is deleted (t.Cleanup is LIFO; registering
	// this after the org cleanup above guarantees it runs first).
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := st.Pool.Exec(cleanupCtx,
			`DELETE FROM customer_consents WHERE org_id = $1`, orgID); err != nil {
			t.Logf("cleanup consents: %v", err)
		}
		if _, err := st.Pool.Exec(cleanupCtx,
			`DELETE FROM customer_org_links WHERE org_id = $1`, orgID); err != nil {
			t.Logf("cleanup org links: %v", err)
		}
		var custIDs []uuid.UUID
		rows, err := st.Pool.Query(cleanupCtx,
			`SELECT DISTINCT customer_id FROM customer_identities
			 WHERE customer_id IN (SELECT resolved_customer_id FROM customer_import_rows
			                        WHERE import_id = $1 AND resolved_customer_id IS NOT NULL)`,
			importIDStr)
		if err != nil {
			t.Logf("cleanup resolve customer ids: %v", err)
		} else {
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err == nil {
					custIDs = append(custIDs, id)
				}
			}
			rows.Close()
		}
		if _, err := st.Pool.Exec(cleanupCtx,
			`DELETE FROM customer_imports WHERE id = $1`, importIDStr); err != nil {
			t.Logf("cleanup customer_imports: %v", err)
		}
		if len(custIDs) > 0 {
			if _, err := st.Pool.Exec(cleanupCtx,
				`DELETE FROM customer_identities WHERE customer_id = ANY($1)`, custIDs); err != nil {
				t.Logf("cleanup customer identities: %v", err)
			}
			if _, err := st.Pool.Exec(cleanupCtx,
				`DELETE FROM customers WHERE id = ANY($1)`, custIDs); err != nil {
				t.Logf("cleanup customers: %v", err)
			}
		}
	})

	// ── GET the created import back ──────────────────────────────────────────
	getStatus, getResp := restJSON(t, base, "GET", "/v1/admin/customer-imports/"+importIDStr, adminJWT, adminHeaders, nil)
	if getStatus != 200 {
		t.Fatalf("GET admin/customer-imports/{id} status = %d, want 200 (body %v)", getStatus, getResp)
	}
	if got, _ := getResp["status"].(string); got != "uploaded" {
		t.Fatalf("GET admin/customer-imports/{id} status field = %q, want uploaded", got)
	}

	// ── POST .../dry-run: must report the fixture's row count and NEVER
	// write customer_import_rows (job.go rolls its dry_run tx back). ────────
	dryStatus, dryResp := restJSON(t, base, "POST", "/v1/admin/customer-imports/"+importIDStr+"/dry-run", adminJWT, adminHeaders, nil)
	if dryStatus != 200 {
		t.Fatalf("POST dry-run status = %d, want 200 (body %v)", dryStatus, dryResp)
	}
	dryRows := numberField(t, dryResp, "rows")
	if dryRows != float64(sc10FixtureRows) {
		t.Fatalf("dry-run rows = %v, want %d", dryRows, sc10FixtureRows)
	}
	sc10AssertTallySum(t, "dry-run", dryResp)

	var rowCount int
	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_import_rows WHERE import_id = $1`, importIDStr,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count customer_import_rows after dry-run: %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("dry-run wrote %d customer_import_rows, want 0 (dry_run must never write rows)", rowCount)
	}

	// ── POST .../apply: writes one customer_import_rows entry per source
	// row and flips status to applied. ──────────────────────────────────────
	applyStatus, applyResp := restJSON(t, base, "POST", "/v1/admin/customer-imports/"+importIDStr+"/apply", adminJWT, adminHeaders, nil)
	if applyStatus != 200 {
		t.Fatalf("POST apply status = %d, want 200 (body %v)", applyStatus, applyResp)
	}
	applyRows := numberField(t, applyResp, "rows")
	if applyRows != float64(sc10FixtureRows) {
		t.Fatalf("apply rows = %v, want %d", applyRows, sc10FixtureRows)
	}
	sc10AssertTallySum(t, "apply", applyResp)

	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_import_rows WHERE import_id = $1`, importIDStr,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count customer_import_rows after apply: %v", err)
	}
	if rowCount != sc10FixtureRows {
		t.Fatalf("apply wrote %d customer_import_rows, want %d", rowCount, sc10FixtureRows)
	}

	var status string
	if err := st.Pool.QueryRow(ctx,
		`SELECT status FROM customer_imports WHERE id = $1`, importIDStr,
	).Scan(&status); err != nil {
		t.Fatalf("read import status after apply: %v", err)
	}
	if status != "applied" {
		t.Fatalf("import status after apply = %q, want applied", status)
	}

	// ── GET .../rows: total must match the applied row count. ───────────────
	rowsStatus, rowsResp := restJSON(t, base, "GET", "/v1/admin/customer-imports/"+importIDStr+"/rows", adminJWT, adminHeaders, nil)
	if rowsStatus != 200 {
		t.Fatalf("GET rows status = %d, want 200 (body %v)", rowsStatus, rowsResp)
	}
	if total := numberField(t, rowsResp, "total"); total != float64(sc10FixtureRows) {
		t.Fatalf("GET rows total = %v, want %d", total, sc10FixtureRows)
	}

	// ── re-apply: idempotent via the (import_id, row_hash) shortcut — same
	// tallies, no new customer_import_rows written. ─────────────────────────
	reapplyStatus, reapplyResp := restJSON(t, base, "POST", "/v1/admin/customer-imports/"+importIDStr+"/apply", adminJWT, adminHeaders, nil)
	if reapplyStatus != 200 {
		t.Fatalf("POST re-apply status = %d, want 200 (body %v)", reapplyStatus, reapplyResp)
	}
	reapplyRows := numberField(t, reapplyResp, "rows")
	if reapplyRows != applyRows {
		t.Fatalf("re-apply rows = %v, want same as first apply (%v)", reapplyRows, applyRows)
	}
	if got, want := numberField(t, reapplyResp, "created"), numberField(t, applyResp, "created"); got != want {
		t.Fatalf("re-apply created = %v, want unchanged %v", got, want)
	}
	if got, want := numberField(t, reapplyResp, "matched"), numberField(t, applyResp, "matched"); got != want {
		t.Fatalf("re-apply matched = %v, want unchanged %v", got, want)
	}

	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_import_rows WHERE import_id = $1`, importIDStr,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count customer_import_rows after re-apply: %v", err)
	}
	if rowCount != sc10FixtureRows {
		t.Fatalf("re-apply left %d customer_import_rows, want unchanged %d (no duplicate insert)", rowCount, sc10FixtureRows)
	}
}

// sc10AssertTallySum checks the customerimport.Report invariant that
// created+matched+merge_candidates+skipped always sums to rows, regardless
// of the real fixture's actual per-row resolution mix.
func sc10AssertTallySum(t *testing.T, phase string, resp map[string]interface{}) {
	t.Helper()
	rows := numberField(t, resp, "rows")
	sum := numberField(t, resp, "created") + numberField(t, resp, "matched") +
		numberField(t, resp, "merge_candidates") + numberField(t, resp, "skipped")
	if sum != rows {
		t.Fatalf("%s tallies don't sum to rows: created=%v matched=%v merge_candidates=%v skipped=%v rows=%v",
			phase, resp["created"], resp["matched"], resp["merge_candidates"], resp["skipped"], rows)
	}
}
