package onboarding

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"+34 600 111 222":    "+34600111222",
		"+420 (777) 111-222": "+420777111222",
		"600111222":          "",
		"+0123456789":        "",
		"+34abc":             "",
		"":                   "",
	}
	for in, want := range cases {
		if got := NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeValue(t *testing.T) {
	get := func(key string) Field { f, _ := LookupField(key); return f }
	if v, r := NormalizeValue(get("country"), " es "); v != "ES" || r != "" {
		t.Errorf("country = %v %q", v, r)
	}
	if _, r := NormalizeValue(get("country"), "Spain"); r != "invalid" {
		t.Errorf("country Spain reason = %q", r)
	}
	if v, r := NormalizeValue(get("website"), "example.org/shop"); v != "https://example.org/shop" || r != "" {
		t.Errorf("website = %v %q", v, r)
	}
	if _, r := NormalizeValue(get("website"), "javascript:alert(1)"); r != "invalid" {
		t.Errorf("website javascript reason = %q", r)
	}
	if v, r := NormalizeValue(get("telegram_username"), "@ana_events"); v != "ana_events" || r != "" {
		t.Errorf("telegram = %v %q", v, r)
	}
	if _, r := NormalizeValue(get("seating"), "standing"); r != "not_allowed" {
		t.Errorf("seating reason = %q", r)
	}
	if v, r := NormalizeValue(get("event_types"), []any{"concert", "concert", "tour"}); r != "" || len(v.([]string)) != 2 {
		t.Errorf("event_types = %v %q", v, r)
	}
	if _, r := NormalizeValue(get("event_types"), []any{"opera"}); r != "not_allowed" {
		t.Errorf("event_types reason = %q", r)
	}
	if _, r := NormalizeValue(get("accept_terms"), "yes"); r != "invalid" {
		t.Errorf("a string is not a bool: %q", r)
	}
	if _, r := NormalizeValue(get("notes"), string(make([]rune, 2001))); r == "" {
		t.Error("a 2001-rune note was accepted")
	}
	if v, r := NormalizeValue(get("org_name"), "   "); v != nil || r != "" {
		t.Errorf("blank = %v %q, want a clear", v, r)
	}
	if v, r := NormalizeValue(get("first_event_date"), "2027-02-30"); v != nil || r != "invalid" {
		t.Errorf("impossible date = %v %q", v, r)
	}
}

func TestValidatePatch_RefusesWholePatchOnOneBadKey(t *testing.T) {
	out, errs := ValidatePatch(map[string]any{"org_name": "Ok", "country": "nope", "email": "a@b.co", "zzz": 1}, nil)
	if out != nil {
		t.Fatal("a patch with errors must apply nothing")
	}
	if errs["country"] != "invalid" || errs["email"] != "read_only" || errs["zzz"] != "unknown_field" {
		t.Fatalf("errors = %v", errs)
	}
}

func TestMissingAndProgress(t *testing.T) {
	a := Answers{"first_name": "A", "last_name": "B", "email": "a@b.co", "phone": "+34600111222"}
	pct, step := Progress(a)
	if step != StepOrganization || pct <= 0 || pct >= 100 {
		t.Fatalf("progress = %d %s", pct, step)
	}
	a["country"] = "ES"
	// address_country defaults from country, so it is not missing.
	for _, k := range Missing(a) {
		if k == "address_country" {
			t.Fatal("address_country should default from country")
		}
	}
	for _, k := range []string{"accept_terms", "accept_privacy", "confirm_authority"} {
		a[k] = false
	}
	found := false
	for _, k := range Missing(a) {
		if k == "accept_terms" {
			found = true
		}
	}
	if !found {
		t.Fatal("a false consent must count as missing")
	}
}

func TestEveryLocaleLabelsEveryFieldAndOption(t *testing.T) {
	for _, loc := range SupportedLocales {
		for _, s := range Steps {
			if _, ok := labels[loc]["s."+s]; !ok {
				t.Errorf("%s: no title for step %s", loc, s)
			}
		}
		for _, f := range Fields {
			if _, ok := labels[loc]["f."+f.Key]; !ok {
				t.Errorf("%s: no label for field %s", loc, f.Key)
			}
			for _, o := range f.Options {
				if _, ok := labels[loc]["o."+f.Key+"."+o]; !ok {
					t.Errorf("%s: no label for option %s.%s", loc, f.Key, o)
				}
			}
		}
	}
}

func TestBuildSchema_FallsBackToEnglish(t *testing.T) {
	s := BuildSchema("fr-FR", nil, "1", "1")
	if s.Locale != "en" || len(s.Steps) != len(Steps) || s.AcceptedCountries == nil {
		t.Fatalf("schema = %+v", s)
	}
}

func TestTaxIDFormat(t *testing.T) {
	if r, _ := taxIDFormat("vat", "ES B-1234567"); r != CheckPass {
		t.Errorf("good VAT: %s", r)
	}
	if r, _ := taxIDFormat("vat", "12345"); r != CheckWarn {
		t.Errorf("bad VAT: %s", r)
	}
	if r, _ := taxIDFormat("ein", "12-3456789"); r != CheckPass {
		t.Errorf("good EIN: %s", r)
	}
	if r, _ := taxIDFormat("ico", "123"); r != CheckWarn {
		t.Errorf("short ICO: %s", r)
	}
}
