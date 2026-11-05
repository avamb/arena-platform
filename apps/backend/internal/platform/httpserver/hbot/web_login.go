package hbot

// web_login.go — the bot's "Open in browser" button (spec
// 08_architecture/37 section 4.3, task WEB-AUTH-2, migration 0151, owner
// decision 2026-10-14: for every role, according to its rights).
//
//	POST /v1/bot/web-login-codes          a one-time link for the person
//	                                      behind a linked Telegram account
//	POST /v1/bot/web-login-notices/claim  sign-ins the bot must report, each
//	                                      handed out once
//
// A code has a purpose (migration 0169). "web" is the button above. "miniapp"
// (spec 44, RT-12a) is the Telegram mini app editor: the link is
// <EVENT_CENTER_PUBLIC_URL>/tg/edit#code=<code>&kind=<kind>&event=<event id>,
// the exchange opens a 30-minute session of channel event_center_tg, the
// chat gets no "signed in" notice, and issuing it takes back no code of
// another purpose.
//
// Both are called by the bot PROCESS under BOT_SERVICE_TOKEN. The web
// exchanges the code at POST /v1/auth/web/link (hauth).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/orgperm"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// WebLoginCodeTTL is how long a link from the bot opens the web.
const WebLoginCodeTTL = 5 * time.Minute

// Kinds of text the mini app editor opens (RT-12a). They travel in the
// fragment of the link next to the code.
const (
	MiniappKindDescription = "description"
	MiniappKindEmailNote   = "email_note"
	// MiniappKindInvitations opens the staff invitations screen of one
	// session (spec 46, INV-05): the link is
	// <EVENT_CENTER_PUBLIC_URL>/tg/invitations?session=<id>&event=<id>#code=<code>.
	MiniappKindInvitations = "invitations"
)

// webTargetPattern is the only shape of "target" the plain link accepts: the
// shop section of one organization of the web event center (spec 61 section
// 4.5). Anything else is dropped and the link opens the home page.
var webTargetPattern = regexp.MustCompile(`^/o/([0-9a-fA-F-]{36})/shop(/[A-Za-z0-9_-]+)*$`)

// webLoginNoticeBatch caps one claim of sign-in notices.
const webLoginNoticeBatch = 50

// WithEventCenterURL sets the origin of the web event center
// (EVENT_CENTER_PUBLIC_URL); empty switches "Open in browser" off.
func (h *Handler) WithEventCenterURL(origin string) *Handler {
	h.eventCenterURL = strings.TrimRight(strings.TrimSpace(origin), "/")
	return h
}

// HandleCreateWebLoginCode serves POST /v1/bot/web-login-codes.
// Body {"telegram_user_id": 123} for "Open in browser" (purpose web, the
// default), or {"telegram_user_id": 123, "purpose": "miniapp", "kind":
// "description" or "email_note", "event_id": "<uuid>"} for the mini app editor,
// or kind "invitations" with "event_id" and "session_id" for the staff
// invitations screen (spec 46, INV-05).
//
//	201 {url, expires_at}             the link for the private chat
//	400 bot.invalid_purpose           purpose is neither web nor miniapp
//	400 bot.invalid_miniapp_target    a miniapp code needs kind and event_id
//	404 bot.not_linked                the account is linked to nobody
//	403 auth.user_deactivated
//	403 bot.web_login_superadmin      a platform superadmin works in admin-web
//	503 bot.web_login_not_configured  EVENT_CENTER_PUBLIC_URL is empty
//
// A new code takes back the person's earlier unused codes of the SAME
// purpose.
func (h *Handler) HandleCreateWebLoginCode(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	if h.eventCenterURL == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"bot.web_login_not_configured", "the web event center address is not configured", r))
		return
	}
	ctx := r.Context()
	var req struct {
		TelegramUserID int64  `json:"telegram_user_id"`
		Purpose        string `json:"purpose"`
		Kind           string `json:"kind"`
		EventID        string `json:"event_id"`
		SessionID      string `json:"session_id"`
		Target         string `json:"target"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_json", "request body is not valid JSON", r))
		return
	}
	if req.TelegramUserID <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_telegram_user_id", "telegram_user_id must be a positive integer", r,
			map[string]any{"field": "telegram_user_id"}))
		return
	}

	purpose := strings.TrimSpace(req.Purpose)
	if purpose == "" {
		purpose = gen.WebLoginPurposeWeb
	}
	var linkSuffix, invitationsPath string
	switch purpose {
	case gen.WebLoginPurposeWeb:
	case gen.WebLoginPurposeMiniapp:
		eventID, perr := uuid.Parse(strings.TrimSpace(req.EventID))
		sessionID, serr := uuid.Parse(strings.TrimSpace(req.SessionID))
		switch {
		case perr == nil && (req.Kind == MiniappKindDescription || req.Kind == MiniappKindEmailNote):
			linkSuffix = "&kind=" + req.Kind + "&event=" + eventID.String()
		case perr == nil && serr == nil && req.Kind == MiniappKindInvitations:
			invitationsPath = "/tg/invitations?session=" + sessionID.String() + "&event=" + eventID.String()
		default:
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"bot.invalid_miniapp_target", "a miniapp code needs kind (description, email_note or invitations), a valid event_id and, for invitations, a valid session_id", r,
				map[string]any{"field": "kind"}))
			return
		}
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_purpose", "purpose must be web or miniapp", r,
			map[string]any{"field": "purpose"}))
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)

	link, err := q.GetBotTelegramLink(ctx, req.TelegramUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("hbot: web login: read telegram link failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	if err != nil || link.RevokedAt != nil {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"bot.not_linked", "this Telegram account is not linked to an Arena account", r))
		return
	}
	user, err := q.GetUserByID(ctx, link.UserID)
	if err != nil {
		h.logger.Error("hbot: web login: read user failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	if user.DeactivatedAt != nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"auth.user_deactivated", "this account has been deactivated", r))
		return
	}
	platform, err := q.HasPlatformRole(ctx, link.UserID)
	if err != nil {
		h.logger.Error("hbot: web login: read roles failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	// Any platform-level role, not only platform_superadmin: a person with one
	// works in admin-web and gets no password-less web session (WL-1).
	if platform {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"bot.web_login_superadmin", "a platform superadmin works in the admin console", r))
		return
	}

	// The optional target (spec 61 section 4.5) is kept only when it names the
	// shop of an organization the person works in; a platform-level role never
	// gets here (refused above). A target that does not qualify is ignored.
	var target string
	if purpose == gen.WebLoginPurposeWeb {
		target = h.acceptWebTarget(ctx, q, link.UserID, req.Target)
	}

	code, err := newInvitationCode()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.web_login_failed", "failed to draw a code", r))
		return
	}
	expiresAt := h.now().Add(WebLoginCodeTTL)
	err = q.RevokeOpenWebLoginCodesOfPurpose(ctx, link.UserID, purpose)
	if err == nil {
		err = q.InsertWebLoginCode(ctx, users.TokenHash(code), link.UserID, req.TelegramUserID, expiresAt, purpose)
	}
	if err != nil {
		h.logger.Error("hbot: web login: save code failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to save the code", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("hbot: web login: commit failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to save the code", r))
		return
	}
	h.writeAudit(r, "user", link.UserID.String(), "v1.auth.web_login_code_issue", "user", link.UserID.String(),
		map[string]any{"via": "telegram_bot", "purpose": purpose})
	loginURL := h.eventCenterURL + "/login/link#code=" + code
	if purpose == gen.WebLoginPurposeWeb && target != "" {
		loginURL += "&next=" + url.QueryEscape(target)
	}
	if purpose == gen.WebLoginPurposeMiniapp {
		loginURL = h.eventCenterURL + "/tg/edit#code=" + code + linkSuffix
		if invitationsPath != "" {
			loginURL = h.eventCenterURL + invitationsPath + "#code=" + code
		}
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"url":        loginURL,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

// HandleClaimWebLoginNotices serves POST /v1/bot/web-login-notices/claim.
// Each sign-in of the last day is handed out once; the bot tells the person
// and offers "Sign out everywhere".
func (h *Handler) HandleClaimWebLoginNotices(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	notices, err := h.queries.ClaimWebLoginNotices(r.Context(), webLoginNoticeBatch)
	if err != nil {
		h.logger.Error("hbot: claim web login notices failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	out := make([]map[string]any, 0, len(notices))
	for _, n := range notices {
		out = append(out, map[string]any{
			"telegram_user_id": n.TelegramUserID,
			"signed_in_at":     n.UsedAt.UTC().Format(time.RFC3339),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"notices": out})
}

// acceptWebTarget returns target when it is a shop path of an organization
// the user holds an active membership of (the organization not archived), and
// "" otherwise. A failed lookup also yields "": the link then simply opens the
// home page.
func (h *Handler) acceptWebTarget(ctx context.Context, q *gen.Queries, userID uuid.UUID, target string) string {
	target = strings.TrimSpace(target)
	m := webTargetPattern.FindStringSubmatch(target)
	if m == nil {
		return ""
	}
	orgID, err := uuid.Parse(m[1])
	if err != nil {
		return ""
	}
	rows, err := q.ListMembershipsByUser(ctx, userID)
	if err != nil {
		h.logger.Warn("hbot: web login: read memberships for target failed", slog.String("error", err.Error()))
		return ""
	}
	// An archived organization is not one the person works in. The shared rule
	// (orgperm) decides what counts as a membership.
	live := make([]gen.MembershipRow, 0, len(rows))
	for _, row := range rows {
		if !row.OrgArchived {
			live = append(live, row)
		}
	}
	if orgperm.HasMembershipRow(live, orgID) {
		return target
	}
	return ""
}
