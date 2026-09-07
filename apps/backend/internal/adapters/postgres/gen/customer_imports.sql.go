// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: customer_imports.sql

package gen

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// W1-C7a (feature #519): customer_imports / customer_import_rows (migration
// 0098, spec §3.7 / §12.4).
// ─────────────────────────────────────────────────────────────────────────────

// CustomerImportRow mirrors the customer_imports table.
type CustomerImportRow struct {
	ID           uuid.UUID  `json:"id"`
	OrgID        *uuid.UUID `json:"org_id"`
	SourceLabel  string     `json:"source_label"`
	FileMediaID  uuid.UUID  `json:"file_media_id"`
	Mapping      []byte     `json:"mapping"`
	LegalBasis   string     `json:"legal_basis"`
	Status       string     `json:"status"`
	DryRunReport []byte     `json:"dry_run_report"`
	ApplyReport  []byte     `json:"apply_report"`
	CreatedBy    uuid.UUID  `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func scanCustomerImportRow(row interface {
	Scan(dest ...any) error
}) (CustomerImportRow, error) {
	var r CustomerImportRow
	err := row.Scan(
		&r.ID,
		&r.OrgID,
		&r.SourceLabel,
		&r.FileMediaID,
		&r.Mapping,
		&r.LegalBasis,
		&r.Status,
		&r.DryRunReport,
		&r.ApplyReport,
		&r.CreatedBy,
		&r.CreatedAt,
		&r.UpdatedAt,
	)
	return r, err
}

const insertCustomerImport = `-- name: InsertCustomerImport :one
INSERT INTO customer_imports
    (org_id, source_label, file_media_id, mapping, legal_basis, status, created_by)
VALUES ($1, $2, $3, $4::jsonb, $5, 'uploaded', $6)
RETURNING id, org_id, source_label, file_media_id, mapping, legal_basis,
          status, dry_run_report, apply_report, created_by, created_at, updated_at`

// InsertCustomerImport creates a customer_imports row for POST
// /v1/admin/customer-imports (feature #520, W1-C7b). Status always starts
// at 'uploaded' (migration 0098's customer_imports_status_check).
func (q *Queries) InsertCustomerImport(
	ctx context.Context,
	orgID *uuid.UUID,
	sourceLabel string,
	fileMediaID uuid.UUID,
	mappingJSON []byte,
	legalBasis string,
	createdBy uuid.UUID,
) (CustomerImportRow, error) {
	row := q.db.QueryRow(ctx, insertCustomerImport,
		orgID, sourceLabel, fileMediaID, mappingJSON, legalBasis, createdBy)
	return scanCustomerImportRow(row)
}

const getCustomerImportByID = `-- name: GetCustomerImportByID :one
SELECT id, org_id, source_label, file_media_id, mapping, legal_basis,
       status, dry_run_report, apply_report, created_by, created_at, updated_at
FROM   customer_imports
WHERE  id = $1`

// GetCustomerImportByID loads a customer_imports row for the job handler.
// Returns pgx.ErrNoRows when absent.
func (q *Queries) GetCustomerImportByID(ctx context.Context, id uuid.UUID) (CustomerImportRow, error) {
	row := q.db.QueryRow(ctx, getCustomerImportByID, id)
	return scanCustomerImportRow(row)
}

const updateCustomerImportStatus = `-- name: UpdateCustomerImportStatus :exec
UPDATE customer_imports
SET    status     = $2,
       updated_at = now()
WHERE  id = $1`

// UpdateCustomerImportStatus bumps status with no report change — used for
// the transitional 'dry_run_running' / 'applying' states at run start.
func (q *Queries) UpdateCustomerImportStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := q.db.Exec(ctx, updateCustomerImportStatus, id, status)
	return err
}

const setCustomerImportDryRunReport = `-- name: SetCustomerImportDryRunReport :exec
UPDATE customer_imports
SET    dry_run_report = $2::jsonb,
       status         = 'dry_run_done',
       updated_at     = now()
WHERE  id = $1`

// SetCustomerImportDryRunReport persists the dry-run summary and flips
// status to 'dry_run_done'. Called AFTER the dry-run transaction has been
// rolled back — never from inside it.
func (q *Queries) SetCustomerImportDryRunReport(ctx context.Context, id uuid.UUID, reportJSON []byte) error {
	_, err := q.db.Exec(ctx, setCustomerImportDryRunReport, id, reportJSON)
	return err
}

const setCustomerImportApplyReport = `-- name: SetCustomerImportApplyReport :exec
UPDATE customer_imports
SET    apply_report = $2::jsonb,
       status       = 'applied',
       updated_at   = now()
WHERE  id = $1`

// SetCustomerImportApplyReport persists the apply summary and flips status
// to 'applied'.
func (q *Queries) SetCustomerImportApplyReport(ctx context.Context, id uuid.UUID, reportJSON []byte) error {
	_, err := q.db.Exec(ctx, setCustomerImportApplyReport, id, reportJSON)
	return err
}

const markCustomerImportFailed = `-- name: MarkCustomerImportFailed :exec
UPDATE customer_imports
SET    status     = 'failed',
       updated_at = now()
WHERE  id = $1`

// MarkCustomerImportFailed flips status to 'failed'.
func (q *Queries) MarkCustomerImportFailed(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, markCustomerImportFailed, id)
	return err
}

// CustomerImportRowRow mirrors the customer_import_rows table. Named with
// the doubled "Row" suffix (table CustomerImportRow + sqlc "Row" struct
// convention) to stay unambiguous next to CustomerImportRow above.
type CustomerImportRowRow struct {
	ID                 uuid.UUID  `json:"id"`
	ImportID           uuid.UUID  `json:"import_id"`
	RowNo              int32      `json:"row_no"`
	RowHash            string     `json:"row_hash"`
	Raw                []byte     `json:"raw"`
	ResolvedCustomerID *uuid.UUID `json:"resolved_customer_id"`
	OrgID              *uuid.UUID `json:"org_id"`
	Action             *string    `json:"action"`
	Reason             *string    `json:"reason"`
	CreatedAt          time.Time  `json:"created_at"`
}

func scanCustomerImportRowRow(row interface {
	Scan(dest ...any) error
}) (CustomerImportRowRow, error) {
	var r CustomerImportRowRow
	err := row.Scan(
		&r.ID,
		&r.ImportID,
		&r.RowNo,
		&r.RowHash,
		&r.Raw,
		&r.ResolvedCustomerID,
		&r.OrgID,
		&r.Action,
		&r.Reason,
		&r.CreatedAt,
	)
	return r, err
}

const insertCustomerImportRow = `-- name: InsertCustomerImportRow :one
INSERT INTO customer_import_rows
    (import_id, row_no, row_hash, raw, resolved_customer_id, org_id, action, reason)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
ON CONFLICT (import_id, row_hash) DO NOTHING
RETURNING id, import_id, row_no, row_hash, raw, resolved_customer_id, org_id,
          action, reason, created_at`

// InsertCustomerImportRow records the outcome of one applied row. On a
// (import_id, row_hash) conflict (a re-applied row) no row is inserted and
// this method returns (CustomerImportRowRow{}, pgx.ErrNoRows) — callers
// should fall back to GetCustomerImportRowByHash to report the row as
// already-applied without re-running the resolver.
func (q *Queries) InsertCustomerImportRow(
	ctx context.Context,
	importID uuid.UUID,
	rowNo int32,
	rowHash string,
	rawJSON []byte,
	resolvedCustomerID *uuid.UUID,
	orgID *uuid.UUID,
	action *string,
	reason *string,
) (CustomerImportRowRow, error) {
	row := q.db.QueryRow(ctx, insertCustomerImportRow,
		importID, rowNo, rowHash, rawJSON, resolvedCustomerID, orgID, action, reason)
	r, err := scanCustomerImportRowRow(row)
	if err != nil && errors.Is(err, pgx.ErrNoRows) {
		return CustomerImportRowRow{}, pgx.ErrNoRows
	}
	return r, err
}

const getCustomerImportRowByHash = `-- name: GetCustomerImportRowByHash :one
SELECT id, import_id, row_no, row_hash, raw, resolved_customer_id, org_id,
       action, reason, created_at
FROM   customer_import_rows
WHERE  import_id = $1
  AND  row_hash = $2`

// GetCustomerImportRowByHash is the re-apply idempotency lookup. Returns
// pgx.ErrNoRows when this exact row has not been applied before.
func (q *Queries) GetCustomerImportRowByHash(ctx context.Context, importID uuid.UUID, rowHash string) (CustomerImportRowRow, error) {
	row := q.db.QueryRow(ctx, getCustomerImportRowByHash, importID, rowHash)
	return scanCustomerImportRowRow(row)
}

const listCustomerImportRows = `-- name: ListCustomerImportRows :many
SELECT id, import_id, row_no, row_hash, raw, resolved_customer_id, org_id,
       action, reason, created_at
FROM   customer_import_rows
WHERE  import_id = $1
  AND  ($2::text = '' OR action = $2::text)
ORDER BY row_no`

// ListCustomerImportRows lists customer_import_rows for GET
// /v1/admin/customer-imports/{id}/rows, optionally filtered by action. Pass
// an empty string for actionFilter to list every row regardless of action.
func (q *Queries) ListCustomerImportRows(ctx context.Context, importID uuid.UUID, actionFilter string) ([]CustomerImportRowRow, error) {
	rows, err := q.db.Query(ctx, listCustomerImportRows, importID, actionFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomerImportRowRow
	for rows.Next() {
		r, scanErr := scanCustomerImportRowRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Customer-aggregate additions (migration 0091 tables) the apply path needs.
// ─────────────────────────────────────────────────────────────────────────────

const insertCustomerConsentImport = `-- name: InsertCustomerConsent :exec
INSERT INTO customer_consents (customer_id, org_id, kind, given_at, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (customer_id, org_id, kind) DO NOTHING`

// InsertCustomerConsent records a consent grant. First import wins — a
// conflicting (customer_id, org_id, kind) row is left untouched rather than
// overwritten, so a re-applied row never clobbers an earlier given_at or
// source (see the customer_consents PK).
func (q *Queries) InsertCustomerConsent(ctx context.Context, customerID uuid.UUID, orgID uuid.UUID, kind string, givenAt time.Time, source string) error {
	_, err := q.db.Exec(ctx, insertCustomerConsentImport, customerID, orgID, kind, givenAt, source)
	return err
}

const upsertCustomerOrgLinkStats = `-- name: UpsertCustomerOrgLinkStats :exec
INSERT INTO customer_org_links
    (customer_id, org_id, source, first_order_at, last_order_at, orders_count, tickets_count)
VALUES ($1, $2, 'import', $3, $3, 1, $4)
ON CONFLICT (customer_id, org_id) DO UPDATE
SET orders_count  = customer_org_links.orders_count + 1,
    tickets_count = customer_org_links.tickets_count + EXCLUDED.tickets_count,
    first_order_at = LEAST(customer_org_links.first_order_at, EXCLUDED.first_order_at),
    last_order_at  = GREATEST(customer_org_links.last_order_at, EXCLUDED.last_order_at)`

// UpsertCustomerOrgLinkStats ensures the (customer, org) rollup exists and
// advances its order/ticket counters plus first/last-order timestamps for
// one imported order. Intended to be called once per imported order row —
// unlike UpsertCustomerOrgLink (insert-only-if-absent), this always bumps
// the counters.
func (q *Queries) UpsertCustomerOrgLinkStats(ctx context.Context, customerID uuid.UUID, orgID uuid.UUID, orderAt time.Time, ticketsCount int32) error {
	_, err := q.db.Exec(ctx, upsertCustomerOrgLinkStats, customerID, orgID, orderAt, ticketsCount)
	return err
}
