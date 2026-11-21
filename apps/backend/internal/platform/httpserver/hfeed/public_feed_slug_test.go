package hfeed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// P-5: the feed carries the page address and the catalog texts, and keeps the
// keys (as null) for an event that has none.
func TestPublicFeedEventFromRow_SlugAgeRatingShortDescription(t *testing.T) {
	t.Parallel()
	slug, age, short := "summer-festival", "12+", "Open-air festival."
	e := gen.EventRow{
		ID: uuid.New(), OrgID: uuid.New(), Name: "Festival", Status: "published", Visibility: "public",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Slug: &slug, AgeRating: &age, ShortDescription: &short,
	}
	resp := unsignedFeed().publicFeedEventFromRow(context.Background(), e)
	if resp.Slug == nil || *resp.Slug != slug || resp.AgeRating == nil || *resp.AgeRating != age ||
		resp.ShortDescription == nil || *resp.ShortDescription != short {
		t.Fatalf("fields not projected: %+v", resp)
	}

	e.Slug, e.AgeRating, e.ShortDescription = nil, nil, nil
	raw, err := json.Marshal(unsignedFeed().publicFeedEventFromRow(context.Background(), e))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"slug", "age_rating", "short_description"} {
		v, ok := m[k]
		if !ok || v != nil {
			t.Errorf("key %q: present=%v value=%v, want present null", k, ok, v)
		}
	}
}
