package salelinks

import (
	"net/url"
	"strings"
)

// utmKeys are the only query parameters a link may add besides ref.
var utmKeys = []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"}

// BuildDestination adds the link utm labels and ref=<code> to destination. The
// parameters the destination already carries are kept; ours win on the same
// name. Only the five utm_* keys are taken from utm, empty values are skipped,
// and the fragment is kept. A destination that cannot be parsed is returned
// unchanged.
func BuildDestination(destination string, utm map[string]string, code string) string {
	if destination == "" {
		return ""
	}
	u, err := url.Parse(destination)
	if err != nil {
		return destination
	}
	q := u.Query()
	for _, k := range utmKeys {
		if v := strings.TrimSpace(utm[k]); v != "" {
			q.Set(k, v)
		}
	}
	if code != "" {
		q.Set("ref", code)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// DefaultUTM returns the labels a new link starts with (spec 43 section 2,
// decision 3): utm_source=qr, utm_medium=offline, utm_campaign the campaign
// slug and utm_content the code. The placements social, email and messenger
// replace the source and medium. An empty slug or code leaves that key out.
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
	if s := strings.TrimSpace(campaignSlug); s != "" {
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
