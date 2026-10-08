// Package honboarding is the HTTP surface of the organizer application flow
// (08_architecture/34_onboarding_applications_ru.md, appendix A): the public
// routes the website calls straight from the browser, and the operator's
// routes under /v1/admin/onboarding.
package honboarding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/onboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/provisioning"
)

// TokenHeader carries the applicant's access token.
const TokenHeader = "X-Onboarding-Token"

const maxBody = 64 * 1024

// Handler serves the onboarding routes.
type Handler struct {
	svc      *onboarding.Service
	verifier Verifier
	logger   *slog.Logger
	// ipSalt keys the client-address hash stored with an application.
	ipSalt string
	// trustedProxies is TRUSTED_PROXY_COUNT.
	trustedProxies int
}

// New builds a Handler.
func New(svc *onboarding.Service, verifier Verifier, ipSalt string, trustedProxies int, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, verifier: verifier, ipSalt: ipSalt, trustedProxies: trustedProxies, logger: logger}
}

// ─── public view ─────────────────────────────────────────────────────────────

type publicApplication struct {
	ID                 string             `json:"id"`
	Status             string             `json:"status"`
	Locale             string             `json:"locale"`
	Email              string             `json:"email"`
	EmailConfirmed     bool               `json:"email_confirmed"`
	CurrentStep        string             `json:"current_step"`
	Progress           int                `json:"progress"`
	Answers            onboarding.Answers `json:"answers"`
	MissingFields      []string           `json:"missing_fields"`
	RequestedFields    []string           `json:"requested_fields"`
	InfoRequestMessage string             `json:"info_request_message,omitempty"`
	ExpiresAt          string             `json:"expires_at"`
	SubmittedAt        string             `json:"submitted_at,omitempty"`
}

func toPublic(a *onboarding.Application) publicApplication {
	missing := onboarding.Missing(a.Answers)
	if missing == nil {
		missing = []string{}
	}
	p := publicApplication{
		ID: a.ID.String(), Status: a.Status, Locale: a.Locale, Email: a.Email,
		EmailConfirmed: a.EmailConfirmedAt != nil, CurrentStep: a.CurrentStep, Progress: a.ProgressPct,
		Answers: a.Answers, MissingFields: missing, RequestedFields: a.RequestedFields,
		ExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if a.InfoRequestMessage != nil {
		p.InfoRequestMessage = *a.InfoRequestMessage
	}
	if a.SubmittedAt != nil {
		p.SubmittedAt = a.SubmittedAt.UTC().Format(time.RFC3339)
	}
	return p
}

// ─── public routes ───────────────────────────────────────────────────────────

// HandleFormSchema serves GET /v1/onboarding/form-schema?locale=xx.
func (h *Handler) HandleFormSchema(w http.ResponseWriter, r *http.Request) {
	schema, err := h.svc.Schema(r.Context(), r.URL.Query().Get("locale"))
	if err != nil {
		h.internal(w, r, "form-schema", err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	httputil.WriteJSON(w, http.StatusOK, schema)
}

type startRequest struct {
	FirstName      string            `json:"first_name"`
	LastName       string            `json:"last_name"`
	Email          string            `json:"email"`
	Phone          string            `json:"phone"`
	Locale         string            `json:"locale"`
	Source         string            `json:"source"`
	UTM            map[string]string `json:"utm"`
	TurnstileToken string            `json:"turnstile_token"`
	Honeypot       string            `json:"honeypot"`
}

// HandleStart serves POST /v1/onboarding/applications.
func (h *Handler) HandleStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if !h.decode(w, r, &req) {
		return
	}
	if req.Honeypot != "" {
		// A bot filled the hidden field: answer like a success, store nothing.
		httputil.WriteJSON(w, http.StatusCreated, map[string]any{
			"application_id": uuid.NewString(), "access_token": uuid.NewString() + uuid.NewString(), "progress": 0,
		})
		return
	}
	ip := httputil.TrustedClientIP(r, h.trustedProxies)
	if !h.checkCaptcha(w, r, req.TurnstileToken, ip) {
		return
	}
	// "telegram" and "operator" are server-side sources; the browser is the site.
	started, err := h.svc.Start(r.Context(), onboarding.StartInput{
		FirstName: req.FirstName, LastName: req.LastName, Email: req.Email, Phone: req.Phone,
		Locale: req.Locale, Source: onboarding.SourceSite, UTM: req.UTM, IPHash: h.hashIP(ip),
	})
	if err != nil {
		h.fail(w, r, "start", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"application_id": started.App.ID.String(),
		"access_token":   started.AccessToken,
		"progress":       started.App.ProgressPct,
		"current_step":   started.App.CurrentStep,
	})
}

// HandleGet serves GET /v1/onboarding/applications/{id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	app, err := h.svc.Get(r.Context(), id, r.Header.Get(TokenHeader))
	if err != nil {
		h.fail(w, r, "get", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"application": toPublic(app)})
}

type saveRequest struct {
	Answers map[string]any `json:"answers"`
}

// HandleSaveAnswers serves PUT /v1/onboarding/applications/{id}/answers.
func (h *Handler) HandleSaveAnswers(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var req saveRequest
	if !h.decode(w, r, &req) {
		return
	}
	if len(req.Answers) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("onboarding.empty_body", "answers are required", r))
		return
	}
	app, err := h.svc.SaveAnswers(r.Context(), id, r.Header.Get(TokenHeader), req.Answers)
	if err != nil {
		h.fail(w, r, "save", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"application": toPublic(app)})
}

// HandleSubmit serves POST /v1/onboarding/applications/{id}/submit.
func (h *Handler) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	app, err := h.svc.Submit(r.Context(), id, r.Header.Get(TokenHeader))
	if err != nil {
		h.fail(w, r, "submit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"application": toPublic(app)})
}

type confirmRequest struct {
	Token string `json:"token"`
}

// HandleConfirm serves POST /v1/onboarding/confirm.
func (h *Handler) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	var req confirmRequest
	if !h.decode(w, r, &req) {
		return
	}
	res, err := h.svc.Confirm(r.Context(), strings.TrimSpace(req.Token))
	if err != nil {
		h.fail(w, r, "confirm", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"application_id": res.App.ID.String(),
		"access_token":   res.AccessToken,
		"application":    toPublic(res.App),
	})
}

type resumeRequest struct {
	Email          string `json:"email"`
	TurnstileToken string `json:"turnstile_token"`
}

// HandleResume serves POST /v1/onboarding/resume. It answers 202 whether or
// not the address has an application.
func (h *Handler) HandleResume(w http.ResponseWriter, r *http.Request) {
	var req resumeRequest
	if !h.decode(w, r, &req) {
		return
	}
	if !h.checkCaptcha(w, r, req.TurnstileToken, httputil.TrustedClientIP(r, h.trustedProxies)) {
		return
	}
	if err := h.svc.Resume(r.Context(), req.Email); err != nil {
		h.logger.Error("onboarding: resume failed", slog.Any("error", err))
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// ─── operator routes ─────────────────────────────────────────────────────────

// HandleAdminList serves GET /v1/admin/onboarding/applications.
func (h *Handler) HandleAdminList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && !validStatus(status) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"onboarding.invalid_field", "unknown status", r, map[string]any{"fields": map[string]string{"status": "not_allowed"}}))
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	res, err := h.svc.List(r.Context(), onboarding.ListFilter{Status: status, Query: q.Get("q"), Limit: limit, Offset: offset})
	if err != nil {
		h.internal(w, r, "admin list", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

// HandleAdminGet serves GET /v1/admin/onboarding/applications/{id}.
func (h *Handler) HandleAdminGet(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	d, err := h.svc.Detail(r.Context(), id)
	if err != nil {
		h.fail(w, r, "admin get", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, d)
}

// HandleAdminRecheck serves POST /v1/admin/onboarding/applications/{id}/recheck.
func (h *Handler) HandleAdminRecheck(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	d, err := h.svc.Recheck(r.Context(), id)
	if err != nil {
		h.fail(w, r, "recheck", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, d)
}

// HandleAdminApprove serves POST /v1/admin/onboarding/applications/{id}/approve.
func (h *Handler) HandleAdminApprove(w http.ResponseWriter, r *http.Request) {
	id, reviewer, ok := h.decision(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Approve(r.Context(), id, onboarding.Actor{Type: "operator", ID: reviewer.String()}, reviewer)
	if err != nil {
		h.fail(w, r, "approve", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

type rejectRequest struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// HandleAdminReject serves POST …/{id}/reject.
func (h *Handler) HandleAdminReject(w http.ResponseWriter, r *http.Request) {
	id, reviewer, ok := h.decision(w, r)
	if !ok {
		return
	}
	var req rejectRequest
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.svc.Reject(r.Context(), id, reviewer, req.Reason, req.Message); err != nil {
		h.fail(w, r, "reject", err)
		return
	}
	h.ok(w, r, id)
}

type requestInfoRequest struct {
	Fields  []string `json:"fields"`
	Message string   `json:"message"`
}

// HandleAdminRequestInfo serves POST …/{id}/request-info.
func (h *Handler) HandleAdminRequestInfo(w http.ResponseWriter, r *http.Request) {
	id, reviewer, ok := h.decision(w, r)
	if !ok {
		return
	}
	var req requestInfoRequest
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.svc.RequestInfo(r.Context(), id, reviewer, req.Fields, req.Message); err != nil {
		h.fail(w, r, "request info", err)
		return
	}
	h.ok(w, r, id)
}

type extendRequest struct {
	Days int `json:"days"`
}

// HandleAdminExtend serves POST …/{id}/extend.
func (h *Handler) HandleAdminExtend(w http.ResponseWriter, r *http.Request) {
	id, reviewer, ok := h.decision(w, r)
	if !ok {
		return
	}
	var req extendRequest
	if !h.decodeOptional(w, r, &req) {
		return
	}
	if err := h.svc.Extend(r.Context(), id, reviewer, req.Days); err != nil {
		h.fail(w, r, "extend", err)
		return
	}
	h.ok(w, r, id)
}

// HandleAdminResend serves POST …/{id}/resend.
func (h *Handler) HandleAdminResend(w http.ResponseWriter, r *http.Request) {
	id, reviewer, ok := h.decision(w, r)
	if !ok {
		return
	}
	if err := h.svc.IssueLink(r.Context(), id, onboarding.Actor{Type: "operator", ID: reviewer.String()}); err != nil {
		h.fail(w, r, "resend", err)
		return
	}
	h.ok(w, r, id)
}

// HandleAdminPurge serves POST …/{id}/purge.
func (h *Handler) HandleAdminPurge(w http.ResponseWriter, r *http.Request) {
	id, reviewer, ok := h.decision(w, r)
	if !ok {
		return
	}
	if err := h.svc.Purge(r.Context(), id, reviewer); err != nil {
		h.fail(w, r, "purge", err)
		return
	}
	h.ok(w, r, id)
}

type noteRequest struct {
	Body string `json:"body"`
}

// HandleAdminAddNote serves POST …/{id}/notes.
func (h *Handler) HandleAdminAddNote(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	actor, authenticated := auth.ActorFromContext(r.Context())
	author, err := uuid.Parse(actor.ID)
	if !authenticated || err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.unauthenticated", "sign in as an operator", r))
		return
	}
	var req noteRequest
	if !h.decode(w, r, &req) {
		return
	}
	note, err := h.svc.AddNote(r.Context(), id, author, req.Body)
	if err != nil {
		h.fail(w, r, "note", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, note)
}

// HandleAdminGetSettings serves GET /v1/admin/onboarding/settings.
func (h *Handler) HandleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	set, err := h.svc.Settings(r.Context())
	if err != nil {
		h.internal(w, r, "settings", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, set)
}

type settingsRequest struct {
	ApprovalMode   *string   `json:"approval_mode"`
	DraftTTLDays   *int      `json:"draft_ttl_days"`
	PurgeAfterDays *int      `json:"purge_after_days"`
	Countries      *[]string `json:"countries"`
	MaxNewPerDay   *int      `json:"max_new_per_day"`
	TermsVersion   *string   `json:"terms_version"`
	PrivacyVersion *string   `json:"privacy_version"`
}

// HandleAdminPutSettings serves PUT /v1/admin/onboarding/settings. A key that
// is absent keeps its value.
func (h *Handler) HandleAdminPutSettings(w http.ResponseWriter, r *http.Request) {
	reviewer, ok := h.reviewer(w, r)
	if !ok {
		return
	}
	var req settingsRequest
	if !h.decode(w, r, &req) {
		return
	}
	set, err := h.svc.UpdateSettings(r.Context(), onboarding.SettingsUpdate{
		ApprovalMode: req.ApprovalMode, DraftTTLDays: req.DraftTTLDays, PurgeAfterDays: req.PurgeAfterDays,
		Countries: req.Countries, MaxNewPerDay: req.MaxNewPerDay, TermsVersion: req.TermsVersion, PrivacyVersion: req.PrivacyVersion,
	}, reviewer)
	if err != nil {
		h.fail(w, r, "update settings", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, set)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func validStatus(s string) bool {
	for _, x := range onboarding.Statuses {
		if x == s {
			return true
		}
	}
	return false
}

// reviewer extracts the operator's user id and requires X-Admin-Reason, the
// audit gate every state-changing /v1/admin route carries.
func (h *Handler) reviewer(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if strings.TrimSpace(r.Header.Get("X-Admin-Reason")) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("superadmin.missing_reason",
			"X-Admin-Reason header is required for superadmin operations", r))
		return uuid.Nil, false
	}
	actor, authenticated := auth.ActorFromContext(r.Context())
	id, err := uuid.Parse(actor.ID)
	if !authenticated || err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.unauthenticated", "sign in as an operator", r))
		return uuid.Nil, false
	}
	return id, true
}

// decision parses the {id} path parameter and the operator.
func (h *Handler) decision(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	reviewer, ok := h.reviewer(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return id, reviewer, true
}

// ok answers a decision with the refreshed card.
func (h *Handler) ok(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	d, err := h.svc.Detail(r.Context(), id)
	if err != nil {
		h.fail(w, r, "detail", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, d)
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("onboarding.empty_body", "request body is required", r))
		return false
	}
	return h.unmarshal(w, r, body, v)
}

// decodeOptional is decode for bodies that may be empty.
func (h *Handler) decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("onboarding.invalid_json", "cannot read the request body", r))
		return false
	}
	if len(body) == 0 {
		return true
	}
	return h.unmarshal(w, r, body, v)
}

func (h *Handler) unmarshal(w http.ResponseWriter, r *http.Request, body []byte, v any) bool {
	if len(body) > maxBody {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelope("onboarding.body_too_large", "request body is too large", r))
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("onboarding.invalid_json", "request body is not valid JSON", r))
		return false
	}
	return true
}

func (h *Handler) checkCaptcha(w http.ResponseWriter, r *http.Request, token, ip string) bool {
	if h.verifier == nil {
		return true
	}
	passed, err := h.verifier.Verify(r.Context(), token, ip)
	switch {
	case errors.Is(err, ErrCaptchaNotConfigured):
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("onboarding.captcha_not_configured", "the application form is not available yet", r))
		return false
	case err != nil:
		h.logger.Warn("onboarding: captcha check failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("onboarding.captcha_unavailable", "could not verify the captcha, try again", r))
		return false
	case !passed:
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope("onboarding.turnstile_failed", "the captcha was not passed", r))
		return false
	}
	return true
}

func (h *Handler) hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(h.ipSalt + "|" + ip))
	return hex.EncodeToString(sum[:])
}

// fail maps a service error to the error envelope.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var fe *onboarding.FieldsError
	switch {
	case errors.As(err, &fe):
		code, msg := "onboarding.invalid_field", "some fields are invalid"
		if fe.Code == "incomplete" {
			code, msg = "onboarding.incomplete", "required fields are missing"
		}
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(code, msg, r, map[string]any{"fields": fe.Fields}))
	case errors.Is(err, provisioning.ErrDuplicate):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("onboarding.duplicate_organization",
			"an organization with this name already exists; ask the applicant for another name", r))
	case errors.Is(err, onboarding.ErrNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("onboarding.not_found", "application not found", r))
	case errors.Is(err, onboarding.ErrLocked):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("onboarding.locked", "the application can no longer be edited", r))
	case errors.Is(err, onboarding.ErrWrongState):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("onboarding.wrong_state", "the application is not in a state that allows this", r))
	case errors.Is(err, onboarding.ErrEmailNotConfirmed):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("onboarding.email_not_confirmed", "confirm your e-mail first", r))
	case errors.Is(err, onboarding.ErrRateLimited):
		w.Header().Set("Retry-After", "600")
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope("onboarding.rate_limited", "too many requests, try again later", r))
	default:
		h.internal(w, r, op, err)
	}
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.logger.Error("onboarding: "+op+" failed", slog.Any("error", err))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("onboarding.internal", "something went wrong", r))
}
