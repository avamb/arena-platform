package hiam

import (
	"context"
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const passwordResetTokenTTL = time.Hour

var globalAdminUserRoles = map[string]bool{
	"platform_operator":   true,
	"platform_superadmin": true,
}

var orgScopedAdminUserRoles = map[string]bool{
	"agent":                       true,
	"external_ticketing_operator": true,
	"network_operator":            true,
	"organizer":                   true,
}

type adminCreateUserRequest struct {
	Email  string `json:"email"`
	Role   string `json:"role"`
	OrgID  string `json:"org_id,omitempty"`
	Locale string `json:"locale,omitempty"`
}

type adminCreateUserResponse struct {
	User       adminCreatedUserDTO       `json:"user"`
	Onboarding adminCreatedOnboardingDTO `json:"onboarding"`
	Message    string                    `json:"message"`
}

type adminCreatedUserDTO struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Role      string  `json:"role"`
	Scope     string  `json:"scope"`
	OrgID     *string `json:"org_id,omitempty"`
	CreatedAt string  `json:"created_at"`
}

type adminCreatedOnboardingDTO struct {
	PasswordResetIssued bool   `json:"password_reset_issued"`
	ExpiresAt           string `json:"expires_at"`
	Delivery            string `json:"delivery"`
}

// HandleAdminCreateUser serves POST /v1/admin/users.
// It creates a new account by email and immediately assigns the requested role.
// handleAdminCreateUser is the legacy name for this operation.
func (h *Handler) HandleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_user.invalid_body", "cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_user.empty_body", "request body is required", r,
		))
		return
	}

	var req adminCreateUserRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_user.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	email, err := users.NormalizeEmail(req.Email)
	if err != nil || !strings.Contains(email, "@") {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_user.invalid_email", "email address is invalid", r,
			map[string]any{"field": "email"},
		))
		return
	}

	role := strings.TrimSpace(req.Role)
	if role == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_user.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": adminCreateUserRoleList()},
		))
		return
	}

	orgID, scope, ok := validateAdminUserRoleScope(w, r, role, strings.TrimSpace(req.OrgID))
	if !ok {
		return
	}

	locale := strings.TrimSpace(req.Locale)
	if locale == "" {
		locale = "en"
	}

	tempPassword, err := users.GenerateVerificationToken()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"internal.token_generation_failed", "failed to generate onboarding secret", r,
		))
		return
	}
	hash, err := users.HashPassword(tempPassword)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"internal.password_hash_failed", "failed to hash password", r,
		))
		return
	}

	ctx := r.Context()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)
	userRow, err := q.InsertUser(ctx, email, hash, locale)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"admin_user.email_already_registered",
				"this email address is already registered", r,
				map[string]any{"field": "email"},
			))
			return
		}
		h.logger.Error("admin_user: insert user failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to create user", r,
		))
		return
	}

	if scope == "global" {
		if err := assignGlobalUserRole(ctx, tx, userRow.ID, role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"admin_user.invalid_role", "role is not registered", r,
					map[string]any{"field": "role", "allowed": adminCreateUserRoleList()},
				))
				return
			}
			h.logger.Error("admin_user: assign global role failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"admin_user.role_assign_failed", "failed to assign role", r,
			))
			return
		}
	} else {
		if _, err := q.InsertMembership(ctx, userRow.ID, *orgID, role); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"admin_user.invalid_org_id", "org_id does not exist", r,
					map[string]any{"field": "org_id"},
				))
				return
			}
			h.logger.Error("admin_user: insert membership failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"admin_user.role_assign_failed", "failed to assign role", r,
			))
			return
		}
	}

	resetToken, err := users.GenerateVerificationToken()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"internal.token_generation_failed", "failed to generate reset token", r,
		))
		return
	}
	resetExpiresAt := time.Now().UTC().Add(passwordResetTokenTTL)
	if err := q.InsertPasswordResetToken(ctx, resetToken, userRow.ID, resetExpiresAt); err != nil {
		h.logger.Error("admin_user: insert reset token failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"internal.token_insert_failed", "failed to save reset token", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		metadata := map[string]any{
			"reason": reason,
			"email":  userRow.Email,
			"role":   role,
			"scope":  scope,
		}
		if orgID != nil {
			metadata["org_id"] = orgID.String()
		}
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.admin.user.create",
			ResourceType: "user",
			ResourceID:   userRow.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     metadata,
		}); err != nil {
			h.logger.Error("admin_user: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"admin_user.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("admin_user: commit failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to save user", r,
		))
		return
	}

	resetURL := requestBaseURL(r) + "/v1/auth/password-reset/confirm?token=" + resetToken
	slog.Info("EMAIL DELIVERY (dev-mode): admin-created user password setup",
		"to", userRow.Email,
		"subject", "Set up your Arena Platform password",
		"reset_url", resetURL,
		"expires_at", resetExpiresAt.Format(time.RFC3339),
		"user_id", userRow.ID.String(),
	)

	var orgIDString *string
	if orgID != nil {
		s := orgID.String()
		orgIDString = &s
	}
	httputil.WriteJSON(w, http.StatusCreated, adminCreateUserResponse{
		User: adminCreatedUserDTO{
			ID:        userRow.ID.String(),
			Email:     userRow.Email,
			Role:      role,
			Scope:     scope,
			OrgID:     orgIDString,
			CreatedAt: userRow.CreatedAt.UTC().Format(time.RFC3339Nano),
		},
		Onboarding: adminCreatedOnboardingDTO{
			PasswordResetIssued: true,
			ExpiresAt:           resetExpiresAt.Format(time.RFC3339),
			Delivery:            "email",
		},
		Message: "User created. A password setup link has been issued to the email address.",
	})
}

func validateAdminUserRoleScope(w http.ResponseWriter, r *http.Request, role, rawOrgID string) (*uuid.UUID, string, bool) {
	if globalAdminUserRoles[role] {
		if rawOrgID != "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"admin_user.org_not_allowed",
				"org_id must be omitted for global platform roles", r,
				map[string]any{"field": "org_id"},
			))
			return nil, "", false
		}
		return nil, "global", true
	}
	if orgScopedAdminUserRoles[role] {
		if rawOrgID == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"admin_user.missing_org_id",
				"org_id is required for organization-scoped roles", r,
				map[string]any{"field": "org_id"},
			))
			return nil, "", false
		}
		orgID, err := uuid.Parse(rawOrgID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"admin_user.invalid_org_id", "org_id must be a valid UUID", r,
				map[string]any{"field": "org_id"},
			))
			return nil, "", false
		}
		return &orgID, "organization", true
	}
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"admin_user.invalid_role",
		"role must be one of: agent, external_ticketing_operator, network_operator, organizer, platform_operator, platform_superadmin",
		r,
		map[string]any{"field": "role", "allowed": adminCreateUserRoleList()},
	))
	return nil, "", false
}

func assignGlobalUserRole(ctx context.Context, tx pgx.Tx, userID uuid.UUID, role string) error {
	var roleID uuid.UUID
	return tx.QueryRow(ctx, `
		INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, r.id, NULL
		FROM   roles r
		WHERE  r.name = $2
		  AND  r.org_id IS NULL
		RETURNING role_id
	`, userID, role).Scan(&roleID)
}

func adminCreateUserRoleList() []string {
	return []string{
		"agent",
		"external_ticketing_operator",
		"network_operator",
		"organizer",
		"platform_operator",
		"platform_superadmin",
	}
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	return scheme + "://" + host
}
