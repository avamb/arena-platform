package contenttranslate

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{"cs-CZ": "cs", "CS": "cs", " uk ": "uk", "en_GB": "en", "he": "he"}
	for in, want := range cases {
		got, ok := NormalizeLocale(in)
		if !ok || got != want {
			t.Errorf("NormalizeLocale(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "zz", "ja-JP", "english", "-"} {
		if got, ok := NormalizeLocale(in); ok {
			t.Errorf("NormalizeLocale(%q) = %q, true; want false", in, got)
		}
	}
}

func TestSourceHash_StableAndNilEqualsEmpty(t *testing.T) {
	a := Fields{Name: sp("N"), ShortDescription: sp("S"), DescriptionHTML: sp("<p>D</p>")}
	b := Fields{Name: sp("N"), ShortDescription: sp("S"), DescriptionHTML: sp("<p>D</p>"), ReportTextHTML: sp("")}
	if SourceHash(a) != SourceHash(b) {
		t.Fatal("nil and empty ReportTextHTML must hash the same")
	}
	if first, again := SourceHash(a), SourceHash(a); first != again {
		t.Fatal("hash is not stable")
	}
	if len(SourceHash(a)) != 64 {
		t.Fatalf("hash %q is not hex sha256", SourceHash(a))
	}
	c := a
	c.ShortDescription = sp("S2")
	if SourceHash(a) == SourceHash(c) {
		t.Fatal("different fields must hash differently")
	}
	// Moving a value to another field changes the hash.
	if SourceHash(Fields{Name: sp("x")}) == SourceHash(Fields{ShortDescription: sp("x")}) {
		t.Fatal("field position must be part of the hash")
	}
}

func TestSanitizeFields_StripsScriptAndTrims(t *testing.T) {
	f, err := SanitizeFields(Fields{
		Name:             sp("  Title  "),
		ShortDescription: sp("   "),
		DescriptionHTML:  sp(`<p>Hello <script>alert(1)</script><b>world</b></p>`),
		ReportTextHTML:   sp(`<script>alert(2)</script><p onclick="x()">Report</p>`),
	})
	if err != nil {
		t.Fatalf("SanitizeFields: %v", err)
	}
	if f.Name == nil || *f.Name != "Title" {
		t.Errorf("name = %v; want Title", f.Name)
	}
	if f.ShortDescription != nil {
		t.Errorf("blank short description must become nil, got %q", *f.ShortDescription)
	}
	for name, p := range map[string]*string{"description": f.DescriptionHTML, "report": f.ReportTextHTML} {
		if p == nil {
			t.Fatalf("%s became nil", name)
		}
		low := strings.ToLower(*p)
		if strings.Contains(low, "<script") || strings.Contains(low, "onclick") {
			t.Errorf("%s kept unsafe markup: %q", name, *p)
		}
	}
	if !strings.Contains(*f.DescriptionHTML, "world") {
		t.Errorf("description lost its text: %q", *f.DescriptionHTML)
	}
}

func TestSanitizeFields_Limits(t *testing.T) {
	_, err := SanitizeFields(Fields{Name: sp(strings.Repeat("я", MaxNameRunes+1))})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Reason != ReasonNameTooLong {
		t.Fatalf("long name: err = %v; want %s", err, ReasonNameTooLong)
	}
	if _, err := SanitizeFields(Fields{Name: sp(strings.Repeat("я", MaxNameRunes))}); err != nil {
		t.Fatalf("name at the limit refused: %v", err)
	}
	_, err = SanitizeFields(Fields{ShortDescription: sp(strings.Repeat("a", MaxShortDescriptionRunes+1))})
	if !errors.As(err, &fe) || fe.Reason != ReasonShortDescriptionTooLong {
		t.Fatalf("long short description: err = %v; want %s", err, ReasonShortDescriptionTooLong)
	}
}

func TestEventOriginal_FieldsRendersTextFormat(t *testing.T) {
	o := EventOriginal{Name: "Show", Description: sp("Line one\n\nLine two"), DescriptionFormat: "text"}
	f := o.Fields()
	if f.Name == nil || *f.Name != "Show" {
		t.Fatalf("name = %v", f.Name)
	}
	if f.DescriptionHTML == nil || !strings.Contains(*f.DescriptionHTML, "<p>") {
		t.Fatalf("text description not rendered to HTML: %v", f.DescriptionHTML)
	}
	if o.Hash() != SourceHash(f) {
		t.Fatal("Hash must be SourceHash of Fields")
	}
	if !Stale(Translation{SourceHash: "old"}, o.Hash()) || Stale(Translation{SourceHash: o.Hash()}, o.Hash()) {
		t.Fatal("Stale is wrong")
	}
}

// TestSupported_MatchesMigrationCheck keeps Supported and the CHECK of
// content_translations.locale (migration 0220) in step.
func TestSupported_MatchesMigrationCheck(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "sql", "0220_content_translations.sql"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`locale\s+text\s+NOT NULL CHECK \(locale IN \(([^)]*)\)\)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("locale CHECK not found in 0220")
	}
	var got []string
	for _, part := range strings.Split(string(m[1]), ",") {
		got = append(got, strings.Trim(strings.TrimSpace(part), "'"))
	}
	if !slices.Equal(got, Supported) {
		t.Fatalf("migration CHECK %v != Supported %v", got, Supported)
	}
}
