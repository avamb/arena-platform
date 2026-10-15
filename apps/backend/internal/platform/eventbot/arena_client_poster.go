package eventbot

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

// posterFactsCallTimeout is how long one poster reading may take: arena-api
// gives the vision model 45 seconds and a few more to answer.
const posterFactsCallTimeout = 60 * time.Second

// PosterFactsNotConfigured is the error code of arena-api's answer when it has
// no model key: the wizard then asks every question by hand.
const PosterFactsNotConfigured = "media.poster_reader_not_configured"

// PosterFacts asks arena-api what an already uploaded poster says
// (POST /v1/organizations/{org_id}/media/{media_id}/poster-facts). The model
// key lives on the API only; the bot never holds it. The facts are hints for
// the wizard to offer as buttons.
func (c *ArenaClient) PosterFacts(ctx context.Context, jwt string, orgID uuid.UUID, mediaID string) (posterread.Facts, error) {
	var out struct {
		Facts posterread.Facts `json:"facts"`
	}
	slow := *c
	hc := *c.http
	hc.Timeout = posterFactsCallTimeout
	slow.http = &hc
	err := slow.do(ctx, http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/media/"+url.PathEscape(mediaID)+"/poster-facts", jwt, nil, &out)
	return out.Facts, err
}

// posterReaderOff reports whether err is the API saying it has no model key.
func posterReaderOff(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == PosterFactsNotConfigured
}
