package hmaterials

import (
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Warning codes of the advisory map point check. The check never blocks a
// save: the point is stored either way and the caller only shows the text.
const (
	WarnPointFarFromCity     = "venue.point_far_from_city"
	WarnPointNearerOtherCity = "venue.point_nearer_other_city"
)

// MapPointWarning says that a map point looks wrong for the event's city.
type MapPointWarning struct {
	Code             string   `json:"code"`
	Message          string   `json:"message"`
	City             string   `json:"city"`
	DistanceKm       float64  `json:"distance_km"`
	ThresholdKm      float64  `json:"threshold_km"`
	NearerCity       *string  `json:"nearer_city"`
	NearerDistanceKm *float64 `json:"nearer_distance_km"`
}

// MapPointCheck is the answer of GET .../map-point/check.
type MapPointCheck struct {
	Checked  bool              `json:"checked"`
	Warnings []MapPointWarning `json:"warnings"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// MapPointWarnings judges the point against the cities of the event's venues.
// One city that fits is enough to stay quiet (an event may play in several);
// otherwise the warning names the city the point is closest to. Checked is
// false when no venue city is in the geo data, so nothing could be said.
func MapPointWarnings(venues []gen.EventVenueCity, lat, lon float64) MapPointCheck {
	out := MapPointCheck{Warnings: []MapPointWarning{}}
	var worst *geotz.PointCheck
	seen := map[string]bool{}
	for _, v := range venues {
		key := v.Country + "|" + v.City
		if seen[key] {
			continue
		}
		seen[key] = true
		c, ok := geotz.CheckPoint(v.Country, v.City, lat, lon)
		if !ok {
			continue
		}
		out.Checked = true
		if !c.Suspicious() {
			return MapPointCheck{Checked: true, Warnings: []MapPointWarning{}}
		}
		if worst == nil || c.DistanceKm < worst.DistanceKm {
			cc := c
			worst = &cc
		}
	}
	if worst == nil {
		return out
	}
	w := MapPointWarning{
		Code: WarnPointNearerOtherCity, City: worst.City.Name,
		DistanceKm: round1(worst.DistanceKm), ThresholdKm: round1(worst.ThresholdKm),
	}
	if worst.Far {
		w.Code = WarnPointFarFromCity
	}
	if worst.Nearer != nil {
		n, nd := worst.Nearer.Name, round1(worst.NearerKm)
		w.NearerCity, w.NearerDistanceKm = &n, &nd
	}
	const tail = " Open it in Google Maps and check that it is the right place."
	switch {
	case worst.Far && worst.Nearer != nil:
		w.Message = fmt.Sprintf("The point is %.1f km from %s, farther than the usual %.1f km, and it is nearer to %s (%.1f km)."+tail,
			w.DistanceKm, w.City, w.ThresholdKm, *w.NearerCity, *w.NearerDistanceKm)
	case worst.Far:
		w.Message = fmt.Sprintf("The point is %.1f km from %s, farther than the usual %.1f km."+tail,
			w.DistanceKm, w.City, w.ThresholdKm)
	default:
		w.Message = fmt.Sprintf("The point is %.1f km from %s but only %.1f km from %s."+tail,
			w.DistanceKm, w.City, *w.NearerDistanceKm, *w.NearerCity)
	}
	out.Warnings = append(out.Warnings, w)
	return out
}

// checkFor loads the event's venue cities and judges the point. A failed
// lookup answers "not checked": an advisory helper must not break a save.
func (h *ExtrasHandler) checkFor(r *http.Request, eventID uuid.UUID, lat, lon float64) MapPointCheck {
	venues, err := h.queries.ListEventVenueCities(r.Context(), eventID)
	if err != nil {
		return MapPointCheck{Warnings: []MapPointWarning{}}
	}
	return MapPointWarnings(venues, lat, lon)
}

// HandleCheckMapPoint serves GET .../map-point/check?latitude=&longitude=: the
// advisory verdict on a point the organizer is still typing. It stores
// nothing and answers 200 with no warnings when the venue city is unknown.
func (h *ExtrasHandler) HandleCheckMapPoint(w http.ResponseWriter, r *http.Request) {
	_, eventID, ok := h.scope(w, r)
	if !ok {
		return
	}
	lat, e1 := strconv.ParseFloat(r.URL.Query().Get("latitude"), 64)
	lon, e2 := strconv.ParseFloat(r.URL.Query().Get("longitude"), 64)
	if e1 != nil || e2 != nil || !ValidMapPoint(lat, lon) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"event.invalid_map_point",
			"latitude must be between -90 and 90 and longitude between -180 and 180, for example 50.0755 and 14.4378",
			r, map[string]any{"fields": []string{"latitude", "longitude"}},
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.checkFor(r, eventID, lat, lon))
}
