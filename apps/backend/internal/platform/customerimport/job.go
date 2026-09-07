// job.go — the customer.import worker job (feature #519, spec §3.7 /
// §12.4): dry_run/apply over a previously-uploaded customer_imports file,
// feeding each parsed row through customers.Resolve (§12.2) without ever
// auto-merging.
//
// dry_run NEVER writes customer_import_rows — the whole loop runs inside a
// pgx.Tx that is unconditionally rolled back (see 0098's table comment);
// only the resulting Report is persisted, via SetCustomerImportDryRunReport
// called AFTER the rollback. apply runs inside one committed tx and writes
// one customer_import_rows row per source row, using
// UNIQUE(import_id, row_hash) as the re-apply idempotency guard: a row
// whose hash is already recorded is reported "matched" (its previously
// recorded action, verbatim) without touching Resolve, consents or the
// org-link counters again — see GetCustomerImportRowByHash's doc comment.
package customerimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// JobType is the worker_jobs.job_type this package handles.
const JobType = "customer.import"

// Mode values accepted by Payload.Mode.
const (
	ModeDryRun = "dry_run"
	ModeApply  = "apply"
)

// Payload is the worker_jobs.payload shape for JobType.
type Payload struct {
	ImportID uuid.UUID `json:"import_id"`
	Mode     string    `json:"mode"`
}

// Report is the {rows, created, matched, merge_candidates, skipped, by_org,
// errors} summary persisted to customer_imports.dry_run_report /
// apply_report (0098's table comment).
type Report struct {
	Rows            int            `json:"rows"`
	Created         int            `json:"created"`
	Matched         int            `json:"matched"`
	MergeCandidates int            `json:"merge_candidates"`
	Skipped         int            `json:"skipped"`
	ByOrg           map[string]int `json:"by_org"`
	Errors          []string       `json:"errors,omitempty"`
}

// unresolvedOrgKey is the Report.ByOrg bucket for rows whose org could not
// be determined (customer_imports.org_id is NULL and the row's OrgKey did
// not resolve through mapping.Frontends to a UUID).
const unresolvedOrgKey = "unresolved"

// Options configures NewHandler and RunImport.
type Options struct {
	// Pool is the top-level pool used for the row that survives outside a
	// per-run transaction (loading customer_imports, persisting the
	// report). Required.
	Pool *pgxpool.Pool
	// Media resolves customer_imports.file_media_id to bytes. Required.
	Media *mediastore.Repo
	// Logger receives one summary line per run. nil uses slog.Default().
	Logger *slog.Logger
	// Now is injectable for deterministic tests; defaults to
	// time.Now().UTC.
	Now func() time.Time
}

func (o *Options) setDefaults() {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
}

// NewHandler returns the worker.HandlerFunc for JobType. Its signature
// matches worker.HandlerFunc structurally (func(ctx, []byte) error) so it
// can be passed straight to (*worker.Registry).Register without this
// package having to import worker.
func NewHandler(opts Options) func(ctx context.Context, payload []byte) error {
	opts.setDefaults()
	return func(ctx context.Context, payload []byte) error {
		var p Payload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("customerimport: decode payload: %w", err)
		}
		_, err := RunImport(ctx, opts, p.ImportID, p.Mode)
		return err
	}
}

// RunImport executes one dry_run or apply pass over customer_imports row
// importID and returns the resulting Report. On any fatal error (bad
// mapping, unreadable file, unknown source_label, a DB failure) the import
// is marked 'failed' before the error is returned.
func RunImport(ctx context.Context, opts Options, importID uuid.UUID, mode string) (Report, error) {
	opts.setDefaults()
	if mode != ModeDryRun && mode != ModeApply {
		return Report{}, fmt.Errorf("customerimport: unknown mode %q", mode)
	}

	topQ := gen.New(opts.Pool)

	imp, err := topQ.GetCustomerImportByID(ctx, importID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Report{}, fmt.Errorf("customerimport: import %s: %w", importID, err)
		}
		return Report{}, fmt.Errorf("customerimport: load import %s: %w", importID, err)
	}

	rows, sourceLabel, err := loadAndParse(ctx, opts, imp)
	if err != nil {
		_ = topQ.MarkCustomerImportFailed(ctx, importID)
		return Report{}, err
	}

	if mode == ModeDryRun {
		return runDryRun(ctx, opts, topQ, imp, sourceLabel, rows)
	}
	return runApply(ctx, opts, topQ, imp, sourceLabel, rows)
}

// loadAndParse resolves customer_imports.file_media_id to bytes, decodes
// the mapping jsonb, and dispatches to the parser matching source_label.
func loadAndParse(ctx context.Context, opts Options, imp gen.CustomerImportRow) ([]ParsedRow, string, error) {
	var mapping Mapping
	if len(imp.Mapping) > 0 {
		if err := json.Unmarshal(imp.Mapping, &mapping); err != nil {
			return nil, "", fmt.Errorf("customerimport: decode mapping: %w", err)
		}
	}

	obj, err := opts.Media.GetByID(ctx, imp.FileMediaID)
	if err != nil {
		return nil, "", fmt.Errorf("customerimport: load file media %s: %w", imp.FileMediaID, err)
	}
	res, err := opts.Media.Storage().Get(ctx, obj.StorageKey)
	if err != nil {
		return nil, "", fmt.Errorf("customerimport: read file media %s: %w", imp.FileMediaID, err)
	}
	data, err := io.ReadAll(res.Body)
	_ = res.Body.Close() // Windows refuses to reuse/replace an open file handle; always close before returning.
	if err != nil {
		return nil, "", fmt.Errorf("customerimport: read file media %s: %w", imp.FileMediaID, err)
	}

	switch imp.SourceLabel {
	case "bil24_orders_json":
		rows, err := ParseBil24OrdersJSON(data, mapping)
		if err != nil {
			return nil, "", err
		}
		return rows, imp.SourceLabel, nil
	case "wc_customers_csv", "gsheets_csv", "brevo_csv", "generic_csv":
		rows, err := ParseCSVRows(data, mapping)
		if err != nil {
			return nil, "", err
		}
		return rows, imp.SourceLabel, nil
	default:
		return nil, "", fmt.Errorf("customerimport: unknown source_label %q", imp.SourceLabel)
	}
}

// rowOrgID determines the target org for a parsed row: the import's fixed
// org_id when single-org, else the row's OrgKey (already resolved to a
// UUID string by the parser via mapping.Frontends) parsed as a UUID.
// Returns ok=false when neither yields a usable org.
func rowOrgID(imp gen.CustomerImportRow, row ParsedRow) (uuid.UUID, bool) {
	if imp.OrgID != nil {
		return *imp.OrgID, true
	}
	if row.OrgKey == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(row.OrgKey)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// rowHash is the row_hash idempotency key: sha256 of the row's canonical
// RawJSON bytes, hex-encoded.
func rowHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func newReport() Report {
	return Report{ByOrg: make(map[string]int)}
}

func orgBucket(orgID uuid.UUID, ok bool) string {
	if !ok {
		return unresolvedOrgKey
	}
	return orgID.String()
}

// runDryRun resolves every row inside a transaction that is always rolled
// back, then persists only the resulting Report.
func runDryRun(ctx context.Context, opts Options, topQ *gen.Queries, imp gen.CustomerImportRow, sourceLabel string, rows []ParsedRow) (Report, error) {
	if err := topQ.UpdateCustomerImportStatus(ctx, imp.ID, "dry_run_running"); err != nil {
		return Report{}, fmt.Errorf("customerimport: set dry_run_running: %w", err)
	}

	tx, err := opts.Pool.Begin(ctx)
	if err != nil {
		_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
		return Report{}, fmt.Errorf("customerimport: begin dry_run tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // dry_run NEVER commits — see file doc comment.

	store := customers.NewStoreFromQueries(gen.New(tx))
	report := newReport()
	source := "import:" + sourceLabel

	for _, row := range rows {
		report.Rows++
		orgID, ok := rowOrgID(imp, row)
		report.ByOrg[orgBucket(orgID, ok)]++
		if !ok {
			report.Skipped++
			continue
		}

		orderAt := opts.Now()
		if row.FirstOrderAt != nil {
			orderAt = *row.FirstOrderAt
		}
		res, err := customers.Resolve(ctx, store, customers.ResolveInput{
			Email:  row.Email,
			Phone:  row.Phone,
			Name:   row.Name,
			Source: source,
			Now:    orderAt,
		})
		if err != nil {
			report.Skipped++
			report.Errors = append(report.Errors, fmt.Sprintf("row %d: %v", report.Rows, err))
			continue
		}
		switch {
		case res.Created:
			report.Created++
		case res.MergeCandidateQueued:
			report.MergeCandidates++
		default:
			report.Matched++
		}
	}

	_ = tx.Rollback(ctx) // explicit, in addition to the defer, so the report write below is provably post-rollback.

	reportJSON, err := json.Marshal(report)
	if err != nil {
		return Report{}, fmt.Errorf("customerimport: marshal dry_run report: %w", err)
	}
	if err := topQ.SetCustomerImportDryRunReport(ctx, imp.ID, reportJSON); err != nil {
		return Report{}, fmt.Errorf("customerimport: persist dry_run report: %w", err)
	}
	opts.Logger.Info("customerimport: dry_run complete", "import_id", imp.ID, "rows", report.Rows, "created", report.Created, "matched", report.Matched, "merge_candidates", report.MergeCandidates, "skipped", report.Skipped)
	return report, nil
}

// runApply resolves every row inside one committed transaction, writing a
// customer_import_rows entry per row and re-deriving the Report from those
// writes (plus the already-recorded rows a re-apply short-circuits on).
func runApply(ctx context.Context, opts Options, topQ *gen.Queries, imp gen.CustomerImportRow, sourceLabel string, rows []ParsedRow) (Report, error) {
	if err := topQ.UpdateCustomerImportStatus(ctx, imp.ID, "applying"); err != nil {
		return Report{}, fmt.Errorf("customerimport: set applying: %w", err)
	}

	tx, err := opts.Pool.Begin(ctx)
	if err != nil {
		_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
		return Report{}, fmt.Errorf("customerimport: begin apply tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	txQ := gen.New(tx)
	store := customers.NewStoreFromQueries(txQ)
	report := newReport()
	source := "import:" + sourceLabel

	for i, row := range rows {
		report.Rows++
		rowNo := int32(i + 1)
		hash := rowHash(row.RawJSON)

		if existing, err := txQ.GetCustomerImportRowByHash(ctx, imp.ID, hash); err == nil {
			tallyExisting(&report, existing)
			continue
		} else if !errors.Is(err, pgx.ErrNoRows) {
			_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
			return Report{}, fmt.Errorf("customerimport: lookup row %d by hash: %w", rowNo, err)
		}

		orgID, ok := rowOrgID(imp, row)
		if !ok {
			report.ByOrg[unresolvedOrgKey]++
			report.Skipped++
			action := "skipped"
			reason := "org_unresolved"
			if _, err := txQ.InsertCustomerImportRow(ctx, imp.ID, rowNo, hash, row.RawJSON, nil, nil, &action, &reason); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
				return Report{}, fmt.Errorf("customerimport: insert skipped row %d: %w", rowNo, err)
			}
			continue
		}
		report.ByOrg[orgID.String()]++

		orderAt := opts.Now()
		if row.FirstOrderAt != nil {
			orderAt = *row.FirstOrderAt
		}
		res, err := customers.Resolve(ctx, store, customers.ResolveInput{
			Email:  row.Email,
			Phone:  row.Phone,
			Name:   row.Name,
			Source: source,
			Now:    orderAt,
		})
		if err != nil {
			_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
			return Report{}, fmt.Errorf("customerimport: resolve row %d: %w", rowNo, err)
		}

		var action string
		switch {
		case res.Created:
			action = "created"
			report.Created++
		case res.MergeCandidateQueued:
			action = "merge_candidate"
			report.MergeCandidates++
		default:
			action = "matched"
			report.Matched++
		}

		// Consent: import NEVER calls customers.MarkVerified on the
		// identities it attaches (see customer_imports.sql's doc comment) —
		// only a marketing consent grant is recorded here.
		if err := txQ.InsertCustomerConsent(ctx, res.Customer.ID, orgID, "marketing", orderAt, source); err != nil {
			_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
			return Report{}, fmt.Errorf("customerimport: insert consent row %d: %w", rowNo, err)
		}
		if err := txQ.UpsertCustomerOrgLinkStats(ctx, res.Customer.ID, orgID, orderAt, int32(row.TicketsCount)); err != nil {
			_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
			return Report{}, fmt.Errorf("customerimport: upsert org link stats row %d: %w", rowNo, err)
		}

		customerID := res.Customer.ID
		if _, err := txQ.InsertCustomerImportRow(ctx, imp.ID, rowNo, hash, row.RawJSON, &customerID, &orgID, &action, nil); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
			return Report{}, fmt.Errorf("customerimport: insert applied row %d: %w", rowNo, err)
		}
	}

	reportJSON, err := json.Marshal(report)
	if err != nil {
		_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
		return Report{}, fmt.Errorf("customerimport: marshal apply report: %w", err)
	}
	if err := txQ.SetCustomerImportApplyReport(ctx, imp.ID, reportJSON); err != nil {
		_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
		return Report{}, fmt.Errorf("customerimport: persist apply report: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		_ = topQ.MarkCustomerImportFailed(ctx, imp.ID)
		return Report{}, fmt.Errorf("customerimport: commit apply tx: %w", err)
	}
	committed = true

	opts.Logger.Info("customerimport: apply complete", "import_id", imp.ID, "rows", report.Rows, "created", report.Created, "matched", report.Matched, "merge_candidates", report.MergeCandidates, "skipped", report.Skipped)
	return report, nil
}

// tallyExisting folds an already-recorded customer_import_rows entry (a
// re-apply hit) into report using its previously-persisted action, without
// re-deriving org bucketing from the current row (the org recorded at the
// time of the original apply is authoritative).
func tallyExisting(report *Report, existing gen.CustomerImportRowRow) {
	orgKey := unresolvedOrgKey
	if existing.OrgID != nil {
		orgKey = existing.OrgID.String()
	}
	report.ByOrg[orgKey]++

	action := ""
	if existing.Action != nil {
		action = *existing.Action
	}
	switch action {
	case "created":
		report.Created++
	case "merge_candidate":
		report.MergeCandidates++
	case "skipped":
		report.Skipped++
	default: // "matched" or unset
		report.Matched++
	}
}
