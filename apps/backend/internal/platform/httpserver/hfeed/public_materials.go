// public_materials.go — task 1-2c: the materials of an event's page (gallery,
// videos, "where else to buy" links, the map point and, once the event is
// over, the photo report) as buyers see them. Both public surfaces carry the
// same `materials` block: GET /v1/public/pages/{slug}/{event_slug} and
// GET /v1/public/feeds/{token}/events/{event_id}. It is an ADDITION: no
// existing field of either answer changes.
//
// One database round trip (gen.GetEventPublicMaterials) serves all five
// lists. The photo report follows the rule of ListEventReportPhotosPublic:
// hidden until the event's last session has ended.
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
)

// publicMaterialPhoto is one gallery or photo-report picture.
type publicMaterialPhoto struct {
	MediaID string `json:"media_id"`
	// URL is the signed, absolute address of the picture (the same mechanism
	// as the poster and the logo of the page).
	URL     string  `json:"url"`
	Caption *string `json:"caption"`
	// Width and Height are the pixel size when the upload pipeline knew it.
	Width  *int `json:"width"`
	Height *int `json:"height"`
}

// publicMaterialVideo is one video link. EmbedURL is set only for a provider
// the page can embed (youtube-nocookie, Vimeo, Rutube); any other https link
// has neither VideoID nor EmbedURL and is shown as a plain link.
type publicMaterialVideo struct {
	Provider string  `json:"provider"`
	VideoID  *string `json:"video_id"`
	EmbedURL *string `json:"embed_url"`
	URL      string  `json:"url"`
}

// publicMaterialPartnerLink is one "where else to buy" link; the seller name
// is shown beside it so the buyer knows who they would buy from.
type publicMaterialPartnerLink struct {
	URL        string `json:"url"`
	SellerName string `json:"seller_name"`
}

// publicMaterialMapPoint is the event's one map point.
type publicMaterialMapPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Label     *string `json:"label"`
}

// publicMaterials is the `materials` block. Every list is `[]`, never null;
// MapPoint is null when the event has none.
type publicMaterials struct {
	Gallery      []publicMaterialPhoto       `json:"gallery"`
	Videos       []publicMaterialVideo       `json:"videos"`
	PartnerLinks []publicMaterialPartnerLink `json:"partner_links"`
	MapPoint     *publicMaterialMapPoint     `json:"map_point"`
	ReportPhotos []publicMaterialPhoto       `json:"report_photos"`
}

func emptyPublicMaterials() publicMaterials {
	return publicMaterials{
		Gallery:      []publicMaterialPhoto{},
		Videos:       []publicMaterialVideo{},
		PartnerLinks: []publicMaterialPartnerLink{},
		ReportPhotos: []publicMaterialPhoto{},
	}
}

// loadPublicMaterials reads the materials of eventID. It never fails the page:
// a lookup error is logged and the block is empty, like the venue names.
func (h *Handler) loadPublicMaterials(ctx context.Context, eventID uuid.UUID, now time.Time) publicMaterials {
	if h.publicFeedQueries == nil {
		return emptyPublicMaterials()
	}
	row, err := h.publicFeedQueries.GetEventPublicMaterials(ctx, eventID, now)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public materials: lookup failed",
				slog.String("event_id", eventID.String()),
				slog.String("error", err.Error()),
			)
		}
		return emptyPublicMaterials()
	}
	m, err := h.materialsFromRow(ctx, row)
	if err != nil {
		h.logger.Warn("public materials: decode failed",
			slog.String("event_id", eventID.String()),
			slog.String("error", err.Error()),
		)
		return emptyPublicMaterials()
	}
	return m
}

type rawPhoto struct {
	MediaID uuid.UUID `json:"media_id"`
	Caption *string   `json:"caption"`
	Width   *int      `json:"width"`
	Height  *int      `json:"height"`
}

type rawVideo struct {
	Provider string  `json:"provider"`
	VideoID  *string `json:"video_id"`
	URL      string  `json:"url"`
}

// materialsFromRow turns the JSON columns into the public shapes and signs the
// picture addresses.
func (h *Handler) materialsFromRow(ctx context.Context, row gen.EventPublicMaterialsRow) (publicMaterials, error) {
	out := emptyPublicMaterials()

	photos := func(raw []byte) ([]publicMaterialPhoto, error) {
		var in []rawPhoto
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		res := make([]publicMaterialPhoto, 0, len(in))
		for _, p := range in {
			res = append(res, publicMaterialPhoto{
				MediaID: p.MediaID.String(),
				URL:     h.mediaFileURL(ctx, p.MediaID),
				Caption: p.Caption,
				Width:   p.Width,
				Height:  p.Height,
			})
		}
		return res, nil
	}

	var err error
	if out.Gallery, err = photos(row.Gallery); err != nil {
		return publicMaterials{}, err
	}
	if out.ReportPhotos, err = photos(row.ReportPhotos); err != nil {
		return publicMaterials{}, err
	}

	var videos []rawVideo
	if err := json.Unmarshal(row.Videos, &videos); err != nil {
		return publicMaterials{}, err
	}
	for _, v := range videos {
		item := publicMaterialVideo{Provider: v.Provider, VideoID: v.VideoID, URL: v.URL}
		if v.VideoID != nil {
			if embed := eventmaterials.EmbedURL(v.Provider, *v.VideoID); embed != "" {
				item.EmbedURL = &embed
			}
		}
		out.Videos = append(out.Videos, item)
	}

	if err := json.Unmarshal(row.PartnerLinks, &out.PartnerLinks); err != nil {
		return publicMaterials{}, err
	}
	if out.PartnerLinks == nil {
		out.PartnerLinks = []publicMaterialPartnerLink{}
	}

	if len(row.MapPoint) > 0 && string(row.MapPoint) != "null" {
		var mp publicMaterialMapPoint
		if err := json.Unmarshal(row.MapPoint, &mp); err != nil {
			return publicMaterials{}, err
		}
		out.MapPoint = &mp
	}
	return out, nil
}
