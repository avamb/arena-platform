package legaldocs

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *Template {
	t.Helper()
	tpl, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return tpl
}

func TestParse_RefusesUnknownPlaceholders(t *testing.T) {
	cases := map[string]string{
		"unknown key":          "Hello {{seller.secret}}",
		"unknown if key":       "{{#if seller.secret}}x{{/if}}",
		"flag printed":         "{{seller.is_platform}}",
		"unclosed tag":         "Hello {{seller.legal_name",
		"unclosed if":          "{{#if seller.phone}}x",
		"stray end":            "x{{/if}}",
		"stray else":           "x{{else}}y",
		"double else":          "{{#if seller.phone}}a{{else}}b{{else}}c{{/if}}",
		"code is not a key":    "{{ printf \"%s\" 1 }}",
		"partial-like":         "{{> header}}",
		"go template range":    "{{range .}}x{{end}}",
		"empty":                "   \n  ",
		"empty placeholder":    "a {{}} b",
		"if without key":       "{{#if}}a{{/if}}",
		"uppercase key":        "{{SELLER.LEGAL_NAME}}",
		"too deep for its own": strings.Repeat("{{#if seller.phone}}", maxNesting+1) + strings.Repeat("{{/if}}", maxNesting+1),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(src)
			var pe *ParseError
			if !errors.As(err, &pe) || len(pe.Problems) == 0 {
				t.Fatalf("Parse(%q) = %v, want a ParseError", src, err)
			}
		})
	}
}

func TestParse_ReportsEveryProblemWithItsLine(t *testing.T) {
	_, err := Parse("ok {{seller.legal_name}}\n{{bad.one}}\n\n{{bad.two}}")
	var pe *ParseError
	if !errors.As(err, &pe) || len(pe.Problems) != 2 {
		t.Fatalf("problems = %v", err)
	}
	if !strings.HasPrefix(pe.Problems[0], "line 2:") || !strings.HasPrefix(pe.Problems[1], "line 4:") {
		t.Fatalf("lines = %q", pe.Problems)
	}
}

func TestRender_EscapesTemplateTextAndValues(t *testing.T) {
	tpl := mustParse(t, "<script>alert(1)</script> {{seller.legal_name}}")
	out := Render(tpl, Values{"seller.legal_name": `"><img src=x onerror=alert(1)> & Co`}, "en")
	if strings.Contains(out, "<script") || strings.Contains(out, "<img") {
		t.Fatalf("unescaped markup: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "&amp; Co") {
		t.Fatalf("escaping lost text: %s", out)
	}
}

func TestRender_ValueCannotStartMarkup(t *testing.T) {
	tpl := mustParse(t, "{{seller.legal_name}}\n\n{{promoter.name}}")
	out := Render(tpl, Values{"seller.legal_name": "## Not a heading", "promoter.name": "- not\n\n## an item"}, "en")
	if strings.Contains(out, "<h2>") || strings.Contains(out, "<li>") {
		t.Fatalf("a value made markup: %s", out)
	}
	if !strings.Contains(out, "<p>- not ## an item</p>") {
		t.Fatalf("newlines of a value are not folded: %s", out)
	}
}

func TestRender_Markup(t *testing.T) {
	src := "## Title\n\nFirst line\nsecond line.\n\n### Sub\n- one\n- {{seller.phone}}\nAfter list.\n"
	out := Render(mustParse(t, src), Values{"seller.phone": "+34 600"}, "en")
	want := "<article class=\"legal-document\" lang=\"en\" dir=\"ltr\">\n" +
		"<h2>Title</h2>\n" +
		"<p>First line second line.</p>\n" +
		"<h3>Sub</h3>\n" +
		"<ul>\n<li>one</li>\n<li>+34 600</li>\n</ul>\n" +
		"<p>After list.</p>\n" +
		"</article>\n"
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

func TestRender_ConditionalLinesLeaveNoGap(t *testing.T) {
	src := "The seller is {{seller.legal_name}}.\n{{#if seller.phone}}\nPhone: {{seller.phone}}.\n{{/if}}\nWrite to us.\n\n{{#if promoter.name}}\n## Presented by\n\n{{promoter.name}}\n{{else}}\nNo promoter.\n{{/if}}\n"
	without := Render(mustParse(t, src), Values{"seller.legal_name": "X S.L."}, "en")
	if !strings.Contains(without, "<p>The seller is X S.L.. Write to us.</p>") || strings.Contains(without, "Phone") {
		t.Fatalf("false branch: %s", without)
	}
	if !strings.Contains(without, "<p>No promoter.</p>") || strings.Contains(without, "Presented") {
		t.Fatalf("else branch: %s", without)
	}
	with := Render(mustParse(t, src), Values{"seller.legal_name": "X S.L.", "seller.phone": "+34 600", "promoter.name": "Teatro"}, "en")
	if !strings.Contains(with, "<p>The seller is X S.L.. Phone: +34 600. Write to us.</p>") ||
		!strings.Contains(with, "<h2>Presented by</h2>\n<p>Teatro</p>") || strings.Contains(with, "No promoter") {
		t.Fatalf("true branch: %s", with)
	}
}

func TestRender_HebrewIsRightToLeft(t *testing.T) {
	out := Render(mustParse(t, "x"), nil, "he")
	if !strings.HasPrefix(out, `<article class="legal-document" lang="he" dir="rtl">`) {
		t.Fatalf("he: %s", out)
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	tpl := mustParse(t, "## T\n{{seller.legal_name}} {{#if refund.no_refund}}none{{/if}}")
	v := Values{"seller.legal_name": "A", "refund.no_refund": true}
	if Render(tpl, v, "en") != Render(tpl, v, "en") {
		t.Fatal("two renders differ")
	}
}

func TestPlaceholders_EveryKeyIsBuilt(t *testing.T) {
	v := BuildValues(Data{Locale: "en", DocumentVersion: 1, Promoter: &Promoter{Name: "P"}})
	for _, k := range Placeholders() {
		got, ok := v[k]
		if !ok {
			t.Errorf("BuildValues does not fill %q", k)
			continue
		}
		switch placeholders[k] {
		case kindFlag:
			if _, ok := got.(bool); !ok {
				t.Errorf("%q is a flag but holds %T", k, got)
			}
		case kindText:
			if _, ok := got.(string); !ok {
				t.Errorf("%q is text but holds %T", k, got)
			}
		}
	}
	if len(v) != len(placeholders) {
		t.Errorf("BuildValues fills %d keys, the allowlist has %d", len(v), len(placeholders))
	}
}
