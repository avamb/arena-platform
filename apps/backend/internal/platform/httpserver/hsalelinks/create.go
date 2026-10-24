package hsalelinks

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

const maxLabelRunes = 120

// cleanLabel trims a label and checks the 1-120 character rule.
func cleanLabel(s string) (string, bool) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	return s, n >= 1 && n <= maxLabelRunes
}

func invalidLabel() *apiError {
	return &apiError{http.StatusBadRequest, "sales_link.invalid_label", "label must be 1 to 120 characters"}
}

func invalidPlacement() *apiError {
	return &apiError{http.StatusBadRequest, "sales_link.invalid_placement",
		"placement_kind must be one of poster, flyer, sign, social, email, messenger or other"}
}

func invalidUTM() *apiError {
	return &apiError{http.StatusBadRequest, "sales_link.invalid_utm",
		"utm may hold only utm_source, utm_medium, utm_campaign, utm_content and utm_term, each up to 64 characters of letters, digits and . _ ~ -"}
}

// codeInserter claims a code with the link columns, inside the caller
// transaction (a lost code is zero rows, never a unique violation).
type codeInserter struct {
	q      *gen.Queries
	params gen.InsertSalesLinkParams
	// userUTM is the caller utm; empty means "the defaults for the new code".
	userUTM  map[string]string
	campaign string
	row      gen.SalesLinkRow
}

func (c *codeInserter) Insert(ctx context.Context, code string) (bool, error) {
	p := c.params
	p.Code = code
	utm := c.userUTM
	if len(utm) == 0 {
		utm = salelinks.DefaultUTM(p.PlacementKind, c.campaign, code)
	}
	p.UTM = marshalUTM(utm)
	row, ok, err := c.q.InsertSalesLinkIfCodeFree(ctx, p)
	if err != nil || !ok {
		return false, err
	}
	c.row = row
	return true, nil
}

// HandleCreate serves POST /v1/organizations/{org_id}/sales-links.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	var req createRequest
	if !readBody(w, r, &req) {
		return
	}
	label, okLabel := cleanLabel(req.Label)
	if !okLabel {
		invalidLabel().write(w, r)
		return
	}
	placement := strings.TrimSpace(req.PlacementKind)
	if placement == "" {
		placement = "poster"
	}
	if !oneOf(placement, placementKinds) {
		invalidPlacement().write(w, r)
		return
	}
	utm, err := parseUTM(req.UTM)
	if err != nil {
		invalidUTM().write(w, r)
		return
	}
	ctx := r.Context()
	target, aerr, err := h.resolveTarget(ctx, orgID, req.Target)
	if aerr != nil {
		aerr.write(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "target check", err)
		return
	}

	tx, q, err := h.beginTx(ctx)
	if err != nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "failed to begin transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	via := createdVia(ctx)
	ins := &codeInserter{
		q: q, userUTM: utm, campaign: target.campaign,
		params: gen.InsertSalesLinkParams{
			OrgID: orgID, TargetKind: target.kind,
			EventID: target.eventID, SessionID: target.sessionID, PromoterID: target.promoterID, URL: target.url,
			Label: label, PlacementKind: placement, CreatedBy: createdBy(ctx), CreatedVia: &via,
		},
	}
	if _, err := salelinks.Mint(ctx, ins); err != nil {
		h.failed(w, r, "mint code", err)
		return
	}
	if err := h.writeAudit(ctx, tx, r, "v1.sales_link.create", ins.row, map[string]any{
		"target_kind": ins.row.TargetKind, "placement_kind": ins.row.PlacementKind, "label": ins.row.Label,
	}); err != nil {
		h.failed(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.failed(w, r, "commit", err)
		return
	}
	writeJSON(w, http.StatusCreated, h.present(ctx, h.queries, ins.row))
}

// HandleGet serves GET /v1/organizations/{org_id}/sales-links/{id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	id, ok := linkID(w, r)
	if !ok {
		return
	}
	row, err := h.queries.GetSalesLinkByID(r.Context(), id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "sales_link.not_found", "sales link not found")
			return
		}
		h.failed(w, r, "get", err)
		return
	}
	writeJSON(w, http.StatusOK, h.present(r.Context(), h.queries, row))
}

func optionalUUID(w http.ResponseWriter, r *http.Request, name string) (*uuid.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeErr(w, r, http.StatusBadRequest, "sales_link.invalid_filter", name+" must be a UUID")
		return nil, false
	}
	return &id, true
}
