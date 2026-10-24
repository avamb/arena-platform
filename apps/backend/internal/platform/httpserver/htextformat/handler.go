// Package htextformat serves POST /v1/organizations/{org_id}/texts/format
// (spec 43, RT-04): a model adds markup to a text an organizer typed, and the
// code proves that no word was changed before the answer is used.
package htextformat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/textformat"
)

const (
	// QuotaWindow is the sliding window of the per-person and per-organization
	// caps.
	QuotaWindow = 24 * time.Hour
	// writeSlack is the time after the model deadline left to write the answer.
	writeSlack = 10 * time.Second
	// maxBodyBytes bounds the request: 4000 characters of any script fit in
	// 16 KiB, the rest is JSON.
	maxBodyBytes = 64 * 1024
	maxLocaleLen = 35
)

// localeRE is a language tag as the clients send it ("ru", "he", "en-GB").
var localeRE = regexp.MustCompile(`^[A-Za-z]{2,3}(?:[-_][A-Za-z0-9]{1,8}){0,3}$`)

// RowOrgAccess reports whether the caller may act on a row owned by orgID.
// When it refuses it writes the route's own 404 and returns false.
type RowOrgAccess func(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool

// Handler serves the route.
type Handler struct {
	logger       *slog.Logger
	formatter    textformat.Formatter
	rowOrgAccess RowOrgAccess
	// userQuota and orgQuota are the daily caps, nil = no cap.
	userQuota, orgQuota *ratelimit.SlidingWindow
	userMax, orgMax     int
	// timeout is the model deadline (textformat.RequestTimeout; tests lower it).
	timeout time.Duration
}

// New builds a handler without a formatter (every request then falls back
// with reason not_configured), without an organization guard (503) and
// without caps.
func New(logger *slog.Logger) *Handler {
	return &Handler{logger: logger, timeout: textformat.RequestTimeout}
}

// WithFormatter wires the model. A nil formatter keeps the route answering
// the plain conversion. The receiver is returned for chaining.
func (h *Handler) WithFormatter(f textformat.Formatter) *Handler {
	h.formatter = f
	return h
}

// WithRowOrgAccess wires the organization guard.
func (h *Handler) WithRowOrgAccess(fn RowOrgAccess) *Handler {
	h.rowOrgAccess = fn
	return h
}

// WithLimits sets the model calls one person and one organization may use in
// a sliding 24 hours; 0 switches that cap off.
func (h *Handler) WithLimits(perUser, perOrg int) *Handler {
	h.userQuota, h.userMax = newQuota(perUser)
	h.orgQuota, h.orgMax = newQuota(perOrg)
	return h
}

func newQuota(limit int) (*ratelimit.SlidingWindow, int) {
	if limit <= 0 {
		return nil, 0
	}
	return ratelimit.New(ratelimit.Config{MaxAttempts: limit, Window: QuotaWindow}), limit
}

// formatRequest is the body of the route.
type formatRequest struct {
	Text   string `json:"text"`
	Kind   string `json:"kind"`
	Locale string `json:"locale"`
}

// formatResponse is the body of a 200 answer. Clients look at AIApplied only:
// HTML is always safe ART to use, formatted or plain.
type formatResponse struct {
	HTML          string `json:"html"`
	AIApplied     bool   `json:"ai_applied"`
	Reason        string `json:"reason,omitempty"`
	PromptVersion string `json:"prompt_version"`
}

// Format serves POST /v1/organizations/{org_id}/texts/format.
//
// Order of the checks matters: nothing that costs money (the cap slot, the
// model) is touched before the caller, the organization, the body and the
// text have been proven, and a refused request never takes a slot. After
// that every failure is a 200 with the plain conversion of the text
// (richtext.Normalize) and ai_applied=false, so saving a text never breaks
// because of the model. The text and the model's answer are never logged or
// returned in an error.
func (h *Handler) Format(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if h.rowOrgAccess == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.org_access_unavailable", "organization access guard is not configured", r,
		))
		return
	}
	// The call costs the organization and the person a slot of their daily
	// caps, so it counts as a write.
	if !h.rowOrgAccess(w, r, orgID, true, "org.not_found", "organization not found") {
		return
	}

	var req formatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"text.invalid_request", "the body must be JSON with text, kind and locale", r,
		))
		return
	}
	kind, ok := textformat.ParseKind(strings.TrimSpace(req.Kind))
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"text.unknown_kind", "kind must be one of: description, email_note, session_message", r,
			map[string]any{"field": "kind"},
		))
		return
	}
	locale := strings.TrimSpace(req.Locale)
	if locale != "" && (len(locale) > maxLocaleLen || !localeRE.MatchString(locale)) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"text.invalid_locale", "locale must be a language tag such as ru, he or en-GB", r,
			map[string]any{"field": "locale"},
		))
		return
	}
	profile := kind.Profile()
	text := strings.TrimSpace(req.Text)
	n := utf8.RuneCountInString(text)
	if n > profile.MaxVisible {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"text.too_long", "the text is longer than this kind of text allows", r,
			map[string]any{"field": "text", "kind": string(kind), "max_chars": profile.MaxVisible},
		))
		return
	}
	// The plain conversion is both the fallback answer and the proof that the
	// text may be saved at all: a text with forbidden characters is refused
	// here, before the model is asked.
	plain, err := richtext.Normalize(text, richtext.FormatText, profile)
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"text.invalid", "the text is not allowed: "+err.Error(), r,
			map[string]any{"field": "text", "reason": string(richtext.ReasonOf(err))},
		))
		return
	}

	fallback := func(reason textformat.Reason, modelErr string) {
		h.respond(w, orgID, kind, n, plain, false, reason, modelErr)
	}
	if text == "" {
		fallback(textformat.ReasonNoText, "")
		return
	}
	if h.formatter == nil {
		fallback(textformat.ReasonNotConfigured, "")
		return
	}
	if !h.takeSlots(r, orgID) {
		fallback(textformat.ReasonQuota, "")
		return
	}

	// The router's request timeout (30 s) and the server's write deadline are
	// shorter than a slow call plus the answer, so the call runs on its own
	// deadline and the connection's write deadline moves past it, as the
	// poster reading does. A caller that hangs up does not stop a call that
	// was already paid for.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.timeout + writeSlack))
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.timeout)
	defer cancel()
	out, err := h.formatter.Format(ctx, textformat.Input{Text: text, Kind: kind, Locale: locale})
	if err != nil {
		reason, detail := classify(err)
		fallback(reason, detail)
		return
	}
	html, reason := textformat.Verify(text, out.HTML, profile)
	if reason != "" {
		fallback(reason, "")
		return
	}
	h.respond(w, orgID, kind, n, html, true, "", "")
}

// takeSlots takes one slot of the person's and one of the organization's
// daily cap, or none. A slot is taken only here, right before the model call.
func (h *Handler) takeSlots(r *http.Request, orgID uuid.UUID) bool {
	user := ""
	if actor, ok := auth.ActorFromContext(r.Context()); ok {
		user = actor.ID
	}
	org := orgID.String()
	if (h.userQuota != nil && h.userQuota.Count(user) >= h.userMax) ||
		(h.orgQuota != nil && h.orgQuota.Count(org) >= h.orgMax) {
		return false
	}
	if h.userQuota != nil && !h.userQuota.Take(user) {
		return false
	}
	if h.orgQuota != nil && !h.orgQuota.Take(org) {
		return false
	}
	return true
}

// classify turns an error of the model call into the reason of the fallback
// and a short word for the log. The error text itself is not logged: it may
// quote the model's answer.
func classify(err error) (textformat.Reason, string) {
	var ne net.Error
	switch {
	case errors.Is(err, textformat.ErrNotConfigured):
		return textformat.ReasonNotConfigured, ""
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return textformat.ReasonTimeout, "timeout"
	}
	var api *textformat.APIError
	if errors.As(err, &api) {
		return textformat.ReasonModelError, api.Error()
	}
	switch {
	case errors.Is(err, textformat.ErrTruncated):
		return textformat.ReasonModelError, "answer_truncated"
	case errors.Is(err, textformat.ErrNoToolCall):
		return textformat.ReasonModelError, "no_tool_call"
	}
	return textformat.ReasonModelError, "call_failed"
}

// respond writes the 200 answer and the one log line of the request: reason,
// prompt version, lengths, kind and organization, never a text.
func (h *Handler) respond(w http.ResponseWriter, orgID uuid.UUID, kind textformat.Kind, inRunes int, html string, applied bool, reason textformat.Reason, modelErr string) {
	attrs := []any{
		slog.String("org_id", orgID.String()),
		slog.String("kind", string(kind)),
		slog.String("prompt_version", textformat.PromptVersion),
		slog.Bool("ai_applied", applied),
		slog.String("reason", string(reason)),
		slog.Int("text_chars", inRunes),
		slog.Int("html_chars", utf8.RuneCountInString(html)),
	}
	if modelErr != "" {
		attrs = append(attrs, slog.String("model_error", modelErr))
	}
	if applied {
		h.logger.Info("texts: formatted", attrs...)
	} else {
		h.logger.Warn("texts: formatted without the model", attrs...)
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, formatResponse{
		HTML:          html,
		AIApplied:     applied,
		Reason:        string(reason),
		PromptVersion: textformat.PromptVersion,
	})
}
