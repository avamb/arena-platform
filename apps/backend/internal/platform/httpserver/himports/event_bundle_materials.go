// event_bundle_materials.go — task 1-2c: the page materials of an event, read
// next to its bundles (GET .../event-bundle, field `materials`).
//
// A bundle is the importer's input and the importer does not read materials,
// so they sit BESIDE `bundles`, never inside one. A client that copies an
// event reads this block to show what the copy would carry, imports the
// bundles as a new event, then calls
// POST .../events/{new_event_id}/materials/copy-from/{event_id} to move the
// gallery, videos, partner links and map point onto it. The photo report is
// not listed: it is about a show that already happened and is never copied.
package himports

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// EventBundleGalleryItem is one gallery photo of the event.
type EventBundleGalleryItem struct {
	MediaID  uuid.UUID `json:"media_id"`
	Caption  *string   `json:"caption"`
	Position int       `json:"position"`
}

// EventBundleVideo is one video link of the event.
type EventBundleVideo struct {
	Provider string  `json:"provider"`
	VideoID  *string `json:"video_id"`
	URL      string  `json:"url"`
	Position int     `json:"position"`
}

// EventBundlePartnerLink is one "where else to buy" link of the event.
type EventBundlePartnerLink struct {
	URL        string `json:"url"`
	SellerName string `json:"seller_name"`
	Position   int    `json:"position"`
}

// EventBundleMapPoint is the event's map point.
type EventBundleMapPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Label     *string `json:"label"`
}

// EventBundleMaterials is the `materials` block of the read. Every list is
// `[]` when empty, never null; MapPoint is null when the event has none.
type EventBundleMaterials struct {
	Gallery      []EventBundleGalleryItem `json:"gallery"`
	Videos       []EventBundleVideo       `json:"videos"`
	PartnerLinks []EventBundlePartnerLink `json:"partner_links"`
	MapPoint     *EventBundleMapPoint     `json:"map_point"`
}

// normalizeBundleMaterials makes every list non-nil, so the JSON never says null.
func normalizeBundleMaterials(m EventBundleMaterials) EventBundleMaterials {
	if m.Gallery == nil {
		m.Gallery = []EventBundleGalleryItem{}
	}
	if m.Videos == nil {
		m.Videos = []EventBundleVideo{}
	}
	if m.PartnerLinks == nil {
		m.PartnerLinks = []EventBundlePartnerLink{}
	}
	return m
}

// loadBundleMaterials reads the four copyable lists of the event, in display
// order, with the organizer-side queries of tasks 1-2a and 1-2b.
func loadBundleMaterials(ctx context.Context, q *gen.Queries, eventID uuid.UUID) (EventBundleMaterials, error) {
	out := EventBundleMaterials{
		Gallery:      []EventBundleGalleryItem{},
		Videos:       []EventBundleVideo{},
		PartnerLinks: []EventBundlePartnerLink{},
	}
	photos, err := q.ListEventGalleryItems(ctx, eventID)
	if err != nil {
		return out, fmt.Errorf("read gallery: %w", err)
	}
	for _, p := range photos {
		out.Gallery = append(out.Gallery, EventBundleGalleryItem{MediaID: p.MediaID, Caption: p.Caption, Position: int(p.Position)})
	}
	videos, err := q.ListEventVideos(ctx, eventID)
	if err != nil {
		return out, fmt.Errorf("read videos: %w", err)
	}
	for _, v := range videos {
		out.Videos = append(out.Videos, EventBundleVideo{Provider: v.Provider, VideoID: v.VideoID, URL: v.URL, Position: int(v.Position)})
	}
	links, err := q.ListEventPartnerLinks(ctx, eventID)
	if err != nil {
		return out, fmt.Errorf("read partner links: %w", err)
	}
	for _, l := range links {
		out.PartnerLinks = append(out.PartnerLinks, EventBundlePartnerLink{URL: l.URL, SellerName: l.SellerName, Position: int(l.Position)})
	}
	mp, err := q.GetEventMapPoint(ctx, eventID)
	switch {
	case err == nil:
		out.MapPoint = &EventBundleMapPoint{Latitude: mp.Latitude, Longitude: mp.Longitude, Label: mp.Label}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return out, fmt.Errorf("read map point: %w", err)
	}
	return out, nil
}
