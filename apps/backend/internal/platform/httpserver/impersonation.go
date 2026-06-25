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
package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// maxImpersonationDuration is the upper bound on impersonation token lifetime.
// Tokens older than 30 minutes cannot be issued — the admin must re-authenticate
// to start a new impersonation session.
const maxImpersonationDuration = 30 * time.Minute

// defaultImpersonationDuration is used when duration_seconds is zero or not supplied.
const defaultImpersonationDuration = 30 * time.Minute

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

// handleImpersonate serves POST /v1/admin/impersonate.
//
// Issues a scoped impersonation JWT on behalf of a platform superadmin.
// The issued token acts as the target user but carries extra claims
// (impersonated_by, impersonation_reason) so all downstream audit
// entries can tag the real actor.
func (s *Server) handleImpersonate(w http.ResponseWriter, r *http.Request) {
	if s.stub == nil || !s.stub.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"impersonation.unavailable",
			"impersonation requires the dev auth stub to be enabled",
			r,
		))
		return
	}

	// Parse and validate request body.
	var req impersonateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"impersonation.invalid_body",
			"request body is not valid JSON: "+err.Error(),
			r,
		))
		return
	}

	// user_id is mandatory and must be a valid UUID.
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"impersonation.missing_user_id",
			"user_id is required",
			r,
		))
		return
	}
	if _, err := uuid.Parse(req.UserID); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"impersonation.invalid_user_id",
			"user_id must be a valid UUID",
			r,
		))
		return
	}

	// reason is mandatory.
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"impersonation.missing_reason",
			"reason is required and must not be empty",
			r,
		))
		return
	}

	// Resolve duration, applying default and cap.
	duration := time.Duration(req.DurationSeconds) * time.Second
	if duration <= 0 {
		duration = defaultImpersonationDuration
	}
	if duration > maxImpersonationDuration {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"impersonation.duration_too_long",
			"duration_seconds must not exceed 1800 (30 minutes)",
			r,
		))
		return
	}

	// Identify the admin actor who is requesting impersonation.
	adminActor, ok := auth.ActorFromContext(r.Context())
	if !ok || !adminActor.IsAuthenticated() {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope(
			"auth.missing_token",
			"impersonation requires an authenticated admin actor",
			r,
		))
		return
	}

	// Issue the impersonation token. The target user's ID becomes "sub";
	// the admin's ID is stored in the "impersonated_by" claim.
	token, expiresAt, err := s.stub.IssueToken(r.Context(), auth.IssueRequest{
		ActorID:             req.UserID,
		ActorType:           auth.ActorTypeUser,
		TTL:                 duration,
		ImpersonatedBy:      adminActor.ID,
		ImpersonationReason: req.Reason,
	})
	if err != nil {
		s.logger.Error("impersonation: token mint failed",
			slog.String("admin_actor_id", adminActor.ID),
			slog.String("target_user_id", req.UserID),
			slog.Any("error", err),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"impersonation.mint_failed",
			"failed to mint impersonation token",
			r,
		))
		return
	}

	// Audit log the issuance — fire-and-forget. Failure to write the audit
	// entry must NOT abort the response (the admin has already received the
	// token), but it is logged at ERROR level so alerts fire.
	if s.audit != nil {
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    string(adminActor.Type),
			ActorID:      adminActor.ID,
			Action:       "impersonation.issue",
			ResourceType: "user",
			ResourceID:   req.UserID,
			RequestID:    logging.RequestID(r.Context()),
			TraceID:      logging.TraceID(r.Context()),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"reason":           req.Reason,
				"duration_seconds": int(duration.Seconds()),
				"expires_at":       expiresAt.UTC().Format(time.RFC3339),
			},
		}
		if err := s.audit.Write(r.Context(), ev); err != nil {
			s.logger.Error("impersonation: audit write failed",
				slog.String("admin_actor_id", adminActor.ID),
				slog.String("target_user_id", req.UserID),
				slog.Any("error", err),
			)
		}
	}

	s.logger.Info("impersonation: token issued",
		slog.String("admin_actor_id", adminActor.ID),
		slog.String("target_user_id", req.UserID),
		slog.String("reason", req.Reason),
		slog.Duration("duration", duration),
		slog.Time("expires_at", expiresAt),
	)

	writeJSON(w, http.StatusOK, impersonateResponse{
		Token:              token,
		ExpiresAt:          expiresAt.UTC().Format(time.RFC3339),
		ImpersonatedUserID: req.UserID,
		ImpersonatedBy:     adminActor.ID,
		Reason:             req.Reason,
	})
}
