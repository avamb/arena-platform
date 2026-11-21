// Package directions is the ONE place that builds the "how to get there"
// links of a ticket (owner decision 2026-11-07): plain links that open a
// maps app, never an embedded map. The e-ticket PDF, the ticket letter, the
// change letter and the invitation all call Links.
package directions

import (
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Links holds the two map links. Both are empty when nothing is known.
type Links struct {
	GoogleURL string
	WazeURL   string
}

// Empty reports whether there is no link to print.
func (l Links) Empty() bool { return l.GoogleURL == "" && l.WazeURL == "" }

// Build returns the links for a venue. Coordinates win, but only when BOTH
// are present and inside the WGS-84 range; otherwise the address (with the
// city appended when known) is used; with neither, the result is empty.
func Build(lat, lng *float64, address, city string) Links {
	if lat != nil && lng != nil && validCoord(*lat, 90) && validCoord(*lng, 180) {
		ll := formatCoord(*lat) + "," + formatCoord(*lng)
		return Links{
			GoogleURL: "https://www.google.com/maps/search/?api=1&query=" + ll,
			WazeURL:   "https://waze.com/ul?ll=" + ll + "&navigate=yes",
		}
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return Links{}
	}
	q := address
	if c := strings.TrimSpace(city); c != "" {
		q += ", " + c
	}
	esc := url.QueryEscape(q)
	return Links{
		GoogleURL: "https://www.google.com/maps/search/?api=1&query=" + esc,
		WazeURL:   "https://waze.com/ul?q=" + esc + "&navigate=yes",
	}
}

func validCoord(v, limit float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= limit
}

// formatCoord prints up to 6 decimals, dot separator, no trailing zeros.
func formatCoord(v float64) string {
	s := strconv.FormatFloat(v, 'f', 6, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}
