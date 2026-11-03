package eventpage

import (
	"encoding/json"
	"sort"
	"strings"
)

// eventTypes are schema.org Event and its subtypes (spec 59 §3).
var eventTypes = map[string]bool{
	"event": true, "musicevent": true, "theaterevent": true, "comedyevent": true,
	"danceevent": true, "festival": true, "childrensevent": true, "educationevent": true,
	"exhibitionevent": true, "screeningevent": true, "socialevent": true, "sportsevent": true,
	"literaryevent": true, "businessevent": true, "visualartsevent": true, "foodevent": true,
	"eventseries": true,
}

// ldEvent is one Event read from JSON-LD, its values still as the page wrote them.
type ldEvent struct {
	Name, Description, StartDate, DoorTime, Status, AgeRange, Language string
	VenueName, City, Country, Street                                   string
	Online                                                             bool
	Images                                                             []string
	Offers                                                             []ldOffer
	PriceLow, PriceHigh, PriceCurrency                                 string
}

// ldOffer is one Offer: a category, a price as written, a currency.
type ldOffer struct{ Name, Price, Currency string }

// Bounds of the walk, so a hostile page cannot make it expensive.
const (
	maxLDDepth  = 12
	maxLDNodes  = 4000
	maxLDEvents = 60
)

// parseJSONLD reads every Event of the page's JSON-LD blocks. A block that is
// not valid JSON is skipped; the page is never refused because of it.
func parseJSONLD(blocks []string) []ldEvent {
	w := ldWalker{}
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		b = strings.TrimPrefix(b, "<!--")
		b = strings.TrimSuffix(b, "-->")
		b = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(b), ";"))
		// Numbers stay as written ("34.90"), never a float.
		dec := json.NewDecoder(strings.NewReader(b))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			continue
		}
		w.walk(v, 0)
	}
	return w.events
}

type ldWalker struct {
	nodes  int
	events []ldEvent
}

func (w *ldWalker) walk(v any, depth int) {
	if depth > maxLDDepth || w.nodes > maxLDNodes || len(w.events) >= maxLDEvents {
		return
	}
	w.nodes++
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			w.walk(x, depth+1)
		}
	case map[string]any:
		if isEventType(t["@type"]) {
			w.addEvent(t, depth)
			return
		}
		// Keys in order, so the dates come out the same on every reading.
		keys := make([]string, 0, len(t))
		for k := range t {
			if k != "@context" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			w.walk(t[k], depth+1)
		}
	}
}

// addEvent records an Event and the events of its subEvent (an
// EventSeries lists its dates there). Other fields of an Event are not
// walked: a superEvent or a workPerformed is not another date.
func (w *ldWalker) addEvent(m map[string]any, depth int) {
	ev := ldEvent{
		Name:        ldText(m["name"]),
		Description: ldText(m["description"]),
		StartDate:   ldText(m["startDate"]),
		DoorTime:    ldText(m["doorTime"]),
		Status:      ldText(m["eventStatus"]),
		AgeRange:    ldText(m["typicalAgeRange"]),
		Language:    ldText(m["inLanguage"]),
		Images:      ldImages(m["image"]),
	}
	readLocation(&ev, m["location"])
	readOffers(&ev, m["offers"], 0)
	if ev.StartDate != "" || ev.Name != "" {
		w.events = append(w.events, ev)
	}
	if sub, ok := m["subEvent"]; ok {
		parent := ev
		before := len(w.events)
		w.walk(sub, depth+1)
		// A date of a series inherits what it does not say itself.
		for i := before; i < len(w.events); i++ {
			inherit(&w.events[i], parent)
		}
	}
}

func inherit(e *ldEvent, p ldEvent) {
	if e.Name == "" {
		e.Name = p.Name
	}
	if e.Description == "" {
		e.Description = p.Description
	}
	if e.VenueName == "" && e.City == "" {
		e.VenueName, e.City, e.Country, e.Street, e.Online = p.VenueName, p.City, p.Country, p.Street, p.Online
	}
	if len(e.Offers) == 0 {
		e.Offers = p.Offers
	}
	if len(e.Images) == 0 {
		e.Images = p.Images
	}
	if e.AgeRange == "" {
		e.AgeRange = p.AgeRange
	}
}

func isEventType(t any) bool {
	switch v := t.(type) {
	case string:
		return eventTypes[strings.ToLower(lastSegment(v))]
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && eventTypes[strings.ToLower(lastSegment(s))] {
				return true
			}
		}
	}
	return false
}

// lastSegment turns "https://schema.org/MusicEvent" or "schema:MusicEvent"
// into "MusicEvent".
func lastSegment(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, "/:#"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ldText reads a text value: a string, a number, {"@value": …}, or the
// first such entry of an array.
func ldText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case map[string]any:
		if s := ldText(t["@value"]); s != "" {
			return s
		}
		if s := ldText(t["name"]); s != "" {
			return s
		}
		return ldText(t["@id"])
	case []any:
		for _, x := range t {
			if s := ldText(x); s != "" {
				return s
			}
		}
	}
	return ""
}

// ldImages reads image: a URL, an ImageObject, or a list of them.
func ldImages(v any) []string {
	var out []string
	var add func(any)
	add = func(x any) {
		if len(out) >= 5 {
			return
		}
		switch t := x.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				out = append(out, s)
			}
		case map[string]any:
			if s := ldText(t["url"]); s != "" {
				out = append(out, s)
			} else if s := ldText(t["contentUrl"]); s != "" {
				out = append(out, s)
			}
		case []any:
			for _, y := range t {
				add(y)
			}
		}
	}
	add(v)
	return out
}

// readLocation reads location: a Place (name, address), a VirtualLocation,
// a plain text, or a list of them (the first physical place wins).
func readLocation(ev *ldEvent, v any) {
	switch t := v.(type) {
	case string:
		ev.VenueName = strings.TrimSpace(t)
	case []any:
		for _, x := range t {
			readLocation(ev, x)
			if ev.VenueName != "" || ev.City != "" {
				return
			}
		}
	case map[string]any:
		typ := strings.ToLower(lastSegment(ldText(t["@type"])))
		if typ == "virtuallocation" {
			ev.Online = true
			return
		}
		ev.VenueName = ldText(t["name"])
		switch a := t["address"].(type) {
		case string:
			ev.Street = strings.TrimSpace(a)
		case map[string]any:
			ev.Street = ldText(a["streetAddress"])
			ev.City = ldText(a["addressLocality"])
			ev.Country = ldText(a["addressCountry"])
		case []any:
			if len(a) > 0 {
				if m, ok := a[0].(map[string]any); ok {
					ev.Street = ldText(m["streetAddress"])
					ev.City = ldText(m["addressLocality"])
					ev.Country = ldText(m["addressCountry"])
				}
			}
		}
	}
}

// readOffers reads offers: an Offer, an AggregateOffer (its own offers, or
// only a low and high price), or a list of them.
func readOffers(ev *ldEvent, v any, depth int) {
	if depth > 3 {
		return
	}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			readOffers(ev, x, depth+1)
		}
	case map[string]any:
		typ := strings.ToLower(lastSegment(ldText(t["@type"])))
		if typ == "aggregateoffer" {
			if inner, ok := t["offers"]; ok {
				before := len(ev.Offers)
				readOffers(ev, inner, depth+1)
				if len(ev.Offers) > before {
					return
				}
			}
			ev.PriceLow = ldText(t["lowPrice"])
			ev.PriceHigh = ldText(t["highPrice"])
			ev.PriceCurrency = ldText(t["priceCurrency"])
			return
		}
		o := ldOffer{
			Name:     ldText(t["name"]),
			Price:    ldText(t["price"]),
			Currency: ldText(t["priceCurrency"]),
		}
		if o.Name == "" {
			o.Name = ldText(t["category"])
		}
		if spec, ok := t["priceSpecification"].(map[string]any); ok {
			if o.Price == "" {
				o.Price = ldText(spec["price"])
			}
			if o.Currency == "" {
				o.Currency = ldText(spec["priceCurrency"])
			}
		}
		if o.Price != "" {
			ev.Offers = append(ev.Offers, o)
		}
	}
}
