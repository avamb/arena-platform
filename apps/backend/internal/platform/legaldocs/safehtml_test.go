package legaldocs

import "testing"

func TestSafeRenderedHTML_AcceptsWhatRenderWrites(t *testing.T) {
	tpl := mustParse(t, "## <b>Title</b>\n\n- {{seller.legal_name}}\n- x > y\n\n### Sub\n{{promoter.name}} & co <script>")
	for _, v := range []Values{
		{},
		{"seller.legal_name": `"><script>alert(1)</script>`, "promoter.name": "<img src=x onerror=alert(1)>"},
		{"seller.legal_name": "</article><p>", "promoter.name": "<!-- -->"},
	} {
		for _, l := range Locales {
			out := Render(tpl, v, l)
			if !SafeRenderedHTML(out) {
				t.Fatalf("Render output refused for %v/%s:\n%s", v, l, out)
			}
		}
	}
}

func TestSafeRenderedHTML_RefusesAnythingElse(t *testing.T) {
	const open = `<article class="legal-document" lang="en" dir="ltr">` + "\n"
	cases := map[string]string{
		"no article":       "<p>x</p>",
		"script":           open + "<p>x</p><script>alert(1)</script>\n</article>\n",
		"attribute":        open + `<p onclick="x">y</p>` + "\n</article>\n",
		"link":             open + `<a href="https://evil.example">y</a>` + "\n</article>\n",
		"raw gt":           open + "<p>a > b</p>\n</article>\n",
		"comment":          open + "<!-- x --><p>y</p>\n</article>\n",
		"nested article":   open + open + "</article>\n</article>\n",
		"unclosed":         open + "<p>x</p>\n",
		"bad dir":          `<article class="legal-document" lang="en" dir="x">` + "</article>",
		"style":            open + "<style>body{}</style>\n</article>\n",
		"uppercase script": open + "<SCRIPT>x</SCRIPT>\n</article>\n",
	}
	for name, s := range cases {
		if SafeRenderedHTML(s) {
			t.Errorf("%s accepted: %q", name, s)
		}
	}
}
