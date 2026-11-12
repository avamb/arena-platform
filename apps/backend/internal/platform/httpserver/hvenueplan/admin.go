package hvenueplan

// admin.go — the platform administrators' side (spec 64 section 3.3): take
// an intake, upload a hand-drawn SVG draft, return it to the organizer,
// reject it. Approving is approve.go.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

const (
	maxNote   = 2000
	maxReason = 1000
)

// DraftBodyLimit is the body cap of the admin draft upload: one SVG and a note.
const DraftBodyLimit = maxDraftSVG + multipartSlack

// claimedByCaller answers 409 when another administrator holds the intake.
func claimedByCaller(w http.ResponseWriter, r *http.Request, in gen.VenuePlanIntakeRow) bool {
	me := actorUserID(r.Context())
	if in.OperatorID != nil && (me == nil || *in.OperatorID != *me) {
		writeErrDetails(w, r, http.StatusConflict, "venue_plan_intake.claimed_by_other",
			"another administrator works on this intake, take it over with claim and force true",
			map[string]any{"operator_id": in.OperatorID.String()})
		return false
	}
	return true
}

type claimBody struct {
	Force bool `json:"force"`
}

// HandleAdminClaim answers POST /v1/admin/plan-intakes/{intake_id}/claim.
func (h *Handler) HandleAdminClaim(w http.ResponseWriter, r *http.Request) {
	in, reason, ok := h.adminIntake(w, r, true)
	if !ok {
		return
	}
	var body claimBody
	if !readJSON(w, r, &body, true) {
		return
	}
	me := actorUserID(r.Context())
	if me == nil {
		writeErr(w, r, http.StatusForbidden, "venue_plan_intake.superadmin_only", "a signed-in administrator is required")
		return
	}
	h.locked(w, r, in.ID, true, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, cur gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		if venueplan.IsFinal(venueplan.State(cur.State)) {
			wrongState(w, r, cur.State)
			return cur, false
		}
		if !body.Force && !claimedByCaller(w, r, cur) {
			return cur, false
		}
		out, err := q.ClaimVenuePlanIntake(ctx, cur.ID, *me)
		if err != nil {
			h.failed(w, r, "claim", err)
			return cur, false
		}
		meta := map[string]any{"reason": reason, "forced": body.Force && cur.OperatorID != nil && *cur.OperatorID != *me}
		if cur.OperatorID != nil {
			meta["previous_operator_id"] = cur.OperatorID.String()
		}
		if err := h.writeAudit(ctx, tx, r, "v1.venue_plan_intake.claim", out, meta); err != nil {
			h.failed(w, r, "audit", err)
			return cur, false
		}
		return out, true
	})
}

type draftJSONBody struct {
	SVG  string  `json:"svg"`
	Note *string `json:"note"`
}

// readDraftUpload reads the SVG and the note from multipart (fields svg and
// note) or from JSON {svg, note}.
func readDraftUpload(w http.ResponseWriter, r *http.Request) (svg []byte, note *string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, DraftBodyLimit)
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	bad := func(msg string) ([]byte, *string, bool) {
		writeErr(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_body", msg)
		return nil, nil, false
	}
	if mt == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			return bad("send the SVG as multipart/form-data in the field svg")
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return bad("the multipart body could not be read, please send it again")
			}
			switch part.FormName() {
			case "svg":
				svg, err = io.ReadAll(io.LimitReader(part, maxDraftSVG+1))
				if err != nil {
					return bad("the multipart body could not be read, please send it again")
				}
			case "note":
				raw, _ := io.ReadAll(io.LimitReader(part, maxNote*4+1))
				s := string(raw)
				note = &s
			}
			_ = part.Close()
		}
	} else {
		raw, err := io.ReadAll(io.LimitReader(r.Body, DraftBodyLimit+1))
		if err != nil {
			return bad("the request body could not be read, please send it again")
		}
		var body draftJSONBody
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			return bad("send JSON {svg, note} or multipart fields svg and note")
		}
		svg, note = []byte(body.SVG), body.Note
	}
	if len(svg) == 0 {
		return bad("the svg is missing")
	}
	if len(svg) > maxDraftSVG {
		writeErrDetails(w, r, http.StatusRequestEntityTooLarge, "venue_plan_intake.file_too_large",
			"the SVG may be at most 3 MB", map[string]any{"limit_mb": maxDraftSVG / mb})
		return nil, nil, false
	}
	if note != nil {
		n := strings.TrimSpace(*note)
		if utf8.RuneCountInString(n) > maxNote {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_note",
				"the note may be at most 2000 characters", map[string]any{"field": "note"})
			return nil, nil, false
		}
		if n == "" {
			note = nil
		} else {
			note = &n
		}
	}
	return svg, note, true
}

// HandleAdminDraft answers POST /v1/admin/plan-intakes/{intake_id}/drafts.
func (h *Handler) HandleAdminDraft(w http.ResponseWriter, r *http.Request) {
	in, reason, ok := h.adminIntake(w, r, true)
	if !ok {
		return
	}
	svg, note, ok := readDraftUpload(w, r)
	if !ok {
		return
	}
	if !looksLikeSVG(svg) {
		writeErr(w, r, http.StatusUnsupportedMediaType, "venue_plan_intake.unsupported_format",
			"the draft must be an SVG document")
		return
	}
	// Checked BEFORE anything is stored: a refused SVG leaves nothing behind.
	g, warnings, planType, verrs := checkPlanSVG(svg)
	if len(verrs) > 0 {
		writeValidationFailure(w, r, verrs)
		return
	}
	report, err := json.Marshal(summaryOf(g, planType, warnings))
	if err != nil {
		h.failed(w, r, "report", err)
		return
	}
	ctx := r.Context()
	st := &stored{h: h}
	key, sha, size, err := st.put(ctx, mediastore.OwnerVenuePlanSource, "image/svg+xml", svg)
	if err != nil {
		h.failed(w, r, "storage write", err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			st.drop(ctx)
		}
	}()
	me := actorUserID(ctx)
	h.locked(w, r, in.ID, true, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, cur gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		if cur.State != string(venueplan.StateOperator) {
			wrongState(w, r, cur.State)
			return cur, false
		}
		if !claimedByCaller(w, r, cur) {
			return cur, false
		}
		obj, err := h.media.InsertTx(ctx, tx, mediastore.InsertInput{
			OrgID: &cur.OrgID, OwnerType: mediastore.OwnerVenuePlanSource, OwnerID: &cur.ID,
			StorageBackend: h.media.Backend(), StorageKey: key, ContentType: "image/svg+xml",
			ByteSize: size, ChecksumSHA256: sha,
		})
		if err != nil {
			h.failed(w, r, "draft media", err)
			return cur, false
		}
		d, err := q.InsertVenuePlanDraft(ctx, gen.InsertVenuePlanDraftParams{
			IntakeID: cur.ID, Author: string(venueplan.AuthorOperatorSVG), AuthorUserID: me,
			Report: report, SvgMediaID: obj.ID, Note: note,
		})
		if err != nil {
			h.failed(w, r, "draft insert", err)
			return cur, false
		}
		out, err := q.SetVenuePlanIntakeCurrentDraft(ctx, cur.ID, d.N)
		if err != nil {
			h.failed(w, r, "current draft", err)
			return cur, false
		}
		if err := h.writeAudit(ctx, tx, r, "v1.venue_plan_intake.draft", out, map[string]any{
			"reason": reason, "draft": d.N, "svg_sha256": sha, "plan_type": planType,
		}); err != nil {
			h.failed(w, r, "audit", err)
			return cur, false
		}
		committed = true
		return out, true
	})
}

type returnBody struct {
	Note *string `json:"note"`
}

// HandleAdminReturn answers POST /v1/admin/plan-intakes/{intake_id}/return.
func (h *Handler) HandleAdminReturn(w http.ResponseWriter, r *http.Request) {
	in, reason, ok := h.adminIntake(w, r, true)
	if !ok {
		return
	}
	var body returnBody
	if !readJSON(w, r, &body, true) {
		return
	}
	var note *string
	if body.Note != nil {
		n := strings.TrimSpace(*body.Note)
		if utf8.RuneCountInString(n) > maxNote {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_note",
				"the note may be at most 2000 characters", map[string]any{"field": "note"})
			return
		}
		if n != "" {
			note = &n
		}
	}
	h.locked(w, r, in.ID, true, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, cur gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		if !claimedByCaller(w, r, cur) {
			return cur, false
		}
		if cur.State == string(venueplan.StateOperator) && cur.CurrentDraft == nil {
			writeErr(w, r, http.StatusConflict, "venue_plan_intake.no_draft",
				"upload a draft before returning the intake to the organizer")
			return cur, false
		}
		out, ok := h.finishMove(ctx, w, r, tx, q, cur, venueplan.StateReview,
			gen.UpdateVenuePlanIntakeStateParams{ReturnNote: note}, "", "v1.venue_plan_intake.return",
			map[string]any{"reason": reason, "draft": cur.CurrentDraft, "has_note": note != nil})
		if !ok {
			return cur, false
		}
		if _, err := q.InsertVenuePlanNotice(ctx, cur.ID, string(venueplan.NoticeReturned), note); err != nil {
			h.failed(w, r, "notice", err)
			return cur, false
		}
		return out, true
	})
}

type rejectBody struct {
	Reason string `json:"reason"`
}

// HandleAdminReject answers POST /v1/admin/plan-intakes/{intake_id}/reject.
func (h *Handler) HandleAdminReject(w http.ResponseWriter, r *http.Request) {
	in, adminReason, ok := h.adminIntake(w, r, true)
	if !ok {
		return
	}
	var body rejectBody
	if !readJSON(w, r, &body, false) {
		return
	}
	text := strings.TrimSpace(body.Reason)
	if text == "" || utf8.RuneCountInString(text) > maxReason {
		writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_reason",
			"the reason must be 1 to 1000 characters", map[string]any{"field": "reason"})
		return
	}
	h.locked(w, r, in.ID, true, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, cur gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		if !claimedByCaller(w, r, cur) {
			return cur, false
		}
		out, ok := h.finishMove(ctx, w, r, tx, q, cur, venueplan.StateRejected,
			gen.UpdateVenuePlanIntakeStateParams{RejectReason: &text}, "", "v1.venue_plan_intake.reject",
			map[string]any{"reason": adminReason})
		if !ok {
			return cur, false
		}
		if _, err := q.InsertVenuePlanNotice(ctx, cur.ID, string(venueplan.NoticeRejected), &text); err != nil {
			h.failed(w, r, "notice", err)
			return cur, false
		}
		return out, true
	})
}
