//go:build integration

// job_integration_test.go — the live-DB proof feature #519 needs alongside
// parsers_test.go's fixture-driven parser coverage: only a real Postgres can
// prove dry_run truly never writes customer_import_rows (its tx is rolled
// back — see job.go's doc comment), that apply is idempotent via the
// UNIQUE(import_id, row_hash) re-apply shortcut, and that a customer's
// identity attached through the import path is never marked verified (spec
// §12.2 step 5 — only PAY_ORDER/explicit confirmation may do that).
package customerimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// TestRunImport_LiveDB drives one full dry_run -> apply -> re-apply cycle
// against a real Postgres and mediastore-backed local file, using a tiny
// hand-built bil24_orders_json export (two orders resolving to the SAME
// customer by email, so Created/Matched counts are distinguishable).
func TestRunImport_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newImportFixture(t, ctx, pool)
	defer f.cleanup()

	// Two orders, same email — the second must resolve to the same customer
	// (Matched) rather than creating a second one.
	orders := []map[string]any{
		{
			"id":   1001,
			"date": "2026-01-05T10:00:00.000+00:00",
			"user": map[string]any{"id": 1, "email": ""},
			"frontend": map[string]any{
				"name": "https://www.example-" + f.suffix + ".com/",
			},
			"ticketList": []map[string]any{
				{"discountReason": "EARLYBIRD", "actionEvent": map[string]any{"actionName": "Wine Tasting"}},
			},
			"ticketQuantity": 1,
			"email":          "import-" + f.suffix + "@example.test",
			"phone":          "+972500000" + f.digits,
			"fullName":       "Import Fixture " + f.suffix,
		},
		{
			"id":   1002,
			"date": "2026-01-06T10:00:00.000+00:00",
			"user": map[string]any{"id": 1, "email": ""},
			"frontend": map[string]any{
				"name": "https://www.example-" + f.suffix + ".com/",
			},
			"ticketList":     []map[string]any{},
			"ticketQuantity": 2,
			"email":          "import-" + f.suffix + "@example.test",
			"phone":          "+972500000" + f.digits,
			"fullName":       "Import Fixture " + f.suffix,
		},
	}
	fileBytes, err := json.Marshal(orders)
	if err != nil {
		t.Fatalf("marshal fixture orders: %v", err)
	}

	mapping := Mapping{
		Frontends: map[string]uuid.UUID{
			"https://www.example-" + f.suffix + ".com/": f.orgID,
		},
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}

	importID := f.seedImport(t, ctx, fileBytes, mappingJSON)

	opts := Options{
		Pool:  pool,
		Media: f.media,
		Now:   func() time.Time { return time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) },
	}

	// ── dry_run: must never write customer_import_rows. ────────────────────
	dryReport, err := RunImport(ctx, opts, importID, ModeDryRun)
	if err != nil {
		t.Fatalf("RunImport dry_run: %v", err)
	}
	if dryReport.Rows != 2 {
		t.Fatalf("dry_run Rows = %d, want 2", dryReport.Rows)
	}
	if sum := dryReport.Created + dryReport.Matched + dryReport.MergeCandidates + dryReport.Skipped; sum != dryReport.Rows {
		t.Fatalf("dry_run tallies don't sum to Rows: created=%d matched=%d merge=%d skipped=%d rows=%d",
			dryReport.Created, dryReport.Matched, dryReport.MergeCandidates, dryReport.Skipped, dryReport.Rows)
	}
	if dryReport.Created != 1 || dryReport.Matched != 1 {
		t.Fatalf("dry_run Created/Matched = %d/%d, want 1/1 (same email across two rows)", dryReport.Created, dryReport.Matched)
	}
	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM customer_import_rows WHERE import_id = $1`, importID).Scan(&rowCount); err != nil {
		t.Fatalf("count customer_import_rows after dry_run: %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("dry_run wrote %d customer_import_rows, want 0 (dry_run must never write rows)", rowCount)
	}
	var custCountAfterDry int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_identities WHERE source = $1`, "import:bil24_orders_json",
	).Scan(&custCountAfterDry); err != nil {
		t.Fatalf("count identities after dry_run: %v", err)
	}
	if custCountAfterDry != 0 {
		t.Fatalf("dry_run left %d identities with source=import:bil24_orders_json behind (tx must roll back)", custCountAfterDry)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM customer_imports WHERE id = $1`, importID).Scan(&status); err != nil {
		t.Fatalf("read import status after dry_run: %v", err)
	}
	if status != "dry_run_done" {
		t.Fatalf("import status after dry_run = %q, want dry_run_done", status)
	}

	// ── apply: must write exactly one row per source row, create exactly
	// one customer, and never mark the attached identity verified. ────────
	applyReport, err := RunImport(ctx, opts, importID, ModeApply)
	if err != nil {
		t.Fatalf("RunImport apply: %v", err)
	}
	if applyReport.Rows != 2 || applyReport.Created != 1 || applyReport.Matched != 1 {
		t.Fatalf("apply report = %+v, want Rows=2 Created=1 Matched=1", applyReport)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM customer_import_rows WHERE import_id = $1`, importID).Scan(&rowCount); err != nil {
		t.Fatalf("count customer_import_rows after apply: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("apply wrote %d customer_import_rows, want 2", rowCount)
	}

	var custID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT ci.id FROM customers ci
		 JOIN customer_identities id ON id.customer_id = ci.id
		 WHERE id.kind = 'email' AND id.value_normalized = $1`,
		"import-"+f.suffix+"@example.test",
	).Scan(&custID); err != nil {
		t.Fatalf("look up resolved customer: %v", err)
	}

	var verifiedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT verified_at FROM customer_identities WHERE customer_id = $1 AND kind = 'email'`, custID,
	).Scan(&verifiedAt); err != nil {
		t.Fatalf("read identity verified_at: %v", err)
	}
	if verifiedAt != nil {
		t.Fatalf("import-attached email identity has verified_at = %v, want nil (import must never call MarkVerified)", *verifiedAt)
	}

	var consentCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_consents WHERE customer_id = $1 AND org_id = $2 AND kind = 'marketing'`,
		custID, f.orgID,
	).Scan(&consentCount); err != nil {
		t.Fatalf("count consents: %v", err)
	}
	if consentCount != 1 {
		t.Fatalf("marketing consent rows = %d, want 1 (first-import-wins, second row must not duplicate)", consentCount)
	}

	var ordersCount, ticketsCount int
	if err := pool.QueryRow(ctx,
		`SELECT orders_count, tickets_count FROM customer_org_links WHERE customer_id = $1 AND org_id = $2`,
		custID, f.orgID,
	).Scan(&ordersCount, &ticketsCount); err != nil {
		t.Fatalf("read org link stats: %v", err)
	}
	if ordersCount != 2 || ticketsCount != 3 {
		t.Fatalf("org link stats = orders=%d tickets=%d, want orders=2 tickets=3 (1+2 across the two rows)", ordersCount, ticketsCount)
	}

	if err := pool.QueryRow(ctx, `SELECT status FROM customer_imports WHERE id = $1`, importID).Scan(&status); err != nil {
		t.Fatalf("read import status after apply: %v", err)
	}
	if status != "applied" {
		t.Fatalf("import status after apply = %q, want applied", status)
	}

	// ── re-apply: idempotent via the row_hash shortcut — no new customer
	// rows, no changed counters, tallies come from tallyExisting. ─────────
	reapplyReport, err := RunImport(ctx, opts, importID, ModeApply)
	if err != nil {
		t.Fatalf("RunImport re-apply: %v", err)
	}
	if reapplyReport.Rows != 2 || reapplyReport.Created != 1 || reapplyReport.Matched != 1 {
		t.Fatalf("re-apply report = %+v, want Rows=2 Created=1 Matched=1 (folded from tallyExisting)", reapplyReport)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM customer_import_rows WHERE import_id = $1`, importID).Scan(&rowCount); err != nil {
		t.Fatalf("count customer_import_rows after re-apply: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("re-apply wrote %d customer_import_rows (want still 2 — no duplicate insert)", rowCount)
	}
	if err := pool.QueryRow(ctx,
		`SELECT orders_count, tickets_count FROM customer_org_links WHERE customer_id = $1 AND org_id = $2`,
		custID, f.orgID,
	).Scan(&ordersCount, &ticketsCount); err != nil {
		t.Fatalf("re-read org link stats: %v", err)
	}
	if ordersCount != 2 || ticketsCount != 3 {
		t.Fatalf("org link stats after re-apply = orders=%d tickets=%d, want unchanged orders=2 tickets=3", ordersCount, ticketsCount)
	}
}

// importFixture seeds the row graph customer_imports needs: an organization,
// a user (created_by), a local-storage-backed mediastore.Repo, and the
// uploaded file's media_objects row.
type importFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	media   *mediastore.Repo
	orgID   uuid.UUID
	userID  uuid.UUID
	suffix  string
	digits  string
	rowIDs  []uuid.UUID
	fileIDs []uuid.UUID
}

func newImportFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *importFixture {
	t.Helper()

	orgID := uuid.New()
	userID := uuid.New()
	suffix := orgID.String()[:8]

	local, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocalStorage: %v", err)
	}
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: local})
	if err != nil {
		t.Fatalf("mediastore.New: %v", err)
	}

	f := &importFixture{t: t, pool: pool, media: repo, orgID: orgID, userID: userID, suffix: suffix, digits: suffix[:6]}

	if _, err := pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Import Fixture Org "+suffix, "import-fixture-"+suffix,
	); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`,
		userID, "import-fixture-"+suffix+"@example.test",
	); err != nil {
		f.cleanup()
		t.Fatalf("seed user: %v", err)
	}
	return f
}

// seedImport stores fileBytes through the fixture's mediastore.Repo (the
// same read path RunImport uses via loadAndParse) and inserts the
// customer_imports row referencing it.
func (f *importFixture) seedImport(t *testing.T, ctx context.Context, fileBytes, mappingJSON []byte) uuid.UUID {
	t.Helper()

	key := "customer-imports/" + uuid.NewString() + ".json"
	if _, err := f.media.Storage().Put(ctx, storage.PutInput{
		Key:         key,
		ContentType: "application/json",
		Size:        int64(len(fileBytes)),
		Body:        bytes.NewReader(fileBytes),
	}); err != nil {
		t.Fatalf("put fixture file: %v", err)
	}
	// owner_type has no obviously-fitting value for a raw customer/order
	// export — media_objects_owner_type_check (0052/0078/0082) enumerates
	// UI-asset kinds (org_logo, event_poster, artist_photo, seating_plan_svg,
	// session_poster), none of which describe an import file. Repo.Insert
	// itself does not validate against mediastore.AllowedOwnerTypes (that
	// allowlist only gates POST /v1/media), so any DB-valid value works for
	// seeding purposes; org_logo is reused here as a harmless placeholder.
	sum := sha256.Sum256(fileBytes)
	obj, err := f.media.Insert(ctx, mediastore.InsertInput{
		OrgID:          &f.orgID,
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
	f.fileIDs = append(f.fileIDs, obj.ID)

	importID := uuid.New()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO customer_imports (id, org_id, source_label, file_media_id, mapping, legal_basis, created_by)
		 VALUES ($1, $2, 'bil24_orders_json', $3, $4::jsonb, 'organizer_contract', $5)`,
		importID, f.orgID, obj.ID, mappingJSON, f.userID,
	); err != nil {
		t.Fatalf("seed customer_imports row: %v", err)
	}
	f.rowIDs = append(f.rowIDs, importID)
	return importID
}

func (f *importFixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("import fixture cleanup (%s): %v", sql, err)
		}
	}

	// Resolve the customer(s) this fixture's import created BEFORE deleting
	// the identities that key the lookup.
	var custIDs []uuid.UUID
	rows, err := f.pool.Query(ctx,
		`SELECT DISTINCT customer_id FROM customer_identities WHERE value_normalized LIKE $1`,
		"%"+f.suffix+"%")
	if err != nil {
		f.t.Logf("import fixture cleanup (resolve customer ids): %v", err)
	} else {
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err == nil {
				custIDs = append(custIDs, id)
			}
		}
		rows.Close()
	}

	exec(`DELETE FROM customer_consents WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM customer_org_links WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM customer_import_rows WHERE import_id = ANY($1)`, f.rowIDs)
	exec(`DELETE FROM customer_imports WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM media_objects WHERE id = ANY($1)`, f.fileIDs)
	exec(`DELETE FROM customer_identities WHERE customer_id = ANY($1)`, custIDs)
	exec(`DELETE FROM customers WHERE id = ANY($1)`, custIDs)
	exec(`DELETE FROM users WHERE id = $1`, f.userID)
	exec(`DELETE FROM organizations WHERE id = $1`, f.orgID)
}
