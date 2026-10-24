// short_link_url_test.go keeps the rule of
// 08_architecture/43_sales_links_qr_ru.md (QL-14): the address printed under a
// QR code is built in ONE place, salelinks.ShortURL(base, code), from the
// configured SHORT_LINK_BASE_URL. A non-test Go file that writes a "/go/"
// path or the go.arenasoldout.com host into a string literal is formatting a
// short link by hand (or hard-coding the host), and the next change of the
// host would miss it.
//
// Exempt, by design:
//   - internal/platform/salelinks (the builder) and internal/platform/config
//     (the default host of SHORT_LINK_BASE_URL);
//   - generated files (a "Code generated" header);
//   - in httpserver/mount_go.go only, the route patterns of the redirect,
//     exactly "/go", "/go/", "/go/{code}" and "/go/{code}/*" (chi patterns,
//     not links; elsewhere a bare "/go/" literal is still flagged, because
//     "/go/" + code is exactly the hand-built link this guard exists for);
//   - the placeholder "/go/{code}" inside prose such as a metric help text;
//   - a literal with "allow:shortlink" in a comment on its line or the line
//     above, which must say why (a path test in the router, a stored default).
//
// Comments and doc text are never scanned: only string literals are.
package staticanalysis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const shortLinkAllowMarker = "allow:shortlink"

// shortLinkRoutePattern is the placeholder form of the path; it is removed
// from a literal before the "/go/" test.
const shortLinkRoutePattern = "/go/{code}"

// shortLinkRouteFile is the one file that registers the redirect routes, and
// shortLinkRoutePatterns the exact chi patterns it may spell. A pattern is a
// route, not a link.
const shortLinkRouteFile = "internal/platform/httpserver/mount_go.go"

var shortLinkRoutePatterns = map[string]bool{
	"/go":          true,
	"/go/":         true,
	"/go/{code}":   true,
	"/go/{code}/*": true,
}

// shortLinkExemptDirs are slash-separated directories (relative to
// apps/backend) whose files may name the host and the path.
var shortLinkExemptDirs = []string{
	"internal/platform/salelinks",
	"internal/platform/config",
}

// shortLinkViolations returns "file:line: literal" for every string literal of
// src that formats a short link by hand. rel is the slash path of the file
// relative to apps/backend and decides the directory exemption.
func shortLinkViolations(t *testing.T, rel string, src []byte) []string {
	t.Helper()
	for _, dir := range shortLinkExemptDirs {
		if strings.HasPrefix(rel, dir+"/") {
			return nil
		}
	}
	if strings.Contains(string(src), "// Code generated") {
		head := string(src)
		if len(head) > 2048 {
			head = head[:2048]
		}
		if strings.Contains(head, "Code generated") && strings.Contains(head, "DO NOT EDIT") {
			return nil
		}
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	allowedLines := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, shortLinkAllowMarker) {
				end := fset.Position(c.End()).Line
				allowedLines[end] = true
				allowedLines[end+1] = true
			}
		}
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val := lit.Value
		// The route pattern is a placeholder, never a link: the route itself
		// and prose such as a metric help text may name it.
		if rel == shortLinkRouteFile && shortLinkRoutePatterns[strings.Trim(val, "\"`")] {
			return true
		}
		pathCheck := strings.ReplaceAll(val, shortLinkRoutePattern, "")
		if !strings.Contains(pathCheck, "/go/") && !strings.Contains(strings.ToLower(val), "go.arenasoldout.com") {
			return true
		}
		line := fset.Position(lit.Pos()).Line
		if allowedLines[line] {
			return true
		}
		out = append(out, rel+":"+strconv.Itoa(line)+": "+val)
		return true
	})
	return out
}

func TestShortLinkURL_BuiltOnlyBySalelinks(t *testing.T) {
	backend := filepath.Join(repoRoot(t), "apps", "backend")
	var violations []string
	for _, top := range []string{"cmd", "internal"} {
		root := filepath.Join(backend, top)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == ".gomodcache" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path) // #nosec G304 -- repo-local test scan
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			rel, _ := filepath.Rel(backend, path)
			violations = append(violations, shortLinkViolations(t, filepath.ToSlash(rel), src)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(violations) > 0 {
		t.Errorf("a short link is built by salelinks.ShortURL from SHORT_LINK_BASE_URL (spec 43, QL-14); "+
			"add \"// %s: <why>\" only for a path test or a stored default:\n  %s",
			shortLinkAllowMarker, strings.Join(violations, "\n  "))
	}
}

// TestShortLinkURL_GuardCatchesHandFormatting proves the scanner itself: it
// must flag the two bad shapes and let the exemptions through.
func TestShortLinkURL_GuardCatchesHandFormatting(t *testing.T) {
	bad := `package x
func a(code string) string { return "https://go.arenasoldout.com/" + code }
func b(code string) string { return "/go/" + code }
func c() string { return ` + "`https://GO.arenasoldout.com`" + ` }
`
	if got := shortLinkViolations(t, "internal/platform/x/x.go", []byte(bad)); len(got) != 3 {
		t.Fatalf("want 3 violations, got %d: %v", len(got), got)
	}
	good := `package x
// A comment may say go.arenasoldout.com and /go/{code} freely.
func route() string { return "/go/{code}" }
func test(p string) bool {
	// allow:shortlink path test of the router
	return p == "/go/"
}
func inline(p string) bool { return p == "/go/" } // allow:shortlink path test
`
	if got := shortLinkViolations(t, "internal/platform/x/x.go", []byte(good)); len(got) != 0 {
		t.Fatalf("exemptions must pass, got %v", got)
	}
	routes := `package x
func r() []string { return []string{"/go", "/go/", "/go/{code}", "/go/{code}/*"} }
`
	if got := shortLinkViolations(t, shortLinkRouteFile, []byte(routes)); len(got) != 0 {
		t.Fatalf("the route patterns pass in mount_go.go, got %v", got)
	}
	if got := shortLinkViolations(t, "internal/platform/x/x.go", []byte(routes)); len(got) != 1 {
		t.Fatalf("outside mount_go.go the bare \"/go/\" literal is flagged (the placeholder forms are not), got %v", got)
	}
	if got := shortLinkViolations(t, "internal/platform/salelinks/x.go", []byte(bad)); len(got) != 0 {
		t.Fatalf("salelinks is exempt, got %v", got)
	}
	gen := "// Code generated by x. DO NOT EDIT.\n" + bad
	if got := shortLinkViolations(t, "internal/platform/y/y.go", []byte(gen)); len(got) != 0 {
		t.Fatalf("generated files are exempt, got %v", got)
	}
}
