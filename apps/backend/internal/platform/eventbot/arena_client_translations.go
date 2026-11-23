package eventbot

// arena_client_translations.go — the translations of an event's texts into
// other languages (lane L1): the list with the original, a hand-written
// translation (PUT replaces the whole row of the locale), and the machine run.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func translationsPath(orgID, eventID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/translations"
}

// ListTranslations reads the original texts of an event and its translations.
func (c *ArenaClient) ListTranslations(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (openapi.ContentTranslationList, error) {
	var out openapi.ContentTranslationList
	err := c.do(ctx, http.MethodGet, translationsPath(orgID, eventID), jwt, nil, &out)
	return out, err
}

// PutTranslation stores a hand-written translation for one locale. The route
// replaces the row as a whole, so the caller passes every text it wants to keep.
func (c *ArenaClient) PutTranslation(ctx context.Context, jwt string, orgID, eventID uuid.UUID, locale string, body openapi.PutContentTranslationRequest) (openapi.ContentTranslationItem, error) {
	var out openapi.ContentTranslationItem
	err := c.do(ctx, http.MethodPut, translationsPath(orgID, eventID)+"/"+locale, jwt, body, &out)
	return out, err
}

// MachineTranslate queues the machine translation of the given locales. The job
// never touches a manual translation. A platform with the model off answers 503
// translation.not_configured.
func (c *ArenaClient) MachineTranslate(ctx context.Context, jwt string, orgID, eventID uuid.UUID, locales []string) error {
	return c.do(ctx, http.MethodPost, translationsPath(orgID, eventID)+"/machine", jwt, openapi.MachineTranslateRequest{Locales: locales}, nil)
}
