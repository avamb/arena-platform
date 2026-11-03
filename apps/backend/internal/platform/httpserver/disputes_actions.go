package httpserver

// disputes_actions.go — the dispute routes that change something
// (disputes_routes.go): rebuild, submit, accept, deadline, answered
// outside arena, a dispute entered by hand and the low-value rule.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// providerWork runs fn on a context detached from the request, with the
// dispute budget, so a closed connection never leaves a half-sent answer.
func (s *Server) providerWork(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc) {
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(disputeWorkBudget + 10*time.Second))
	}
	return context.WithTimeout(context.WithoutCancel(r.Context()), disputeWorkBudget)
}

func (s *Server) handleRebuildDisputePackage(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	id, ok := disputeID(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.providerWork(w, r)
	defer cancel()
	res, err := svc.Build(ctx, orgID, id, disputeActor(r))
	if err != nil {
		s.writeDisputeError(w, r, "build", err)
		return
	}
	s.writeDisputeCard(w, r, svc, orgID, id, http.StatusOK, map[string]any{
		"draft_saved": res.DraftSaved, "draft_error": disputeNilIfEmpty(res.DraftError),
	})
}

func disputeNilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type submitDisputeBody struct {
	TestOutcome    string               `json:"test_outcome"`
	Email          *disputes.EmailDraft `json:"email"`
	PackageVersion int                  `json:"package_version"`
}

func decodeDisputeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "could not read the body", r))
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "the body is not valid JSON for this route", r))
		return false
	}
	return true
}

func (s *Server) handleSubmitDispute(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	id, ok := disputeID(w, r)
	if !ok {
		return
	}
	var b submitDisputeBody
	if !decodeDisputeBody(w, r, &b) {
		return
	}
	switch b.TestOutcome {
	case "", "win", "lose":
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "test_outcome must be win or lose", r))
		return
	}
	ctx, cancel := s.providerWork(w, r)
	defer cancel()
	if _, err := svc.Submit(ctx, orgID, id, disputes.SubmitInput{TestOutcome: b.TestOutcome, Email: b.Email, PackageVersion: b.PackageVersion}, disputeActor(r)); err != nil {
		s.writeDisputeError(w, r, "submit", err)
		return
	}
	s.writeDisputeCard(w, r, svc, orgID, id, http.StatusOK, nil)
}

func (s *Server) handleAcceptDispute(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	id, ok := disputeID(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.providerWork(w, r)
	defer cancel()
	if _, err := svc.Accept(ctx, orgID, id, disputeActor(r)); err != nil {
		s.writeDisputeError(w, r, "accept", err)
		return
	}
	s.writeDisputeCard(w, r, svc, orgID, id, http.StatusOK, nil)
}

type deadlineBody struct {
	DueBy     *string `json:"due_by"`
	RespondTo *string `json:"respond_to"`
}

func (s *Server) handleSetDisputeDeadline(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	id, ok := disputeID(w, r)
	if !ok {
		return
	}
	var b deadlineBody
	if !decodeDisputeBody(w, r, &b) {
		return
	}
	in := disputes.DeadlineInput{RespondTo: b.RespondTo}
	if b.DueBy != nil {
		t, err := time.Parse(time.RFC3339, *b.DueBy)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "due_by must be RFC 3339 with a zone", r))
			return
		}
		in.DueBy = &t
	}
	if _, err := svc.SetDeadline(r.Context(), orgID, id, in, disputeActor(r)); err != nil {
		s.writeDisputeError(w, r, "deadline", err)
		return
	}
	s.writeDisputeCard(w, r, svc, orgID, id, http.StatusOK, nil)
}

func (s *Server) handleDisputeSentByOrganizer(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	id, ok := disputeID(w, r)
	if !ok {
		return
	}
	if _, err := svc.MarkSentByOrganizer(r.Context(), orgID, id, disputeActor(r)); err != nil {
		s.writeDisputeError(w, r, "mark_sent", err)
		return
	}
	s.writeDisputeCard(w, r, svc, orgID, id, http.StatusOK, nil)
}

type manualDisputeBody struct {
	OrderID           string  `json:"order_id"`
	Reason            string  `json:"reason"`
	DueBy             *string `json:"due_by"`
	RespondTo         string  `json:"respond_to"`
	ProviderDisputeID string  `json:"provider_dispute_id"`
	Amount            int64   `json:"amount"`
}

func (s *Server) handleCreateManualDispute(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	var b manualDisputeBody
	if !decodeDisputeBody(w, r, &b) {
		return
	}
	orderID, err := uuid.Parse(b.OrderID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "order_id must be a UUID", r))
		return
	}
	in := disputes.ManualInput{OrderID: orderID, Reason: b.Reason, RespondTo: b.RespondTo, ProviderDisputeID: b.ProviderDisputeID, AmountMinor: b.Amount}
	if b.DueBy != nil {
		t, err := time.Parse(time.RFC3339, *b.DueBy)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "due_by must be RFC 3339 with a zone", r))
			return
		}
		in.DueBy = &t
	}
	d, err := svc.CreateManual(r.Context(), orgID, in, disputeActor(r))
	if err != nil {
		s.writeDisputeError(w, r, "create", err)
		return
	}
	s.writeDisputeCard(w, r, svc, orgID, d.ID, http.StatusCreated, nil)
}

func (s *Server) handleGetDisputeSettings(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	st, err := svc.SettingsFor(r.Context(), orgID)
	if err != nil {
		s.writeDisputeError(w, r, "settings", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, st)
}

type disputeSettingsBody struct {
	Enabled    *bool            `json:"low_value_enabled"`
	Thresholds map[string]int64 `json:"low_value_thresholds"`
}

func (s *Server) handlePutDisputeSettings(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	var b disputeSettingsBody
	if !decodeDisputeBody(w, r, &b) {
		return
	}
	if b.Enabled == nil || b.Thresholds == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "low_value_enabled and low_value_thresholds are required", r))
		return
	}
	clean := map[string]int64{}
	for cur, v := range b.Thresholds {
		cur = strings.ToUpper(strings.TrimSpace(cur))
		if len(cur) != 3 || v < 0 || v > 100_000_00 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request",
				"each threshold is a 3-letter currency and a non-negative amount in minor units", r))
			return
		}
		clean[cur] = v
	}
	st, err := svc.SaveSettings(r.Context(), orgID, *b.Enabled, clean, disputeActor(r).UserID)
	if err != nil {
		s.writeDisputeError(w, r, "settings", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, st)
}
