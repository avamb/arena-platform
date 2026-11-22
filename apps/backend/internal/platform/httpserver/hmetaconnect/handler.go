// Package hmetaconnect serves the Meta business connection of an
// organization (spec 08_architecture/66, stage M1):
//
//	POST   /v1/organizations/{org_id}/integrations/meta/connect
//	GET    /v1/organizations/{org_id}/integrations/meta
//	PATCH  /v1/organizations/{org_id}/integrations/meta
//	DELETE /v1/organizations/{org_id}/integrations/meta
//	GET    /v1/integrations/meta/callback          (public, signed state)
//	POST   /v1/integrations/meta/deauthorize       (public, Meta signed_request)
//	POST   /v1/integrations/meta/data-deletion     (public, Meta signed_request)
//
// The organization routes need org.update in the path organization (the
// owner) and refuse an organization API key. No answer, log line or audit
// row carries the access token, the app secret or the login code.
package hmetaconnect

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

// DefaultDataDeletionStatusURL is the page Meta shows a person after a data
// deletion request; the confirmation code is added as ?code=.
const DefaultDataDeletionStatusURL = "https://arenasoldout.com/legal/data-deletion"

// Settings is the Meta configuration the handler needs.
type Settings struct {
	// Enabled is config.MetaEnabled() and a parsable edge list.
	Enabled      bool
	AppID        string
	AppSecret    string
	ConfigID     string
	GraphVersion string
	// RedirectURL is META_OAUTH_REDIRECT_URL (the dialog/oauth login and its
	// code exchange). Empty = no login_url is offered.
	RedirectURL string
	// EventCenterURL is EVENT_CENTER_PUBLIC_URL, where the callback sends the
	// person back.
	EventCenterURL string
	// DataDeletionStatusURL is META_DATA_DELETION_STATUS_URL.
	DataDeletionStatusURL string
	Edges                 []metaconnect.Edge
}

// Handler serves the routes. The organization routes run after the
// org.update gate and the membership check of {org_id}.
type Handler struct {
	pool   *pgxpool.Pool
	audit  audit.Writer
	cfg    Settings
	box    *secretbox.Box
	graph  *metaconnect.Client
	logger *slog.Logger
	now    func() time.Time
}

// New builds a Handler. A nil box or graph switches the feature off.
func New(pool *pgxpool.Pool, auditWriter audit.Writer, cfg Settings, box *secretbox.Box, graph *metaconnect.Client, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if box == nil || graph == nil || len(cfg.Edges) == 0 {
		cfg.Enabled = false
	}
	if cfg.DataDeletionStatusURL == "" {
		cfg.DataDeletionStatusURL = DefaultDataDeletionStatusURL
	}
	return &Handler{pool: pool, audit: auditWriter, cfg: cfg, box: box, graph: graph, logger: logger, now: time.Now}
}

// WithClock replaces the clock (tests).
func (h *Handler) WithClock(now func() time.Time) *Handler { h.now = now; return h }

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

// guard refuses an API key and a missing database, then reads {org_id}.
func (h *Handler) guard(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		writeErr(w, r, http.StatusForbidden, "meta.api_key_forbidden", "an API key cannot manage the Meta connection")
		return uuid.Nil, false
	}
	if h.pool == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "database is not available")
		return uuid.Nil, false
	}
	return httputil.UUIDPathParam(w, r, "org_id")
}

func (h *Handler) unavailable(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, http.StatusServiceUnavailable, "meta.unavailable", "the Meta connection is temporarily unavailable, try again in a minute")
}

// ---------------------------------------------------------------------------
// POST .../integrations/meta/connect
// ---------------------------------------------------------------------------

// ConnectAnswer is what the event center needs to open the Meta login.
type ConnectAnswer struct {
	AppID        string `json:"app_id"`
	ConfigID     string `json:"config_id"`
	GraphVersion string `json:"graph_version"`
	// State is for a JS SDK login (mode js): the event center sends the
	// person to the callback with the code FB.login returned and this state.
	State string `json:"state"`
	// LoginURL is the dialog/oauth address with its own state (mode
	// redirect); "" when no redirect address is configured.
	LoginURL string `json:"login_url"`
}

// HandleConnect signs two states (one per login mode) for the caller and
// answers the login parameters.
func (h *Handler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return
	}
	if !h.cfg.Enabled {
		writeErr(w, r, http.StatusServiceUnavailable, "meta.disabled", "the Meta connection is not switched on for this platform")
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		writeErr(w, r, http.StatusForbidden, "meta.user_required", "only a signed-in person can connect Meta")
		return
	}
	now := h.now()
	jsState, err := metaconnect.SignState(h.cfg.AppSecret, orgID, userID, metaconnect.ModeJS, now)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	ans := ConnectAnswer{AppID: h.cfg.AppID, ConfigID: h.cfg.ConfigID, GraphVersion: h.cfg.GraphVersion, State: jsState}
	if h.cfg.RedirectURL != "" {
		redirState, err := metaconnect.SignState(h.cfg.AppSecret, orgID, userID, metaconnect.ModeRedirect, now)
		if err != nil {
			h.unavailable(w, r)
			return
		}
		q := url.Values{
			"client_id":                      {h.cfg.AppID},
			"config_id":                      {h.cfg.ConfigID},
			"response_type":                  {"code"},
			"override_default_response_type": {"true"},
			"redirect_uri":                   {h.cfg.RedirectURL},
			"state":                          {redirState},
		}
		ans.LoginURL = "https://www.facebook.com/" + h.cfg.GraphVersion + "/dialog/oauth?" + q.Encode()
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, ans)
}

// ---------------------------------------------------------------------------
// GET /v1/integrations/meta/callback
// ---------------------------------------------------------------------------

// Redirect reasons (?meta=error&reason=<code>).
const (
	ReasonDisabled     = "disabled"
	ReasonStateInvalid = "state_invalid"
	ReasonDenied       = "denied"
	ReasonNoCode       = "no_code"
	ReasonForbidden    = "forbidden"
	ReasonExchange     = "exchange_failed"
	ReasonGraph        = "graph_failed"
	ReasonUnavailable  = "unavailable"
)

// back sends the person to the event center. orgID nil = the event center
// root (the state could not be trusted). Never a JSON body.
func (h *Handler) back(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID, reason string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	base := strings.TrimRight(h.cfg.EventCenterURL, "/")
	if base == "" {
		// A platform misconfiguration, not an outcome of the login: there is
		// nowhere to send the person.
		writeErr(w, r, http.StatusServiceUnavailable, "meta.event_center_unconfigured", "the event center address is not configured")
		return
	}
	target := base + "/"
	if orgID != nil {
		target = base + "/o/" + orgID.String() + "/ads/meta"
	}
	q := url.Values{}
	if reason == "" {
		q.Set("meta", "connected")
	} else {
		q.Set("meta", "error")
		q.Set("reason", reason)
	}
	http.Redirect(w, r, target+"?"+q.Encode(), http.StatusFound)
}

// HandleCallback finishes a login: it verifies the state, checks again that
// the state's person holds org.update in the state's organization, exchanges
// the code, reads /me, the permissions and the assets, stores the connection
// and redirects back to the event center.
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		h.back(w, r, nil, ReasonDisabled)
		return
	}
	q := r.URL.Query()
	st, err := metaconnect.VerifyState(h.cfg.AppSecret, q.Get("state"), h.now())
	if err != nil {
		h.back(w, r, nil, ReasonStateInvalid)
		return
	}
	org := st.OrgID
	if q.Get("error") != "" || q.Get("error_reason") != "" {
		h.back(w, r, &org, ReasonDenied)
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > 4096 {
		h.back(w, r, &org, ReasonNoCode)
		return
	}
	if h.pool == nil {
		h.back(w, r, &org, ReasonUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	allowed, err := metaconnect.UserCanConnect(ctx, h.pool, st.UserID, st.OrgID)
	if err != nil {
		h.logger.Error("metaconnect: permission check failed", "org_id", org.String())
		h.back(w, r, &org, ReasonUnavailable)
		return
	}
	if !allowed {
		h.back(w, r, &org, ReasonForbidden)
		return
	}
	redirectURI := ""
	if st.Mode == metaconnect.ModeRedirect {
		redirectURI = h.cfg.RedirectURL
	}
	tok, err := h.graph.ExchangeCode(ctx, code, redirectURI)
	if err != nil {
		h.logger.Warn("metaconnect: code exchange failed", "org_id", org.String(), "mode", st.Mode, "reason", metaconnect.ErrorCode(err))
		h.back(w, r, &org, ReasonExchange)
		return
	}
	me, err := h.graph.Me(ctx, tok.AccessToken)
	if err != nil {
		h.logger.Warn("metaconnect: GET /me failed", "org_id", org.String(), "reason", metaconnect.ErrorCode(err))
		h.back(w, r, &org, ReasonGraph)
		return
	}
	scopes, err := h.graph.Permissions(ctx, tok.AccessToken)
	if err != nil {
		h.logger.Warn("metaconnect: GET /me/permissions failed, scopes stay empty", "org_id", org.String(), "reason", metaconnect.ErrorCode(err))
		scopes = []string{}
	}
	assets, skips, err := h.graph.Assets(ctx, tok.AccessToken, me.ClientBusinessID, h.cfg.Edges)
	if err != nil {
		h.logger.Warn("metaconnect: reading the assets failed", "org_id", org.String(), "reason", metaconnect.ErrorCode(err))
		h.back(w, r, &org, ReasonGraph)
		return
	}
	skipped := make([]string, 0, len(skips))
	for _, s := range skips {
		skipped = append(skipped, s.Edge+"="+s.Reason)
	}
	counts := map[string]int{metaconnect.KindDataset: 0, metaconnect.KindAdAccount: 0, metaconnect.KindPage: 0}
	for _, a := range assets {
		counts[a.Kind]++
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.back(w, r, &org, ReasonUnavailable)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := metaconnect.SaveConnection(ctx, tx, h.box, metaconnect.NewConnection{
		OrgID: org, UserID: st.UserID, BusinessID: me.ClientBusinessID, MetaUserID: me.ID,
		ConfigID: h.cfg.ConfigID, Token: tok, Scopes: scopes, Assets: assets,
	}); err != nil {
		h.logger.Error("metaconnect: saving the connection failed", "org_id", org.String())
		h.back(w, r, &org, ReasonUnavailable)
		return
	}
	if h.audit != nil {
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt: time.Now().UTC(), ActorType: "user", ActorID: st.UserID.String(),
			Action: "v1.org.meta.connect", ResourceType: "organization", ResourceID: org.String(),
			RequestID: logging.RequestID(ctx), TraceID: logging.TraceID(ctx), IP: httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id": org.String(), "meta_business_id": me.ClientBusinessID, "mode": st.Mode,
				"datasets": counts[metaconnect.KindDataset], "ad_accounts": counts[metaconnect.KindAdAccount],
				"pages": counts[metaconnect.KindPage], "skipped_edges": skipped, "scopes": scopes,
			},
		}); err != nil {
			h.back(w, r, &org, ReasonUnavailable)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.back(w, r, &org, ReasonUnavailable)
		return
	}
	if len(skips) > 0 {
		h.logger.Info("metaconnect: some asset edges were skipped", "org_id", org.String(), "skipped", strings.Join(skipped, ","))
	}
	h.back(w, r, &org, "")
}

// ---------------------------------------------------------------------------
// GET / PATCH / DELETE .../integrations/meta
// ---------------------------------------------------------------------------

// AssetView is one asset in an answer.
type AssetView struct {
	Kind     string  `json:"kind"`
	MetaID   string  `json:"meta_id"`
	Name     string  `json:"name"`
	Currency *string `json:"currency"`
	Timezone *string `json:"timezone"`
	Selected bool    `json:"selected"`
}

// ConnectionView is a stored connection without its token.
type ConnectionView struct {
	Status              string      `json:"status"`
	MetaBusinessID      string      `json:"meta_business_id"`
	ConfigID            string      `json:"config_id"`
	Scopes              []string    `json:"scopes"`
	LastError           *string     `json:"last_error"`
	ConnectedBy         *string     `json:"connected_by"`
	ConnectedAt         time.Time   `json:"connected_at"`
	CheckedAt           *time.Time  `json:"checked_at"`
	TokenExpiresAt      *time.Time  `json:"token_expires_at"`
	SelectedDatasetID   *string     `json:"selected_dataset_id"`
	SelectedAdAccountID *string     `json:"selected_ad_account_id"`
	Assets              []AssetView `json:"assets"`
}

// Integration is the answer of GET, PATCH and DELETE.
type Integration struct {
	Enabled    bool            `json:"enabled"`
	Connection *ConnectionView `json:"connection"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *Handler) view(ctx context.Context, q metaconnect.Querier, orgID uuid.UUID) (Integration, error) {
	out := Integration{Enabled: h.cfg.Enabled}
	c, assets, err := metaconnect.Load(ctx, q, orgID)
	if errors.Is(err, metaconnect.ErrNotConnected) {
		return out, nil
	}
	if err != nil {
		return Integration{}, err
	}
	v := &ConnectionView{
		Status: c.Status, MetaBusinessID: c.MetaBusinessID, ConfigID: c.ConfigID, Scopes: c.Scopes,
		LastError: strPtr(c.LastError), ConnectedAt: c.ConnectedAt, CheckedAt: c.CheckedAt,
		TokenExpiresAt: c.TokenExpiresAt, Assets: make([]AssetView, 0, len(assets)),
	}
	if c.ConnectedBy != nil {
		s := c.ConnectedBy.String()
		v.ConnectedBy = &s
	}
	for _, a := range assets {
		v.Assets = append(v.Assets, AssetView{Kind: a.Kind, MetaID: a.MetaID, Name: a.Name,
			Currency: strPtr(a.Currency), Timezone: strPtr(a.Timezone), Selected: a.Selected})
		if a.Selected && a.Kind == metaconnect.KindDataset {
			v.SelectedDatasetID = strPtr(a.MetaID)
		}
		if a.Selected && a.Kind == metaconnect.KindAdAccount {
			v.SelectedAdAccountID = strPtr(a.MetaID)
		}
	}
	out.Connection = v
	return out, nil
}

// HandleGet answers whether the feature is on and the stored connection.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return
	}
	v, err := h.view(r.Context(), h.pool, orgID)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, v)
}

// HandlePatch selects the dataset and/or the ad account the platform uses.
func (h *Handler) HandlePatch(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	var req map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err != nil || dec.Decode(&req) != nil || dec.More() || len(req) == 0 {
		writeErr(w, r, http.StatusBadRequest, "meta.invalid_body", "send dataset_id, ad_account_id or both")
		return
	}
	picks := map[string]string{}
	for k, raw := range req {
		var kind string
		switch k {
		case "dataset_id":
			kind = metaconnect.KindDataset
		case "ad_account_id":
			kind = metaconnect.KindAdAccount
		default:
			writeErr(w, r, http.StatusBadRequest, "meta.invalid_body", "unknown field "+k)
			return
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil || strings.TrimSpace(v) == "" || len(v) > 64 {
			writeErr(w, r, http.StatusBadRequest, "meta.invalid_body", k+" must be a non-empty Meta id")
			return
		}
		picks[kind] = strings.TrimSpace(v)
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, kind := range []string{metaconnect.KindDataset, metaconnect.KindAdAccount} {
		id, ok := picks[kind]
		if !ok {
			continue
		}
		switch err := metaconnect.Select(ctx, tx, orgID, kind, id); {
		case errors.Is(err, metaconnect.ErrNotConnected):
			writeErr(w, r, http.StatusNotFound, "meta.not_connected", "the organization has no Meta connection")
			return
		case errors.Is(err, metaconnect.ErrAssetNotFound):
			writeErr(w, r, http.StatusUnprocessableEntity, "meta.asset_not_found", "this Meta "+strings.ReplaceAll(kind, "_", " ")+" is not shared with this organization's connection")
			return
		case err != nil:
			h.unavailable(w, r)
			return
		}
	}
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		md := map[string]any{"org_id": orgID.String()}
		if v, ok := picks[metaconnect.KindDataset]; ok {
			md["dataset_id"] = v
		}
		if v, ok := picks[metaconnect.KindAdAccount]; ok {
			md["ad_account_id"] = v
		}
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt: time.Now().UTC(), ActorType: "user", ActorID: actor.ID,
			Action: "v1.org.meta.select", ResourceType: "organization", ResourceID: orgID.String(),
			RequestID: logging.RequestID(ctx), TraceID: logging.TraceID(ctx), IP: httputil.ExtractClientIP(r),
			Metadata: md,
		}); err != nil {
			h.unavailable(w, r)
			return
		}
	}
	v, err := h.view(ctx, tx, orgID)
	if err != nil || tx.Commit(ctx) != nil {
		h.unavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, v)
}

// HandleDelete disconnects: Meta is asked to drop the access (best effort),
// then the token is wiped, the connection set to revoked and its assets
// removed, whatever Meta answered.
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.guard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sealed, _, err := metaconnect.SealedToken(ctx, h.pool, orgID)
	if errors.Is(err, metaconnect.ErrNotConnected) {
		writeErr(w, r, http.StatusNotFound, "meta.not_connected", "the organization has no Meta connection")
		return
	}
	if err != nil {
		h.unavailable(w, r)
		return
	}
	revokedAtMeta := false
	if sealed != "" && h.box != nil && h.graph != nil {
		if tok, err := metaconnect.OpenToken(h.box, orgID, sealed); err == nil {
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := h.graph.RevokePermissions(rctx, tok); err == nil {
				revokedAtMeta = true
			} else {
				h.logger.Warn("metaconnect: revoke at Meta failed, the token is wiped anyway", "org_id", orgID.String(), "reason", metaconnect.ErrorCode(err))
			}
			cancel()
		} else {
			h.logger.Warn("metaconnect: stored token does not open, wiping it", "org_id", orgID.String())
		}
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := metaconnect.Revoke(ctx, tx, orgID, "", true); err != nil {
		h.unavailable(w, r)
		return
	}
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt: time.Now().UTC(), ActorType: "user", ActorID: actor.ID,
			Action: "v1.org.meta.disconnect", ResourceType: "organization", ResourceID: orgID.String(),
			RequestID: logging.RequestID(ctx), TraceID: logging.TraceID(ctx), IP: httputil.ExtractClientIP(r),
			Metadata: map[string]any{"org_id": orgID.String(), "revoked_at_meta": revokedAtMeta},
		}); err != nil {
			h.unavailable(w, r)
			return
		}
	}
	v, err := h.view(ctx, tx, orgID)
	if err != nil || tx.Commit(ctx) != nil {
		h.unavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, v)
}

// ---------------------------------------------------------------------------
// POST /v1/integrations/meta/deauthorize and /data-deletion
// ---------------------------------------------------------------------------

func (h *Handler) signedRequest(w http.ResponseWriter, r *http.Request) (metaconnect.SignedRequest, bool) {
	if h.cfg.AppSecret == "" {
		writeErr(w, r, http.StatusServiceUnavailable, "meta.disabled", "the Meta connection is not switched on for this platform")
		return metaconnect.SignedRequest{}, false
	}
	if h.pool == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "database is not available")
		return metaconnect.SignedRequest{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		writeErr(w, r, http.StatusBadRequest, "meta.invalid_signed_request", "send signed_request as a form field")
		return metaconnect.SignedRequest{}, false
	}
	sr, err := metaconnect.ParseSignedRequest(r.PostForm.Get("signed_request"), h.cfg.AppSecret)
	if err != nil {
		writeErr(w, r, http.StatusBadRequest, "meta.invalid_signed_request", "signed_request is missing or its signature is wrong")
		return metaconnect.SignedRequest{}, false
	}
	return sr, true
}

// systemAudit writes an audit row of a Meta callback (no person, no login).
func (h *Handler) systemAudit(ctx context.Context, r *http.Request, tx pgx.Tx, action string, orgID uuid.UUID, md map[string]any) error {
	if h.audit == nil {
		return nil
	}
	return h.audit.WriteTx(ctx, tx, audit.Event{
		OccurredAt: time.Now().UTC(), ActorType: "system", Action: action,
		ResourceType: "organization", ResourceID: orgID.String(),
		RequestID: logging.RequestID(ctx), TraceID: logging.TraceID(ctx), IP: httputil.ExtractClientIP(r),
		Metadata: md,
	})
}

// HandleDeauthorize is Meta's deauthorize callback: every connection made by
// the signed user id is set to revoked and its token wiped.
func (h *Handler) HandleDeauthorize(w http.ResponseWriter, r *http.Request) {
	sr, ok := h.signedRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	orgs, err := metaconnect.OrgsByMetaUser(ctx, tx, sr.UserID)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	for _, org := range orgs {
		if _, err := metaconnect.Revoke(ctx, tx, org, "deauthorized_at_meta", false); err != nil {
			h.unavailable(w, r)
			return
		}
		if err := h.systemAudit(ctx, r, tx, "v1.org.meta.deauthorize", org, map[string]any{"org_id": org.String()}); err != nil {
			h.unavailable(w, r)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.unavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"received": true})
}

// HandleDataDeletion is Meta's data-deletion callback: every connection made
// by the signed user id is deleted with its assets; the answer is the status
// page and the confirmation code Meta shows the person.
func (h *Handler) HandleDataDeletion(w http.ResponseWriter, r *http.Request) {
	sr, ok := h.signedRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		h.unavailable(w, r)
		return
	}
	code := hex.EncodeToString(raw)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	orgs, err := metaconnect.OrgsByMetaUser(ctx, tx, sr.UserID)
	if err != nil {
		h.unavailable(w, r)
		return
	}
	for _, org := range orgs {
		if err := metaconnect.Delete(ctx, tx, org); err != nil {
			h.unavailable(w, r)
			return
		}
		if err := h.systemAudit(ctx, r, tx, "v1.org.meta.data_deletion", org,
			map[string]any{"org_id": org.String(), "confirmation_code": code}); err != nil {
			h.unavailable(w, r)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.unavailable(w, r)
		return
	}
	statusURL := h.cfg.DataDeletionStatusURL
	sep := "?"
	if strings.Contains(statusURL, "?") {
		sep = "&"
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"url":               statusURL + sep + "code=" + url.QueryEscape(code),
		"confirmation_code": code,
	})
}
