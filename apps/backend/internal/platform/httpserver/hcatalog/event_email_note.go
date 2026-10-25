// event_email_note.go - the organizer's own text of an event for the buyer
// letters (EC-31, migration 0165).
//
//	PATCH /v1/organizations/{org_id}/events/{id}                          email_note: absent keeps, null or "" clears
//	POST  /v1/organizations/{org_id}/events/{event_id}/email-note/test    (event.update)
//
// The text passes richtext.Normalize (the letter-text profile) before it is
// stored as ART with note_format 'art' (spec 44, RT-02): email_note_format is
// "text" (default, the old marks) or "art" (HTML); at most five https links,
// 2000 visible characters. The ticket and session-change letters print it as
// "About the event".
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// EmailNoteTestQuota is the per-organization cap of test letters.
// ratelimit.SlidingWindow implements it: Take records an attempt only while
// the organization is under its limit.
type EmailNoteTestQuota interface {
	Take(key string) bool
}

// EmailNoteTestWindow is the window of the cap (eventnote.TestEmailsPerHour
// letters inside it).
const EmailNoteTestWindow = time.Hour

// WithEmailNoteTestLimiter wires the cap of test letters. nil switches it off.
// The limiter must live as long as the process (the Server owns it), never
// the per-request Handler.
func (h *Handler) WithEmailNoteTestLimiter(q EmailNoteTestQuota) *Handler {
	h.emailNoteTestLimiter = q
	return h
}

// parseEmailNote checks the PATCH field. set is false when the key is absent;
// note is "" when the field asks to clear. format is the request's
// email_note_format. A refused text has been answered
// (422 event.invalid_email_note) and ok is false.
func parseEmailNote(w http.ResponseWriter, r *http.Request, opt optionalString, format string) (note string, set, ok bool) {
	if !opt.Present {
		return "", false, true
	}
	if opt.Value == nil {
		return "", true, true
	}
	clean, err := eventtext.NormalizeNote(*opt.Value, format)
	if err != nil {
		writeInvalidEmailNote(w, r, err, eventtext.Reason(err))
		return "", false, false
	}
	return clean, true, true
}

func writeInvalidEmailNote(w http.ResponseWriter, r *http.Request, err error, reason string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_email_note", "the event letter text is not allowed: "+err.Error(), r,
		map[string]any{"field": "email_note", "reason": reason},
	))
}

// writeEmailNote stores (or, for "", removes) the note through q, which is
// bound to the PATCH's transaction.
func writeEmailNote(ctx context.Context, q *gen.Queries, eventID, orgID uuid.UUID, note string) error {
	if note == "" {
		return q.DeleteEventEmailNote(ctx, eventID, orgID)
	}
	return q.UpsertEventEmailNote(ctx, eventID, orgID, note, richtext.FormatART)
}

// auditEmailNote records the change after it has been committed. The audit
// row never carries the text.
func (h *Handler) auditEmailNote(r *http.Request, eventID, orgID uuid.UUID, note string) {
	ctx := r.Context()
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if auditErr := h.audit.Write(ctx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
			ActorID:      actor.ID,
			Action:       "v1.event.email_note.update",
			ResourceType: "event",
			ResourceID:   eventID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     map[string]any{"org_id": orgID.String(), "cleared": note == "", "length": len([]rune(note))},
		}); auditErr != nil {
			h.logger.Warn("event: email note audit write failed", slog.String("error", auditErr.Error()))
		}
	}
}

// hydrateEmailNotes fills EmailNote from one batched query. Like the other
// hydrations it is presentational: a failure logs and leaves the field nil.
func (h *Handler) hydrateEmailNotes(ctx context.Context, responses []eventResponse) {
	if h.eventQueries == nil || len(responses) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, r := range responses {
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	notes, err := h.eventQueries.ListEventEmailNotes(ctx, ids)
	if err != nil {
		h.logger.Warn("event: email note hydration failed", slog.String("error", err.Error()))
		return
	}
	for i := range responses {
		if id, err := uuid.Parse(responses[i].ID); err == nil {
			if n, ok := notes[id]; ok {
				note, format := n.Note, n.Format
				responses[i].EmailNote = &note
				responses[i].EmailNoteFormat = &format
			}
		}
	}
}

// emailNoteTestRequest is the optional body of the test route.
type emailNoteTestRequest struct {
	// To is the address to send to: the caller's own, or that of an active
	// member of the organization. Empty means the caller's own.
	To string `json:"to"`
	// Note previews an unsaved text. Absent means the stored note.
	Note *string `json:"note"`
	// NoteFormat is the format of Note: "text" (default) or "art".
	NoteFormat string `json:"note_format"`
	// Locale picks the language of the letter; empty means the caller's.
	Locale string `json:"locale"`
}

// emailNoteTestResponse is the 202 answer.
type emailNoteTestResponse struct {
	Queued bool   `json:"queued"`
	To     string `json:"to"`
}

// HandleEmailNoteTest queues a sample ticket letter carrying the event's note
// for the caller (or a member of the organization). It is a preview: nothing
// is recorded as a buyer letter and no ticket exists.
func (h *Handler) HandleEmailNoteTest(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return
	}

	var req emailNoteTestRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_body", "cannot read request body", r))
		return
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("event.invalid_json", "request body is not valid JSON", r))
			return
		}
	}

	// The text to preview: the draft in the body, else the stored note. A
	// draft is validated exactly like the save (eventtext.NormalizeNote, the
	// same profile and limits) and becomes ART, so the letter is the one the
	// buyer would get from that text. Nothing here is stored.
	var note, noteFormat string
	if req.Note != nil {
		draftFormat, fok := parseTextFormat(w, r, req.NoteFormat, "note_format", "event.invalid_email_note_format")
		if !fok {
			return
		}
		clean, nerr := eventtext.NormalizeNote(*req.Note, draftFormat)
		if nerr != nil {
			writeInvalidEmailNote(w, r, nerr, eventtext.Reason(nerr))
			return
		}
		note, noteFormat = clean, richtext.FormatART
	} else {
		stored, storedFormat, gerr := h.eventQueries.GetEventEmailNoteWithFormat(ctx, eventID, orgID)
		if gerr != nil && !errors.Is(gerr, pgx.ErrNoRows) {
			h.logger.Error("event: email note test read failed", slog.String("error", gerr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"event.email_note_test_failed", "failed to queue the test letter", r))
			return
		}
		note, noteFormat = stored, storedFormat
	}
	if note == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"event.email_note_empty", "the event has no letter text to preview: write one first", r))
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.logger.Error("event: email note test begin failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.email_note_test_failed", "failed to queue the test letter", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	own, ownLocale := callerEmail(ctx, tx)
	to, status, code, msg := resolveTestRecipient(ctx, tx, orgID, strings.TrimSpace(req.To), own)
	if code != "" {
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
		return
	}
	locale := strings.TrimSpace(req.Locale)
	if locale == "" {
		locale = ownLocale
	}

	// Only a request that passed every check above takes a slot of the cap.
	if h.emailNoteTestLimiter != nil && !h.emailNoteTestLimiter.Take(orgID.String()) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"event.email_note_test_rate_limited", "too many test letters for this organization: try again in an hour", r))
		return
	}
	if _, err := worker.EnqueueInTx(ctx, tx, eventnote.JobTypeTestEmail, eventnote.TestPayload{
		OrgID: orgID.String(), EventID: eventID.String(), To: to, Locale: locale, Note: note, NoteFormat: noteFormat,
	}, 3); err != nil {
		h.logger.Error("event: email note test enqueue failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.email_note_test_failed", "failed to queue the test letter", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("event: email note test commit failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.email_note_test_failed", "failed to queue the test letter", r))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if auditErr := h.audit.Write(ctx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
			ActorID:      actor.ID,
			Action:       "v1.event.email_note.test",
			ResourceType: "event",
			ResourceID:   eventID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     map[string]any{"org_id": orgID.String(), "to_self": strings.EqualFold(to, own)},
		}); auditErr != nil {
			h.logger.Warn("event: email note test audit write failed", slog.String("error", auditErr.Error()))
		}
	}
	httputil.WriteJSON(w, http.StatusAccepted, emailNoteTestResponse{Queued: true, To: to})
}

// queryRower is the part of a pgx transaction the recipient checks read.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// callerEmail is the signed-in user's own address and language. Both are empty
// for a caller that is not a user (an organization API key).
func callerEmail(ctx context.Context, q queryRower) (addr, locale string) {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.Type == auth.ActorTypeService {
		return "", ""
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil {
		return "", ""
	}
	_ = q.QueryRow(ctx, `SELECT btrim(email), preferred_locale FROM users
		WHERE id = $1 AND deactivated_at IS NULL AND anonymized_at IS NULL`, id).Scan(&addr, &locale)
	return addr, locale
}

// resolveTestRecipient decides where the test letter goes: the caller's own
// address, or the address of an ACTIVE member of the organization. Any other
// address is refused, so the route cannot be used to mail strangers.
func resolveTestRecipient(ctx context.Context, q queryRower, orgID uuid.UUID, to, own string) (addr string, status int, code, msg string) {
	if to == "" {
		if own == "" {
			return "", http.StatusBadRequest, "event.email_note_test_recipient_required",
				"name the address to send the test letter to"
		}
		return own, 0, "", ""
	}
	parsed, err := mail.ParseAddress(to)
	if err != nil || parsed.Address != to || strings.ContainsAny(to, "\r\n") {
		return "", http.StatusBadRequest, "event.email_note_test_invalid_recipient", "the address is not valid"
	}
	if own != "" && strings.EqualFold(own, to) {
		return own, 0, "", ""
	}
	var member string
	err = q.QueryRow(ctx, `SELECT btrim(u.email) FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND m.status = 'active' AND lower(btrim(u.email)) = lower($2)
		  AND u.deactivated_at IS NULL AND u.anonymized_at IS NULL`, orgID, to).Scan(&member)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", http.StatusUnprocessableEntity, "event.email_note_test_recipient_not_member",
			"the test letter can go only to your own address or to a member of the organization"
	}
	if err != nil {
		return "", http.StatusInternalServerError, "event.email_note_test_failed", "failed to queue the test letter"
	}
	return member, 0, "", ""
}
