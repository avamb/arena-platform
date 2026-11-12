package hvenueplan

// approve.go — a draft becomes a seating plan (spec 64 section 2.4): ONE
// transaction makes the plan (active, private, unverified), its version 1 with
// the geometry and the SVG, approves the intake and writes the free billing
// fact behind a SAVEPOINT.

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

const (
	maxPlanName = 200
	// maxDraftSVG is the biggest draft SVG (the SVG limit of spec 2.1).
	maxDraftSVG = 3 * mb
	// ownerSeatingPlanSVG is the media owner type of a plan version's SVG.
	ownerSeatingPlanSVG = "seating_plan_svg"
)

type approveBody struct {
	Draft int32   `json:"draft"`
	Name  *string `json:"name"`
}

// HandleApprove answers POST .../plan-intakes/{intake_id}/approve.
func (h *Handler) HandleApprove(w http.ResponseWriter, r *http.Request) {
	in, ok := h.orgIntake(w, r)
	if !ok {
		return
	}
	h.approve(w, r, in, false, "")
}

// HandleAdminApprove answers POST /v1/admin/plan-intakes/{intake_id}/approve.
func (h *Handler) HandleAdminApprove(w http.ResponseWriter, r *http.Request) {
	in, reason, ok := h.adminIntake(w, r, true)
	if !ok {
		return
	}
	h.approve(w, r, in, true, reason)
}

// writeValidationFailure is the 422 of an SVG that does not import, in the
// shape of the seating plan version route (code, element, detail per error).
func writeValidationFailure(w http.ResponseWriter, r *http.Request, errs seating.ValidationErrors) {
	details := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		details = append(details, map[string]any{"code": e.Code, "element": e.Element, "detail": e.Detail})
	}
	writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.draft_validation_failed",
		"the plan SVG failed geometry validation", map[string]any{"errors": details})
}

// readStored reads a stored object, at most limit bytes.
func (h *Handler) readStored(ctx context.Context, mediaID uuid.UUID, limit int64) ([]byte, error) {
	obj, err := h.media.GetByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	res, err := h.media.Storage().Get(ctx, obj.StorageKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("stored draft SVG is over the limit")
	}
	return raw, nil
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request, in gen.VenuePlanIntakeRow, admin bool, adminReason string) {
	ctx := r.Context()
	var body approveBody
	if !readJSON(w, r, &body, false) {
		return
	}
	name := ""
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
		if name == "" || utf8.RuneCountInString(name) > maxPlanName {
			writeErrDetails(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.invalid_name",
				"the plan name must be 1 to 200 characters", map[string]any{"field": "name"})
			return
		}
	}
	if name == "" {
		if name = strings.TrimSpace(h.venueName(ctx, h.queries, in.VenueID)); name == "" {
			name = "Hall plan"
		}
		if utf8.RuneCountInString(name) > maxPlanName {
			name = string([]rune(name)[:maxPlanName])
		}
	}
	d, err := h.queries.GetVenuePlanDraft(ctx, in.ID, body.Draft)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			draftStale(w, r, in.CurrentDraft)
			return
		}
		h.failed(w, r, "draft lookup", err)
		return
	}
	// The draft is checked again on the stored bytes: nothing reaches a plan
	// that the importer does not take.
	raw, err := h.readStored(ctx, d.SvgMediaID, maxDraftSVG)
	if err != nil {
		h.failed(w, r, "draft read", err)
		return
	}
	g, _, planType, verrs := checkPlanSVG(raw)
	if len(verrs) > 0 {
		writeValidationFailure(w, r, verrs)
		return
	}
	checksum, err := seating.Checksum(g)
	if err != nil {
		h.failed(w, r, "checksum", err)
		return
	}
	canonical, err := seating.CanonicalJSON(g)
	if err != nil {
		h.failed(w, r, "geometry", err)
		return
	}
	seated, standing := g.SeatCount(), g.GACapacity()
	if seated > math.MaxInt32 || standing > math.MaxInt32 {
		writeErr(w, r, http.StatusUnprocessableEntity, "venue_plan_intake.capacity_overflow",
			"the plan exceeds the supported capacity range")
		return
	}
	st := &stored{h: h}
	key, sha, size, err := st.put(ctx, ownerSeatingPlanSVG, "image/svg+xml", raw)
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
	h.locked(w, r, in.ID, admin, http.StatusOK, func(ctx context.Context, tx pgx.Tx, q *gen.Queries, cur gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		if cur.OrgID != in.OrgID {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.not_found", "hall plan intake not found")
			return cur, false
		}
		allowed := cur.State == string(venueplan.StateReview) || (admin && cur.State == string(venueplan.StateOperator))
		if !allowed || !venueplan.CanTransition(venueplan.State(cur.State), venueplan.StateApproved) {
			wrongState(w, r, cur.State)
			return cur, false
		}
		if admin && !claimedByCaller(w, r, cur) {
			return cur, false
		}
		if cur.CurrentDraft == nil || *cur.CurrentDraft != d.N {
			draftStale(w, r, cur.CurrentDraft)
			return cur, false
		}
		plan, err := q.InsertSeatingPlan(ctx, cur.VenueID, cur.OrgID, name, planType, "private", "active", nil)
		if err != nil {
			h.failed(w, r, "plan insert", err)
			return cur, false
		}
		if _, err := q.SetSeatingPlanVerification(ctx, gen.SetSeatingPlanVerificationParams{
			PlanID: plan.ID, State: string(venueplan.VerificationUnverified),
		}); err != nil {
			h.failed(w, r, "plan verification", err)
			return cur, false
		}
		obj, err := h.media.InsertTx(ctx, tx, mediastore.InsertInput{
			OrgID: &cur.OrgID, OwnerType: ownerSeatingPlanSVG, OwnerID: &plan.ID, StorageBackend: h.media.Backend(),
			StorageKey: key, ContentType: "image/svg+xml", ByteSize: size, ChecksumSHA256: sha,
		})
		if err != nil {
			h.failed(w, r, "plan svg", err)
			return cur, false
		}
		ver, err := q.InsertSeatingPlanVersion(ctx, plan.ID, 1, canonical, checksum, &obj.ID,
			int32(seated), int32(standing)) //nolint:gosec // bounded above
		if err != nil {
			h.failed(w, r, "plan version", err)
			return cur, false
		}
		if _, err := q.SetSeatingPlanCurrentVersion(ctx, plan.ID, cur.OrgID, &ver.ID); err != nil {
			h.failed(w, r, "plan current version", err)
			return cur, false
		}
		from := cur.State
		out, err := q.ApproveVenuePlanIntake(ctx, cur.ID, cur.State, plan.ID, 1)
		if err != nil {
			h.failed(w, r, "approve", err)
			return cur, false
		}
		if admin {
			if _, err := q.InsertVenuePlanNotice(ctx, cur.ID, string(venueplan.NoticeApproved), nil); err != nil {
				h.failed(w, r, "notice", err)
				return cur, false
			}
		}
		h.billingFact(ctx, tx, q, out, admin)
		meta := map[string]any{
			"draft": d.N, "plan_id": plan.ID.String(), "plan_version": 1, "plan_type": planType,
			"geometry_checksum": checksum, "svg_sha256": sha, "state_from": from, "state_to": out.State,
			"by_admin": admin,
		}
		if admin {
			meta["reason"] = adminReason
		}
		if err := h.writeAudit(ctx, tx, r, "v1.venue_plan_intake.approve", out, meta); err != nil {
			h.failed(w, r, "audit", err)
			return cur, false
		}
		if err := h.notify().PlanApproved(ctx, tx, out.ID); err != nil {
			h.failed(w, r, "notify", err)
			return cur, false
		}
		committed = true // the deferred cleanup must not remove the SVG of a committed plan
		return out, true
	})
}

// billingFact writes the free venue_plan_intake fact behind a SAVEPOINT. It
// never fails the approval: an organization without a billing account is
// skipped, any other error is logged.
func (h *Handler) billingFact(ctx context.Context, tx pgx.Tx, q *gen.Queries, in gen.VenuePlanIntakeRow, admin bool) {
	sources, err := q.ListVenuePlanIntakeSources(ctx, in.ID)
	if err != nil {
		h.logger.Warn("venue plan intake: billing fact skipped", "intake_id", in.ID.String(), "error", err.Error())
		return
	}
	drafts, err := q.ListVenuePlanDrafts(ctx, in.ID)
	if err != nil {
		h.logger.Warn("venue plan intake: billing fact skipped", "intake_id", in.ID.String(), "error", err.Error())
		return
	}
	questions, err := q.ListVenuePlanQuestions(ctx, in.ID)
	if err != nil {
		h.logger.Warn("venue plan intake: billing fact skipped", "intake_id", in.ID.String(), "error", err.Error())
		return
	}
	fact := billing.VenuePlanIntakeFact{
		IntakeID: in.ID, OrgID: in.OrgID, ApprovedAt: h.now(), Questions: len(questions), Drafts: len(drafts),
		OperatorInvolved: admin || in.OperatorID != nil,
	}
	for _, s := range sources {
		fact.SourceKinds = append(fact.SourceKinds, s.Kind)
	}
	for _, d := range drafts {
		if d.Author == string(venueplan.AuthorOperator) || d.Author == string(venueplan.AuthorOperatorSVG) {
			fact.OperatorInvolved = true
		}
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		h.logger.Warn("venue plan intake: billing savepoint failed", "intake_id", in.ID.String(), "error", err.Error())
		return
	}
	if err := billing.RecordVenuePlanIntake(ctx, sp, fact); err != nil {
		_ = sp.Rollback(ctx)
		if !errors.Is(err, billing.ErrNoBillingAccount) {
			h.logger.Warn("venue plan intake: billing fact not written", "intake_id", in.ID.String(), "error", err.Error())
		}
		return
	}
	if err := sp.Commit(ctx); err != nil {
		h.logger.Warn("venue plan intake: billing savepoint release failed", "intake_id", in.ID.String(), "error", err.Error())
	}
}
