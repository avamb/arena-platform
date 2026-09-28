package eventbot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The ArenaClient half the wizard uses: references, "+ new …" creations,
// the poster upload and the event-bundle import. It implements RefIO.

// Countries lists the geo countries in lang.
func (c *ArenaClient) Countries(ctx context.Context, jwt, lang string) ([]RefItem, error) {
	var out openapi.GeoCountriesResponse
	if err := c.do(ctx, http.MethodGet, "/v1/geo/countries?lang="+url.QueryEscape(NormalizeLocale(lang))+"&limit=250", jwt, nil, &out); err != nil {
		return nil, err
	}
	items := make([]RefItem, 0, len(out.Countries))
	for _, x := range out.Countries {
		items = append(items, RefItem{ID: x.Id.String(), Name: x.Name, ISO2: x.Iso2, Currency: x.Currency})
	}
	return items, nil
}

// Cities lists the cities of a country in lang.
func (c *ArenaClient) Cities(ctx context.Context, jwt, countryID, lang string) ([]RefItem, error) {
	var out openapi.GeoCitiesResponse
	q := "/v1/geo/cities?country_id=" + url.QueryEscape(countryID) + "&lang=" + url.QueryEscape(NormalizeLocale(lang)) + "&limit=250"
	if err := c.do(ctx, http.MethodGet, q, jwt, nil, &out); err != nil {
		return nil, err
	}
	items := make([]RefItem, 0, len(out.Cities))
	for _, x := range out.Cities {
		items = append(items, RefItem{ID: x.Id.String(), Name: x.Name, ISO2: x.CountryIso2})
	}
	return items, nil
}

// Venues lists the organization's venues.
func (c *ArenaClient) Venues(ctx context.Context, jwt string, orgID uuid.UUID) ([]VenueRef, error) {
	var out struct {
		Venues []openapi.VenueItem `json:"venues"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/venues", jwt, nil, &out); err != nil {
		return nil, err
	}
	items := make([]VenueRef, 0, len(out.Venues))
	for _, v := range out.Venues {
		if string(v.Status) == "archived" {
			continue
		}
		items = append(items, venueRefFrom(v))
	}
	return items, nil
}

func venueRefFrom(v openapi.VenueItem) VenueRef {
	r := VenueRef{ID: v.Id.String(), Name: v.Name}
	if v.CityId != nil {
		r.CityID = v.CityId.String()
	}
	if v.Country != nil {
		r.CountryISO2 = strings.ToUpper(*v.Country)
	}
	if v.Timezone != nil {
		r.Timezone = *v.Timezone
	}
	if v.CapacityDefault != nil {
		r.Capacity = *v.CapacityDefault
	}
	return r
}

// Promoters lists the organization's promoters (archived ones excluded).
func (c *ArenaClient) Promoters(ctx context.Context, jwt string, orgID uuid.UUID) ([]RefItem, error) {
	var out openapi.PromoterListEnvelope
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/promoters", jwt, nil, &out); err != nil {
		return nil, err
	}
	items := make([]RefItem, 0, len(out.Promoters))
	for _, p := range out.Promoters {
		if p.Archived {
			continue
		}
		items = append(items, RefItem{ID: p.Id.String(), Name: p.Name})
	}
	return items, nil
}

// Channels lists the organization's sales channels.
func (c *ArenaClient) Channels(ctx context.Context, jwt string, orgID uuid.UUID) ([]RefItem, error) {
	var out struct {
		Channels []openapi.Channel `json:"channels"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/channels", jwt, nil, &out); err != nil {
		return nil, err
	}
	items := make([]RefItem, 0, len(out.Channels))
	for _, ch := range out.Channels {
		items = append(items, RefItem{ID: ch.Id.String(), Name: ch.Name})
	}
	return items, nil
}

// CreateCity adds (or finds) a city of the country by name.
func (c *ArenaClient) CreateCity(ctx context.Context, jwt string, orgID uuid.UUID, countryID, name, lang string) (RefItem, error) {
	loc := NormalizeLocale(lang)
	var out openapi.OrgCityResponse
	body := map[string]any{"country_id": countryID, "name": name, "locale": loc}
	if err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/cities", jwt, body, &out); err != nil {
		return RefItem{}, err
	}
	return RefItem{ID: out.City.Id.String(), Name: out.City.Name, ISO2: out.City.CountryIso2}, nil
}

// CreateVenue adds a venue of the organization.
func (c *ArenaClient) CreateVenue(ctx context.Context, jwt string, orgID uuid.UUID, v VenueCreate) (VenueRef, error) {
	body := map[string]any{
		"name":     v.Name,
		"city_id":  v.CityID,
		"country":  strings.ToUpper(v.CountryISO2),
		"timezone": v.Timezone,
		"status":   "active",
	}
	if v.Address != "" {
		body["address_line1"] = v.Address
	}
	if v.Capacity > 0 {
		body["capacity_default"] = v.Capacity
	}
	var out struct {
		Venue openapi.VenueItem `json:"venue"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/venues", jwt, body, &out); err != nil {
		return VenueRef{}, err
	}
	return venueRefFrom(out.Venue), nil
}

// CreatePromoter adds a promoter of the organization.
func (c *ArenaClient) CreatePromoter(ctx context.Context, jwt string, orgID uuid.UUID, name, legalID string) (RefItem, error) {
	body := map[string]any{"name": name}
	if legalID != "" {
		body["legal_id"] = legalID
	}
	var out openapi.PromoterEnvelope
	if err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/promoters", jwt, body, &out); err != nil {
		return RefItem{}, err
	}
	return RefItem{ID: out.Promoter.Id.String(), Name: out.Promoter.Name}, nil
}

// UploadedMedia is what POST /v1/media answers.
type UploadedMedia struct {
	ID     string `json:"id"`
	Width  *int32 `json:"width"`
	Height *int32 `json:"height"`
}

// UploadPoster stores the poster bytes as an event_poster media object of
// the organization and returns its id.
func (c *ArenaClient) UploadPoster(ctx context.Context, jwt string, orgID uuid.UUID, filename, contentType string, data []byte) (UploadedMedia, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("owner_type", "event_poster")
	_ = mw.WriteField("org_id", orgID.String())
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return UploadedMedia{}, err
	}
	if _, err := part.Write(data); err != nil {
		return UploadedMedia{}, err
	}
	if err := mw.Close(); err != nil {
		return UploadedMedia{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/media", &buf)
	if err != nil {
		return UploadedMedia{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+jwt)
	res, err := c.http.Do(req)
	if err != nil {
		return UploadedMedia{}, fmt.Errorf("POST /v1/media: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return UploadedMedia{}, &APIError{Status: res.StatusCode, Code: "media.upload_failed", Message: strings.TrimSpace(string(raw))}
	}
	var out UploadedMedia
	if err := decodeJSON(raw, &out); err != nil {
		return UploadedMedia{}, err
	}
	_ = contentType
	return out, nil
}

// MediaSignedURL returns a short-lived download URL of a media object, the
// value the event-bundle's bigPosterUrl takes.
func (c *ArenaClient) MediaSignedURL(ctx context.Context, jwt, mediaID string) (string, error) {
	var out struct {
		SignedURL string `json:"signed_url"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/media/"+url.PathEscape(mediaID), jwt, nil, &out); err != nil {
		return "", err
	}
	return out.SignedURL, nil
}

// ImportResult is the event-bundle import response the wizard reads.
type ImportResult struct {
	EventID   string `json:"event_id"`
	SessionID string `json:"session_id"`
	CompatIDs struct {
		ActionID      int64 `json:"action_id"`
		ActionEventID int64 `json:"action_event_id"`
	} `json:"compat_ids"`
	Warnings []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"warnings"`
}

// ImportEventBundle posts one date of the event.
func (c *ArenaClient) ImportEventBundle(ctx context.Context, jwt string, orgID uuid.UUID, req bil24compat.ImportSessionRequest) (ImportResult, error) {
	var out ImportResult
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/imports/event-bundle", jwt, req, &out)
	return out, err
}

// OrganizationSlug returns the organization's public slug (its storefront
// path), or "" when it cannot be read.
func (c *ArenaClient) OrganizationSlug(ctx context.Context, jwt string, orgID uuid.UUID) (string, error) {
	var out struct {
		Organization openapi.OrganizationItem `json:"organization"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String(), jwt, nil, &out); err != nil {
		return "", err
	}
	return out.Organization.Slug, nil
}
