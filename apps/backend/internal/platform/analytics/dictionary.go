// Package analytics is the platform's own product analytics (spec
// 08_architecture/50_product_analytics_ru.md): the event dictionary, the
// buffered batch writer into the `analytics` schema and the monthly partition
// maintenance job.
//
// The package is deliberately blind to people. The dictionary below decides
// WHAT may be stored: an event whose name is not listed is dropped whole, a
// property that is not listed for it is dropped silently, strings are cut to
// MaxStringLen characters and stripped of control characters. No allowed
// property carries an e-mail, a phone, a name, a message text, a field value
// or the id of an entity (spec 50 section 11); the dictionary test enforces it.
package analytics

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// Surfaces an event may come from.
const (
	SurfaceSite        = "site"
	SurfaceEventCenter = "event_center"
	SurfaceBot         = "bot"
)

// Identifier kinds of analytics.events.id_kind.
const (
	IDKindAID  = "aid"  // visitor id of arenasoldout.com, only after consent
	IDKindCID  = "cid"  // contact code of a link in one of our e-mails
	IDKindUser = "user" // platform user id (event center, linked bot user)
	IDKindTG   = "tg"   // keyed hash of a Telegram id (a stranger in the bot)
	IDKindAnon = "anon" // page-view id of a visitor without consent
)

// MaxStringLen is the longest stored string property, in characters (spec 50
// section 4.2).
const MaxStringLen = 200

// PropKind is the JSON type a property must have.
type PropKind int

const (
	// PropString is a string, cut to MaxLen (or MaxStringLen) characters.
	PropString PropKind = iota
	// PropInt is a whole number between Min and Max.
	PropInt
	// PropBool is true or false.
	PropBool
)

// PropDef describes one allowed property of an event.
type PropDef struct {
	Kind PropKind
	// MaxLen cuts a string property; zero means MaxStringLen.
	MaxLen int
	// Min and Max bound an integer property; Max zero means 86400.
	Min, Max int
	// OneOfStrings, when set, keeps only these string values.
	OneOfStrings []string
	// OneOfInts, when set, keeps only these integer values.
	OneOfInts []int
	// Path makes a string property a page path: everything from the first
	// "?" or "#" is cut off, because a query string may carry an address.
	Path bool
	// Pattern, when set, keeps only values that match it entirely (checked
	// after cleaning and cutting).
	Pattern *regexp.Regexp
	// Host makes a string property a host name: a full URL is reduced to its
	// host.
	Host bool
}

// EventDef describes one event of the dictionary.
type EventDef struct {
	// Surfaces lists where the event may come from; never empty.
	Surfaces []string
	// ServerOnly marks an event only the server writes (email_click): a
	// client batch naming it is dropped.
	ServerOnly bool
	// Props are the allowed properties by name.
	Props map[string]PropDef
}

func str(maxLen int) PropDef { return PropDef{Kind: PropString, MaxLen: maxLen} }

var (
	pathProp = PropDef{Kind: PropString, MaxLen: MaxStringLen, Path: true}
	hostProp = PropDef{Kind: PropString, MaxLen: 100, Host: true}
	utmProp  = str(100)
	// identProp is an identifier-like value (a screen template, an error code,
	// a flow or step name): letters, digits and _ . / : $ - only, 1-64
	// characters. Free text there would be a place to leak what a person typed.
	identPattern = regexp.MustCompile(`^[A-Za-z0-9_./:$-]{1,64}$`)
	identProp    = PropDef{Kind: PropString, MaxLen: 64, Pattern: identPattern}
	// labelProp is the data-track label of a clicked element: a constant
	// chosen by our developers, lower-case [a-z0-9_.-], at most 64 characters.
	labelProp = PropDef{Kind: PropString, MaxLen: 64, Pattern: regexp.MustCompile(`^[a-z0-9_.-]+$`)}
)

func site() []string        { return []string{SurfaceSite} }
func eventCenter() []string { return []string{SurfaceEventCenter} }
func bot() []string         { return []string{SurfaceBot} }

// Events is the dictionary v1 (spec 50 section 5). Adding an event is a row
// here plus a line in the completeness test.
//
// `name` is allowed on click and rage_click only: there it is the value of
// the element's data-track attribute, a constant written by our developers
// ("create_event"), never something a person typed. The privacy test lists
// that exception explicitly.
var Events = map[string]EventDef{
	"page_view": {Surfaces: site(), Props: map[string]PropDef{
		"path": pathProp, "title": str(MaxStringLen), "lang": str(16), "ref_host": hostProp,
		"utm_source": utmProp, "utm_medium": utmProp, "utm_campaign": utmProp,
		"utm_content": utmProp, "utm_term": utmProp,
	}},
	"engaged": {Surfaces: []string{SurfaceSite, SurfaceEventCenter}, Props: map[string]PropDef{
		"path": pathProp, "screen": identProp, "seconds": {Kind: PropInt, Max: 86400},
	}},
	"scroll": {Surfaces: site(), Props: map[string]PropDef{
		"path": pathProp, "depth": {Kind: PropInt, OneOfInts: []int{25, 50, 75, 100}},
	}},
	"click": {Surfaces: []string{SurfaceSite, SurfaceEventCenter}, Props: map[string]PropDef{
		"name": labelProp, "path": pathProp, "screen": identProp,
	}},
	"email_click": {Surfaces: site(), ServerOnly: true, Props: map[string]PropDef{
		// aid is set by the server when the same visit carried both codes: it
		// is the pair the rollup glues (spec 50 section 3).
		"path": pathProp, "utm_campaign": utmProp, "aid": str(40),
	}},
	"consent": {Surfaces: site(), Props: map[string]PropDef{
		"analytics": {Kind: PropBool},
	}},
	"screen_view": {Surfaces: eventCenter(), Props: map[string]PropDef{
		"screen": identProp,
	}},
	"flow_step": {Surfaces: eventCenter(), Props: map[string]PropDef{
		"flow": identProp, "step": identProp,
	}},
	"ui_error": {Surfaces: eventCenter(), Props: map[string]PropDef{
		"screen": identProp, "code": identProp,
	}},
	"rage_click": {Surfaces: eventCenter(), Props: map[string]PropDef{
		"screen": identProp, "name": labelProp,
	}},
	"bot_update": {Surfaces: bot(), Props: map[string]PropDef{
		"kind": identProp, "prefix": identProp, "step": identProp,
	}},
	"wizard_step": {Surfaces: bot(), Props: map[string]PropDef{
		"from": identProp, "to": identProp, "seconds": {Kind: PropInt, Max: 86400},
		"mode": {Kind: PropString, MaxLen: 16, OneOfStrings: []string{"create", "edit"}},
	}},
	"wizard_input_error": {Surfaces: bot(), Props: map[string]PropDef{
		"step": identProp, "reason": identProp,
	}},
	"wizard_exit": {Surfaces: bot(), Props: map[string]PropDef{
		"step": identProp,
		"how":  {Kind: PropString, MaxLen: 16, OneOfStrings: []string{"cancel", "keep", "drop", "done", "expired"}},
	}},
}

// EventNames returns the dictionary's event names, sorted.
func EventNames() []string {
	out := make([]string, 0, len(Events))
	for n := range Events {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// allowedOn reports whether the event may come from surface.
func (d EventDef) allowedOn(surface string) bool {
	for _, s := range d.Surfaces {
		if s == surface {
			return true
		}
	}
	return false
}

// Sanitize reduces raw (a decoded JSON object) to the properties the
// dictionary allows for event name on surface. It returns ok=false when the
// event is unknown or not allowed on that surface; fromClient makes a
// server-only event unknown as well. The returned map is new: raw is never
// modified.
func Sanitize(surface, name string, raw map[string]any, fromClient bool) (map[string]any, bool) {
	def, ok := Events[name]
	if !ok || !def.allowedOn(surface) || (fromClient && def.ServerOnly) {
		return nil, false
	}
	out := make(map[string]any, len(def.Props))
	for key, pd := range def.Props {
		v, present := raw[key]
		if !present {
			continue
		}
		if cleaned, ok := pd.clean(v); ok {
			out[key] = cleaned
		}
	}
	return out, true
}

func (pd PropDef) clean(v any) (any, bool) {
	switch pd.Kind {
	case PropString:
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		s = cleanString(s, pd)
		if s == "" {
			return nil, false
		}
		if pd.Pattern != nil && !pd.Pattern.MatchString(s) {
			return nil, false
		}
		if len(pd.OneOfStrings) > 0 && !containsString(pd.OneOfStrings, s) {
			return nil, false
		}
		return s, true
	case PropInt:
		n, ok := wholeNumber(v)
		if !ok {
			return nil, false
		}
		max := pd.Max
		if max == 0 {
			max = 86400
		}
		if n < pd.Min || n > max {
			return nil, false
		}
		if len(pd.OneOfInts) > 0 {
			found := false
			for _, a := range pd.OneOfInts {
				if a == n {
					found = true
				}
			}
			if !found {
				return nil, false
			}
		}
		return n, true
	case PropBool:
		b, ok := v.(bool)
		return b, ok
	}
	return nil, false
}

// cleanString trims, drops control characters and invalid UTF-8 (a NUL byte
// would make the whole jsonb COPY fail), applies the path / host rules and
// cuts to the property's length in characters.
func cleanString(s string, pd PropDef) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if pd.Path {
		if i := strings.IndexAny(s, "?#"); i >= 0 {
			s = s[:i]
		}
	}
	if pd.Host {
		s = hostOf(s)
	}
	s = scrubEmails(s)
	max := pd.MaxLen
	if max == 0 {
		max = MaxStringLen
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// hostOf reduces "https://user@host:8080/path?q" to "host:8080"-less "host".
func hostOf(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

func containsString(list []string, s string) bool {
	for _, a := range list {
		if a == s {
			return true
		}
	}
	return false
}

// scrubEmails replaces anything shaped like an e-mail address with
// "[redacted]", also when it is percent-encoded ("a%40b.com"). A free-text
// property (a page title, a utm value, a path) can carry an address the
// visitor's page put there; the dictionary promises none is stored.
func scrubEmails(s string) string {
	if u, err := url.PathUnescape(s); err == nil && u != s {
		if scrubbed := opsalert.ScrubEmails(u); scrubbed != u {
			return scrubbed
		}
	}
	return opsalert.ScrubEmails(s)
}

// wholeNumber reads an integer property from any numeric Go type: float64 from
// a decoded JSON body, int / int32 / int64 and friends from in-process callers
// (the bot). A float must be a whole number.
func wholeNumber(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		if n > math.MaxInt32 || n < math.MinInt32 {
			return 0, false
		}
		return int(n), true
	case uint:
		return clampUint(uint64(n))
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return clampUint(uint64(n))
	case uint64:
		return clampUint(n)
	case float32:
		return wholeFloat(float64(n))
	case float64:
		return wholeFloat(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return wholeFloat(f)
	}
	return 0, false
}

func clampUint(n uint64) (int, bool) {
	if n > math.MaxInt32 {
		return 0, false
	}
	return int(n), true
}

func wholeFloat(f float64) (int, bool) {
	if f != math.Trunc(f) || f > math.MaxInt32 || f < math.MinInt32 {
		return 0, false
	}
	return int(f), true
}
