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
	}
}

// APIError is a non-2xx answer of arena-api, with the error envelope's code.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Fields is error.details.fields of a 422: field key -> reason.
	Fields map[string]string
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
	if body != nil {
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
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.http.Do(req)
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
					Fields map[string]string `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil {
			ae.Code = env.Error.Code
			ae.Message = env.Error.Message
			ae.Fields = env.Error.Details.Fields
		}
		if ae.Code == "" {
			ae.Code = "http." + http.StatusText(res.StatusCode)
		}
		return ae
	}
	if out == nil || len(raw) == 0 {
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

// Membership is one organization the linked user belongs to (from /v1/me).
type Membership struct {
	OrgID   uuid.UUID
	OrgName string
	Role    string
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
		me.Memberships = append(me.Memberships, Membership{OrgID: id, OrgName: m.OrgName, Role: m.Role})
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
			ID   string `json:"id"`
			Name string `json:"name"`
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
		ms = append(ms, Membership{OrgID: id, OrgName: o.Name, Role: membershipRoleOwner})
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
