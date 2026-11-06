// Package hsaleschats serves the sales bot's chats (spec 08_architecture/61
// sections 7a.4 and 7b): the platform superadmin's admin screen "Telegram
// chats" and the service routes the sales bot (arena-bot, package salesbot)
// calls with BOT_SERVICE_TOKEN.
//
// Superadmin routes (mounted behind superadmin.read AND the platform
// superadmin marker; every write needs X-Admin-Reason and writes its audit row
// on the change's own transaction):
//
//	GET    /v1/admin/organizations/{org_id}/sales-chats
//	POST   /v1/admin/organizations/{org_id}/sales-chats/connect-codes
//	GET    /v1/admin/sales-chats/operator
//	POST   /v1/admin/sales-chats/operator/connect-codes
//	PATCH  /v1/admin/sales-chats/{id}
//	DELETE /v1/admin/sales-chats/{id}
//
// Service routes (BOT_SERVICE_TOKEN, 503 while it is unset):
//
//	POST /v1/bot/shop-delivery/steps     a button of a delivery card
//	POST /v1/bot/shop-delivery/card      the card as it is now
//	POST /v1/bot/sales-chats/connect     "/connect <code>" in a group
//	POST /v1/bot/sales-chats/removed     the bot was removed from a group
//	POST /v1/bot/sales-chats/migrated    a group became a supergroup
package hsaleschats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// Audit actions.
const (
	AuditConnectCode = "v1.admin.sales_chat.connect_code"
	AuditUpdate      = "v1.admin.sales_chat.update"
	AuditDelete      = "v1.admin.sales_chat.delete"
	AuditConnected   = "v1.sales_chat.connected"
	AuditBotRemoved  = "v1.sales_chat.bot_removed"
)

const maxBody = 32 << 10

// Handler serves the routes.
type Handler struct {
	pool   *pgxpool.Pool
	shop   *shop.Service
	cards  *salesnotify.PGStore
	audit  audit.Writer
	logger *slog.Logger
	now    func() time.Time
}

// New builds the handler. A nil pool makes every route answer 503.
func New(pool *pgxpool.Pool, shopSvc *shop.Service, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{pool: pool, shop: shopSvc, audit: auditWriter, logger: logger, now: time.Now}
	if pool != nil {
		h.cards = salesnotify.NewPGStore(pool)
	}
	return h
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "sales chats: "+what, "error", err, "request_id", logging.RequestID(r.Context()))
	writeErr(w, r, http.StatusInternalServerError, "sales_chat.failed", "nothing was changed: the operation failed, please try again")
}

// ready answers 503 without a database, or - for an admin write - without an
// audit writer that puts its row on the change's transaction.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request, write bool) bool {
	if h.pool == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable",
			"chats are not available right now, please try again in a moment")
		return false
	}
	if write && !audit.IsDurableInTx(h.audit) {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.audit_unavailable",
			"nothing was changed: the audit log is not available, please try again in a moment")
		return false
	}
	return true
}

// decodeBody reads one JSON object into dst, refusing unknown keys and
// trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, r, http.StatusBadRequest, "sales_chat.invalid_body",
			"the request body is not valid JSON of the expected shape: "+err.Error())
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeErr(w, r, http.StatusBadRequest, "sales_chat.invalid_body", "the request body has data after the JSON object")
		return false
	}
	return true
}

// inTx runs fn in a transaction.
func (h *Handler) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func actorUUID(r *http.Request) *uuid.UUID {
	a, _ := auth.ActorFromContext(r.Context())
	id, err := uuid.Parse(a.ID)
	if err != nil {
		return nil
	}
	return &id
}

// auditTx writes the audit row of an admin change on its transaction; the
// operator's X-Admin-Reason is metadata.reason.
func (h *Handler) auditTx(r *http.Request, tx pgx.Tx, action, resourceID string, orgID *string, reason string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["reason"] = reason
	if orgID != nil {
		meta["org_id"] = *orgID
	}
	actorID := ""
	if id := actorUUID(r); id != nil {
		actorID = id.String()
	}
	return h.audit.WriteTx(r.Context(), tx, audit.Event{
		OccurredAt:   h.now().UTC(),
		ActorType:    "user",
		ActorID:      actorID,
		Action:       action,
		ResourceType: "sales_chat",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

// auditBot records what the bot did on behalf of a Telegram account, best
// effort: the bot process is not a platform user.
func (h *Handler) auditBot(r *http.Request, tx pgx.Tx, action, resourceID string, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	ev := audit.Event{
		OccurredAt:   h.now().UTC(),
		ActorType:    "system",
		Action:       action,
		ResourceType: "sales_chat",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(r.Context()),
		Metadata:     meta,
	}
	if tx != nil && audit.IsDurableInTx(h.audit) {
		return h.audit.WriteTx(r.Context(), tx, ev)
	}
	return h.audit.Write(r.Context(), ev)
}
