package httpserver

// disputes_routes.go — the organizer's dispute API (spec 51.3 section 9):
// the queue, the card with its package, the PDF files, rebuilding the
// package, the one press that answers (submit) or accepts, the deadline of
// an e-mail dispute, a dispute entered by hand and the low-value setting.
// Every route checks membership of the path organization and loads the
// dispute INSIDE it, so another organization's dispute is the route's own
// 404. An organization API key is refused: the card carries the buyer's
// name and address, and only a person answers a bank.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// disputeWorkBudget bounds a request that calls the provider (upload of the
// files, the answer, the close): the router's 30 s timeout is too short.
const disputeWorkBudget = 2 * time.Minute

func (s *Server) mountDisputeRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "dispute.read", "disputes")
		pr.Get("/organizations/{org_id}/disputes", s.handleListDisputes)
		pr.Get("/organizations/{org_id}/disputes/{dispute_id}", s.handleGetDispute)
		pr.Get("/organizations/{org_id}/disputes/{dispute_id}/files/{file_id}", s.handleDisputeFile)
		pr.Get("/organizations/{org_id}/dispute-settings", s.handleGetDisputeSettings)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "dispute.respond", "disputes")
		pr.Post("/organizations/{org_id}/disputes", s.handleCreateManualDispute)
		pr.Post("/organizations/{org_id}/disputes/{dispute_id}/package", s.handleRebuildDisputePackage)
		pr.Post("/organizations/{org_id}/disputes/{dispute_id}/deadline", s.handleSetDisputeDeadline)
		pr.Post("/organizations/{org_id}/disputes/{dispute_id}/sent-by-organizer", s.handleDisputeSentByOrganizer)
		pr.Put("/organizations/{org_id}/dispute-settings", s.handlePutDisputeSettings)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "dispute.respond", "disputes")
		pr.Use(s.requireWebStepUp) // money: the web asks for the password again
		pr.Post("/organizations/{org_id}/disputes/{dispute_id}/submit", s.handleSubmitDispute)
		pr.Post("/organizations/{org_id}/disputes/{dispute_id}/accept", s.handleAcceptDispute)
	})
}

// disputeScope checks the caller and the path organization and returns the
// service; false means an answer was written.
func (s *Server) disputeScope(w http.ResponseWriter, r *http.Request) (*disputes.Service, uuid.UUID, bool) {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope("dispute.api_key_forbidden",
			"an API key cannot read or answer disputes; a member of the organization does it signed in", r))
		return nil, uuid.Nil, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return nil, uuid.Nil, false
	}
	svc := s.disputeService()
	if svc == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dispute.unavailable", "disputes are not available right now", r))
		return nil, uuid.Nil, false
	}
	return svc, orgID, true
}

func disputeActor(r *http.Request) disputes.Actor {
	var a disputes.Actor
	if actor, ok := auth.ActorFromContext(r.Context()); ok {
		if id, err := uuid.Parse(actor.ID); err == nil {
			a.UserID = &id
		}
		a.Label = string(actor.Type)
	}
	return a
}

func (s *Server) writeDisputeError(w http.ResponseWriter, r *http.Request, what string, err error) {
	var inv *disputes.ErrInvalid
	switch {
	case errors.Is(err, disputes.ErrNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("dispute.not_found", "dispute not found", r))
	case errors.As(err, &inv):
		status := http.StatusUnprocessableEntity
		switch inv.Code {
		case "dispute.already_submitted", "dispute.duplicate", "dispute.closed_for_evidence":
			status = http.StatusConflict
		case "dispute.order_not_found":
			status = http.StatusNotFound
		}
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope(inv.Code, inv.Message, r))
	case errors.Is(err, disputes.ErrProviderUnavailable):
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dispute.provider_unavailable",
			"the payment provider did not answer; nothing was sent, try again in a minute", r))
	default:
		s.logger.Error("dispute action failed", slog.String("action", what), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("dispute."+what+"_failed", "the dispute action failed", r))
	}
}

func disputeID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	return httputil.UUIDPathParam(w, r, "dispute_id")
}

// --- handlers ---------------------------------------------------------------

func (s *Server) handleListDisputes(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := disputes.ListFilter{Group: q.Get("group")}
	switch f.Group {
	case "", "open", "review", "closed":
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "group must be open, review or closed", r))
		return
	}
	if v := q.Get("order_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", "order_id must be a UUID", r))
			return
		}
		f.OrderID = &id
	}
	for name, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("dispute.invalid_request", name+" must be a non-negative number", r))
				return
			}
			*dst = n
		}
	}
	list, total, err := svc.List(r.Context(), orgID, f)
	if err != nil {
		s.writeDisputeError(w, r, "list", err)
		return
	}
	open, err := svc.CountOpen(r.Context(), orgID)
	if err != nil {
		s.writeDisputeError(w, r, "list", err)
		return
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, d := range list {
		ids = append(ids, d.ID)
	}
	sums, err := svc.Summaries(r.Context(), orgID, ids)
	if err != nil {
		s.writeDisputeError(w, r, "list", err)
		return
	}
	items := make([]disputeJSON, 0, len(list))
	for _, d := range list {
		items = append(items, toDisputeJSON(d, sums[d.ID]))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"disputes": items, "total": total, "open_count": open})
}

func (s *Server) writeDisputeCard(w http.ResponseWriter, r *http.Request, svc *disputes.Service, orgID, id uuid.UUID, status int, extra map[string]any) {
	ctx := r.Context()
	d, err := svc.Get(ctx, orgID, id)
	if err != nil {
		s.writeDisputeError(w, r, "read", err)
		return
	}
	sums, err := svc.Summaries(ctx, orgID, []uuid.UUID{id})
	if err != nil {
		s.writeDisputeError(w, r, "read", err)
		return
	}
	pkg, err := svc.LatestPackage(ctx, orgID, id)
	if err != nil {
		s.writeDisputeError(w, r, "read", err)
		return
	}
	evs, err := svc.Events(ctx, orgID, id)
	if err != nil {
		s.writeDisputeError(w, r, "read", err)
		return
	}
	st, err := svc.SettingsFor(ctx, orgID)
	if err != nil {
		s.writeDisputeError(w, r, "read", err)
		return
	}
	events := make([]disputeEventJSON, 0, len(evs))
	for _, e := range evs {
		p := e.Payload
		if len(p) == 0 {
			p = json.RawMessage("{}")
		}
		events = append(events, disputeEventJSON{Kind: e.Kind, ProviderStatus: e.ProviderStatus, ActorUserID: disputeIDStr(e.ActorUserID),
			ActorLabel: e.ActorLabel, Payload: p, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339)})
	}
	body := map[string]any{
		"dispute": toDisputeJSON(d, sums[id]),
		"package": toPackageJSON(pkg),
		"events":  events,
		"low_value": map[string]any{
			"enabled":   st.Enabled,
			"threshold": st.Threshold(d.Currency),
			"advised":   d.LowValue && d.Open(),
		},
		"test_outcome_available": d.Channel == disputes.ChannelAPI && d.Open() && svc.ConfigIsTest(ctx, d),
	}
	for k, v := range extra {
		body[k] = v
	}
	httputil.WriteJSON(w, status, body)
}

func (s *Server) handleGetDispute(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	id, ok := disputeID(w, r)
	if !ok {
		return
	}
	s.writeDisputeCard(w, r, svc, orgID, id, http.StatusOK, nil)
}

func (s *Server) handleDisputeFile(w http.ResponseWriter, r *http.Request) {
	svc, orgID, ok := s.disputeScope(w, r)
	if !ok {
		return
	}
	id, ok := disputeID(w, r)
	if !ok {
		return
	}
	fileID, ok := httputil.UUIDPathParam(w, r, "file_id")
	if !ok {
		return
	}
	info, content, err := svc.File(r.Context(), orgID, id, fileID)
	if err != nil {
		if errors.Is(err, disputes.ErrNotFound) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("dispute.file_not_found", "file not found", r))
			return
		}
		s.writeDisputeError(w, r, "file", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(info.Filename, `"`, "")+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
