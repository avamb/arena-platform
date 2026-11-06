package hiam

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Organization theme (migration 0204, spec 62 §12.1, decision R-24): a flat
// object of design tokens stored in organizations.branding. The event center
// writes them into :root; the ticket PDF and letters take their accent colour
// from "primary". The key list and the font list are closed on purpose: the
// values end up in CSS and in a PDF, so nothing free-form may pass.

// BrandingColorKeys are the colour tokens an organization may set.
var BrandingColorKeys = []string{"primary", "on_primary", "on_primary_muted", "ink", "accent", "bg"}

// BrandingFonts is the font allow-list of the "font" token.
var BrandingFonts = []string{"Assistant", "Onest", "Golos Text"}

var (
	brandingHexColor  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	brandingRGBAColor = regexp.MustCompile(`^rgba\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(0|1|0?\.\d{1,3}|1\.0{1,3})\s*\)$`)
)

// validBrandingColor accepts "#rrggbb" or "rgba(r, g, b, a)" with r, g, b in
// 0-255 and a in 0-1.
func validBrandingColor(v string) bool {
	if brandingHexColor.MatchString(v) {
		return true
	}
	m := brandingRGBAColor.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	for _, part := range m[1:4] {
		n := 0
		for _, c := range part {
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// ValidateBranding checks a branding object and answers it normalized
// (trimmed values, hex colours lower-cased; empty values dropped). The
// returned field names the first offending key, "" when the object is valid.
func ValidateBranding(in map[string]string) (map[string]string, string) {
	out := make(map[string]string, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(in[k])
		if v == "" {
			continue
		}
		switch {
		case k == "font":
			ok := false
			for _, f := range BrandingFonts {
				if v == f {
					ok = true
					break
				}
			}
			if !ok {
				return nil, k
			}
		case isBrandingColorKey(k):
			if !validBrandingColor(v) {
				return nil, k
			}
			if strings.HasPrefix(v, "#") {
				v = strings.ToLower(v)
			} else {
				v = strings.Join(strings.Fields(v), "")
				v = strings.ReplaceAll(v, ",", ", ")
			}
		default:
			return nil, k
		}
		out[k] = v
	}
	return out, ""
}

func isBrandingColorKey(k string) bool {
	for _, c := range BrandingColorKeys {
		if k == c {
			return true
		}
	}
	return false
}

// optionalBranding is the tri-state PATCH member: absent keeps, null clears
// (back to the platform palette), an object replaces the whole theme.
// Invalid is set when the member is present but is neither null nor an
// object of strings.
type optionalBranding struct {
	Present bool
	Value   map[string]string
	Invalid bool
}

func (v *optionalBranding) UnmarshalJSON(data []byte) error {
	v.Present = true
	if string(data) == "null" {
		v.Value = nil
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		v.Invalid = true
		return nil
	}
	v.Value = m
	return nil
}

// orgBranding reads organizations.branding; a failed read answers an empty
// theme (the platform palette) and is logged.
func (h *Handler) orgBranding(ctx context.Context, orgID uuid.UUID) map[string]string {
	b, err := h.orgQueries.GetOrganizationBranding(ctx, orgID)
	if err != nil || b == nil {
		if err != nil {
			h.logger.Warn("org: branding read failed", "org_id", orgID.String(), "error", err.Error())
		}
		return map[string]string{}
	}
	return b
}
