package hlegal

// admin.go — the platform superadmin's minimal template routes (P3a; the
// screen is P3b). Mounted behind legal.manage, which only
// platform_superadmin holds and no organization API key may carry. Every
// state change needs X-Admin-Reason and writes an audit row in the same
// transaction. A published version is never changed: a new text is a new
// draft, and publishing it retires the previous version.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/legaldocs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// maxDocumentBody bounds a create request (the CHECK allows 100 000
// characters of template; a character may take four bytes).
const maxDocumentBody = 512 << 10

// legalDocument is one template version as the admin routes answer it.
type legalDocument struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Locale       string  `json:"locale"`
	Version      int32   `json:"version"`
	Status       string  `json:"status"`
	BodyTemplate string  `json:"body_template"`
	CreatedBy    *string `json:"created_by"`
	CreatedAt    string  `json:"created_at"`
	PublishedAt  *string `json:"published_at"`
	PublishedBy  *string `json:"published_by"`
	RetiredAt    *string `json:"retired_at"`
}

func rfc(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func idStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func documentFromRow(r gen.LegalDocumentRow) legalDocument {
	return legalDocument{
		ID: r.ID.String(), Kind: r.Kind, Locale: r.Locale, Version: r.Version, Status: r.Status,
		BodyTemplate: r.BodyTemplate, CreatedBy: idStr(r.CreatedBy), CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		PublishedAt: rfc(r.PublishedAt), PublishedBy: idStr(r.PublishedBy), RetiredAt: rfc(r.RetiredAt),
	}
}

// actorID is the signed-in operator, nil when the actor id is not a UUID.
func actorID(r *http.Request) *uuid.UUID {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		return nil
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil {
		return nil
	}
	return &id
}

func invalidTemplate(w http.ResponseWriter, r *http.Request, err error) {
	var pe *legaldocs.ParseError
	problems := []string{err.Error()}
	if errors.As(err, &pe) {
		problems = pe.Problems
	}
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"legal.invalid_template",
		"The template has problems. Fix them and save again.",
		r, map[string]any{"problems": problems, "placeholders": legaldocs.Placeholders()}))
}

// HandleList serves GET /v1/admin/legal-documents?kind=&locale=.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w, r) {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	locale := strings.TrimSpace(r.URL.Query().Get("locale"))
	if kind != "" && !legaldocs.KnownKind(kind) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("legal.invalid_kind", "Use kind sale_terms.", r))
		return
	}
	if locale != "" && !legaldocs.KnownLocale(locale) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("legal.invalid_locale",
			"Use one of the languages en, ru, cs, he, es, de, fr.", r))
		return
	}
	rows, err := h.queries.ListLegalDocuments(r.Context(), kind, locale)
	if err != nil {
		h.adminFailed(w, r, "list", err)
		return
	}
	items := make([]legalDocument, 0, len(rows))
	for _, row := range rows {
		items = append(items, documentFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "placeholders": legaldocs.Placeholders()})
}

// HandleGet serves GET /v1/admin/legal-documents/{id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w, r) {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	row, err := h.queries.GetLegalDocument(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		docNotFound(w, r)
		return
	}
	if err != nil {
		h.adminFailed(w, r, "get", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, documentFromRow(row))
}

func docNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"legal.document_not_found", "There is no such version. Check the id.", r))
}

type createRequest struct {
	Kind         string `json:"kind"`
	Locale       string `json:"locale"`
	BodyTemplate string `json:"body_template"`
}

// HandleCreate serves POST /v1/admin/legal-documents: a new draft, numbered
// after the last version of its kind and language. The template must parse
// (only allowlisted placeholders).
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w, r) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDocumentBody+1))
	if err != nil || len(raw) > maxDocumentBody {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("legal.invalid_body",
			"Send a JSON body of at most 512 KB with kind, locale and body_template.", r))
		return
	}
	var req createRequest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("legal.invalid_body",
			"Send a JSON body with kind, locale and body_template.", r))
		return
	}
	if !legaldocs.KnownKind(req.Kind) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("legal.invalid_kind", "Use kind sale_terms.", r))
		return
	}
	if !legaldocs.KnownLocale(req.Locale) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("legal.invalid_locale",
			"Use one of the languages en, ru, cs, he, es, de, fr.", r))
		return
	}
	if _, err := legaldocs.Parse(req.BodyTemplate); err != nil {
		invalidTemplate(w, r, err)
		return
	}
	if len([]rune(req.BodyTemplate)) > 100000 {
		invalidTemplate(w, r, errors.New("the template is longer than 100 000 characters"))
		return
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.adminFailed(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := h.queries.WithTx(tx).InsertLegalDocumentDraft(ctx, req.Kind, req.Locale, req.BodyTemplate, actorID(r))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("legal.version_conflict",
				"Another draft of this language was saved at the same moment. Save again.", r))
			return
		}
		h.adminFailed(w, r, "insert", err)
		return
	}
	if !h.writeAudit(w, r, tx, "v1.legal_document.create", row, reason) {
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.adminFailed(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, documentFromRow(row))
}

// HandlePublish serves POST /v1/admin/legal-documents/{id}/publish: the
// draft becomes the published version of its kind and language and the
// previous one is retired, in one transaction.
func (h *Handler) HandlePublish(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w, r) {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.adminFailed(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	doc, err := q.LockLegalDocument(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		docNotFound(w, r)
		return
	}
	if err != nil {
		h.adminFailed(w, r, "lock", err)
		return
	}
	if doc.Status != legaldocs.StatusDraft {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("legal.not_draft",
			"Only a draft can be published. A published version never changes: save a new draft.", r))
		return
	}
	if _, err := legaldocs.Parse(doc.BodyTemplate); err != nil {
		invalidTemplate(w, r, err)
		return
	}
	if _, err := q.RetirePublishedLegalDocument(ctx, doc.Kind, doc.Locale); err != nil {
		h.adminFailed(w, r, "retire", err)
		return
	}
	row, err := q.PublishLegalDocument(ctx, id, actorID(r))
	if err != nil {
		h.adminFailed(w, r, "publish", err)
		return
	}
	if !h.writeAudit(w, r, tx, "v1.legal_document.publish", row, reason) {
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.adminFailed(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, documentFromRow(row))
}

func (h *Handler) writeAudit(w http.ResponseWriter, r *http.Request, tx pgx.Tx, action string, row gen.LegalDocumentRow, reason string) bool {
	if h.audit == nil {
		return true
	}
	ctx := r.Context()
	actor, _ := auth.ActorFromContext(ctx)
	err := h.audit.WriteTx(ctx, tx, audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "legal_document",
		ResourceID:   row.ID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata: map[string]any{
			"kind":         row.Kind,
			"locale":       row.Locale,
			"version":      row.Version,
			"admin_reason": reason,
		},
	})
	if err != nil {
		h.adminFailed(w, r, "audit", err)
		return false
	}
	return true
}

func (h *Handler) adminFailed(w http.ResponseWriter, r *http.Request, step string, err error) {
	h.logger.Error("legal documents: "+step+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"legal.write_failed", "We could not save the terms. Try again in a minute.", r))
}
