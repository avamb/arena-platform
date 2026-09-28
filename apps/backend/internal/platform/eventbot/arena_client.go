package eventbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
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
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s %s: %w", method, path, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		ae := &APIError{Status: res.StatusCode}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil {
			ae.Code = env.Error.Code
			ae.Message = env.Error.Message
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
		return fmt.Errorf("decode %s %s: %w", method, path, err)
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

// Me returns the linked user's active organization memberships.
func (c *ArenaClient) Me(ctx context.Context, jwt string) ([]Membership, error) {
	var out struct {
		OrganizationMemberships []struct {
			OrgID   string `json:"org_id"`
			OrgName string `json:"org_name"`
			Role    string `json:"role"`
			Status  string `json:"status"`
		} `json:"organization_memberships"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/me", jwt, nil, &out); err != nil {
		return nil, err
	}
	ms := make([]Membership, 0, len(out.OrganizationMemberships))
	for _, m := range out.OrganizationMemberships {
		if m.Status != "" && m.Status != "active" {
			continue
		}
		id, err := uuid.Parse(m.OrgID)
		if err != nil {
			continue
		}
		ms = append(ms, Membership{OrgID: id, OrgName: m.OrgName, Role: m.Role})
	}
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
