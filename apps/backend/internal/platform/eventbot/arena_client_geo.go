package eventbot

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The geography half of the ArenaClient (spec 64 section 2.1.1): the world
// country list, the venue directory and the zones of a country. The world list
// is static apart from `in_reference`, so it is kept per language for a few
// minutes.

// worldCacheTTL is how long a world list is reused. A venue that creates a
// country drops the cache at once (CreateVenue), so the TTL only bounds how
// long another person's new country stays unseen.
const worldCacheTTL = 5 * time.Minute

// WorldCountry is one country of the full ISO 3166-1 list the server embeds.
type WorldCountry struct {
	ISO2          string
	ISO3          string
	Name          string // in the request language
	NameEn        string
	Currency      string
	Region        string
	InReference   bool
	CountryID     string // the reference row, "" outside the reference
	HasOrganizers bool
}

// WorldList is GET /v1/geo/world-countries.
type WorldList struct {
	// Regions are the region codes in display order.
	Regions   []string
	Countries []WorldCountry
}

// ByISO2 finds a country by its two-letter code, any letter case.
func (l WorldList) ByISO2(iso2 string) (WorldCountry, bool) {
	iso2 = strings.ToUpper(strings.TrimSpace(iso2))
	for _, c := range l.Countries {
		if c.ISO2 == iso2 {
			return c, true
		}
	}
	return WorldCountry{}, false
}

type worldCacheEntry struct {
	list WorldList
	at   time.Time
}

// worldCache is the per-language cache of the world list.
type worldCache struct {
	mu sync.Mutex
	m  map[string]worldCacheEntry
}

// The methods tolerate a nil cache (a client built without NewArenaClient):
// it then never caches.
func (c *worldCache) get(lang string) (WorldList, bool) {
	if c == nil {
		return WorldList{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[lang]
	if !ok || time.Since(e.at) > worldCacheTTL {
		return WorldList{}, false
	}
	return e.list, true
}

func (c *worldCache) put(lang string, l WorldList) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]worldCacheEntry{}
	}
	c.m[lang] = worldCacheEntry{list: l, at: time.Now()}
}

func (c *worldCache) drop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}

// WorldCountries lists every country of the world in lang (GET
// /v1/geo/world-countries, public: jwt may be empty).
func (c *ArenaClient) WorldCountries(ctx context.Context, jwt, lang string) (WorldList, error) {
	lang = NormalizeLocale(lang)
	if l, ok := c.world.get(lang); ok {
		return l, nil
	}
	var out openapi.GeoWorldCountriesResponse
	if err := c.do(ctx, http.MethodGet, "/v1/geo/world-countries?lang="+url.QueryEscape(lang), jwt, nil, &out); err != nil {
		return WorldList{}, err
	}
	list := WorldList{Regions: out.Regions, Countries: make([]WorldCountry, 0, len(out.Countries))}
	for _, x := range out.Countries {
		wc := WorldCountry{
			ISO2: strings.ToUpper(x.Iso2), ISO3: strings.ToUpper(x.Iso3), Name: x.Name, NameEn: x.NameEn,
			Currency: x.Currency, Region: x.Region, InReference: x.InReference, HasOrganizers: x.HasOrganizers,
		}
		if x.CountryId != nil {
			wc.CountryID = x.CountryId.String()
		}
		list.Countries = append(list.Countries, wc)
	}
	c.world.put(lang, list)
	return list, nil
}

// DirCountry is a country with venues in the organization's scope.
type DirCountry struct {
	ISO2      string
	CountryID string
	Name      string
	Region    string
	Venues    int
	OwnVenues int
}

// DirCity is a city of the chosen country with venues in scope.
type DirCity struct {
	ID        string
	Name      string
	ISO2      string
	Venues    int
	OwnVenues int
}

// DirVenue is a venue of the directory: the organization's own, or a verified
// one of another organization.
type DirVenue struct {
	ID          string
	Name        string
	CityID      string
	CityName    string
	CountryISO2 string
	Address     string
	Timezone    string
	Capacity    int
	Own         bool
	Verified    bool
	// HasPlan is only a label ("seating plan"): the plan offer of the wizard
	// reads the plans itself.
	HasPlan bool
}

// Ref is the venue as the wizard keeps it.
func (v DirVenue) Ref() VenueRef {
	return VenueRef{ID: v.ID, Name: v.Name, CityID: v.CityID, CountryISO2: v.CountryISO2,
		Timezone: v.Timezone, Capacity: v.Capacity, Shared: !v.Own}
}

// VenueDir is GET /v1/organizations/{org_id}/venue-directory.
type VenueDir struct {
	QuickPick []string
	Countries []DirCountry
	Cities    []DirCity
	Venues    []DirVenue
	Total     int
}

// VenueDirectory reads the venue directory: the counters always, the cities of
// country, and the venues matching country, cityID and q (any of them empty =
// not filtered; with none of the three no venues come back).
func (c *ArenaClient) VenueDirectory(ctx context.Context, jwt string, orgID uuid.UUID, country, cityID, q, lang string) (VenueDir, error) {
	v := url.Values{"limit": {"100"}, "lang": {NormalizeLocale(lang)}}
	if country != "" {
		v.Set("country", strings.ToUpper(country))
	}
	if cityID != "" {
		v.Set("city_id", cityID)
	}
	if q != "" {
		v.Set("q", q)
	}
	var out openapi.VenueDirectoryResponse
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/venue-directory?"+v.Encode(), jwt, nil, &out); err != nil {
		return VenueDir{}, err
	}
	dir := VenueDir{QuickPick: out.QuickPick, Total: int(out.Total)}
	for _, x := range out.Countries {
		dc := DirCountry{ISO2: strings.ToUpper(x.Iso2), Name: x.Name, Region: x.Region, Venues: int(x.Venues), OwnVenues: int(x.OwnVenues)}
		if x.CountryId != nil {
			dc.CountryID = x.CountryId.String()
		}
		dir.Countries = append(dir.Countries, dc)
	}
	for _, x := range out.Cities {
		dir.Cities = append(dir.Cities, DirCity{ID: x.Id.String(), Name: x.Name, ISO2: strings.ToUpper(x.CountryIso2), Venues: int(x.Venues), OwnVenues: int(x.OwnVenues)})
	}
	for _, x := range out.Venues {
		dv := DirVenue{ID: x.Id.String(), Name: x.Name, Own: x.Own, Verified: x.ReviewStatus == "verified", HasPlan: x.HasPlan}
		if x.CityId != nil {
			dv.CityID = x.CityId.String()
		}
		if x.CityName != nil {
			dv.CityName = *x.CityName
		}
		if x.CountryIso2 != nil {
			dv.CountryISO2 = strings.ToUpper(*x.CountryIso2)
		}
		if x.Address != nil {
			dv.Address = *x.Address
		}
		if x.Timezone != nil {
			dv.Timezone = *x.Timezone
		}
		if x.CapacityDefault != nil {
			dv.Capacity = *x.CapacityDefault
		}
		dir.Venues = append(dir.Venues, dv)
	}
	return dir, nil
}

// TzOption is one time zone of a country, as the API names it.
type TzOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CountryTimezones lists the zones of any ISO country (GET
// /v1/geo/countries/{iso2}/timezones); single says the country has one zone.
func (c *ArenaClient) CountryTimezones(ctx context.Context, jwt, iso2 string) (zones []TzOption, single bool, err error) {
	var out openapi.GeoCountryTimezonesResponse
	if err = c.do(ctx, http.MethodGet, "/v1/geo/countries/"+url.PathEscape(strings.ToUpper(iso2))+"/timezones", jwt, nil, &out); err != nil {
		return nil, false, err
	}
	for _, z := range out.Timezones {
		zones = append(zones, TzOption{ID: z.Id, Label: z.Label})
	}
	return zones, out.Single, nil
}
