package templates

import (
	"regexp"
	"testing"
)

func TestRendererVersion_NamesTheResolvedFileAndItsHash(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	v := r.Version(TemplateKindTicket, "ru")
	if !regexp.MustCompile(`^ticket\.ru@[0-9a-f]{12}$`).MatchString(v) {
		t.Fatalf("version = %q", v)
	}
	if fallback := r.Version(TemplateKindTicket, "xx"); fallback[:len("ticket.en@")] != "ticket.en@" {
		t.Fatalf("an unknown locale must name the file it falls back to, got %q", fallback)
	}
	if r.Version("nope", "en") != "" {
		t.Fatal("an unknown kind has no version")
	}
	if r.Version(TemplateKindTicket, "ru") == r.Version(TemplateKindTicket, "en") {
		t.Fatal("two different files must not share a version")
	}
}
