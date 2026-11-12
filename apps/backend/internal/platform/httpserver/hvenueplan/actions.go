package hvenueplan

// actions.go — what the organizer does on an intake: answers, a text
// correction, calling an administrator, cancelling.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

const (
	maxCorrectionText = 2000
	maxAnswers        = 50
)

// lockedFn changes a locked intake inside tx. It writes its own error answer
// and returns ok=false to roll back.
type lockedFn func(ctx context.Context, tx pgx.Tx, q *gen.Queries, in gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool)

// locked runs fn on the intake locked FOR UPDATE, commits, and answers the
// card with status.
func (h *Handler) locked(w http.ResponseWriter, r *http.Request, id uuid.UUID, admin bool, status int, fn lockedFn) {
	ctx := r.Context()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "failed to begin transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	in, err := q.LockVenuePlanIntake(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.not_found", "hall plan intake not found")
			return
		}
		h.failed(w, r, "lock", err)
		return
	}
	out, ok := fn(ctx, tx, q, in)
	if !ok {
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.failed(w, r, "commit", err)
		return
	}
	h.writeCard(w, r, status, out, admin)
}

// currentDraftRow checks that n is the current draft of the locked intake.
func (h *Handler) currentDraftRow(w http.ResponseWriter, r *http.Request, q *gen.Queries, in gen.VenuePlanIntakeRow, n int32) (gen.VenuePlanDraftRow, bool) {
	if in.CurrentDraft == nil || *in.CurrentDraft != n {
		draftStale(w, r, in.CurrentDraft)
		return gen.VenuePlanDraftRow{}, false
	}
	d, err := q.GetVenuePlanDraft(r.Context(), in.ID, n)
	if err != nil {
		h.failed(w, r, "draft lookup", err)
		return gen.VenuePlanDraftRow{}, false
	}
	return d, true
}

type answersBody struct {
	Draft   int32              `json:"draft"`
	Answers []venueplan.Answer `json:"answers"`
}

// HandleAnswers answers POST .../plan-intakes/{intake_id}/answers.
func (h *Handler) HandleAnswers(w http.ResponseWriter, r *http.Request) {
	in, ok := h.orgIntake(w, r)
	if !ok {
		return
	}
	var body answersBody
	if !readJSON(w, r, &body, false) {
		return
	}
	if len(body.Answers) == 0 || len(body.Answers) > maxAnswers {
		writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_answers",
			"send from 1 to 50 answers", map[string]any{"field": "answers"})
		return
	}
	for _, a := range body.Answers {
		if a.QuestionID == uuid.Nil || len(a.Answer) == 0 || string(a.Answer) == "null" {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_answers",
				"every answer needs a question_id and an answer", map[string]any{"field": "answers"})
			return
		}
	}
	user := actorUserID(r.Context())
	h.locked(w, r, in.ID, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, in gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		if in.State != string(venueplan.StateReview) {
			wrongState(w, r, in.State)
			return in, false
		}
		d, ok := h.currentDraftRow(w, r, q, in, body.Draft)
		if !ok {
			return in, false
		}
		for _, a := range body.Answers {
			n, err := q.AnswerVenuePlanQuestion(ctx, a.QuestionID, d.ID, a.Answer, user)
			if err != nil {
				h.failed(w, r, "answer", err)
				return in, false
			}
			if n == 0 {
				writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.unknown_question",
					"a question_id is not a question of this draft", map[string]any{"question_id": a.QuestionID.String()})
				return in, false
			}
		}
		ops, err := json.Marshal(body.Answers)
		if err != nil {
			h.failed(w, r, "answers", err)
			return in, false
		}
		if _, err := q.InsertVenuePlanCorrection(ctx, gen.InsertVenuePlanCorrectionParams{
			IntakeID: in.ID, DraftID: &d.ID, Kind: string(venueplan.CorrectionAnswer), Operations: ops,
			Status: string(venueplan.CorrectionPending), CreatedBy: user,
		}); err != nil {
			h.failed(w, r, "answers", err)
			return in, false
		}
		// Wave 1 has no renderer: the answers go to an administrator. With a
		// renderer the worker builds the next draft (state generating).
		to, reason := venueplan.StateOperator, venueplan.EscalationAnswers
		p := gen.UpdateVenuePlanIntakeStateParams{EscalationReason: reasonPtr(reason)}
		if h.renderer != nil {
			to, reason, p = venueplan.StateGenerating, "", gen.UpdateVenuePlanIntakeStateParams{}
		}
		return h.finishMove(ctx, w, r, tx, q, in, to, p, reason, "v1.venue_plan_intake.answer",
			map[string]any{"draft": d.N, "answers": len(body.Answers)})
	})
}

// finishMove moves the state, writes the audit row and tells the notifier.
func (h *Handler) finishMove(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, q *gen.Queries,
	in gen.VenuePlanIntakeRow, to venueplan.State, p gen.UpdateVenuePlanIntakeStateParams,
	reason venueplan.EscalationReason, action string, meta map[string]any) (gen.VenuePlanIntakeRow, bool) {
	from := in.State
	out, moved, err := moveState(ctx, q, in, to, p)
	if err != nil {
		h.failed(w, r, "state change", err)
		return in, false
	}
	if !moved {
		wrongState(w, r, in.State)
		return in, false
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["state_from"], meta["state_to"] = from, out.State
	if err := h.writeAudit(ctx, tx, r, action, out, meta); err != nil {
		h.failed(w, r, "audit", err)
		return in, false
	}
	if reason != "" {
		if err := h.notify().IntakeEscalated(ctx, tx, out.ID, reason); err != nil {
			h.failed(w, r, "notify", err)
			return in, false
		}
	}
	return out, true
}

type correctionBody struct {
	Draft int32  `json:"draft"`
	Text  string `json:"text"`
}

// HandleCorrectionText answers POST .../plan-intakes/{intake_id}/corrections-text.
func (h *Handler) HandleCorrectionText(w http.ResponseWriter, r *http.Request) {
	in, ok := h.orgIntake(w, r)
	if !ok {
		return
	}
	var body correctionBody
	if !readJSON(w, r, &body, false) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || utf8.RuneCountInString(text) > maxCorrectionText {
		writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_text",
			"write from 1 to 2000 characters", map[string]any{"field": "text", "limit": maxCorrectionText})
		return
	}
	// The interpreter (spec 4.10) is an outside call: it runs before the
	// transaction, on the draft's model. Nil in wave 1.
	var ops json.RawMessage
	understood := false
	if h.interpreter != nil {
		if d, err := h.queries.GetVenuePlanDraft(r.Context(), in.ID, body.Draft); err == nil {
			got, ierr := h.interpreter.Interpret(r.Context(), venueplan.TextInput{
				IntakeID: in.ID, Draft: int(body.Draft), Model: d.Model, Text: text,
			})
			if ierr == nil {
				ops, understood = got, true
			}
		}
	}
	user := actorUserID(r.Context())
	h.locked(w, r, in.ID, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, in gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		if in.State != string(venueplan.StateReview) {
			wrongState(w, r, in.State)
			return in, false
		}
		d, ok := h.currentDraftRow(w, r, q, in, body.Draft)
		if !ok {
			return in, false
		}
		if _, err := q.InsertVenuePlanCorrection(ctx, gen.InsertVenuePlanCorrectionParams{
			IntakeID: in.ID, DraftID: &d.ID, Kind: string(venueplan.CorrectionOrganizerText), Text: &text,
			Operations: ops, Status: string(venueplan.CorrectionPending), CreatedBy: user,
		}); err != nil {
			h.failed(w, r, "correction", err)
			return in, false
		}
		to, reason := venueplan.StateOperator, venueplan.EscalationOrganizerText
		p := gen.UpdateVenuePlanIntakeStateParams{EscalationReason: reasonPtr(reason)}
		if understood {
			to, reason, p = venueplan.StateGenerating, "", gen.UpdateVenuePlanIntakeStateParams{}
		}
		return h.finishMove(ctx, w, r, tx, q, in, to, p, reason, "v1.venue_plan_intake.correction_text",
			map[string]any{"draft": d.N, "text_length": utf8.RuneCountInString(text), "understood": understood})
	})
}

type escalateBody struct {
	Text string `json:"text"`
}

// HandleEscalate answers POST .../plan-intakes/{intake_id}/escalate.
func (h *Handler) HandleEscalate(w http.ResponseWriter, r *http.Request) {
	in, ok := h.orgIntake(w, r)
	if !ok {
		return
	}
	var body escalateBody
	if !readJSON(w, r, &body, true) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if utf8.RuneCountInString(text) > venueplan.MaxEscalationText {
		writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_text",
			"write at most 1000 characters", map[string]any{"field": "text", "limit": venueplan.MaxEscalationText})
		return
	}
	h.locked(w, r, in.ID, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, in gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		p := gen.UpdateVenuePlanIntakeStateParams{EscalationReason: reasonPtr(venueplan.EscalationOrganizerRequest)}
		if text != "" {
			p.EscalationText = &text
		}
		return h.finishMove(ctx, w, r, tx, q, in, venueplan.StateOperator, p, venueplan.EscalationOrganizerRequest,
			"v1.venue_plan_intake.escalate", map[string]any{"has_text": text != ""})
	})
}

// HandleCancel answers POST .../plan-intakes/{intake_id}/cancel.
func (h *Handler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	in, ok := h.orgIntake(w, r)
	if !ok {
		return
	}
	var body struct{}
	if !readJSON(w, r, &body, true) {
		return
	}
	h.locked(w, r, in.ID, false, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, in gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		return h.finishMove(ctx, w, r, tx, q, in, venueplan.StateCancelled, gen.UpdateVenuePlanIntakeStateParams{}, "",
			"v1.venue_plan_intake.cancel", nil)
	})
}
