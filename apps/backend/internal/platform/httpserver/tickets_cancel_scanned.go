package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// cancelBodyLimit bounds the cancel body read ahead of the handler.
const cancelBodyLimit = 1 << 16

// cancelMoneyAllowedForScan refuses a ticket cancellation that would return
// money (refund_mode manual or automatic) for a ticket that was scanned at
// the gate, BEFORE anything is cancelled: the ticket stays valid, nothing is
// written but the journal of the attempt. A cancellation without money
// (refund_mode none) is not concerned. The body is put back for the handler.
func (s *Server) cancelMoneyAllowedForScan(w http.ResponseWriter, r *http.Request, orgID, ticketID uuid.UUID) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cancelBodyLimit))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("ticket.invalid_body", "request body could not be read", r))
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		RefundMode string `json:"refund_mode"`
	}
	if json.Unmarshal(raw, &body) != nil || (body.RefundMode != "manual" && body.RefundMode != "automatic") {
		return true // the handler validates the body and answers for it
	}
	engine := s.refundEngine()
	if engine == nil {
		return true
	}
	scanned, err := engine.TicketScanned(r.Context(), ticketID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			refunds.CodeEngineUnavailable, "whether the ticket was scanned could not be checked; try again", r))
		return false
	}
	if !scanned {
		return true
	}
	var actor refunds.Actor
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		actor = refunds.Actor{Type: "user", ID: a.ID}
		if a.IsService() {
			actor.Type = "api_key"
		}
	}
	via, _ := audit.ClientChannelFromContext(r.Context())
	notice := engine.RecordScannedBlock(r.Context(), orgID, ticketID, actor, via)
	e := refunds.ScannedRefusalWithNotice(ticketID, notice)
	httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelopeWithDetails(e.Code, e.Message, r, e.Details))
	return false
}
