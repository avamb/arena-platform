// impersonation.go implements the platform admin impersonation endpoint (feature #167).
//
// Platform superadmins can issue a scoped JWT that temporarily acts as a target
// user, allowing them to diagnose user-specific issues without sharing credentials.
//
// Every impersonation session is:
//   - Short-lived: capped at 30 minutes (maxImpersonationDuration).
//   - Scoped: the issued JWT carries impersonated_by + impersonation_reason claims
//     so every downstream audit entry can tag the true actor.
//   - Audit-logged: the act of issuing the token is written to audit_events with
//     action="impersonation.issue", resource_type="user", resource_id=<target_user_id>.
//
// Endpoint:
//
//	POST /v1/admin/impersonate
//
// Request body:
//
//	{
//	  "user_id":          "<uuid>",     — target user to impersonate (required)
//	  "reason":           "<text>",     — business justification (required)
//	  "duration_seconds": <int>         — token lifetime in seconds; max 1800 (optional, default 1800)
//	}
//
// Successful response (200 OK):
//
//	{
//	  "token":                "<jwt>",       — impersonation bearer token
//	  "expires_at":           "<rfc3339>",   — absolute expiry (UTC)
//	  "impersonated_user_id": "<uuid>",      — the user being impersonated
//	  "impersonated_by":      "<uuid>",      — the admin actor who issued this token
//	  "reason":               "<text>"       — echoed back for client confirmation
//	}
//
// Access control:
//   - Requires a valid JWT (any actor).
//   - Requires the superadmin.read permission (inherits from admin/platform_superadmin role).
//
// The impersonation token carries the target user's ID as "sub" claim, plus two
// extra claims ("impersonated_by" = admin actor ID, "impersonation_reason" = reason)
// so that any handler that inspects auth.ActorFromContext can detect impersonation
// via actor.IsImpersonated() and log accordingly.
package hiam

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// MaxImpersonationDuration (maxImpersonationDuration) is the upper bound on
// impersonation token lifetime. Tokens older than 30 minutes cannot be issued
// — the admin must re-authenticate to start a new impersonation session.
const MaxImpersonationDuration = 30 * time.Minute

// DefaultImpersonationDuration (defaultImpersonationDuration) is used when
// duration_seconds is zero or not supplied.
const DefaultImpersonationDuration = 30 * time.Minute

// impersonateRequest is the JSON body for POST /v1/admin/impersonate.
type impersonateRequest struct {
	// UserID is the UUID of the target user to impersonate. Required.
	UserID string `json:"user_id"`
	// Reason is the mandatory human-readable business justification.
	Reason string `json:"reason"`
	// DurationSeconds sets the token lifetime. Capped at 1800 (30 min).
	// When zero or absent, defaultImpersonationDuration is used.
	DurationSeconds int `json:"duration_seconds"`
}

// impersonateResponse is the JSON body returned on success.
type impersonateResponse struct {
	Token              string `json:"token"`
	ExpiresAt          string `json:"expires_at"`
	ImpersonatedUserID string `json:"impersonated_user_id"`
	ImpersonatedBy     string `json:"impersonated_by"`
	Reason             string `json:"reason"`
}

// HandleImpersonate serves POST /v1/admin/impersonate.
// handleImpersonate is the legacy name for this operation.
//
// Issues a scoped impersonation JWT on behalf of a platform superadmin.
// The issued token acts as the target user but carries extra claims
// (impersonated_by, impersonation_reason) so all downstream audit
// entries can tag the real actor. Detects impersonation via actor.IsImpersonated().
func (h *Handler) HandleImpersonate(w http.ResponseWriter, r *http.Request) {
	if h.stub == nil || !h.stub.Enabled() {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"impersonation.unavailable",
			"impersonation requires the dev auth stub to be enabled",
			r,
		))
		return
	}

	var req impersonateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"impersonation.invalid_body",
			"request body is not valid JSON: "+err.Error(),
			r,
		))
		return
	}

	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"impersonation.missing_user_id",
			"user_id is required",
			r,
		))
		return
	}
	if _, err := uuid.Parse(req.UserID); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"impersonation.invalid_user_id",
			"user_id must be a valid UUID",
			r,
		))
		return
	}

	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"impersonation.missing_reason",
			"reason is required and must not be empty",
			r,
		))
		return
	}

	duration := time.Duration(req.DurationSeconds) * time.Second
	if duration <= 0 {
		duration = DefaultImpersonationDuration
	}
	if duration > MaxImpersonationDuration {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"impersonation.duration_too_long",
			"duration_seconds must not exceed 1800 (30 minutes)",
			r,
		))
		return
	}

	adminActor, ok := auth.ActorFromContext(r.Context())
	if !ok || !adminActor.IsAuthenticated() {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"auth.missing_token",
			"impersonation requires an authenticated admin actor",
			r,
		))
		return
	}

	token, expiresAt, err := h.stub.IssueToken(r.Context(), auth.IssueRequest{
		ActorID:             req.UserID,
		ActorType:           auth.ActorTypeUser,
		TTL:                 duration,
		ImpersonatedBy:      adminActor.ID,
		ImpersonationReason: req.Reason,
	})
	if err != nil {
		h.logger.Error("impersonation: token mint failed",
			slog.String("admin_actor_id", adminActor.ID),
			slog.String("target_user_id", req.UserID),
			slog.Any("error", err),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"impersonation.mint_failed",
			"failed to mint impersonation token",
			r,
		))
		return
	}

	if h.audit != nil {
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    string(adminActor.Type),
			ActorID:      adminActor.ID,
			Action:       "impersonation.issue",
			ResourceType: "user",
			ResourceID:   req.UserID,
			RequestID:    logging.RequestID(r.Context()),
			TraceID:      logging.TraceID(r.Context()),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"reason":           req.Reason,
				"duration_seconds": int(duration.Seconds()),
				"expires_at":       expiresAt.UTC().Format(time.RFC3339),
			},
		}
		if err := h.audit.Write(r.Context(), ev); err != nil {
			h.logger.Error("impersonation: audit write failed",
				slog.String("admin_actor_id", adminActor.ID),
				slog.String("target_user_id", req.UserID),
				slog.Any("error", err),
			)
		}
	}

	h.logger.Info("impersonation: token issued",
		slog.String("admin_actor_id", adminActor.ID),
		slog.String("target_user_id", req.UserID),
		slog.String("reason", req.Reason),
		slog.Duration("duration", duration),
		slog.Time("expires_at", expiresAt),
	)

	httputil.WriteJSON(w, http.StatusOK, impersonateResponse{
		Token:              token,
		ExpiresAt:          expiresAt.UTC().Format(time.RFC3339),
		ImpersonatedUserID: req.UserID,
		ImpersonatedBy:     adminActor.ID,
		Reason:             req.Reason,
	})
}
