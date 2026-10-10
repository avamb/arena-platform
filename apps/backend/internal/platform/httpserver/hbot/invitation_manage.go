package hbot

// invitation_manage.go — taking a bot invitation back and sending its letter
// again (EC-16, spec 08_architecture/35 §6.7). Both routes are owner-only
// (membership.revoke / membership.grant, which the manager does not hold),
// carry {org_id} in the path (a non-member of that organization is refused by
// requireOrgMembership), and answer an invitation of ANOTHER organization with
// the route's own 404, exactly as a missing one.
//
//	DELETE /v1/organizations/{org_id}/bot-invitations/{id}
//	POST   /v1/organizations/{org_id}/bot-invitations/{id}/resend
//
// What revoking does. The invitation row is annulled (revoked_at; the row is
// kept for the audit trail) so its code no longer redeems. The MEMBERSHIP that
// the invitation created at once is then removed too - but only when the
// person takes part in nothing else, which the code defines as ALL of:
//
//  1. the invitation was never accepted (an accepted one: only the row is
//     voided, the member stays, and the owner uses the existing remove-member
//     flow - reason "accepted");
//  2. the membership was INSERTED by this invitation's own transaction
//     (bot_invitations.membership_created); a person who was a member before
//     keeps their membership however the invitation ends - "member_before";
//  3. no OTHER invitation still keeps them in this organization: another one
//     accepted, or not yet accepted and neither expired nor revoked -
//     "other_invitation";
//  4. they have not started working here through the bot by another route:
//     no live Telegram link whose current organization this is, no wizard
//     draft and no open dialog (BotUserWorksInOrg) - "in_use". (A person who
//     already had a link from another organization may open this one the
//     moment the membership exists, without ever accepting;)
//  5. the membership still has the role the invitation gave it (an owner who
//     promoted or demoted them meant it) - "role_changed";
//  6. removing it would not leave the organization without an active owner -
//     "last_owner".
//
// The user ACCOUNT created for an invitation is left alone: it is harmless
// without a membership and a later invitation reuses it.
//
// What resending does. The code is stored only as a hash, so a resend mints a
// NEW code (the old link stops working), gives it a fresh 7-day life, stamps
// last_sent_at and queues a fresh bot.invitation_email job - all in one
// transaction. An expired invitation can be resent (that is how it is
// revived); an accepted or a revoked one cannot (409). The minimum interval
// between two letters is ten minutes, enforced inside the UPDATE itself
// (ResendBotInvitation), so two concurrent presses cannot both pass; the
// refusal is 429 with Retry-After.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// ResendMinInterval is the shortest time between two letters of one invitation.
const ResendMinInterval = 10 * time.Minute

// Why a membership stayed after its invitation was revoked.
const (
	KeptAccepted        = "accepted"
	KeptMemberBefore    = "member_before"
	KeptOtherInvitation = "other_invitation"
	KeptInUse           = "in_use"
	KeptRoleChanged     = "role_changed"
	KeptLastOwner       = "last_owner"
)

// revokeInvitationResponse is the answer of DELETE .../bot-invitations/{id}.
type revokeInvitationResponse struct {
	InvitationID      string `json:"invitation_id"`
	Email             string `json:"email"`
	Revoked           bool   `json:"revoked"`
	MembershipRemoved bool   `json:"membership_removed"`
	// KeptReason says why the membership stayed (one of the Kept* values);
	// absent when it was removed.
	KeptReason string `json:"kept_reason,omitempty"`
}

// invitationIDParam reads {id}.
func invitationIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_invitation_id", "id must be a valid UUID", r,
		))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) invitationNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"bot.invitation_not_found", "the invitation does not exist", r,
	))
}

// HandleRevokeInvitation serves DELETE /v1/organizations/{org_id}/bot-invitations/{id}.
func (h *Handler) HandleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
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
	invID, ok := invitationIDParam(w, r)
	if !ok {
		return
	}
	superadmin, ok := h.requireOrgMembership(w, r, orgID)
	if !ok {
		return
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

	inv, err := q.GetBotInvitationInOrgForUpdate(ctx, invID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.invitationNotFound(w, r)
			return
		}
		h.invitationFailed(w, r, "revoke: load", err)
		return
	}
	if inv.RevokedAt != nil {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"bot.invitation_revoked", "the invitation was already revoked", r,
		))
		return
	}
	if _, err := q.RevokeBotInvitation(ctx, inv.ID); err != nil {
		h.invitationFailed(w, r, "revoke: update", err)
		return
	}

	removed, kept, err := h.removeUnusedMembership(ctx, q, inv)
	if err != nil {
		h.invitationFailed(w, r, "revoke: membership", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.invitationFailed(w, r, "revoke: commit", err)
		return
	}

	h.writeAudit(r, "user", actorUserID(ctx), "v1.bot.invitation.revoke", "bot_invitation", inv.ID.String(), map[string]any{
		"org_id":             orgID.String(),
		"user_id":            inv.UserID.String(),
		"role":               inv.Role,
		"accepted":           inv.AcceptedAt != nil,
		"membership_removed": removed,
		"kept_reason":        kept,
		"superadmin":         superadmin,
	})
	h.logger.Info("hbot: invitation revoked",
		slog.String("invitation_id", inv.ID.String()),
		slog.String("org_id", orgID.String()),
		slog.Bool("membership_removed", removed),
		slog.String("kept_reason", kept),
	)
	httputil.WriteJSON(w, http.StatusOK, revokeInvitationResponse{
		InvitationID: inv.ID.String(), Email: inv.Email, Revoked: true,
		MembershipRemoved: removed, KeptReason: kept,
	})
}

// removeUnusedMembership applies the rules documented at the top of this
// file to the membership the revoked invitation created. It returns whether
// the membership was removed and, when not, why.
func (h *Handler) removeUnusedMembership(ctx context.Context, q *gen.Queries, inv gen.BotInvitationRow) (removed bool, kept string, err error) {
	if inv.AcceptedAt != nil {
		return false, KeptAccepted, nil
	}
	created := inv.MembershipCreated
	if !created {
		// A second invitation to someone the first one brought in records
		// created=false (the membership was already there), yet the person is
		// still not a member "from before": ask about every invitation.
		var err error
		if created, err = q.BotInvitationCreatedMembership(ctx, inv.UserID, inv.OrgID); err != nil {
			return false, "", err
		}
	}
	if !created {
		return false, KeptMemberBefore, nil
	}
	others, err := q.CountOtherLiveBotInvitations(ctx, inv.UserID, inv.OrgID, inv.ID)
	if err != nil {
		return false, "", err
	}
	if others > 0 {
		return false, KeptOtherInvitation, nil
	}
	works, err := q.BotUserWorksInOrg(ctx, inv.UserID, inv.OrgID)
	if err != nil {
		return false, "", err
	}
	if works {
		return false, KeptInUse, nil
	}
	role := MembershipRoleFor(inv.Role)
	if role == "org_admin" {
		owners, err := q.CountActiveOrgAdmins(ctx, inv.OrgID)
		if err != nil {
			return false, "", err
		}
		if owners <= 1 {
			return false, KeptLastOwner, nil
		}
	}
	if _, err := q.RevokeMembership(ctx, inv.UserID, inv.OrgID, role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No active membership with the role the invitation gave: the
			// owner changed it on purpose, or it is already gone.
			return false, KeptRoleChanged, nil
		}
		return false, "", err
	}
	return true, "", nil
}

// resendRequest is the optional body of the resend route.
type resendRequest struct {
	Locale string `json:"locale"`
}

// HandleResendInvitation serves POST /v1/organizations/{org_id}/bot-invitations/{id}/resend.
func (h *Handler) HandleResendInvitation(w http.ResponseWriter, r *http.Request) {
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
	invID, ok := invitationIDParam(w, r)
	if !ok {
		return
	}
	superadmin, ok := h.requireOrgMembership(w, r, orgID)
	if !ok {
		return
	}
	var req resendRequest
	if body, _ := io.ReadAll(io.LimitReader(r.Body, 4*1024)); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"bot.invalid_json", "request body is not valid JSON", r,
			))
			return
		}
	}

	inv, err := h.queries.GetBotInvitationInOrg(ctx, invID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.invitationNotFound(w, r)
			return
		}
		h.invitationFailed(w, r, "resend: load", err)
		return
	}
	if refused := h.resendRefusal(w, r, inv); refused {
		return
	}
	org, err := h.queries.GetOrganizationByID(ctx, orgID)
	if err != nil {
		h.invitationFailed(w, r, "resend: organization", err)
		return
	}
	locale := NormalizeLocale(req.Locale)
	if req.Locale == "" {
		if u, uerr := h.queries.GetUserByEmail(ctx, inv.Email); uerr == nil {
			locale = NormalizeLocale(u.PreferredLocale)
		}
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

	updated, err := q.ResendBotInvitation(ctx, inv.ID, orgID, users.TokenHash(code), expiresAt, int32(ResendMinInterval/time.Second))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.invitationFailed(w, r, "resend: update", err)
			return
		}
		// Did not qualify: it changed since the read above, or it is too soon.
		_ = tx.Rollback(ctx)
		again, rerr := h.queries.GetBotInvitationInOrg(ctx, invID, orgID)
		if rerr != nil {
			h.invitationNotFound(w, r)
			return
		}
		if h.resendRefusal(w, r, again) {
			return
		}
		h.resendTooSoon(w, r, again)
		return
	}
	jobID, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypeBotInvitationEmail, authemail.BotInvitationEmailPayload{
		UserID:    updated.UserID.String(),
		Email:     updated.Email,
		Code:      code,
		ExpiresAt: expiresAt,
		OrgName:   org.Name,
		Role:      updated.Role,
		Locale:    locale,
	}, invitationEmailMaxAttempts)
	if err != nil {
		h.invitationFailed(w, r, "resend: enqueue", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.invitationFailed(w, r, "resend: commit", err)
		return
	}

	h.writeAudit(r, "user", actorUserID(ctx), "v1.bot.invitation.resend", "bot_invitation", updated.ID.String(), map[string]any{
		"org_id":      orgID.String(),
		"user_id":     updated.UserID.String(),
		"role":        updated.Role,
		"was_expired": !inv.ExpiresAt.After(h.now()),
		"superadmin":  superadmin,
	})
	// Identifiers only - never the code or the link.
	h.logger.Info("hbot: invitation resent",
		slog.String("invitation_id", updated.ID.String()),
		slog.String("org_id", orgID.String()),
		slog.String("job_id", jobID),
	)

	membershipRole := MembershipRoleFor(updated.Role)
	if ms, merr := h.queries.ListMembershipsByUser(ctx, updated.UserID); merr == nil {
		for _, m := range ms {
			if m.OrgID == orgID && m.Status == "active" {
				membershipRole = m.Role
				break
			}
		}
	}
	response := map[string]any{
		"invitation": invitationDTO{
			ID:             updated.ID.String(),
			OrgID:          orgID.String(),
			UserID:         updated.UserID.String(),
			Email:          updated.Email,
			Role:           updated.Role,
			MembershipRole: membershipRole,
			ExpiresAt:      expiresAt.Format(time.RFC3339),
			Delivery:       "email",
		},
		"resend_available_at": updated.LastSentAt.Add(ResendMinInterval).Format(time.RFC3339),
	}
	if superadmin && h.botUsername != "" {
		response["deep_link"] = authemail.BotDeepLink(h.botUsername, code)
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

// resendRefusal answers 409 for an invitation that can no longer be resent
// (accepted, revoked) and reports whether it did.
func (h *Handler) resendRefusal(w http.ResponseWriter, r *http.Request, inv gen.BotInvitationRow) bool {
	switch {
	case inv.RevokedAt != nil:
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"bot.invitation_revoked", "the invitation was revoked", r,
		))
		return true
	case inv.AcceptedAt != nil:
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"bot.invitation_accepted", "the invitation was already accepted", r,
		))
		return true
	}
	return false
}

// resendTooSoon answers 429 with the seconds left.
func (h *Handler) resendTooSoon(w http.ResponseWriter, r *http.Request, inv gen.BotInvitationRow) {
	wait := int(inv.LastSentAt.Add(ResendMinInterval).Sub(h.now()).Seconds()) + 1
	if wait < 1 {
		wait = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(wait))
	httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelopeWithDetails(
		"bot.invitation_resend_too_soon", "the letter was sent a moment ago, try again later", r,
		map[string]any{"retry_after_seconds": wait},
	))
}

func (h *Handler) invitationFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("hbot: invitation "+what+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"bot.invitation_failed", "the invitation could not be updated", r,
	))
}
