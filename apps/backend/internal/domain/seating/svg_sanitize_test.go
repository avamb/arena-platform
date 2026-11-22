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
		{"set", `<a href="#a"><set attributeName="href" to="x"/></a>`, `<a href="#a"></a>`},
		{"animate", `<rect><animate attributeName="x" to="1"/></rect>`, `<rect></rect>`},
		{"animateTransform", `<rect><animateTransform attributeName="transform"/></rect>`, `<rect></rect>`},
		{"event attribute", `<rect onclick="x()" width="3" onLoad="y()"/>`, `<rect width="3"/>`},
		{"outside href on a", `<a href="https://example.com/">t</a>`, `<a>t</a>`},
		{"outside href on use", `<use href="other.svg#x"/>`, `<use/>`},
		{"local href kept", `<use href="#seat"/>`, `<use href="#seat"/>`},
		{"data png on image kept", `<image href="data:image/png;base64,AAAA"/>`, `<image href="data:image/png;base64,AAAA"/>`},
		{"data jpeg on image kept", `<image href="data:image/jpeg;base64,AAAA"/>`, `<image href="data:image/jpeg;base64,AAAA"/>`},
		{"data svg on image dropped", `<image href="data:image/svg+xml;base64,AAAA"/>`, `<image/>`},
		{"data png on use dropped", `<use href="data:image/png;base64,AAAA"/>`, `<use/>`},
		{"outside image dropped", `<image href="https://example.com/a.png"/>`, `<image/>`},
		{"javascript with whitespace", `<a href="java&#x9;script:x()">t</a>`, `<a>t</a>`},
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
	want := `<svg:rect width="5"/><svg:a>t</svg:a>`
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
	for _, tc := range keyGoldenCases {
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
				g, _, errs := ImportSVG(raw)
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
