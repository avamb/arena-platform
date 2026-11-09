package hbot

// telegram_connect.go — "Connect Telegram" (acceptance run 2026-11-09,
// finding F-007, migration 0213).
//
//	GET  /v1/organizations/{org_id}/telegram-connect        is Telegram
//	                                                        connected for me?
//	POST /v1/organizations/{org_id}/telegram-connect-codes  a one-time link
//	                                                        t.me/<bot>?start=con_<code>
//	POST /v1/bot/telegram-connect/preview                   the bot's
//	                                                        confirmation screen
//	POST /v1/bot/telegram-connect/accept                    the press of "Connect"
//
// The first two are the signed-in person's own: a member of the path
// organization, never an organization API key (a key has no Telegram). The
// last two are called by the bot PROCESS under BOT_SERVICE_TOKEN.
//
// The code is the person's bearer link for 15 minutes. Whoever opens it is
// asked first ("Connect this Telegram to <organization>?", the account's
// e-mail masked), so a photo of the QR code cannot link a stranger's
// Telegram without a press in that Telegram. A new code takes the person's
// earlier unused codes back. The link written is the same bot_telegram_links
// row an accepted bot invitation writes; a Telegram account already linked to
// ANOTHER person is refused (409), as the invitation does.

import (
	"encoding/base64"
	"encoding/json"
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/orgperm"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/qr"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// TelegramConnectCodeTTL is how long a "Connect Telegram" link works.
const TelegramConnectCodeTTL = 15 * time.Minute

// telegramConnectQRSize is the side of the QR picture in pixels.
const telegramConnectQRSize = 320

// TelegramConnectStartPrefix starts the /start payload of a connect link.
const TelegramConnectStartPrefix = "con_"

// TelegramConnectLink builds t.me/<bot>?start=con_<code>.
func TelegramConnectLink(botUsername, code string) string {
	return "https://t.me/" + strings.TrimPrefix(strings.TrimSpace(botUsername), "@") + "?start=" + TelegramConnectStartPrefix + code
}

// connectCaller decides who asks for a code: the signed-in user of a
// membership in the path organization. An API key, a stranger and a person
// without a membership row there (a platform superadmin included) are
// refused; the answer has then been written.
func (h *Handler) connectCaller(w http.ResponseWriter, r *http.Request) (userID, orgID uuid.UUID, ok bool) {
	orgID, err := uuid.Parse(chi.URLParam(r, "org_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"org.invalid_id", "org_id is not a UUID", r, map[string]any{"field": "org_id"}))
		return uuid.Nil, uuid.Nil, false
	}
	actor, authed := auth.ActorFromContext(r.Context())
	if !authed || actor.Type != auth.ActorTypeUser {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"telegram_connect.user_only", "only a signed-in person can connect their Telegram", r))
		return uuid.Nil, uuid.Nil, false
	}
	userID, err = uuid.Parse(actor.ID)
	if err != nil || h.membershipQueries == nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r))
		return uuid.Nil, uuid.Nil, false
	}
	rows, err := h.membershipQueries.ListMembershipsByUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("hbot: telegram connect: read memberships failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return uuid.Nil, uuid.Nil, false
	}
	if !orgperm.HasMembershipRow(liveMemberships(rows), orgID) {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r))
		return uuid.Nil, uuid.Nil, false
	}
	return userID, orgID, true
}

// HandleTelegramConnectStatus serves GET /v1/organizations/{org_id}/telegram-connect.
//
//	200 {available, bot_username, linked, accounts:[{telegram_username, linked_at}]}
func (h *Handler) HandleTelegramConnectStatus(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	userID, _, ok := h.connectCaller(w, r)
	if !ok {
		return
	}
	links, err := h.queries.ListLiveTelegramLinksOfUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("hbot: telegram connect: read links failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	accounts := make([]map[string]any, 0, len(links))
	for _, l := range links {
		var name any
		if l.TelegramUsername != nil && *l.TelegramUsername != "" {
			name = *l.TelegramUsername
		}
		accounts = append(accounts, map[string]any{
			"telegram_username": name,
			// The page compares it with the time its link was made: a newer one is "connected now".
			"linked_at": l.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	username := strings.TrimPrefix(strings.TrimSpace(h.botUsername), "@")
	var bot any
	if username != "" {
		bot = username
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"available":    username != "",
		"bot_username": bot,
		"linked":       len(links) > 0,
		"accounts":     accounts,
	})
}

// HandleCreateTelegramConnectCode serves
// POST /v1/organizations/{org_id}/telegram-connect-codes (no body).
//
//	201 {url, start_param, expires_at, qr_png}
//	403 telegram_connect.user_only           an API key or a service caller
//	403 org.access_denied                    no membership in the organization
//	503 telegram_connect.not_configured      EVENTS_TELEGRAM_BOT_USERNAME is empty
func (h *Handler) HandleCreateTelegramConnectCode(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	userID, orgID, ok := h.connectCaller(w, r)
	if !ok {
		return
	}
	if strings.TrimPrefix(strings.TrimSpace(h.botUsername), "@") == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"telegram_connect.not_configured", "the Telegram bot is not configured on this server", r))
		return
	}
	ctx := r.Context()
	code, err := newInvitationCode()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"telegram_connect.failed", "failed to draw a code", r))
		return
	}
	expiresAt := h.now().Add(TelegramConnectCodeTTL)
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)
	err = q.RevokeOpenTelegramConnectCodes(ctx, userID)
	if err == nil {
		err = q.InsertTelegramConnectCode(ctx, users.TokenHash(code), userID, orgID, expiresAt)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		h.logger.Error("hbot: telegram connect: save code failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to save the code", r))
		return
	}
	h.writeAudit(r, "user", userID.String(), "v1.telegram_connect.code_issue", "organization", orgID.String(), nil)
	link := TelegramConnectLink(h.botUsername, code)
	out := map[string]any{
		"url":         link,
		"start_param": TelegramConnectStartPrefix + code,
		"expires_at":  expiresAt.UTC().Format(time.RFC3339),
		"qr_png":      nil,
	}
	// The QR code for a person who reads the page on a computer; the web has
	// no QR library of its own. A failure only drops the picture.
	if png, err := qr.PNG(link, telegramConnectQRSize, 4); err == nil {
		out["qr_png"] = base64.StdEncoding.EncodeToString(png)
	} else {
		h.logger.Warn("hbot: telegram connect: render QR failed", slog.String("error", err.Error()))
	}
	httputil.WriteJSON(w, http.StatusCreated, out)
}

type telegramConnectBotRequest struct {
	Code             string `json:"code"`
	TelegramUserID   int64  `json:"telegram_user_id"`
	TelegramUsername string `json:"telegram_username"`
	Locale           string `json:"locale"`
}

// readConnectBotRequest decodes the bot's body; the answer has been written
// when ok is false.
func readConnectBotRequest(w http.ResponseWriter, r *http.Request) (telegramConnectBotRequest, bool) {
	var req telegramConnectBotRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_json", "request body is not valid JSON", r))
		return req, false
	}
	req.Code = strings.TrimPrefix(strings.TrimSpace(req.Code), TelegramConnectStartPrefix)
	if req.Code == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_code", "code is required", r, map[string]any{"field": "code"}))
		return req, false
	}
	if req.TelegramUserID <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_telegram_user_id", "telegram_user_id must be a positive integer", r,
			map[string]any{"field": "telegram_user_id"}))
		return req, false
	}
	return req, true
}

func writeConnectCodeInvalid(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"bot.connect_code_invalid", "the link does not exist, was already used, was replaced by a newer one or has expired", r))
}

// connectState says what the Telegram account is to the code's person:
// "new" (not linked), "same_user" (already linked to them) or "other_user"
// (linked to somebody else: the link is refused).
func connectState(link gen.BotTelegramLinkRow, err error, userID uuid.UUID) string {
	switch {
	case err != nil, link.RevokedAt != nil:
		return "new"
	case link.UserID == userID:
		return "same_user"
	default:
		return "other_user"
	}
}

// usableConnectCode reports whether the code's person may still be linked:
// the account is active and still a member of the organization, and the
// organization is not archived. ok false has written the answer (404, the
// same as an unknown code: a link that cannot work any more says nothing
// about why).
func (h *Handler) usableConnectCode(w http.ResponseWriter, r *http.Request, q *gen.Queries, c gen.TelegramConnectCodeRow) bool {
	if c.DeactivatedAt != nil || c.OrgArchived {
		writeConnectCodeInvalid(w, r)
		return false
	}
	rows, err := q.ListMembershipsByUser(r.Context(), c.UserID)
	if err != nil {
		h.logger.Error("hbot: telegram connect: read memberships failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return false
	}
	if !orgperm.HasMembershipRow(liveMemberships(rows), c.OrgID) {
		writeConnectCodeInvalid(w, r)
		return false
	}
	return true
}

// HandlePreviewTelegramConnect serves POST /v1/bot/telegram-connect/preview.
// Body {code, telegram_user_id}. Uses nothing.
//
//	200 {org_id, org_name, email_masked, state}
//	404 bot.connect_code_invalid
func (h *Handler) HandlePreviewTelegramConnect(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	req, ok := readConnectBotRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := h.queries.GetLiveTelegramConnectCode(ctx, users.TokenHash(req.Code))
	if errors.Is(err, pgx.ErrNoRows) {
		writeConnectCodeInvalid(w, r)
		return
	}
	if err != nil {
		h.logger.Error("hbot: telegram connect: read code failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	if !h.usableConnectCode(w, r, h.queries, c) {
		return
	}
	link, lerr := h.queries.GetBotTelegramLink(ctx, req.TelegramUserID)
	if lerr != nil && !errors.Is(lerr, pgx.ErrNoRows) {
		h.logger.Error("hbot: telegram connect: read link failed", slog.String("error", lerr.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"org_id":       c.OrgID.String(),
		"org_name":     c.OrgName,
		"email_masked": MaskEmail(c.Email),
		"state":        connectState(link, lerr, c.UserID),
	})
}

// HandleAcceptTelegramConnect serves POST /v1/bot/telegram-connect/accept.
// Body {code, telegram_user_id, telegram_username, locale}.
//
//	200 {org_id, org_name, user_id, role, locale, already_linked}
//	404 bot.connect_code_invalid
//	409 bot.telegram_already_linked   the account is linked to another person
func (h *Handler) HandleAcceptTelegramConnect(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	req, ok := readConnectBotRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)
	unavailable := func(what string, err error) {
		h.logger.Error("hbot: telegram connect: "+what+" failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
	}

	c, err := q.LockLiveTelegramConnectCode(ctx, users.TokenHash(req.Code))
	if errors.Is(err, pgx.ErrNoRows) {
		writeConnectCodeInvalid(w, r)
		return
	}
	if err != nil {
		unavailable("lock code", err)
		return
	}
	if !h.usableConnectCode(w, r, q, c) {
		return
	}
	link, lerr := q.GetBotTelegramLink(ctx, req.TelegramUserID)
	if lerr != nil && !errors.Is(lerr, pgx.ErrNoRows) {
		unavailable("read link", lerr)
		return
	}
	state := connectState(link, lerr, c.UserID)
	if state == "other_user" {
		// The code stays usable: the person may open it in the right Telegram.
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"bot.telegram_already_linked", "this Telegram account is already linked to another user", r))
		return
	}
	locale := NormalizeLocale(req.Locale)
	if state == "same_user" && strings.TrimSpace(req.Locale) == "" {
		locale = link.Locale // a relink keeps the language the person chose in the bot
	}
	var username *string
	if u := strings.TrimPrefix(strings.TrimSpace(req.TelegramUsername), "@"); u != "" {
		username = &u
	}
	orgID := c.OrgID
	if _, err := q.UpsertBotTelegramLink(ctx, req.TelegramUserID, c.UserID, username, locale, &orgID); err != nil {
		unavailable("upsert link", err)
		return
	}
	if err := q.UseTelegramConnectCode(ctx, c.ID, req.TelegramUserID); err != nil {
		unavailable("use code", err)
		return
	}
	rows, err := q.ListMembershipsByUser(ctx, c.UserID)
	if err != nil {
		unavailable("read memberships", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		unavailable("commit", err)
		return
	}
	role := botRoleIn(rows, c.OrgID)
	h.writeAudit(r, "user", c.UserID.String(), "v1.telegram_connect.accept", "organization", c.OrgID.String(),
		map[string]any{"via": "telegram_bot", "telegram_user_id": req.TelegramUserID, "already_linked": state == "same_user"})
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"org_id":         c.OrgID.String(),
		"org_name":       c.OrgName,
		"user_id":        c.UserID.String(),
		"role":           role,
		"locale":         locale,
		"already_linked": state == "same_user",
	})
}

// botRoleIn is the bot role ("owner" wins) the person holds in the
// organization. It reads roles only: membership was decided before, with
// orgperm.HasMembershipRow.
func botRoleIn(rows []gen.MembershipRow, orgID uuid.UUID) string {
	role := ""
	for _, m := range rows {
		if m.OrgID == orgID && m.Status == "active" {
			if b := BotRoleFor(m.Role); b == RoleOwner || role == "" {
				role = b
			}
		}
	}
	return role
}

// liveMemberships drops the memberships of archived organizations: an
// archived organization is not one the person works in.
func liveMemberships(rows []gen.MembershipRow) []gen.MembershipRow {
	out := make([]gen.MembershipRow, 0, len(rows))
	for _, m := range rows {
		if !m.OrgArchived {
			out = append(out, m)
		}
	}
	return out
}

// MaskEmail shows the first letter of the mailbox and the whole domain
// ("a•••@example.com"): enough for the person to recognize their own
// account, too little for a stranger who scanned the code to learn it.
func MaskEmail(s string) string {
	local, domain, ok := strings.Cut(strings.TrimSpace(s), "@")
	if !ok || local == "" {
		return ""
	}
	first := []rune(local)[0]
	return string(first) + "•••@" + domain
}
