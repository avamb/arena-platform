package eventbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// ArenaClient is the bot's view of arena-api: a thin typed HTTP client over
// the same REST routes the WordPress plugin and admin-web use. Business
// calls carry a short-lived JWT of the linked user; the single service call
// (accepting an invitation) carries BOT_SERVICE_TOKEN.
type ArenaClient struct {
	baseURL      string
	serviceToken string
	http         *http.Client
	// formatTimeout is how long FormatText waits (default formatCallTimeout).
	formatTimeout time.Duration
	// world keeps the world country list per language (arena_client_geo.go).
	world *worldCache
}

// NewArenaClient builds a client for baseURL (http://api:8080 in compose).
func NewArenaClient(baseURL, serviceToken string, httpClient *http.Client) *ArenaClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &ArenaClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		serviceToken: serviceToken,
		http:         httpClient,
		world:        &worldCache{},
	}
}

// APIError is a non-2xx answer of arena-api, with the error envelope's code.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Fields is error.details.fields of a 422: field key -> reason.
	Fields map[string]string
	// Reason is error.details.reason: the short word of a refused text
	// (richtext reason, for example "too_long").
	Reason string
	// OwnersNotice is error.details.owners_notice of a refund refused for a
	// scanned ticket: queued, throttled, no_recipient, or empty.
	OwnersNotice string
	// TicketID is error.details.ticket_id: the ticket a refusal is about
	// (refund.ticket_scanned names the scanned one).
	TicketID string
	// Field is error.details.field: the field a 422 is about.
	Field string
	// Capability is error.details.capability of a refused or missing
	// referral programme: whether the event's channels can pay the cashback.
	Capability *openapi.ReferralCapability
	// Blockers is error.details.blockers of 422 shop.cannot_open: what keeps
	// the storefront closed (settings_not_saved, no_active_product,
	// payment_not_configured).
	Blockers []string
	// RefundableMinor is error.details.refundable_minor of a refused shop
	// refund: what is really left to return, minor units.
	RefundableMinor *int64
	// TicketNumber is error.details.ticket_number of 409
	// reissue.ticket_not_reissuable: the ticket that blocks the reissue.
	TicketNumber int64
	// FileName is error.details.file_name of a refused plan intake file: the
	// file the server refused.
	FileName string
	// RetryAfter is the Retry-After header of a 429, in seconds (0 when absent).
	RetryAfter int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("arena-api: %d %s: %s", e.Status, e.Code, e.Message)
}

// IsAPIError reports whether err is an APIError with the given status.
func IsAPIError(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// APIErrorCode returns the envelope code of err, or "".
func APIErrorCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func (c *ArenaClient) do(ctx context.Context, method, path, bearer string, body any, out any) error {
	route := routeOf(path)
	var reader io.Reader
	contentType := "application/json"
	if mp, ok := body.(multipartBody); ok {
		// A form already built by the caller (plan intake files).
		reader = bytes.NewReader(mp.data)
		contentType = mp.contentType
	} else if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, route, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, route, err)
	}
	req.Header.Set("Accept", "application/json")
	// Every organization route lets a platform superadmin act across
	// organizations only when the request also states a reason
	// (`requireOrgMembership` -> httputil.RequireAdminReason). The header is
	// inert for everybody else, so the client states its reason always: the
	// audit trail then names the bot on every superadmin call.
	req.Header.Set("X-Admin-Reason", AdminReason)
	// Names the bot as the client, so arena-api records audit metadata.via
	// = telegram_bot on every row this call writes.
	req.Header.Set(audit.HeaderClientChannel, ClientChannel)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if key, ok := ctx.Value(idempotencyKeyCtx{}).(string); ok && key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.clientFor(ctx).Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, route, unwrapURLError(err))
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s %s: %w", method, route, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		ae := &APIError{Status: res.StatusCode}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details struct {
					Fields       map[string]string `json:"fields"`
					Reason       string            `json:"reason"`
					OwnersNotice string            `json:"owners_notice"`
					TicketID     string            `json:"ticket_id"`
					Field        json.RawMessage   `json:"field"`
					Capability   json.RawMessage   `json:"capability"`
				} `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil {
			ae.Code = env.Error.Code
			ae.Message = env.Error.Message
			ae.Fields = env.Error.Details.Fields
			ae.Reason = env.Error.Details.Reason
			ae.OwnersNotice = env.Error.Details.OwnersNotice
			ae.TicketID = env.Error.Details.TicketID
			// Read apart so that a details object of another shape on some
			// other route can never cost the code and the message above.
			_ = json.Unmarshal(env.Error.Details.Field, &ae.Field)
			if len(env.Error.Details.Capability) > 0 {
				var capab openapi.ReferralCapability
				if json.Unmarshal(env.Error.Details.Capability, &capab) == nil {
					ae.Capability = &capab
				}
			}
		}
		// The shop routes' extra details, read apart so that a shape the other
		// routes use can never cost the fields above.
		var extra struct {
			Error struct {
				Details struct {
					Blockers        []string `json:"blockers"`
					RefundableMinor *int64   `json:"refundable_minor"`
					TicketNumber    int64    `json:"ticket_number"`
					FileName        string   `json:"file_name"`
				} `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &extra) == nil {
			ae.Blockers = extra.Error.Details.Blockers
			ae.RefundableMinor = extra.Error.Details.RefundableMinor
			ae.TicketNumber = extra.Error.Details.TicketNumber
			ae.FileName = extra.Error.Details.FileName
		}
		if n, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && n > 0 {
			ae.RetryAfter = n
		}
		if ae.Code == "" {
			ae.Code = "http." + http.StatusText(res.StatusCode)
		}
		return ae
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if text, ok := out.(*rawText); ok {
		*text = rawText(raw)
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, route, err)
	}
	return nil
}

// AcceptInvitationRequest is the body of POST /v1/bot/invitations/accept.
type AcceptInvitationRequest struct {
	Code             string `json:"code"`
	Email            string `json:"email"`
	TelegramUserID   int64  `json:"telegram_user_id"`
	TelegramUsername string `json:"telegram_username,omitempty"`
	Locale           string `json:"locale,omitempty"`
}

// AcceptInvitationResponse is what the API answers on success.
type AcceptInvitationResponse struct {
	UserID         string `json:"user_id"`
	Email          string `json:"email"`
	OrgID          string `json:"org_id"`
	OrgName        string `json:"org_name"`
	Role           string `json:"role"`
	MembershipRole string `json:"membership_role"`
	Locale         string `json:"locale"`
}

// AcceptInvitation redeems an invitation code under the service token.
func (c *ArenaClient) AcceptInvitation(ctx context.Context, req AcceptInvitationRequest) (AcceptInvitationResponse, error) {
	var out AcceptInvitationResponse
	err := c.do(ctx, http.MethodPost, "/v1/bot/invitations/accept", c.serviceToken, req, &out)
	return out, err
}

// rawText is a destination of do for a plain-text answer (the courier text):
// the body is stored as it is instead of being decoded as JSON.
type rawText string

// Membership is one organization the linked user belongs to (from /v1/me).
type Membership struct {
	OrgID   uuid.UUID
	OrgName string
	Role    string
	// Modules are the paid modules the organization has today (spec 61); a
	// connected shop is "shop". ShopMode is "full", "orders_only" or "" (none):
	// "orders_only" is a disconnected shop whose orders and refunds stay open.
	Modules  []string
	ShopMode string
	// Archived: a platform superadmin archived the organization. The person
	// stays a member, but the bot must not offer it as a working organization.
	Archived bool
}

// AdminReason is what the bot writes into X-Admin-Reason: a platform
// superadmin working through the bot is audited under it on every call.
const AdminReason = "telegram event center bot"

// ClientChannel is what the bot writes into X-Client-Channel: arena-api stamps
// it into audit_events.metadata.via, so every change made through the bot is
// recognisable in the audit log.
const ClientChannel = audit.ChannelTelegramBot

// superadminRole is the platform-wide role /v1/me reports for the operator.
const superadminRole = "platform_superadmin"

// Me is what /v1/me tells the bot about the linked user.
type Me struct {
	Memberships []Membership
	// Superadmin: the user holds the platform_superadmin role, so every
	// organization is theirs to work in (with X-Admin-Reason, which the
	// client always sends).
	Superadmin bool
}

// Me returns the linked user's active organization memberships.
func (c *ArenaClient) Me(ctx context.Context, jwt string) ([]Membership, error) {
	me, err := c.MeInfo(ctx, jwt)
	if err != nil {
		return nil, err
	}
	return me.Memberships, nil
}

// MeInfo returns the memberships together with the platform role flag.
func (c *ArenaClient) MeInfo(ctx context.Context, jwt string) (Me, error) {
	var out struct {
		Roles                   []string `json:"roles"`
		OrganizationMemberships []struct {
			OrgID   string `json:"org_id"`
			OrgName string `json:"org_name"`
			Role    string `json:"role"`
			Status  string `json:"status"`
			// Archived is absent on an API older than the flag: false then.
			Archived bool `json:"archived"`
			// Modules and ShopMode are absent on an API older than spec 61.
			Modules  []string `json:"modules"`
			ShopMode *string  `json:"shop_mode"`
		} `json:"organization_memberships"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/me", jwt, nil, &out); err != nil {
		return Me{}, err
	}
	me := Me{Memberships: make([]Membership, 0, len(out.OrganizationMemberships))}
	for _, m := range out.OrganizationMemberships {
		if m.Status != "" && m.Status != "active" {
			continue
		}
		id, err := uuid.Parse(m.OrgID)
		if err != nil {
			continue
		}
		ms := Membership{OrgID: id, OrgName: m.OrgName, Role: m.Role, Archived: m.Archived, Modules: m.Modules}
		if m.ShopMode != nil {
			ms.ShopMode = *m.ShopMode
		}
		me.Memberships = append(me.Memberships, ms)
	}
	for _, r := range out.Roles {
		if r == superadminRole {
			me.Superadmin = true
		}
	}
	return me, nil
}

// AllOrganizations lists every organization of the platform as owner
// memberships — the superadmin's organization chooser. GET /v1/organizations
// answers the whole list only to a caller with org.read on the platform
// level; the bot calls it for a superadmin identity alone.
func (c *ArenaClient) AllOrganizations(ctx context.Context, jwt string) ([]Membership, error) {
	var out struct {
		Organizations []struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Modules  []string `json:"modules"`
			ShopMode *string  `json:"shop_mode"`
		} `json:"organizations"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations", jwt, nil, &out); err != nil {
		return nil, err
	}
	ms := make([]Membership, 0, len(out.Organizations))
	for _, o := range out.Organizations {
		id, err := uuid.Parse(o.ID)
		if err != nil {
			continue
		}
		m := Membership{OrgID: id, OrgName: o.Name, Role: membershipRoleOwner, Modules: o.Modules}
		if o.ShopMode != nil {
			m.ShopMode = *o.ShopMode
		}
		ms = append(ms, m)
	}
	sort.SliceStable(ms, func(i, j int) bool { return strings.ToLower(ms[i].OrgName) < strings.ToLower(ms[j].OrgName) })
	return ms, nil
}

// ListEvents returns the organization's events (every status).
func (c *ArenaClient) ListEvents(ctx context.Context, jwt string, orgID uuid.UUID) ([]openapi.EventItem, error) {
	var out openapi.EventListResponse
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/events", jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

// ListSessions returns an event's sessions ordered by start.
func (c *ArenaClient) ListSessions(ctx context.Context, jwt string, orgID, eventID uuid.UUID) ([]openapi.SessionItem, error) {
	var out openapi.SessionListResponse
	path := "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/sessions"
	if err := c.do(ctx, http.MethodGet, path, jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// SessionSummary returns the one-screen numbers of a session.
func (c *ArenaClient) SessionSummary(ctx context.Context, jwt string, orgID, sessionID uuid.UUID) (openapi.SessionSummary, error) {
	var out openapi.SessionSummary
	path := "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/summary"
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// routeOf is a request path without its query string. Errors are logged by
// the callers, and the orders search puts what the organizer typed — a buyer's
// e-mail or phone — in the query: the log line carries the route only.
func routeOf(path string) string {
	route, _, _ := strings.Cut(path, "?")
	return route
}

// unwrapURLError drops the request URL net/http repeats inside its error
// (with the query string), keeping the cause.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// idempotencyKeyCtx carries an Idempotency-Key for one call (do sends it when
// present). Used by the refund screens: a retry or a double press of the same
// confirmed operation replays the first answer instead of refunding twice.
type idempotencyKeyCtx struct{}

func withIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKeyCtx{}, key)
}

// callTimeoutKey carries a longer timeout for one call (an upload of tens of
// megabytes does not fit the client's usual 15 seconds).
type callTimeoutKey struct{}

func withCallTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, callTimeoutKey{}, d)
}

// clientFor is the HTTP client of one call: the client's own, or a copy of it
// with the timeout the context asks for.
func (c *ArenaClient) clientFor(ctx context.Context) *http.Client {
	if d, ok := ctx.Value(callTimeoutKey{}).(time.Duration); ok && d > c.http.Timeout {
		cp := *c.http
		cp.Timeout = d
		return &cp
	}
	return c.http
}
