package geotz

// point.go answers "is this map point plausibly in this city?". The data is
// the P lines of cities.tsv.gz (GeoNames cities15000, see NOTICE): every city
// of 15 000 people or more with its point to 2 decimals (about 1 km) and its
// population in thousands. The check is ADVISORY - the callers show a warning
// and still save the point.
//
// GeoNames has no city radius, so one is estimated from the population: a
// city is taken as a disc of the given population at 4 000 people per km2
// (a compact European or Israeli city; Tel Aviv, 432 000 people, comes out at
// 5.9 km). A point is "far" when it is more than
//
//	max(15 km, 4 x radius)    (never more than 60 km)
//
// from the centre. The floor keeps a small town from flagging a venue in its
// outskirts, the 4x scales a big city's sprawl (Tel Aviv: 23.7 km), and the
// cap stops a megacity (Moscow, 12 million) from excusing anything.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"math"
	"strconv"
	"strings"
	"sync"
)

const (
	earthRadiusKm      = 6371.0088
	densityPerKm2      = 4000.0
	minThresholdKm     = 15.0
	thresholdRadiusMul = 4.0
	maxThresholdKm     = 60.0
)

// Place is one city with its point.
type Place struct {
	Name       string // ASCII name for messages, "Ramat HaSharon"
	Country    string // ISO-3166-1 alpha-2
	Lat, Lon   float64
	Population int // people, rounded down to thousands
}

// RadiusKm is the estimated radius of the city (see the file comment).
func (p Place) RadiusKm() float64 {
	pop := float64(p.Population)
	if pop < 1000 {
		pop = 1000
	}
	return math.Sqrt(pop / (math.Pi * densityPerKm2))
}

// ThresholdKm is how far from the centre a point may be before it is "far".
func (p Place) ThresholdKm() float64 {
	t := math.Max(minThresholdKm, thresholdRadiusMul*p.RadiusKm())
	return math.Min(t, maxThresholdKm)
}

var (
	pointOnce   sync.Once
	placesBy    map[string][]*Place          // ISO2 -> every city, biggest first
	placeByName map[string]map[string]*Place // ISO2 -> normalised name -> city
)

func loadPoints() {
	placesBy = map[string][]*Place{}
	placeByName = map[string]map[string]*Place{}
	zr, err := gzip.NewReader(bytes.NewReader(citiesGz))
	if err != nil {
		return
	}
	defer func() { _ = zr.Close() }()
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) != 7 || p[0] != "P" {
			continue
		}
		lat, e1 := strconv.ParseFloat(p[2], 64)
		lon, e2 := strconv.ParseFloat(p[3], 64)
		kpop, e3 := strconv.Atoi(p[4])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		pl := &Place{Name: p[5], Country: p[1], Lat: lat, Lon: lon, Population: kpop * 1000}
		placesBy[p[1]] = append(placesBy[p[1]], pl)
		m := placeByName[p[1]]
		if m == nil {
			m = map[string]*Place{}
			placeByName[p[1]] = m
		}
		for _, n := range strings.Split(p[6], "|") {
			if _, taken := m[n]; !taken { // the biggest city comes first and wins
				m[n] = pl
			}
		}
	}
}

// DistanceKm is the great-circle (haversine) distance between two points.
func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// CityPoint finds the named city of the country (Latin, Cyrillic or Hebrew
// spelling). It reports false for a blank name, an unknown country or a city
// below 15 000 people - the caller then skips the check.
func CityPoint(iso2, name string) (Place, bool) {
	pointOnce.Do(loadPoints)
	n := Normalize(name)
	if n == "" {
		return Place{}, false
	}
	if p := placeByName[strings.ToUpper(strings.TrimSpace(iso2))][n]; p != nil {
		return *p, true
	}
	return Place{}, false
}

// Nearest returns the city of the country closest to the point, and its
// distance. skip names a city to leave out (the venue's own), by its point.
func Nearest(iso2 string, lat, lon float64, skip *Place) (Place, float64, bool) {
	pointOnce.Do(loadPoints)
	var best *Place
	bestKm := math.MaxFloat64
	for _, p := range placesBy[strings.ToUpper(strings.TrimSpace(iso2))] {
		if skip != nil && p.Lat == skip.Lat && p.Lon == skip.Lon && p.Name == skip.Name {
			continue
		}
		if d := DistanceKm(lat, lon, p.Lat, p.Lon); d < bestKm {
			best, bestKm = p, d
		}
	}
	if best == nil {
		return Place{}, 0, false
	}
	return *best, bestKm, true
}

// PointCheck is the verdict on one point against one city.
type PointCheck struct {
	City        Place
	DistanceKm  float64 // from the city centre
	ThresholdKm float64
	Far         bool // DistanceKm > ThresholdKm
	// Nearer is set when the point lies outside the city's own radius and
	// another city of the country is closer than the city centre is.
	Nearer   *Place
	NearerKm float64
}

// Suspicious is true when the check deserves a warning.
func (c PointCheck) Suspicious() bool { return c.Far || c.Nearer != nil }

// CheckPoint judges the point against the named city of the country. It
// reports false when the city is not in the data, so nothing can be said.
func CheckPoint(iso2, city string, lat, lon float64) (PointCheck, bool) {
	own, ok := CityPoint(iso2, city)
	if !ok {
		return PointCheck{}, false
	}
	d := DistanceKm(lat, lon, own.Lat, own.Lon)
	c := PointCheck{City: own, DistanceKm: d, ThresholdKm: own.ThresholdKm()}
	c.Far = d > c.ThresholdKm
	if d > own.RadiusKm() {
		if other, od, found := Nearest(iso2, lat, lon, &own); found && od < d {
			c.Nearer, c.NearerKm = &other, od
		}
	}
	return c, true
}
