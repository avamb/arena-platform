package eventbot

// arena_client_series.go - the calls behind the bot's "Tour" step and button
// (spec 63, L-2). A tour is an event series: the organizer's grouping of events
// (one tour, many cities). The rules live in arena-api (hcatalog): a tour of
// another organization or an archived one is refused with 422
// event.invalid_series. The wire types are not in the generated openapi package
// of this build, so the bot reads only the few fields it needs.

import (
	"context"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

// SeriesItem is one tour as the bot shows it.
type SeriesItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	EventCount int    `json:"event_count"`
}

// SeriesSource lists the organization's active tours. The wizard asks for it
// through a type assertion on its reference source, so a source without tours
// simply has no "Tour" step.
type SeriesSource interface {
	ActiveSeries(ctx context.Context, jwt string, orgID uuid.UUID) ([]SeriesItem, error)
}

// SeriesLinker binds an event to a tour ("" unbinds). SaveIO implementations
// that have it bind a NEW event right after its first date is saved.
type SeriesLinker interface {
	LinkEventSeries(ctx context.Context, jwt string, orgID uuid.UUID, eventID, seriesID string) error
}

// ActiveSeries returns the organization's non-archived tours, in the server's
// order (position, then name).
func (c *ArenaClient) ActiveSeries(ctx context.Context, jwt string, orgID uuid.UUID) ([]SeriesItem, error) {
	var out struct {
		Items []SeriesItem `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/series", jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// LinkEventSeries sends series_id on PATCH .../events/{id}: a tour id binds the
// event, "" sends null and unbinds it.
func (c *ArenaClient) LinkEventSeries(ctx context.Context, jwt string, orgID uuid.UUID, eventID, seriesID string) error {
	var value any
	if seriesID != "" {
		value = seriesID
	}
	body := map[string]any{"series_id": value}
	return c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String()+"/events/"+url.PathEscape(eventID), jwt, body, nil)
}

// EventSeries reads the tour of one event from the organization's event list:
// the tour's id and name, both "" when the event has none.
func (c *ArenaClient) EventSeries(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (id, name string, err error) {
	var out struct {
		Events []struct {
			ID         string  `json:"id"`
			SeriesID   *string `json:"series_id"`
			SeriesName *string `json:"series_name"`
		} `json:"events"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/events", jwt, nil, &out); err != nil {
		return "", "", err
	}
	for _, e := range out.Events {
		if e.ID != eventID.String() {
			continue
		}
		if e.SeriesID != nil {
			id = *e.SeriesID
		}
		if e.SeriesName != nil {
			name = *e.SeriesName
		}
		return id, name, nil
	}
	return "", "", nil
}
