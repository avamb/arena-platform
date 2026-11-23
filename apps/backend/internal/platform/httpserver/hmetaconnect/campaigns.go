package hmetaconnect

// campaigns.go - the owner lists the campaigns of the SELECTED ad account of
// the organization's Meta connection and pauses or resumes one (spec 66, the
// one ads_management use). Nothing here creates a campaign or changes a
// budget or any other field. Before a change the campaign is read and must
// belong to the selected ad account; only then is status=PAUSED|ACTIVE
// posted. No answer, log line or audit row carries the token or Meta's
// message text.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/adminnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

// graphTimeout bounds every Graph call of the campaign routes.
const graphTimeout = 15 * time.Second

// maxStatusBody is the body limit of the status change.
const maxStatusBody = 4 << 10

// CampaignView is one campaign in an answer.
type CampaignView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	EffectiveStatus string `json:"effective_status"`
}

// CampaignList is the answer of GET .../integrations/meta/campaigns.
type CampaignList struct {
	AdAccountID string         `json:"ad_account_id"`
	Campaigns   []CampaignView `json:"campaigns"`
}

// CampaignStatusResult is the answer of POST .../campaigns/{id}/status.
type CampaignStatusResult struct {
	Campaign CampaignView `json:"campaign"`
	Changed  bool         `json:"changed"`
}

// campaignStatusRequest is the body of the status change.
type campaignStatusRequest struct {
	Status *string `json:"status"`
}

func viewOf(c metaconnect.Campaign) CampaignView {
	return CampaignView{ID: c.ID, Name: c.Name, Status: c.Status, EffectiveStatus: c.EffectiveStatus}
}

// campaignAccess is what both routes need after the checks.
type campaignAccess struct {
	orgID     uuid.UUID
	userID    uuid.UUID
	token     string
	sealed    string // the stored value token was opened from
	adAccount string // act_<digits>
}

// markBrokenTimeout bounds the transaction that breaks the connection after
// a 190; it runs without the request's cancel.
const markBrokenTimeout = 10 * time.Second

// campaignUser runs the guard, the feature switch and the person check.
func (h *Handler) campaignUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	if !h.cfg.Enabled {
		writeErr(w, r, http.StatusServiceUnavailable, "meta.disabled", "the Meta connection is not switched on for this platform")
		return uuid.Nil, uuid.Nil, false
	}
	actor, _ := auth.ActorFromContext(r.Context())
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		writeErr(w, r, http.StatusForbidden, "meta.user_required", "only a signed-in person can manage Meta campaigns")
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, userID, true
}

// access checks the owner in the database again, then reads the connection,
// its token and the selected ad account.
func (h *Handler) access(w http.ResponseWriter, r *http.Request, orgID, userID uuid.UUID) (campaignAccess, bool) {
	ctx := r.Context()
	allowed, err := metaconnect.UserCanConnect(ctx, h.pool, userID, orgID)
	if err != nil {
		h.logger.Error("metaconnect: permission check failed", "org_id", orgID.String())
		h.unavailable(w, r)
		return campaignAccess{}, false
	}
	if !allowed {
		writeErr(w, r, http.StatusForbidden, "meta.forbidden", "only the owner of the organization can manage Meta campaigns")
		return campaignAccess{}, false
	}
	sealed, status, err := metaconnect.SealedToken(ctx, h.pool, orgID)
	if errors.Is(err, metaconnect.ErrNotConnected) {
		writeErr(w, r, http.StatusNotFound, "meta.not_connected", "the organization has no Meta connection")
		return campaignAccess{}, false
	}
	if err != nil {
		h.unavailable(w, r)
		return campaignAccess{}, false
	}
	if status != metaconnect.StatusActive || sealed == "" {
		writeErr(w, r, http.StatusConflict, "meta.reconnect_required", "the Meta connection does not work, connect Meta again")
		return campaignAccess{}, false
	}
	_, assets, err := metaconnect.Load(ctx, h.pool, orgID)
	if errors.Is(err, metaconnect.ErrNotConnected) {
		writeErr(w, r, http.StatusNotFound, "meta.not_connected", "the organization has no Meta connection")
		return campaignAccess{}, false
	}
	if err != nil {
		h.unavailable(w, r)
		return campaignAccess{}, false
	}
	adAccount := ""
	for _, a := range assets {
		if a.Kind == metaconnect.KindAdAccount && a.Selected {
			adAccount, _ = metaconnect.NormalizeAdAccountID(a.MetaID)
			if adAccount == "" {
				h.logger.Warn("metaconnect: the selected ad account id is not act_<digits>", "org_id", orgID.String())
			}
		}
	}
	if adAccount == "" {
		writeErr(w, r, http.StatusConflict, "meta.no_ad_account", "select an ad account of the Meta connection first")
		return campaignAccess{}, false
	}
	token, err := metaconnect.OpenToken(h.box, orgID, sealed)
	if err != nil {
		h.logger.Error("metaconnect: the stored Meta token does not open", "org_id", orgID.String())
		writeErr(w, r, http.StatusConflict, "meta.reconnect_required", "the Meta connection does not work, connect Meta again")
		return campaignAccess{}, false
	}
	return campaignAccess{orgID: orgID, userID: userID, token: token, sealed: sealed, adAccount: adAccount}, true
}

// graphFailed answers a Graph error of the campaign routes. 190 breaks the
// connection (once, with the owners' notice in the same transaction, like
// conversions.PGStore.MarkMetaTokenInvalid); a permission error is kept as
// last_error; a throttle is 429; anything else 502. Only codes are logged.
// A 190 breaks the connection only while it still holds acc.sealed: a token
// saved by a reconnect in the meantime stays.
func (h *Handler) graphFailed(w http.ResponseWriter, r *http.Request, acc campaignAccess, step string, err error) {
	orgID := acc.orgID
	reason := metaconnect.ErrorCode(err)
	switch {
	case metaconnect.IsTokenInvalid(err):
		changed, mErr := h.markBroken(r.Context(), orgID, acc.sealed, reason)
		if mErr != nil {
			h.logger.Error("metaconnect: could not mark the Meta connection invalid", "org_id", orgID.String(), "step", step)
		} else if changed {
			h.logger.Warn("metaconnect: the Meta token is invalid, the connection is now in error", "org_id", orgID.String(), "step", step, "reason", reason)
		}
		writeErr(w, r, http.StatusConflict, "meta.reconnect_required", "Meta refused the connection, connect Meta again")
	case metaconnect.IsRateLimited(err):
		h.logger.Warn("metaconnect: Meta throttled a campaign call", "org_id", orgID.String(), "step", step, "reason", reason)
		writeErr(w, r, http.StatusTooManyRequests, "meta.rate_limited", "Meta asks to slow down, try again in a few minutes")
	case metaconnect.IsPermissionError(err):
		if rErr := metaconnect.RecordError(r.Context(), h.pool, orgID, reason); rErr != nil {
			h.logger.Warn("metaconnect: could not record the Meta error", "org_id", orgID.String(), "step", step)
		}
		h.logger.Warn("metaconnect: Meta refused a campaign call", "org_id", orgID.String(), "step", step, "reason", reason)
		writeErr(w, r, http.StatusForbidden, "meta.permission_denied", "the Meta connection has no permission for this, check the access in Meta")
	default:
		h.logger.Warn("metaconnect: a campaign call to Meta failed", "org_id", orgID.String(), "step", step, "reason", reason)
		writeErr(w, r, http.StatusBadGateway, "meta.graph_error", "Meta did not answer, try again in a minute")
	}
}

// markBroken moves an active connection to error and queues the owners'
// notice in the same transaction; changed is true only once per break, and
// only while the row still holds sealed (the token that failed).
func (h *Handler) markBroken(ctx context.Context, orgID uuid.UUID, sealed, reason string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markBrokenTimeout)
	defer cancel()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	changed, err := metaconnect.MarkTokenInvalidIfSealed(ctx, tx, orgID, sealed, reason)
	if err != nil || !changed {
		return false, err
	}
	if _, err := adminnotify.NotifyMetaConnectionBroken(ctx, tx, orgID, h.cfg.EventCenterURL,
		adminnotify.Options{Telegram: h.cfg.OwnerTelegram, Logger: h.logger}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// campaignAuditEvent builds an audit row of a campaign status change by the
// signed-in person userID.
func (h *Handler) campaignAuditEvent(r *http.Request, userID, orgID uuid.UUID, action string, md map[string]any) audit.Event {
	ctx := r.Context()
	return audit.Event{
		OccurredAt: time.Now().UTC(), ActorType: "user", ActorID: userID.String(),
		Action: action, ResourceType: "organization", ResourceID: orgID.String(),
		RequestID: logging.RequestID(ctx), TraceID: logging.TraceID(ctx), IP: httputil.ExtractClientIP(r),
		Metadata: md,
	}
}

// HandleListCampaigns lists the campaigns of the selected ad account.
func (h *Handler) HandleListCampaigns(w http.ResponseWriter, r *http.Request) {
	orgID, userID, ok := h.campaignUser(w, r)
	if !ok {
		return
	}
	acc, ok := h.access(w, r, orgID, userID)
	if !ok {
		return
	}
	gctx, cancel := context.WithTimeout(r.Context(), graphTimeout)
	list, err := h.graph.Campaigns(gctx, acc.token, acc.adAccount)
	cancel()
	if err != nil {
		h.graphFailed(w, r, acc, "list", err)
		return
	}
	out := CampaignList{AdAccountID: acc.adAccount, Campaigns: make([]CampaignView, 0, len(list))}
	for _, c := range list {
		out.Campaigns = append(out.Campaigns, viewOf(c))
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, out)
}

// HandleSetCampaignStatus pauses or resumes one campaign of the selected ad
// account.
func (h *Handler) HandleSetCampaignStatus(w http.ResponseWriter, r *http.Request) {
	orgID, userID, ok := h.campaignUser(w, r)
	if !ok {
		return
	}
	if h.audit == nil {
		// A change at Meta is never made without its audit row.
		h.logger.Error("metaconnect: no audit writer, campaign status changes are refused", "org_id", orgID.String())
		h.unavailable(w, r)
		return
	}
	campaignID := chi.URLParam(r, "campaign_id")
	if !metaconnect.ValidCampaignID(campaignID) {
		writeErr(w, r, http.StatusBadRequest, "meta.invalid_campaign_id", "campaign_id must be the digits of a Meta campaign id")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxStatusBody+1))
	var req campaignStatusRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || len(body) > maxStatusBody || dec.Decode(&req) != nil || dec.More() ||
		req.Status == nil || !metaconnect.ValidCampaignStatus(*req.Status) {
		writeErr(w, r, http.StatusBadRequest, "meta.invalid_body", `send {"status":"PAUSED"} or {"status":"ACTIVE"}`)
		return
	}
	to := *req.Status
	acc, ok := h.access(w, r, orgID, userID)
	if !ok {
		return
	}

	gctx, cancel := context.WithTimeout(r.Context(), graphTimeout)
	cur, err := h.graph.Campaign(gctx, acc.token, campaignID)
	cancel()
	if err != nil {
		var ge *metaconnect.GraphError
		if errors.As(err, &ge) && (ge.Code == 100 || ge.HTTPStatus == http.StatusNotFound) {
			h.logger.Info("metaconnect: Meta does not know the campaign", "org_id", orgID.String(), "reason", metaconnect.ErrorCode(err))
			writeErr(w, r, http.StatusNotFound, "meta.campaign_not_found", "no such campaign in the selected ad account")
			return
		}
		h.graphFailed(w, r, acc, "read", err)
		return
	}
	if cur.ID != campaignID || !metaconnect.SameAdAccount(cur.AccountID, acc.adAccount) {
		writeErr(w, r, http.StatusNotFound, "meta.campaign_not_found", "no such campaign in the selected ad account")
		return
	}
	if !metaconnect.ValidCampaignStatus(cur.Status) {
		writeErr(w, r, http.StatusConflict, "meta.campaign_not_changeable", "this campaign is deleted or archived, it cannot be paused or resumed")
		return
	}
	if cur.Status == to {
		w.Header().Set("Cache-Control", "no-store")
		httputil.WriteJSON(w, http.StatusOK, CampaignStatusResult{Campaign: viewOf(cur), Changed: false})
		return
	}

	// The audit row is written BEFORE the change at Meta: no change without a
	// trace. If it cannot be written, nothing is posted.
	from := cur.Status
	ctx := r.Context()
	if aErr := h.audit.Write(context.WithoutCancel(ctx), h.campaignAuditEvent(r, userID, orgID, "v1.org.meta.campaign_status", map[string]any{
		"org_id": orgID.String(), "ad_account_id": acc.adAccount, "campaign_id": campaignID,
		"campaign_name": cur.Name, "from": from, "to": to,
	})); aErr != nil {
		h.logger.Error("metaconnect: the campaign status audit row was not written, nothing was sent to Meta",
			"org_id", orgID.String(), "campaign_id", campaignID, "from", from, "to", to)
		h.unavailable(w, r)
		return
	}
	gctx, cancel = context.WithTimeout(ctx, graphTimeout)
	err = h.graph.SetCampaignStatus(gctx, acc.token, campaignID, to)
	cancel()
	if err != nil {
		reason := metaconnect.ErrorCode(err)
		if fErr := h.audit.Write(context.WithoutCancel(ctx), h.campaignAuditEvent(r, userID, orgID, "v1.org.meta.campaign_status_failed", map[string]any{
			"org_id": orgID.String(), "campaign_id": campaignID, "to": to, "reason": reason,
		})); fErr != nil {
			h.logger.Error("metaconnect: the failed campaign status audit row was not written",
				"org_id", orgID.String(), "campaign_id", campaignID, "to", to, "reason", reason)
		}
		var ge *metaconnect.GraphError
		if errors.As(err, &ge) && ge.Code == 100 {
			// Meta refused this change of this campaign: not a connection fault.
			h.logger.Info("metaconnect: Meta rejected the campaign status change", "org_id", orgID.String(), "campaign_id", campaignID, "reason", reason)
			writeErr(w, r, http.StatusUnprocessableEntity, "meta.campaign_rejected", "Meta refused to change the status of this campaign, check it in Meta Ads Manager")
			return
		}
		h.graphFailed(w, r, acc, "set_status", err)
		return
	}
	res := viewOf(cur)
	res.Status = to
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, CampaignStatusResult{Campaign: res, Changed: true})
}
