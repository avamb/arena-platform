package eventbot

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// GettingStartedStep is one "first step" of an organization: key is one of
// org_data, payments, event, sales_link.
type GettingStartedStep struct {
	Key  string `json:"key"`
	Done bool   `json:"done"`
}

// GettingStarted is the first-steps list the server keeps for the person: only
// the steps this person may do.
type GettingStarted struct {
	Complete bool                 `json:"complete"`
	Steps    []GettingStartedStep `json:"steps"`
}

// GettingStarted reads GET /v1/organizations/{org_id}/getting-started with the
// person's own token.
func (c *ArenaClient) GettingStarted(ctx context.Context, jwt string, orgID uuid.UUID) (GettingStarted, error) {
	var out struct {
		GettingStarted GettingStarted `json:"getting_started"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/getting-started", jwt, nil, &out)
	return out.GettingStarted, err
}
