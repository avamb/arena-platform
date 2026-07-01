// barcode_batches.go — External barcode batch import HTTP API (feature #146).
//
// Operators upload CSV files containing external barcode values. Each batch goes
// through an operator approval flow before its barcodes are activated for scanning.
//
// # Batch status lifecycle
//
//	uploaded → pending_approval → active | rejected
//
// When a batch is approved:
//   - An 'external_platform' barcode authority is looked up (or a fallback is
//     used when barcodeQueries is nil in tests).
//   - Each batch entry is registered in the barcodes table under that authority
//     (status 'active').
//   - All batch_entries are updated to 'active'.
//   - Batch status becomes 'active'.
//
// When a batch is rejected, all entries are updated to 'rejected' and scanning
// of those barcodes is blocked (they are never inserted into barcodes table).
//
// # Endpoints
//
//	POST  /v1/barcode-batches             — upload CSV batch (barcode_batch.upload)
//	GET   /v1/barcode-batches             — list batches (barcode_batch.read)
//	GET   /v1/barcode-batches/{id}        — get batch detail + entries (barcode_batch.read)
//	POST  /v1/barcode-batches/{id}/approve — approve batch (barcode_batch.approve)
//	POST  /v1/barcode-batches/{id}/reject  — reject batch  (barcode_batch.approve)
package hbarcode

import (
	"encoding/csv"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// MaxBarcodeBatchFileSize is the maximum upload size for a single batch file (10 MiB).
const MaxBarcodeBatchFileSize = 10 << 20

// MaxBarcodeBatchRows is the maximum number of barcode rows accepted per batch.
const MaxBarcodeBatchRows = 50_000

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/barcode-batches  (multipart/form-data)
// ─────────────────────────────────────────────────────────────────────────────

// HandleUploadBarcodeBatch ingests a multipart CSV upload as a pending_approval batch.
func (h *Handler) HandleUploadBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	if h.barcodeBatchQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	// Parse the multipart form. Limit total memory to 32 MiB.
	//nolint:gosec // G120 false positive: 32 MiB cap is explicitly bounded above
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.parse_multipart_failed", "failed to parse multipart form: "+err.Error(), r,
		))
		return
	}

	// Optional allocation_id field.
	var allocationID *uuid.UUID
	if aidStr := r.FormValue("allocation_id"); aidStr != "" {
		aid, err := uuid.Parse(aidStr)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"barcode_batch.invalid_allocation_id", "allocation_id must be a valid UUID", r,
			))
			return
		}
		allocationID = &aid
	}

	// Optional notes field.
	var notes *string
	if n := r.FormValue("notes"); n != "" {
		notes = &n
	}

	// Optional uploaded_by field (caller identity).
	var uploadedBy *string
	if u := r.FormValue("uploaded_by"); u != "" {
		uploadedBy = &u
	}

	// Get the file field.
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.missing_file", "multipart field 'file' is required", r,
		))
		return
	}
	defer file.Close()

	if header.Size > MaxBarcodeBatchFileSize {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelope(
			"barcode_batch.file_too_large",
			"file exceeds maximum allowed size of 10 MiB",
			r,
		))
		return
	}

	filename := header.Filename
	if filename == "" {
		filename = "batch.csv"
	}

	// Detect source from filename/content-type.
	source := "csv"
	lname := strings.ToLower(filename)
	if strings.HasSuffix(lname, ".pdf") {
		source = "pdf"
	}

	// Parse CSV to extract barcode values.
	barcodeRefs, err := ParseBarcodeBatchCSV(io.LimitReader(file, MaxBarcodeBatchFileSize))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.csv_parse_failed", "failed to parse CSV: "+err.Error(), r,
		))
		return
	}

	if len(barcodeRefs) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.empty_file", "CSV file contains no barcode rows", r,
		))
		return
	}

	if len(barcodeRefs) > MaxBarcodeBatchRows {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.too_many_rows",
			"CSV file exceeds maximum allowed rows (50,000)",
			r,
		))
		return
	}

	ctx := r.Context()

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	bq := h.barcodeBatchQueries.WithTx(tx)

	// Insert the batch record.
	batch, err := bq.InsertBarcodeBatch(
		ctx,
		allocationID,
		source,
		"pending_approval",
		filename,
		int32(len(barcodeRefs)), //nolint:gosec // len bounded by 32 MiB upload cap above

		nil, // authority_id resolved on approval
		notes,
		uploadedBy,
	)
	if err != nil {
		h.logger.Error("barcode_batch: insert batch failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.insert_failed", "failed to create batch record", r,
		))
		return
	}

	// Insert each barcode entry.
	for _, ref := range barcodeRefs {
		if _, err := bq.InsertBarcodeBatchEntry(ctx, batch.ID, ref, "pending"); err != nil {
			h.logger.Error("barcode_batch: insert entry failed",
				slog.String("error", err.Error()),
				slog.String("external_ref", ref),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"barcode_batch.entry_insert_failed", "failed to insert batch entry", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.commit_failed", "failed to commit batch transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"batch": BarcodeBatchFromRow(batch),
	})
}

// ParseBarcodeBatchCSV parses a CSV reader and returns deduplicated barcode ref strings.
//
// Format: each row must have at least one column; the first column is the barcode
// value. An optional header row is detected by checking if the first row looks
// non-numeric or contains the word "barcode"/"code"/"ref".
// Empty rows and rows whose first column is empty are skipped.
func ParseBarcodeBatchCSV(r io.Reader) ([]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // variable column count
	cr.TrimLeadingSpace = true

	seen := make(map[string]struct{})
	var refs []string

	isFirst := true
	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) == 0 {
			continue
		}

		val := strings.TrimSpace(record[0])
		if val == "" {
			continue
		}

		// Skip header row: if the first row's first column looks like a
		// column label (contains "barcode", "code", "ref", "id"), skip it.
		if isFirst {
			isFirst = false
			low := strings.ToLower(val)
			if strings.Contains(low, "barcode") ||
				strings.Contains(low, "code") ||
				strings.Contains(low, "ref") ||
				low == "id" {
				continue
			}
		}

		// Deduplicate within the batch.
		if _, exists := seen[val]; exists {
			continue
		}
		seen[val] = struct{}{}
		refs = append(refs, val)
	}

	return refs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/barcode-batches
// ─────────────────────────────────────────────────────────────────────────────

// HandleListBarcodeBatches lists all batches, optionally filtered by allocation_id.
func (h *Handler) HandleListBarcodeBatches(w http.ResponseWriter, r *http.Request) {
	if h.barcodeBatchQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	ctx := r.Context()

	// Optional allocation_id filter.
	if aidStr := r.URL.Query().Get("allocation_id"); aidStr != "" {
		aid, err := uuid.Parse(aidStr)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"barcode_batch.invalid_allocation_id", "allocation_id must be a valid UUID", r,
			))
			return
		}
		rows, err := h.barcodeBatchQueries.ListBarcodeBatchesByAllocation(ctx, aid)
		if err != nil {
			h.logger.Error("barcode_batch: list by allocation failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"barcode_batch.list_failed", "failed to list barcode batches", r,
			))
			return
		}
		batches := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			batches = append(batches, BarcodeBatchFromRow(row))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"batches": batches,
			"total":   len(batches),
		})
		return
	}

	rows, err := h.barcodeBatchQueries.ListAllBarcodeBatches(ctx)
	if err != nil {
		h.logger.Error("barcode_batch: list all failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.list_failed", "failed to list barcode batches", r,
		))
		return
	}

	batches := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		batches = append(batches, BarcodeBatchFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"batches": batches,
		"total":   len(batches),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/barcode-batches/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetBarcodeBatch returns a single batch plus its entries.
func (h *Handler) HandleGetBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	if h.barcodeBatchQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()

	batch, err := h.barcodeBatchQueries.GetBarcodeBatchByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"barcode_batch.not_found", "barcode batch not found", r,
			))
			return
		}
		h.logger.Error("barcode_batch: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.get_failed", "failed to retrieve barcode batch", r,
		))
		return
	}

	entries, err := h.barcodeBatchQueries.ListBatchEntriesByBatchID(ctx, id)
	if err != nil {
		h.logger.Error("barcode_batch: list entries failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.entries_failed", "failed to retrieve batch entries", r,
		))
		return
	}

	entryList := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		entryList = append(entryList, BarcodeBatchEntryFromRow(e))
	}

	out := BarcodeBatchFromRow(batch)
	out["entries"] = entryList
	out["entry_count"] = len(entryList)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"batch": out,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/barcode-batches/{id}/approve
// ─────────────────────────────────────────────────────────────────────────────

// HandleApproveBarcodeBatch transitions a pending batch to 'active', registers
// each entry under the external_platform barcode authority, and commits.
func (h *Handler) HandleApproveBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	if h.barcodeBatchQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()

	// Fetch the current batch.
	batch, err := h.barcodeBatchQueries.GetBarcodeBatchByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"barcode_batch.not_found", "barcode batch not found", r,
			))
			return
		}
		h.logger.Error("barcode_batch: get for approve failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.get_failed", "failed to retrieve barcode batch", r,
		))
		return
	}

	// Only pending_approval batches can be approved.
	if batch.Status != "pending_approval" && batch.Status != "uploaded" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"barcode_batch.invalid_status",
			"only batches in 'pending_approval' or 'uploaded' status can be approved",
			r,
		))
		return
	}

	// Fetch entries.
	entries, err := h.barcodeBatchQueries.ListBatchEntriesByBatchID(ctx, id)
	if err != nil {
		h.logger.Error("barcode_batch: list entries for approve failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.entries_failed", "failed to retrieve batch entries", r,
		))
		return
	}

	// Begin transaction for atomic approval.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	bq := h.barcodeBatchQueries.WithTx(tx)

	// Resolve the external_platform authority. When barcodeQueries is available,
	// look it up by type; otherwise fall back to a nil authority_id (tests).
	var authorityID *uuid.UUID
	if h.barcodeQueries != nil {
		barcodeQ := h.barcodeQueries.WithTx(tx)
		authority, err := barcodeQ.GetBarcodeAuthorityByType(ctx, "external_platform")
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("barcode_batch: resolve authority failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"barcode_batch.authority_failed", "failed to resolve barcode authority", r,
			))
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			authorityID = &authority.ID
		}

		// Register each barcode in the barcodes table.
		if authorityID != nil {
			for _, entry := range entries {
				if _, insertErr := barcodeQ.InsertBarcode(ctx, *authorityID, entry.ExternalRef, nil); insertErr != nil {
					// Skip duplicates — barcode may already be registered from a previous import.
					h.logger.Warn("barcode_batch: barcode already registered, skipping",
						slog.String("external_ref", entry.ExternalRef),
						slog.String("error", insertErr.Error()),
					)
				}
			}
		}
	}

	// Update all batch entries to 'active'.
	if _, err := bq.UpdateBatchEntriesStatus(ctx, id, "active"); err != nil {
		h.logger.Error("barcode_batch: update entries status failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.update_entries_failed", "failed to activate batch entries", r,
		))
		return
	}

	// Update the batch to 'active' and record the authority_id.
	approved, err := bq.UpdateBarcodeBatchAuthorityAndStatus(ctx, id, authorityID, "active")
	if err != nil {
		h.logger.Error("barcode_batch: update batch status failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.update_failed", "failed to approve batch", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.commit_failed", "failed to commit approval transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"batch":    BarcodeBatchFromRow(approved),
		"approved": true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/barcode-batches/{id}/reject
// ─────────────────────────────────────────────────────────────────────────────

// HandleRejectBarcodeBatch terminally rejects a non-active batch.
func (h *Handler) HandleRejectBarcodeBatch(w http.ResponseWriter, r *http.Request) {
	if h.barcodeBatchQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"barcode_batch.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()

	// Fetch the current batch.
	batch, err := h.barcodeBatchQueries.GetBarcodeBatchByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"barcode_batch.not_found", "barcode batch not found", r,
			))
			return
		}
		h.logger.Error("barcode_batch: get for reject failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.get_failed", "failed to retrieve barcode batch", r,
		))
		return
	}

	// Cannot reject an already active or rejected batch.
	if batch.Status == "active" || batch.Status == "rejected" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"barcode_batch.invalid_status",
			"cannot reject a batch that is already 'active' or 'rejected'",
			r,
		))
		return
	}

	ctx2 := r.Context()
	tx, err := h.pool.BeginTx(ctx2, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx2) }()

	bq := h.barcodeBatchQueries.WithTx(tx)

	// Update all batch entries to 'rejected'.
	if _, err := bq.UpdateBatchEntriesStatus(ctx2, id, "rejected"); err != nil {
		h.logger.Error("barcode_batch: update entries to rejected failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.update_entries_failed", "failed to reject batch entries", r,
		))
		return
	}

	// Update the batch itself to 'rejected'.
	rejected, err := bq.UpdateBarcodeBatchStatus(ctx2, id, "rejected")
	if err != nil {
		h.logger.Error("barcode_batch: update batch to rejected failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.update_failed", "failed to reject batch", r,
		))
		return
	}

	if err := tx.Commit(ctx2); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"barcode_batch.commit_failed", "failed to commit rejection transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"batch":    BarcodeBatchFromRow(rejected),
		"rejected": true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Response helpers
// ─────────────────────────────────────────────────────────────────────────────

// BarcodeBatchFromRow renders a gen.BarcodeBatchRow as a JSON object for HTTP responses.
func BarcodeBatchFromRow(r gen.BarcodeBatchRow) map[string]any {
	out := map[string]any{
		"id":          r.ID.String(),
		"source":      r.Source,
		"status":      r.Status,
		"filename":    r.Filename,
		"row_count":   r.RowCount,
		"notes":       r.Notes,
		"uploaded_by": r.UploadedBy,
		"created_at":  r.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":  r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.AllocationID != nil {
		out["allocation_id"] = r.AllocationID.String()
	} else {
		out["allocation_id"] = nil
	}
	if r.AuthorityID != nil {
		out["authority_id"] = r.AuthorityID.String()
	} else {
		out["authority_id"] = nil
	}
	return out
}

// BarcodeBatchEntryFromRow renders a single batch entry row for the GET /{id} response.
func BarcodeBatchEntryFromRow(r gen.BarcodeBatchEntryRow) map[string]any {
	return map[string]any{
		"id":           r.ID.String(),
		"batch_id":     r.BatchID.String(),
		"external_ref": r.ExternalRef,
		"status":       r.Status,
		"created_at":   r.CreatedAt.UTC().Format(time.RFC3339),
	}
}
