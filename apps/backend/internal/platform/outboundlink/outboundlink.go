// Package outboundlink labels the link to a partner's box office (spec 63
// section 19.5). The platform owns the rule, the site hard-codes nothing:
//
//	settings.site.outbound_params = {
//	  "utm_source": "lampyrisevents",       // required, else no label at all
//	  "utm_medium": "referral",             // default "referral"
//	  "sellers": {                          // optional, keyed by the box office host
//	    "bilesuparadize.lv": {"utm": false},               // takes no label: the link stays as it is
//	    "pankoncert.pl":     {"sub_id_param": "subid"}      // the click id goes here
//	  }
//	}
//
// Build adds utm_source, utm_medium, utm_campaign (the event slug) and
// utm_content (the city) to an https link, keeps every parameter the link
// already has (an utm_* value already in the link is never overwritten) and,
// when the box office names a sub-id parameter and a click id is known, adds
// the click id there so a partner's sales report can be tied to a click.
// A link that is not https, or has no host, is returned unchanged.
package outboundlink

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxValueRunes = 100
	maxSellers    = 50
	maxParamName  = 40
)

// Seller is the rule of one box office.
type Seller struct {
	// UTM false: the box office takes no utm label, its link is left alone.
	UTM *bool `json:"utm,omitempty"`
	// SubIDParam names the query parameter that carries the click id.
	SubIDParam string `json:"sub_id_param,omitempty"`
}

// Rules is settings.site.outbound_params.
type Rules struct {
	UTMSource string            `json:"utm_source"`
	UTMMedium string            `json:"utm_medium,omitempty"`
	Sellers   map[string]Seller `json:"sellers,omitempty"`
}

// Parse reads the settings value. An empty or null value is no rule (nil).
// A value that is not an object of this shape is an error, so a bad write is
// refused instead of silently labelling nothing.
func Parse(raw []byte) (*Rules, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, nil
	}
	var r Rules
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("settings.site.outbound_params must be an object: %w", err)
	}
	if !validValue(r.UTMSource) {
		return nil, fmt.Errorf("settings.site.outbound_params.utm_source is required, up to %d characters", maxValueRunes)
	}
	if r.UTMMedium != "" && !validValue(r.UTMMedium) {
		return nil, fmt.Errorf("settings.site.outbound_params.utm_medium is up to %d characters", maxValueRunes)
	}
	if len(r.Sellers) > maxSellers {
		return nil, fmt.Errorf("settings.site.outbound_params.sellers lists at most %d box offices", maxSellers)
	}
	for host, sel := range r.Sellers {
		if host == "" || host != normHost(host) {
			return nil, fmt.Errorf("settings.site.outbound_params.sellers key %q must be a lower-case host without www", host)
		}
		if sel.SubIDParam != "" && !validParamName(sel.SubIDParam) {
			return nil, fmt.Errorf("settings.site.outbound_params.sellers.%s.sub_id_param is letters, digits, _ and -", host)
		}
	}
	return &r, nil
}

// Context is what a label is made of.
type Context struct {
	EventSlug string // utm_campaign
	City      string // utm_content
	ClickID   string // the browser's event id of the click; empty when unknown
}

// Build returns link with the labels of rules. The second value says whether
// anything was added.
func Build(rules *Rules, link string, c Context) (string, bool) {
	if rules == nil {
		return link, false
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return link, false
	}
	sel := rules.Sellers[normHost(u.Hostname())]
	q := u.Query()
	changed := false
	set := func(k, v string) {
		if v == "" || q.Get(k) != "" {
			return
		}
		q.Set(k, v)
		changed = true
	}
	if sel.UTM == nil || *sel.UTM {
		medium := rules.UTMMedium
		if medium == "" {
			medium = "referral"
		}
		set("utm_source", rules.UTMSource)
		set("utm_medium", medium)
		set("utm_campaign", slug(c.EventSlug))
		set("utm_content", slug(c.City))
	}
	if sel.SubIDParam != "" && validValue(c.ClickID) {
		set(sel.SubIDParam, c.ClickID)
	}
	if !changed {
		return link, false
	}
	u.RawQuery = q.Encode()
	return u.String(), true
}

func normHost(h string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "www.")
}

// slug lower-cases s and turns every run of blanks and punctuation into one
// "-", keeping letters and digits of any script.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	out := b.String()
	if utf8.RuneCountInString(out) > maxValueRunes {
		out = string([]rune(out)[:maxValueRunes])
	}
	return out
}

func validValue(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > maxValueRunes {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func validParamName(s string) bool {
	if s == "" || len(s) > maxParamName {
		return false
	}
	for _, r := range s {
		ok := r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}
