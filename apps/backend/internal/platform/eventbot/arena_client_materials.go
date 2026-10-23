package eventbot

// arena_client_materials.go — the calls behind the bot's "Materials" screens
// (task 7-3, spec 40 §5.7): the photos, videos, "where else to buy" links, map
// point and photo report of an event page. The rules (the limits 6 / 4 / 3 /
// 1 / 30, the video link parser, the partner address check, the coordinate
// ranges, the organization of an uploaded photo) are arena-api's
// (hcatalog/event_materials.go, hmaterials/extras*.go); the bot only carries
// the answers. A photo is uploaded first (POST /v1/media with the owner type
// the section needs) and then attached by its media id.

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// materialSection names one list of an event's page materials.
type materialSection struct {
	Code      string // the callback code: g v p m r
	Path      string // the route segment under .../events/{event_id}/
	OrderKey  string // the key of PUT .../order, "" when the section has no list
	OwnerType string // the media owner type of a photo section, "" otherwise
	Max       int    // the limit the API enforces (it reports its own as max_items)
}

// The sections, in the order of the menu.
var materialSections = []materialSection{
	{Code: "g", Path: "media-items", OrderKey: "item_ids", OwnerType: "event_gallery", Max: 6},
	{Code: "v", Path: "videos", OrderKey: "video_ids", Max: 4},
	{Code: "p", Path: "partner-links", OrderKey: "link_ids", Max: 3},
	{Code: "m", Path: "map-point", Max: 1},
	{Code: "r", Path: "photo-report", OrderKey: "photo_ids", OwnerType: "event_report", Max: 30},
}

func materialSectionByCode(code string) (materialSection, bool) {
	for _, s := range materialSections {
		if s.Code == code {
			return s, true
		}
	}
	return materialSection{}, false
}

// isPhoto reports whether the section holds uploaded pictures.
func (s materialSection) isPhoto() bool { return s.OwnerType != "" }

// The API's codes for a refused materials call.
const (
	matCodeGalleryFull    = "event.gallery_full"
	matCodeVideosFull     = "event.videos_full"
	matCodePartnersFull   = "event.partner_links_full"
	matCodeReportFull     = "event.report_photos_full"
	matCodeInvalidMedia   = "event.invalid_media"
	matCodeInvalidReport  = "event.invalid_report_media"
	matCodeInvalidCaption = "event.invalid_caption"
	matCodeInvalidVideo   = "event.invalid_video_url"
	matCodeInvalidPartner = "event.invalid_partner_url"
	matCodeInvalidSeller  = "event.invalid_seller_name"
	matCodeInvalidPoint   = "event.invalid_map_point"
	matCodeMapNotFound    = "event.map_point_not_found"
)

// MaterialItem is one entry of a materials list. Only the members a section
// has are filled: a photo has a media id and a caption, a video a link and a
// provider, a partner link a link and a seller.
type MaterialItem struct {
	ID         uuid.UUID `json:"id"`
	MediaID    string    `json:"media_id"`
	Caption    *string   `json:"caption"`
	URL        string    `json:"url"`
	SellerName string    `json:"seller_name"`
	Provider   string    `json:"provider"`
}

// MaterialList is the answer of a list route and of a reorder.
type MaterialList struct {
	Items    []MaterialItem `json:"items"`
	MaxItems int            `json:"max_items"`
}

// MapPoint is the event's one map point.
type MapPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Label     *string `json:"label"`
}

func materialsBase(orgID, eventID uuid.UUID, sec materialSection) string {
	return "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/" + sec.Path
}

// MaterialsList reads one section's list.
func (c *ArenaClient) MaterialsList(ctx context.Context, jwt string, orgID, eventID uuid.UUID, sec materialSection) (MaterialList, error) {
	var out MaterialList
	err := c.do(ctx, http.MethodGet, materialsBase(orgID, eventID, sec), jwt, nil, &out)
	return out, err
}

// MaterialsAdd appends one entry (POST). The body is the section's own.
func (c *ArenaClient) MaterialsAdd(ctx context.Context, jwt string, orgID, eventID uuid.UUID, sec materialSection, body map[string]any) (MaterialItem, error) {
	var out MaterialItem
	err := c.do(ctx, http.MethodPost, materialsBase(orgID, eventID, sec), jwt, body, &out)
	return out, err
}

// MaterialsPatch changes one entry (caption of a photo, seller of a link).
func (c *ArenaClient) MaterialsPatch(ctx context.Context, jwt string, orgID, eventID uuid.UUID, sec materialSection, id uuid.UUID, body map[string]any) (MaterialItem, error) {
	var out MaterialItem
	err := c.do(ctx, http.MethodPatch, materialsBase(orgID, eventID, sec)+"/"+id.String(), jwt, body, &out)
	return out, err
}

// MaterialsDelete removes one entry; the API closes the gap in the order.
func (c *ArenaClient) MaterialsDelete(ctx context.Context, jwt string, orgID, eventID uuid.UUID, sec materialSection, id uuid.UUID) error {
	return c.do(ctx, http.MethodDelete, materialsBase(orgID, eventID, sec)+"/"+id.String(), jwt, nil, nil)
}

// MaterialsReorder sets the display order: every id of the section exactly
// once, in the new order.
func (c *ArenaClient) MaterialsReorder(ctx context.Context, jwt string, orgID, eventID uuid.UUID, sec materialSection, ids []uuid.UUID) (MaterialList, error) {
	var out MaterialList
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = id.String()
	}
	err := c.do(ctx, http.MethodPut, materialsBase(orgID, eventID, sec)+"/order", jwt, map[string]any{sec.OrderKey: list}, &out)
	return out, err
}

// MapPointGet reads the event's map point; nil means the event has none (the
// API answers 404 event.map_point_not_found for that).
func (c *ArenaClient) MapPointGet(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (*MapPoint, error) {
	var out MapPoint
	sec, _ := materialSectionByCode("m")
	if err := c.do(ctx, http.MethodGet, materialsBase(orgID, eventID, sec), jwt, nil, &out); err != nil {
		if APIErrorCode(err) == matCodeMapNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// MapPointSet creates the point or replaces it as a whole.
func (c *ArenaClient) MapPointSet(ctx context.Context, jwt string, orgID, eventID uuid.UUID, lat, lon float64) (MapPoint, error) {
	var out MapPoint
	sec, _ := materialSectionByCode("m")
	err := c.do(ctx, http.MethodPut, materialsBase(orgID, eventID, sec), jwt, map[string]any{"latitude": lat, "longitude": lon}, &out)
	return out, err
}

// MapPointDelete removes the point.
func (c *ArenaClient) MapPointDelete(ctx context.Context, jwt string, orgID, eventID uuid.UUID) error {
	sec, _ := materialSectionByCode("m")
	return c.do(ctx, http.MethodDelete, materialsBase(orgID, eventID, sec), jwt, nil, nil)
}
