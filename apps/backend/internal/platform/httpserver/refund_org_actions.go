package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// refundActor builds the engine actor of the calling user, if any.
func refundActor(r *http.Request) (refunds.Actor, string) {
	var a refunds.Actor
	if actor, ok := auth.ActorFromContext(r.Context()); ok {
		a = refunds.Actor{Type: "user", ID: actor.ID}
	}
	via, _ := audit.ClientChannelFromContext(r.Context())
	return a, via
}

func (s *Server) writeRefundActionError(w http.ResponseWriter, r *http.Request, what string, err error) {
	var refErr *refunds.Error
	if errors.As(err, &refErr) {
		writeEngineError(w, r, refErr)
		return
	}
	s.logger.Error("refund action failed", slog.String("action", what), slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund."+what+"_failed", "the refund action failed", r))
}

// handleRetryOrgRefund starts a new refund from a failed or never-sent one.
func (s *Server) handleRetryOrgRefund(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	refundID, ok := httputil.UUIDPathParam(w, r, "refund_id")
	if !ok {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	actor, via := refundActor(r)
	wctx, cancel := refundWorkCtx(w, r)
	defer cancel()
	res, err := engine.Retry(wctx, refunds.RetryInput{OrgID: orgID, RefundID: refundID, Actor: actor, Via: via})
	if err != nil {
		s.writeRefundActionError(w, r, "retry", err)
		return
	}
	items := make([]refundItemJSON, 0, len(res.Refunds))
	for _, rf := range res.Refunds {
		items = append(items, refundItem(rf))
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
		"batch_id": res.BatchID.String(), "order_id": res.OrderID.String(),
		"replayed": res.Replayed, "refunds": items,
	})
}

// handleResolveOrgRefund closes a manual_review refund. Platform superadmin
// only: the operator states what they saw at the provider.
func (s *Server) handleResolveOrgRefund(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	if !auth.HasSuperadminOrgAccess(r.Context()) {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope("refund.resolve_superadmin_only", "only a platform operator can close a refund by hand", r))
		return
	}
	refundID, ok := httputil.UUIDPathParam(w, r, "refund_id")
	if !ok {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	var body struct {
		Outcome string `json:"outcome"`
		Note    string `json:"note"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(refunds.CodeInvalidRequest, "request body is not valid JSON", r))
		return
	}
	actor, via := refundActor(r)
	wctx, cancel := refundWorkCtx(w, r)
	defer cancel()
	out, err := engine.Resolve(wctx, refunds.ResolveInput{OrgID: orgID, RefundID: refundID,
		Outcome: body.Outcome, Note: body.Note, Actor: actor, Via: via})
	if err != nil {
		s.writeRefundActionError(w, r, "resolve", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, refundItem(out))
}
