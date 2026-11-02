package eventbot

// arena_client_disputes.go — the calls behind the Disputes screens (spec 51.3
// section 9.2, task DS-11). Reading is open to the owner and the manager
// (dispute.read); every call that answers, accepts or rebuilds is the
// owner's (dispute.respond) and the server refuses a manager with 403.
//
// The package files are PDFs with the buyer's data: they are fetched one at a
// time, never written to disk and never logged.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// disputeCallTimeout bounds a call that talks to the payment provider
// (submit, accept, rebuild): the server waits on the provider before it
// answers.
const disputeCallTimeout = 100 * time.Second

// disputeFileMaxBytes bounds one package file the bot forwards (the server
// keeps a package under the provider's size limit; Telegram takes 50 MB).
const disputeFileMaxBytes = 20 << 20

func disputeBase(orgID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/disputes"
}

// slowDispute is a client whose HTTP timeout fits a provider call.
func (c *ArenaClient) slowDispute() *ArenaClient {
	slow := *c
	hc := *c.http
	hc.Timeout = disputeCallTimeout
	slow.http = &hc
	return &slow
}

// DisputeList reads one page of the organization's disputes of a group
// (open, review, closed): GET .../disputes?group=&limit=&offset=.
func (c *ArenaClient) DisputeList(ctx context.Context, jwt string, orgID uuid.UUID, group string, limit, offset int) (openapi.DisputeList, error) {
	q := url.Values{}
	if group != "" {
		q.Set("group", group)
	}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	var out openapi.DisputeList
	err := c.do(ctx, http.MethodGet, disputeBase(orgID)+"?"+q.Encode(), jwt, nil, &out)
	return out, err
}

// DisputeGet reads one dispute with its package and journal.
func (c *ArenaClient) DisputeGet(ctx context.Context, jwt string, orgID, id uuid.UUID) (openapi.DisputeCard, error) {
	var out openapi.DisputeCard
	err := c.do(ctx, http.MethodGet, disputeBase(orgID)+"/"+id.String(), jwt, nil, &out)
	return out, err
}

// DisputeRebuild builds the evidence package again (owner).
func (c *ArenaClient) DisputeRebuild(ctx context.Context, jwt string, orgID, id uuid.UUID) (openapi.DisputeCard, error) {
	var out openapi.DisputeCard
	err := c.slowDispute().do(ctx, http.MethodPost, disputeBase(orgID)+"/"+id.String()+"/package", jwt, map[string]any{}, &out)
	return out, err
}

// DisputeSubmit sends the answer (owner). testOutcome ("win" or "lose") is
// only for a test payment key; the server refuses it for a live one.
func (c *ArenaClient) DisputeSubmit(ctx context.Context, jwt string, orgID, id uuid.UUID, testOutcome string) (openapi.DisputeCard, error) {
	body := map[string]any{}
	if testOutcome != "" {
		body["test_outcome"] = testOutcome
	}
	var out openapi.DisputeCard
	err := c.slowDispute().do(ctx, http.MethodPost, disputeBase(orgID)+"/"+id.String()+"/submit", jwt, body, &out)
	return out, err
}

// DisputeAccept accepts the dispute: the amount stays with the buyer (owner).
func (c *ArenaClient) DisputeAccept(ctx context.Context, jwt string, orgID, id uuid.UUID) (openapi.DisputeCard, error) {
	var out openapi.DisputeCard
	err := c.slowDispute().do(ctx, http.MethodPost, disputeBase(orgID)+"/"+id.String()+"/accept", jwt, map[string]any{}, &out)
	return out, err
}

// DisputeSentByOrganizer records that the organizer answered outside arena
// (owner; e-mail and manual disputes).
func (c *ArenaClient) DisputeSentByOrganizer(ctx context.Context, jwt string, orgID, id uuid.UUID) (openapi.DisputeCard, error) {
	var out openapi.DisputeCard
	err := c.do(ctx, http.MethodPost, disputeBase(orgID)+"/"+id.String()+"/sent-by-organizer", jwt, map[string]any{}, &out)
	return out, err
}

// DisputeFile downloads one PDF of a package. The bytes are returned to the
// caller and nowhere else.
func (c *ArenaClient) DisputeFile(ctx context.Context, jwt string, orgID, id, fileID uuid.UUID) ([]byte, error) {
	path := disputeBase(orgID) + "/" + id.String() + "/files/" + fileID.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/pdf")
	req.Header.Set("X-Admin-Reason", AdminReason)
	req.Header.Set(audit.HeaderClientChannel, ClientChannel)
	req.Header.Set("Authorization", "Bearer "+jwt)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET dispute file: %w", unwrapURLError(err))
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, disputeFileMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read dispute file: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, apiErrorFrom(res.StatusCode, raw)
	}
	if len(raw) > disputeFileMaxBytes {
		return nil, fmt.Errorf("dispute file larger than %d bytes", disputeFileMaxBytes)
	}
	return raw, nil
}
