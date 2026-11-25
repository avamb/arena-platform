package seating

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeDecorSVG_Rules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"script element", `<g><script>x()</script><rect width="1"/></g>`, `<g><rect width="1"/></g>`},
		{"script element upper case", `<SCRIPT>x()</SCRIPT><rect/>`, `<rect/>`},
		{"foreignObject", `<foreignObject><div>t</div></foreignObject><circle r="2"/>`, `<circle r="2"/>`},
		{"iframe", `<iframe src="x"/><rect/>`, `<rect/>`},
		{"embed", `<embed src="x"/><rect/>`, `<rect/>`},
		{"object", `<object data="x"/><rect/>`, `<rect/>`},
		{"set", `<g><set attributeName="href" to="x"/></g>`, `<g></g>`},
		{"animate", `<rect><animate attributeName="x" to="1"/></rect>`, `<rect></rect>`},
		{"animateTransform", `<rect><animateTransform attributeName="transform"/></rect>`, `<rect></rect>`},
		{"event attribute", `<rect onclick="x()" width="3" onLoad="y()"/>`, `<rect width="3"/>`},
		{"link element", `<a href="https://example.com/">t</a><rect/>`, `<rect/>`},
		{"html img", `<img src="x"/><rect/>`, `<rect/>`},
		{"html meta", `<meta http-equiv="refresh" content="0"/><rect/>`, `<rect/>`},
		{"html form", `<form action="x"><input name="a"/></form><rect/>`, `<rect/>`},
		{"style element", `<style>rect{fill:red}</style><rect/>`, `<rect/>`},
		{"foreign namespace element", `<x:div xmlns:x="http://www.w3.org/1999/xhtml">t</x:div><rect/>`, `<rect/>`},
		{"unknown attribute", `<rect data-x="1" width="2" src="x"/>`, `<rect width="2"/>`},
		{"local url kept", `<rect fill="url(#grad)" style="fill:url('#g');stroke:#000"/>`, `<rect fill="url(#grad)" style="fill:url(&#39;#g&#39;);stroke:#000"/>`},
		{"outside url dropped", `<rect fill="url(https://example.com/a.svg#g)" width="1"/>`, `<rect width="1"/>`},
		{"outside url in style dropped", `<rect style="fill:url(//example.com/x)" width="1"/>`, `<rect width="1"/>`},
		{"import in style dropped", `<rect style="@import 'x.css'" width="1"/>`, `<rect width="1"/>`},
		{"backslash in style dropped", `<rect style="fill:u\72l(x)" width="1"/>`, `<rect width="1"/>`},
		{"href on rect dropped", `<rect href="#a" width="1"/>`, `<rect width="1"/>`},
		{"gradient and clip kept", `<defs><linearGradient id="g"><stop offset="0" stop-color="#fff"/></linearGradient><clipPath id="c"><rect/></clipPath></defs>`, `<defs><linearGradient id="g"><stop offset="0" stop-color="#fff"/></linearGradient><clipPath id="c"><rect/></clipPath></defs>`},
		{"editor metadata kept", `<sodipodi:namedview pagecolor="#fff" inkscape:zoom="1"/>`, `<sodipodi:namedview pagecolor="#fff" inkscape:zoom="1"/>`},
		{"outside href on use", `<use href="other.svg#x"/>`, `<use/>`},
		{"local href kept", `<use href="#seat"/>`, `<use href="#seat"/>`},
		{"data png on image kept", `<image href="data:image/png;base64,AAAA"/>`, `<image href="data:image/png;base64,AAAA"/>`},
		{"data jpeg on image kept", `<image href="data:image/jpeg;base64,AAAA"/>`, `<image href="data:image/jpeg;base64,AAAA"/>`},
		{"data svg on image dropped", `<image href="data:image/svg+xml;base64,AAAA"/>`, `<image/>`},
		{"data png on use dropped", `<use href="data:image/png;base64,AAAA"/>`, `<use/>`},
		{"outside image dropped", `<image href="https://example.com/a.png"/>`, `<image/>`},
		{"javascript with whitespace", `<use href="java&#x9;script:x()"/>`, `<use/>`},
		{"javascript in other attribute", `<rect style="fill:url(JavaScript:x)" width="1"/>`, `<rect width="1"/>`},
		{"plain drawing kept", `<g id="Stage"><rect x="1" y="2" fill="#fff"/><text>Сцена &amp; зал</text></g>`, `<g id="Stage"><rect x="1" y="2" fill="#fff"/><text>Сцена &amp; зал</text></g>`},
		{"known prefixes kept", `<svg:g inkscape:label="Stage" sodipodi:nodetypes="cc"><svg:path d="M0 0"/></svg:g>`, `<svg:g inkscape:label="Stage" sodipodi:nodetypes="cc"><svg:path d="M0 0"/></svg:g>`},
		{"empty", ``, ``},
		{"broken", `<g><rect`, ``},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeDecorSVG(tc.in); got != tc.want {
				t.Fatalf("SanitizeDecorSVG(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestImportSVG_DecorDropsCode proves the importer writes the same filtered
// decor as SanitizeDecorSVG.
func TestImportSVG_DecorDropsCode(t *testing.T) {
	t.Parallel()
	root, err := parseXMLTree([]byte(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">` +
		`<rect width="5" onclick="x()"/><script>x()</script><a xlink:href="https://example.com/">t</a></svg>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := renderDecorSVG(root, nil, nil, nil)
	want := `<svg:rect width="5"/>`
	if got != want {
		t.Fatalf("decor = %q, want %q", got, want)
	}
	if again := SanitizeDecorSVG(got); again != got {
		t.Fatalf("SanitizeDecorSVG(decor) = %q, want %q", again, got)
	}
}

// TestSanitizeDecorSVG_FixturesUnchanged proves the decor of every repo
// fixture reads back byte-identically.
func TestSanitizeDecorSVG_FixturesUnchanged(t *testing.T) {
	t.Parallel()
	cases := append([]keyGoldenCase(nil), keyGoldenCases...)
	for _, name := range []string{"v2_quarantine.svg", "v2_render.svg", "v2_schematic.svg", "v2_service_render.svg"} {
		cases = append(cases, keyGoldenCase{
			name: name,
			path: []string{"..", "..", "..", "..", "venue-svg-generator", "arena_export", "testdata", name},
		})
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(tc.path...))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var decor string
			if tc.sbt {
				plan, _, errs := ImportSBTSVG(raw)
				if len(errs) != 0 {
					t.Fatalf("import errors: %v", errs)
				}
				decor = plan.Geometry.DecorSVG
			} else {
				g, _, errs := ImportAnySVG(raw)
				if len(errs) != 0 {
					t.Fatalf("import errors: %v", errs)
				}
				decor = g.DecorSVG
			}
			if strings.TrimSpace(decor) == "" {
				t.Skip("fixture has no decor")
			}
			if got := SanitizeDecorSVG(decor); got != decor {
				t.Fatalf("decor changed on read back: len %d -> %d", len(decor), len(got))
			}
		})
	}
}
