package hcustomerimports

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

const pgForeignKeyViolation = "23503"

// allowedSourceLabels mirrors the parser dispatch in customerimport.loadAndParse
// and the source_label comment in migration 0098.
var allowedSourceLabels = map[string]bool{
	"bil24_orders_json": true,
	"wc_customers_csv":  true,
	"gsheets_csv":       true,
	"brevo_csv":         true,
	"generic_csv":       true,
}

// allowedLegalBases mirrors customer_imports_legal_basis_check (migration 0098).
var allowedLegalBases = map[string]bool{
	"organizer_contract":  true,
	"legitimate_interest": true,
	"explicit_consent":    true,
}

type createCustomerImportRequest struct {
	OrgID       string          `json:"org_id,omitempty"`
	SourceLabel string          `json:"source_label"`
	FileMediaID string          `json:"file_media_id"`
	Mapping     json.RawMessage `json:"mapping,omitempty"`
	LegalBasis  string          `json:"legal_basis"`
}

type customerImportDTO struct {
	ID           string          `json:"id"`
	OrgID        *string         `json:"org_id"`
	SourceLabel  string          `json:"source_label"`
	FileMediaID  string          `json:"file_media_id"`
	Mapping      json.RawMessage `json:"mapping"`
	LegalBasis   string          `json:"legal_basis"`
	Status       string          `json:"status"`
	DryRunReport json.RawMessage `json:"dry_run_report"`
	ApplyReport  json.RawMessage `json:"apply_report"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

func toCustomerImportDTO(row gen.CustomerImportRow) customerImportDTO {
	dto := customerImportDTO{
		ID:          row.ID.String(),
		SourceLabel: row.SourceLabel,
		FileMediaID: row.FileMediaID.String(),
		Mapping:     jsonOrNull(row.Mapping),
		LegalBasis:  row.LegalBasis,
		Status:      row.Status,
		CreatedBy:   row.CreatedBy.String(),
		CreatedAt:   row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if row.OrgID != nil {
		s := row.OrgID.String()
		dto.OrgID = &s
	}
	dto.DryRunReport = jsonOrNull(row.DryRunReport)
	dto.ApplyReport = jsonOrNull(row.ApplyReport)
	return dto
}

func jsonOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// HandleCreateCustomerImport serves POST /v1/admin/customer-imports. It
// records a customer_imports row referencing an already-uploaded
// file_media_id; dry-run/apply happen via the dedicated endpoints below.
func (h *Handler) HandleCreateCustomerImport(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"customer_import.invalid_body", "cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"customer_import.empty_body", "request body is required", r,
		))
		return
	}

	var req createCustomerImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"customer_import.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	sourceLabel := strings.TrimSpace(req.SourceLabel)
	if !allowedSourceLabels[sourceLabel] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"customer_import.invalid_source_label", "source_label must be one of the supported import formats", r,
			map[string]any{"field": "source_label", "allowed": sortedKeys(allowedSourceLabels)},
		))
		return
	}

	legalBasis := strings.TrimSpace(req.LegalBasis)
	if !allowedLegalBases[legalBasis] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"customer_import.invalid_legal_basis", "legal_basis must be one of the supported bases", r,
			map[string]any{"field": "legal_basis", "allowed": sortedKeys(allowedLegalBases)},
		))
		return
	}

	fileMediaID, err := uuid.Parse(strings.TrimSpace(req.FileMediaID))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"customer_import.invalid_file_media_id", "file_media_id must be a valid UUID", r,
			map[string]any{"field": "file_media_id"},
		))
		return
	}

	var orgID *uuid.UUID
	if raw := strings.TrimSpace(req.OrgID); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"customer_import.invalid_org_id", "org_id must be a valid UUID", r,
				map[string]any{"field": "org_id"},
			))
			return
		}
		orgID = &id
	}

	mapping := req.Mapping
	if len(mapping) == 0 {
		mapping = json.RawMessage("{}")
	} else if !json.Valid(mapping) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"customer_import.invalid_mapping", "mapping must be valid JSON", r,
			map[string]any{"field": "mapping"},
		))
		return
	}

	ctx := r.Context()
	actor, authenticated := auth.ActorFromContext(ctx)
	if !authenticated {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"auth.unauthenticated", "authentication is required", r,
		))
		return
	}
	createdBy, err := uuid.Parse(actor.ID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"customer_import.actor_not_user", "actor is not a user account", r,
		))
		return
	}

	row, err := h.queries.InsertCustomerImport(ctx, orgID, sourceLabel, fileMediaID, []byte(mapping), legalBasis, createdBy)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"customer_import.invalid_reference", "org_id or file_media_id does not exist", r,
				map[string]any{"constraint": pgErr.ConstraintName},
			))
			return
		}
		h.logger.Error("customer_import: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to create customer import", r,
		))
		return
	}

	h.writeAudit(r, "customer_import.create", row.ID.String(), reason, map[string]any{
		"source_label": sourceLabel, "legal_basis": legalBasis,
	})

	httputil.WriteJSON(w, http.StatusCreated, toCustomerImportDTO(row))
}

// writeAudit is a fire-and-forget audit write shared by every handler in
// this package; failures are logged but never abort the response since the
// mutation/read has already occurred.
func (h *Handler) writeAudit(r *http.Request, action, resourceID, reason string, extra map[string]any) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	metadata := map[string]any{"reason": reason}
	for k, v := range extra {
		metadata[k] = v
	}
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "customer_import",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("customer_import: audit write failed", slog.String("action", action), slog.Any("error", err))
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Small fixed sets; simple insertion sort keeps this dependency-free.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
