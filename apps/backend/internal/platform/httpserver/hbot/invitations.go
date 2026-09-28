package hbot

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

const pgUniqueViolation = "23505"

// createInvitationRequest is the body of POST /v1/organizations/{org_id}/bot-invitations.
type createInvitationRequest struct {
	Email  string `json:"email"`
	Role   string `json:"role"`
	Locale string `json:"locale"`
}

// invitationDTO is the invitation as the API reports it — never the code.
type invitationDTO struct {
	ID             string `json:"id"`
	OrgID          string `json:"org_id"`
	UserID         string `json:"user_id"`
	Email          string `json:"email"`
	Role           string `json:"role"`
	MembershipRole string `json:"membership_role"`
	UserCreated    bool   `json:"user_created"`
	ExpiresAt      string `json:"expires_at"`
	Delivery       string `json:"delivery"`
}

// HandleCreateInvitation serves POST /v1/organizations/{org_id}/bot-invitations
// (membership.grant). In ONE transaction it finds or creates the user by
// e-mail, makes sure they hold a membership in the organization (an existing
// membership keeps its role), stores the SHA-256 of a fresh one-time code and
// queues the bot.invitation_email job that carries the Telegram deep link.
//
// The code itself is returned only to the platform superadmin (X-Admin-Reason
// on record), as `deep_link`, for the first-wave hand-over to organizations
// that were provisioned by hand; an organization owner only ever triggers
// the e-mail.
func (h *Handler) HandleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	superadmin, ok := h.requireOrgMembership(w, r, orgID)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_body", "request body is required", r,
		))
		return
	}
	var req createInvitationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_json", "request body is not valid JSON", r,
		))
		return
	}
	email, err := users.NormalizeEmail(req.Email)
	if err != nil || !strings.Contains(email, "@") {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_email", "email address is invalid", r, map[string]any{"field": "email"},
		))
		return
	}
	role, ok := ParseRole(req.Role)
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_role", "role must be owner or manager", r,
			map[string]any{"field": "role", "allowed": []string{RoleOwner, RoleManager}},
		))
		return
	}
	locale := NormalizeLocale(req.Locale)

	org, err := h.queries.GetOrganizationByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"org.not_found", "organization not found", r,
			))
			return
		}
		h.logger.Error("hbot: load organization failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.invite_failed", "failed to load the organization", r,
		))
		return
	}

	code, err := newInvitationCode()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"internal.token_generation_failed", "failed to generate the invitation code", r,
		))
		return
	}
	expiresAt := h.now().Add(InvitationTTL)

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)

	// 1. The user.
	var userID uuid.UUID
	userCreated := false
	userRow, lookupErr := q.GetUserByEmail(ctx, email)
	switch {
	case lookupErr == nil:
		if userRow.DeactivatedAt != nil {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"bot.user_deactivated", "this account has been deactivated", r,
			))
			return
		}
		userID = userRow.ID
	case errors.Is(lookupErr, pgx.ErrNoRows):
		// A bot-only user never types a password; the placeholder is random
		// and unknown to everyone, and the self-service reset flow is the way
		// to set one later.
		tempPassword, tokenErr := users.GenerateVerificationToken()
		if tokenErr != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"internal.token_generation_failed", "failed to generate the account secret", r,
			))
			return
		}
		hash, hashErr := users.HashPassword(tempPassword)
		if hashErr != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"internal.password_hash_failed", "failed to hash the account secret", r,
			))
			return
		}
		created, createErr := q.InsertUser(ctx, email, hash, locale)
		if createErr != nil {
			h.logger.Error("hbot: create invited user failed", slog.String("error", createErr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"bot.invite_failed", "failed to create the invited user", r,
			))
			return
		}
		userID = created.ID
		userCreated = true
	default:
		h.logger.Error("hbot: GetUserByEmail failed", slog.String("error", lookupErr.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.invite_failed", "failed to resolve the user", r,
		))
		return
	}

	// 2. The membership. An existing one keeps its role: the bot never
	// widens what a person already holds.
	membershipRole := ""
	existing, err := q.ListMembershipsByUser(ctx, userID)
	if err != nil {
		h.logger.Error("hbot: list memberships failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.invite_failed", "failed to read memberships", r,
		))
		return
	}
	for _, m := range existing {
		if m.OrgID == orgID && m.Status == "active" {
			membershipRole = m.Role
			break
		}
	}
	if membershipRole == "" {
		m, insErr := q.InsertMembership(ctx, userID, orgID, MembershipRoleFor(role))
		if insErr != nil {
			var pgErr *pgconn.PgError
			if errors.As(insErr, &pgErr) && pgErr.Code == pgUniqueViolation {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"bot.membership_conflict", "the user already holds a membership in this organization", r,
				))
				return
			}
			h.logger.Error("hbot: insert membership failed", slog.String("error", insErr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"bot.invite_failed", "failed to add the member", r,
			))
			return
		}
		membershipRole = m.Role
	}
	effectiveRole := BotRoleFor(membershipRole)

	// 3. The invitation row and its e-mail, in the same transaction.
	var invitedBy *uuid.UUID
	if id := actorUserID(ctx); id != "" {
		if parsed, perr := uuid.Parse(id); perr == nil {
			invitedBy = &parsed
		}
	}
	inv, err := q.InsertBotInvitation(ctx, orgID, userID, email, effectiveRole, users.TokenHash(code), invitedBy, expiresAt)
	if err != nil {
		h.logger.Error("hbot: insert invitation failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.invite_failed", "failed to save the invitation", r,
		))
		return
	}
	jobID, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypeBotInvitationEmail, authemail.BotInvitationEmailPayload{
		UserID:    userID.String(),
		Email:     email,
		Code:      code,
		ExpiresAt: expiresAt,
		OrgName:   org.Name,
		Role:      effectiveRole,
		Locale:    locale,
	}, invitationEmailMaxAttempts)
	if err != nil {
		h.logger.Error("hbot: enqueue invitation email failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.invite_failed", "failed to queue the invitation email", r,
		))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("hbot: commit failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to save the invitation", r,
		))
		return
	}

	h.writeAudit(r, "user", actorUserID(ctx), "v1.bot.invitation.create", "bot_invitation", inv.ID.String(), map[string]any{
		"org_id":          orgID.String(),
		"user_id":         userID.String(),
		"role":            effectiveRole,
		"membership_role": membershipRole,
		"user_created":    userCreated,
		"superadmin":      superadmin,
	})
	// Identifiers only — never the code or the link.
	h.logger.Info("hbot: invitation created",
		slog.String("invitation_id", inv.ID.String()),
		slog.String("org_id", orgID.String()),
		slog.String("user_id", userID.String()),
		slog.String("job_id", jobID),
	)

	response := map[string]any{
		"invitation": invitationDTO{
			ID:             inv.ID.String(),
			OrgID:          orgID.String(),
			UserID:         userID.String(),
			Email:          email,
			Role:           effectiveRole,
			MembershipRole: membershipRole,
			UserCreated:    userCreated,
			ExpiresAt:      expiresAt.Format(time.RFC3339),
			Delivery:       "email",
		},
	}
	if superadmin && h.botUsername != "" {
		response["deep_link"] = authemail.BotDeepLink(h.botUsername, code)
	}
	httputil.WriteJSON(w, http.StatusCreated, response)
}

// acceptInvitationRequest is the body of POST /v1/bot/invitations/accept.
type acceptInvitationRequest struct {
	Code             string `json:"code"`
	Email            string `json:"email"`
	TelegramUserID   int64  `json:"telegram_user_id"`
	TelegramUsername string `json:"telegram_username"`
	Locale           string `json:"locale"`
}

// acceptInvitationResponse tells the bot who the Telegram account now is.
type acceptInvitationResponse struct {
	UserID         string `json:"user_id"`
	Email          string `json:"email"`
	OrgID          string `json:"org_id"`
	OrgName        string `json:"org_name"`
	Role           string `json:"role"`
	MembershipRole string `json:"membership_role"`
	Locale         string `json:"locale"`
	TelegramUserID int64  `json:"telegram_user_id"`
}

// HandleAcceptInvitation serves POST /v1/bot/invitations/accept under the
// bot service token. It redeems a one-time code for a Telegram account: the
// code must be unexpired and unused (every other state answers the same 404,
// so the route cannot be used to enumerate codes), the e-mail the person
// typed must match the invitation, and a Telegram account that is already
// bound to a DIFFERENT user is refused (409) rather than silently re-bound.
func (h *Handler) HandleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_body", "request body is required", r,
		))
		return
	}
	var req acceptInvitationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_json", "request body is not valid JSON", r,
		))
		return
	}
	code := strings.TrimPrefix(strings.TrimSpace(req.Code), authemail.BotInvitationStartPrefix)
	if code == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_code", "code is required", r, map[string]any{"field": "code"},
		))
		return
	}
	if req.TelegramUserID <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_telegram_user_id", "telegram_user_id must be a positive integer", r,
			map[string]any{"field": "telegram_user_id"},
		))
		return
	}
	email, err := users.NormalizeEmail(req.Email)
	if err != nil || !strings.Contains(email, "@") {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bot.invalid_email", "email address is invalid", r, map[string]any{"field": "email"},
		))
		return
	}

	inv, err := h.queries.GetBotInvitationByCodeHash(ctx, users.TokenHash(code))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("hbot: load invitation failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.accept_failed", "failed to load the invitation", r,
		))
		return
	}
	if err != nil || inv.AcceptedAt != nil || !inv.ExpiresAt.After(h.now()) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"bot.invitation_not_found", "the invitation does not exist, was already used or has expired", r,
		))
		return
	}
	if !strings.EqualFold(strings.TrimSpace(inv.Email), email) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"bot.invitation_email_mismatch", "the e-mail does not match the invitation", r,
		))
		return
	}

	locale := NormalizeLocale(req.Locale)
	var username *string
	if u := strings.TrimPrefix(strings.TrimSpace(req.TelegramUsername), "@"); u != "" {
		username = &u
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)

	if link, lerr := q.GetBotTelegramLink(ctx, req.TelegramUserID); lerr == nil {
		if link.RevokedAt == nil && link.UserID != inv.UserID {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"bot.telegram_already_linked", "this Telegram account is already linked to another user", r,
			))
			return
		}
	} else if !errors.Is(lerr, pgx.ErrNoRows) {
		h.logger.Error("hbot: load telegram link failed", slog.String("error", lerr.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.accept_failed", "failed to read the Telegram link", r,
		))
		return
	}
	orgID := inv.OrgID
	if _, err := q.UpsertBotTelegramLink(ctx, req.TelegramUserID, inv.UserID, username, locale, &orgID); err != nil {
		h.logger.Error("hbot: upsert telegram link failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.accept_failed", "failed to link the Telegram account", r,
		))
		return
	}
	if _, err := q.AcceptBotInvitation(ctx, inv.ID, req.TelegramUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Lost a race with another redemption of the same code.
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"bot.invitation_not_found", "the invitation does not exist, was already used or has expired", r,
			))
			return
		}
		h.logger.Error("hbot: accept invitation failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.accept_failed", "failed to accept the invitation", r,
		))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("hbot: commit failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to save the link", r,
		))
		return
	}

	membershipRole := MembershipRoleFor(inv.Role)
	if ms, merr := h.queries.ListMembershipsByUser(ctx, inv.UserID); merr == nil {
		for _, m := range ms {
			if m.OrgID == inv.OrgID && m.Status == "active" {
				membershipRole = m.Role
				break
			}
		}
	}
	orgName := ""
	if org, oerr := h.queries.GetOrganizationByID(ctx, inv.OrgID); oerr == nil {
		orgName = org.Name
	}

	h.writeAudit(r, "bot", inv.UserID.String(), "v1.bot.invitation.accept", "bot_invitation", inv.ID.String(), map[string]any{
		"org_id":           inv.OrgID.String(),
		"telegram_user_id": req.TelegramUserID,
		"role":             inv.Role,
	})
	httputil.WriteJSON(w, http.StatusOK, acceptInvitationResponse{
		UserID:         inv.UserID.String(),
		Email:          inv.Email,
		OrgID:          inv.OrgID.String(),
		OrgName:        orgName,
		Role:           BotRoleFor(membershipRole),
		MembershipRole: membershipRole,
		Locale:         locale,
		TelegramUserID: req.TelegramUserID,
	})
}
