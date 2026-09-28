// Package hbot serves the API surface of the Telegram event-center bot
// (08_architecture/28_telegram_event_center_bot_ru.md §3.3):
//
//	POST /v1/organizations/{org_id}/bot-invitations  — an organization owner
//	     (or the platform superadmin) invites a person into the bot: the
//	     membership is created at once, a one-time code is mailed as a
//	     Telegram deep link, and the superadmin also gets the link back.
//	POST /v1/bot/invitations/accept                  — the bot process, under
//	     its service token, redeems a code for a Telegram account and binds
//	     that account to the invited user.
//
// Everything else the bot does goes through the ordinary REST API as the
// linked user; these two routes are the only bot-specific ones.
package hbot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// TxStarter is the subset of *pgxpool.Pool the handlers need.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// InvitationTTL is how long a bot invitation code stays redeemable.
const InvitationTTL = 7 * 24 * time.Hour

// invitationEmailMaxAttempts bounds the worker's retries of the e-mail job.
const invitationEmailMaxAttempts = 5

// Handler holds the dependencies of the bot routes.
type Handler struct {
	queries           *gen.Queries
	membershipQueries *gen.Queries
	pool              TxStarter
	audit             audit.Writer
	logger            *slog.Logger
	// botUsername is the bot's Telegram username; a non-empty value lets the
	// superadmin response carry the ready deep link.
	botUsername string
	// now is injectable for tests.
	now func() time.Time
}

// New builds a Handler. queries answers reads outside a transaction, pool
// starts the write transactions, auditWriter may be nil (no audit rows).
func New(queries *gen.Queries, pool TxStarter, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		queries: queries,
		pool:    pool,
		audit:   auditWriter,
		logger:  logger,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// WithMembershipQueries supplies the handle requireOrgMembership reads from.
func (h *Handler) WithMembershipQueries(q *gen.Queries) *Handler {
	h.membershipQueries = q
	return h
}

// WithBotUsername sets the Telegram username used to build deep links.
func (h *Handler) WithBotUsername(username string) *Handler {
	h.botUsername = username
	return h
}

// newInvitationCode draws a 32-byte random code, base64url without padding
// (43 characters) — short enough for Telegram's 64-character /start payload
// together with the inv_ prefix, and unguessable.
func newInvitationCode() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("hbot: draw invitation code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// writeAudit records a bot action. actorID must be a UUID string or empty
// (audit_events.actor_id is a uuid column, AGENTS.md).
func (h *Handler) writeAudit(r *http.Request, actorType, actorID, action, resourceType, resourceID string, metadata map[string]any) {
	if h.audit == nil {
		return
	}
	ctx := r.Context()
	ev := audit.Event{
		OccurredAt:   h.now(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(ctx, ev); err != nil {
		h.logger.Warn("hbot: audit write failed", slog.String("action", action), slog.String("error", err.Error()))
	}
}

// actorUserID returns the calling user's id when the actor is a user with a
// UUID id, otherwise "".
func actorUserID(ctx context.Context) string {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.Type != auth.ActorTypeUser {
		return ""
	}
	return actor.ID
}
