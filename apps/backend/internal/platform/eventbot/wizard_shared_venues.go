package eventbot

import (
	"context"

	"github.com/google/uuid"
)

// sharedVenueMark leads the button of a venue another organization owns and
// the platform verified (migration 0172).
const sharedVenueMark = "✓ "

// SharedVenueIO is the optional extension of RefIO that lists the venues the
// platform verified for every organization. A reference source without it
// simply shows the organization's own venues, as before.
type SharedVenueIO interface {
	// SharedVenues lists verified venues of other organizations, narrowed to a
	// city and/or a country (ISO 3166-1 alpha-2); "" means any.
	SharedVenues(ctx context.Context, jwt string, orgID uuid.UUID, cityID, countryISO2 string) ([]VenueRef, error)
}

// sharedVenues is the shared-venue list or nothing: it is a convenience on top
// of the organization's own venues, so a failed read never stops the wizard.
func (w *Wizard) sharedVenues(ctx context.Context, ws WizSession, cityID, countryISO2 string) []VenueRef {
	io, ok := w.refs.(SharedVenueIO)
	if !ok {
		return nil
	}
	venues, err := io.SharedVenues(ctx, ws.JWT, ws.OrgID, cityID, countryISO2)
	if err != nil {
		return nil
	}
	for i := range venues {
		venues[i].Shared = true
	}
	return venues
}

func hasVenueID(venues []VenueRef, id string) bool {
	for _, v := range venues {
		if v.ID == id {
			return true
		}
	}
	return false
}
