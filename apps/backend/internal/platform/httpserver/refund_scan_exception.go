package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// handleGrantScanException lets ONE refund of a ticket that was already
// scanned at the gate go through (a show cancelled half-way, a scan made by
// mistake). Platform superadmin only, with X-Admin-Reason; the ticket must
// belong to the path organization. One exception per ticket, valid for at
// most 24 hours; the organization's owners get an e-mail and the sales bot
// tells the organization's group and the operator.
func (s *Server) handleGrantScanException(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	if !auth.HasSuperadminOrgAccess(r.Context()) {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope("refund.scan_exception_superadmin_only",
			"only a platform operator can allow a refund of a scanned ticket", r))
		return
	}
	ticketID, ok := httputil.UUIDPathParam(w, r, "ticket_id")
	if !ok {
		return
	}
	engine := s.refundEngine()
	if engine == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(refunds.CodeEngineUnavailable, "refunds are not available right now", r))
		return
	}
	var body struct {
		Note  string `json:"note"`
		Hours int    `json:"hours"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || (len(strings.TrimSpace(string(raw))) > 0 && json.Unmarshal(raw, &body) != nil) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(refunds.CodeInvalidRequest, "request body is not valid JSON", r))
		return
	}
	actor, via := refundActor(r)
	ex, err := engine.GrantScanException(r.Context(), refunds.ScanExceptionInput{
		OrgID: orgID, TicketID: ticketID, Actor: actor, Via: via,
		Reason: r.Header.Get("X-Admin-Reason"), Note: body.Note, Hours: body.Hours,
	})
	var refErr *refunds.Error
	if errors.As(err, &refErr) {
		writeEngineError(w, r, refErr)
		return
	}
	if err != nil {
		s.logger.Error("scan exception: grant failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.scan_exception_failed", "failed to record the exception", r))
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"scan_exception": ex})
}
