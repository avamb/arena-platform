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
	"context"
	"encoding/json"
	"errors"
	"io"
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

// --- JSON ------------------------------------------------------------------

type disputeJSON struct {
	ID                 string  `json:"id"`
	Provider           string  `json:"provider"`
	ProviderDisputeID  *string `json:"provider_dispute_id"`
	Channel            string  `json:"channel"`
	OrderID            *string `json:"order_id"`
	OrderNumber        int64   `json:"order_number"`
	EventName          string  `json:"event_name"`
	SessionStartAt     *string `json:"session_start_at"`
	VenueTimezone      string  `json:"venue_timezone"`
	BuyerName          string  `json:"buyer_name"`
	BuyerEmail         string  `json:"buyer_email"`
	Amount             int64   `json:"amount"`
	Currency           string  `json:"currency"`
	Reason             string  `json:"reason"`
	ReasonGroup        string  `json:"reason_group"`
	ProviderStatus     string  `json:"provider_status"`
	State              string  `json:"state"`
	Outcome            *string `json:"outcome"`
	IsInquiry          bool    `json:"is_inquiry"`
	IsChargeRefundable bool    `json:"is_charge_refundable"`
	DueBy              *string `json:"due_by"`
	DueBySource        string  `json:"due_by_source"`
	RespondTo          string  `json:"respond_to"`
	LowValue           bool    `json:"low_value"`
	FundsWithdrawnAt   *string `json:"funds_withdrawn_at"`
	FundsReinstatedAt  *string `json:"funds_reinstated_at"`
	OpenedAt           string  `json:"opened_at"`
	SubmittedAt        *string `json:"submitted_at"`
	AcceptedAt         *string `json:"accepted_at"`
	ClosedAt           *string `json:"closed_at"`
}

func disputeTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

func disputeIDStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	v := id.String()
	return &v
}

func toDisputeJSON(d disputes.Dispute, sm disputes.Summary) disputeJSON {
	j := disputeJSON{
		ID: d.ID.String(), Provider: d.Provider, ProviderDisputeID: d.ProviderDisputeID, Channel: d.Channel,
		OrderID: disputeIDStr(d.OrderID), OrderNumber: sm.OrderNumber, EventName: sm.EventName, VenueTimezone: sm.Timezone,
		BuyerName: sm.BuyerName, BuyerEmail: sm.BuyerEmail,
		Amount: d.AmountMinor, Currency: d.Currency, Reason: d.Reason, ReasonGroup: d.ReasonGroup,
		ProviderStatus: d.ProviderStatus, State: d.State, Outcome: d.Outcome, IsInquiry: d.IsInquiry,
		IsChargeRefundable: d.IsChargeRefundable, DueBy: disputeTime(d.DueBy), DueBySource: d.DueBySource,
		RespondTo: d.RespondTo, LowValue: d.LowValue, FundsWithdrawnAt: disputeTime(d.FundsWithdrawnAt),
		FundsReinstatedAt: disputeTime(d.FundsReinstatedAt), OpenedAt: d.OpenedAt.UTC().Format(time.RFC3339),
		SubmittedAt: disputeTime(d.SubmittedAt), AcceptedAt: disputeTime(d.AcceptedAt), ClosedAt: disputeTime(d.ClosedAt),
	}
	if sm.SessionAt != "" {
		v := sm.SessionAt
		j.SessionStartAt = &v
	}
	return j
}

type disputeFileJSON struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	Bytes       int    `json:"bytes"`
	Pages       int    `json:"pages"`
	SHA256      string `json:"sha256"`
	ForProvider bool   `json:"for_provider"`
}

type disputePackageJSON struct {
	ID         string               `json:"id"`
	Version    int                  `json:"version"`
	Locale     string               `json:"locale"`
	Fields     map[string]string    `json:"fields"`
	Gaps       []string             `json:"gaps"`
	Uploaded   bool                 `json:"uploaded"`
	EmailDraft *disputes.EmailDraft `json:"email_draft"`
	TotalBytes int                  `json:"total_bytes"`
	TotalPages int                  `json:"total_pages"`
	BuiltAt    string               `json:"built_at"`
	Files      []disputeFileJSON    `json:"files"`
}

func toPackageJSON(p *disputes.Package) *disputePackageJSON {
	if p == nil {
		return nil
	}
	files := make([]disputeFileJSON, 0, len(p.Files))
	for _, f := range p.Files {
		files = append(files, disputeFileJSON{ID: f.ID.String(), Kind: f.Kind, Filename: f.Filename, Bytes: f.Bytes, Pages: f.Pages, SHA256: f.SHA256, ForProvider: f.ForProvider})
	}
	gaps := p.Gaps
	if gaps == nil {
		gaps = []string{}
	}
	return &disputePackageJSON{
		ID: p.ID.String(), Version: p.Version, Locale: p.Locale, Fields: p.Fields, Gaps: gaps,
		Uploaded: len(p.ProviderFileIDs) > 0, EmailDraft: p.EmailDraft, TotalBytes: p.TotalBytes,
		TotalPages: p.TotalPages, BuiltAt: p.BuiltAt.UTC().Format(time.RFC3339), Files: files,
	}
}

type disputeEventJSON struct {
	Kind           string          `json:"kind"`
	ProviderStatus string          `json:"provider_status"`
	ActorUserID    *string         `json:"actor_user_id"`
	ActorLabel     string          `json:"actor_label"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      string          `json:"created_at"`
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
	TestOutcome string               `json:"test_outcome"`
	Email       *disputes.EmailDraft `json:"email"`
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
	if _, err := svc.Submit(ctx, orgID, id, disputes.SubmitInput{TestOutcome: b.TestOutcome, Email: b.Email}, disputeActor(r)); err != nil {
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
