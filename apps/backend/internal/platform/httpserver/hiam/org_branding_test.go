package hiam

import (
	"encoding/json"
	"testing"
)

func TestValidateBranding_AcceptsVinoTheme(t *testing.T) {
	in := map[string]string{
		"primary":          "#822121",
		"on_primary":       "#FFFFFF",
		"on_primary_muted": "rgba(255,255,255,.72)",
		"ink":              "#2C2C2C",
		"accent":           "#D9A441",
		"bg":               "#F0E6DC",
		"font":             "Assistant",
	}
	out, bad := ValidateBranding(in)
	if bad != "" {
		t.Fatalf("rejected key %q", bad)
	}
	if out["on_primary"] != "#ffffff" {
		t.Errorf("hex not lower-cased: %q", out["on_primary"])
	}
	if out["on_primary_muted"] != "rgba(255, 255, 255, .72)" {
		t.Errorf("rgba not normalized: %q", out["on_primary_muted"])
	}
}

func TestValidateBranding_DropsEmptyValues(t *testing.T) {
	out, bad := ValidateBranding(map[string]string{"primary": "  ", "font": "Onest"})
	if bad != "" || len(out) != 1 || out["font"] != "Onest" {
		t.Fatalf("got %v, bad %q", out, bad)
	}
}

func TestValidateBranding_RefusesUnknownOrInvalid(t *testing.T) {
	cases := []map[string]string{
		{"logo": "#000000"},
		{"primary": "red"},
		{"primary": "#12345"},
		{"primary": "#1234567"},
		{"bg": "rgba(256,0,0,1)"},
		{"bg": "rgba(0,0,0,2)"},
		{"bg": "rgb(0,0,0)"},
		{"ink": "#000000; background:url(x)"},
		{"font": "Comic Sans MS"},
		{"font": "onest"},
	}
	for _, c := range cases {
		if _, bad := ValidateBranding(c); bad == "" {
			t.Errorf("accepted %v", c)
		}
	}
}

func TestOptionalBranding_TriState(t *testing.T) {
	var req updateOrgRequest
	if err := decodeUpdateOrgRequest([]byte(`{"name":"x"}`), &req); err != nil || req.Branding.Present {
		t.Fatalf("absent: %v %+v", err, req.Branding)
	}
	req = updateOrgRequest{}
	if err := decodeUpdateOrgRequest([]byte(`{"branding":null}`), &req); err != nil || !req.Branding.Present || req.Branding.Value != nil {
		t.Fatalf("null: %v %+v", err, req.Branding)
	}
	req = updateOrgRequest{}
	if err := decodeUpdateOrgRequest([]byte(`{"branding":{"primary":"#001489"}}`), &req); err != nil || req.Branding.Value["primary"] != "#001489" {
		t.Fatalf("object: %v %+v", err, req.Branding)
	}
	req = updateOrgRequest{}
	if err := decodeUpdateOrgRequest([]byte(`{"branding":{"primary":1}}`), &req); err != nil || !req.Branding.Invalid {
		t.Fatalf("non-string: %v %+v", err, req.Branding)
	}
	var b optionalBranding
	_ = json.Unmarshal([]byte(`"x"`), &b)
	if !b.Invalid {
		t.Fatal("a string is not a branding object")
	}
}
