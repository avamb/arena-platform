package hfeed

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// maxVenueSessionStarts bounds the dates listed per venue so a long tour
// cannot bloat a public answer.
const maxVenueSessionStarts = 60

// publicVenue is the public description of one place an event is held at:
// the venue's own postal address, not the seller's legal address, and no
// contact data. Fields the organizer did not fill stay null/empty, nothing
// is guessed.
type publicVenue struct {
	Name       string  `json:"name"`
	Country    *string `json:"country"`
	City       *string `json:"city"`
	Address    *string `json:"address"`
	PostalCode *string `json:"postal_code"`
	Timezone   *string `json:"timezone"`
	// SessionStarts are the RFC 3339 UTC start times of the event's sessions
	// held at this venue, earliest first.
	SessionStarts []string `json:"session_starts"`
}

func trimmedPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

// venueStreet joins the structured street lines; a venue that only has the
// legacy free-form address falls back to it.
func venueStreet(r gen.EventVenueDetailRow) *string {
	var parts []string
	if l := trimmedPtr(r.AddressLine1); l != nil {
		parts = append(parts, *l)
	}
	if l := trimmedPtr(r.AddressLine2); l != nil {
		parts = append(parts, *l)
	}
	if len(parts) > 0 {
		s := strings.Join(parts, ", ")
		return &s
	}
	return trimmedPtr(r.Address)
}

func publicVenueFromRow(r gen.EventVenueDetailRow) publicVenue {
	v := publicVenue{
		Name:          r.Name,
		Country:       trimmedPtr(r.Country),
		City:          trimmedPtr(r.CityName),
		Address:       venueStreet(r),
		PostalCode:    trimmedPtr(r.PostalCode),
		Timezone:      trimmedPtr(r.Timezone),
		SessionStarts: []string{},
	}
	for i, st := range r.Starts {
		if i >= maxVenueSessionStarts {
			break
		}
		v.SessionStarts = append(v.SessionStarts, st.UTC().Format(time.RFC3339))
	}
	return v
}

// loadPublicVenues returns the venues per event id. Best effort: it is
// presentational, so a failed read is logged and yields nothing.
func (h *Handler) loadPublicVenues(ctx context.Context, eventIDs []uuid.UUID) map[uuid.UUID][]publicVenue {
	out := map[uuid.UUID][]publicVenue{}
	if h.publicFeedQueries == nil || len(eventIDs) == 0 {
		return out
	}
	rows, err := h.publicFeedQueries.ListEventVenueDetails(ctx, eventIDs)
	if err != nil {
		h.logger.Warn("public: venue detail hydration failed", slog.String("error", err.Error()))
		return out
	}
	for _, r := range rows {
		out[r.EventID] = append(out[r.EventID], publicVenueFromRow(r))
	}
	return out
}
