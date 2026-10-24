package salelinks

import (
	"net/url"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
)

// utmKeys are the only query parameters a link may add besides ref.
var utmKeys = []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"}

// BuildDestination adds the link utm labels and ref=<code> to destination. The
// query string is edited as text, not through url.Values: the pairs the
// destination already carries stay byte for byte and in order (a value with
// %20, a ";" inside a pair, repeated keys). Only a pair whose decoded key is
// one this call supplies (a non-empty utm_* from utm, or ref when code is set)
// is replaced; ours are appended at the end, the utm keys in a fixed order and
// then ref, escaped with url.QueryEscape. Only the five utm_* keys are taken
// from utm, and the fragment is kept. A destination that cannot be parsed is
// returned unchanged.
func BuildDestination(destination string, utm map[string]string, code string) string {
	if destination == "" {
		return ""
	}
	u, err := url.Parse(destination)
	if err != nil {
		return destination
	}
	type pair struct{ key, value string }
	var ours []pair
	for _, k := range utmKeys {
		if v := strings.TrimSpace(utm[k]); v != "" {
			ours = append(ours, pair{k, v})
		}
	}
	if code != "" {
		ours = append(ours, pair{"ref", code})
	}
	replaced := make(map[string]bool, len(ours))
	for _, p := range ours {
		replaced[p.key] = true
	}
	var parts []string
	if u.RawQuery != "" {
		for _, raw := range strings.Split(u.RawQuery, "&") {
			if raw == "" {
				continue
			}
			key := raw
			if i := strings.IndexByte(raw, '='); i >= 0 {
				key = raw[:i]
			}
			if dk, err := url.QueryUnescape(key); err == nil {
				key = dk
			}
			if replaced[key] {
				continue
			}
			parts = append(parts, raw)
		}
	}
	for _, p := range ours {
		parts = append(parts, p.key+"="+url.QueryEscape(p.value))
	}
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}

// DefaultUTM returns the labels a new link starts with (spec 43 section 2,
// decision 3): utm_source=qr, utm_medium=offline, utm_campaign the campaign
// slug and utm_content the code. The placements social, email and messenger
// replace the source and medium. The slug goes through pagestats.CleanUTM, so a slug with a character\noutside [A-Za-z0-9._~-] leaves utm_campaign out. An empty code leaves\nutm_content out.
func DefaultUTM(placement, campaignSlug, code string) map[string]string {
	source, medium := "qr", "offline"
	switch strings.ToLower(strings.TrimSpace(placement)) {
	case "social":
		source, medium = "social", "social"
	case "email":
		source, medium = "email", "email"
	case "messenger":
		source, medium = "messenger", "social"
	}
	out := map[string]string{"utm_source": source, "utm_medium": medium}
	if s := pagestats.CleanUTM(campaignSlug); s != "" {
		out["utm_campaign"] = s
	}
	if code != "" {
		out["utm_content"] = code
	}
	return out
}

// ShortURL is the address printed under a QR code: <base>/<code>. It is the ONE
// place that formats a short link; nothing else may build one.
func ShortURL(base, code string) string {
	return strings.TrimRight(base, "/") + "/" + code
}
