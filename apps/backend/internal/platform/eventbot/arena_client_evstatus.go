package eventbot

// arena_client_evstatus.go — the calls behind the event card's status buttons
// (EC-10, spec 35 §6.1): the status transition, the dry run of a delete and
// the delete itself. The rules live in arena-api (hcatalog): which transitions
// exist, the publish gate, and the refusal to delete an event that has sold
// anything. The bot only shows the answers.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// SetEventStatus moves the event to a new lifecycle status
// (POST .../events/{id}/status). The API refuses a move the lifecycle does not
// allow with 422 event.invalid_transition, and a publish of an event with no
// date or no priced category with 422 event.publish_requires_*.
func (c *ArenaClient) SetEventStatus(ctx context.Context, jwt string, orgID, eventID uuid.UUID, status string) error {
	path := "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/status"
	return c.do(ctx, http.MethodPost, path, jwt, map[string]string{"status": status}, nil)
}

// EventDeleteImpact asks what deleting the event would touch and whether it is
// allowed (GET .../events/{id}/delete-impact). Nothing is written.
func (c *ArenaClient) EventDeleteImpact(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (openapi.EventDeleteImpact, error) {
	var out openapi.EventDeleteImpact
	path := "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/delete-impact"
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// DeleteEvent deletes the event (DELETE .../events/{id}). An event with paid
// orders or issued tickets is a 409 event.has_paid_orders and stays as it was.
func (c *ArenaClient) DeleteEvent(ctx context.Context, jwt string, orgID, eventID uuid.UUID) error {
	path := "/v1/organizations/" + orgID.String() + "/events/" + eventID.String()
	return c.do(ctx, http.MethodDelete, path, jwt, nil, nil)
}
