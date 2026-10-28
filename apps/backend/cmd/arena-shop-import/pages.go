package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// PageMapping says which rendered theme files become which shop pages.
// File is the name inside the --pages directory; the percent-encoded form
// the exporter wrote ("pages__%D0%BE-...") and the decoded one both match.
type PageMapping struct {
	File     string `json:"file"`
	Slug     string `json:"slug"`
	Title    string `json:"title,omitempty"`
	Position int32  `json:"position"`
}

// PageMappingFile is the JSON mapping file.
type PageMappingFile struct {
	Pages []PageMapping `json:"pages"`
}

// ImportPage is one page ready to store.
type ImportPage struct {
	Slug     string
	Title    string
	BodyHTML string
	Position int32
	File     string
	// RemoteImages counts pictures that still point at the old store's CDN.
	RemoteImages int
	Warnings     []string
}

// LoadPageMapping reads the mapping JSON.
func LoadPageMapping(path string) ([]PageMapping, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-controlled path
	if err != nil {
		return nil, err
	}
	var f PageMappingFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(f.Pages) == 0 {
		return nil, fmt.Errorf("%s lists no pages", path)
	}
	for i, m := range f.Pages {
		if m.File == "" || m.Slug == "" {
			return nil, fmt.Errorf("%s: page %d needs both file and slug", path, i+1)
		}
		if _, ok := shop.NormalizeSlug(m.Slug); !ok {
			return nil, fmt.Errorf("%s: page %d has an unusable slug %q", path, i+1, m.Slug)
		}
	}
	return f.Pages, nil
}

// pageChrome are theme classes whose content is decoration, not the page:
// banner artwork, visually hidden helpers, the contact form's shell.
var pageChrome = []string{
	"banner__media", "visually-hidden", "skip-to-content", "form__message",
	"slideshow__controls", "slider-buttons", "product-card-wrapper", "card-wrapper", "pagination-wrapper",
}

// LoadPages extracts every mapped page from dir. A page that cannot be read
// or ends up empty is reported in problems and left out.
func LoadPages(dir string, maps []PageMapping, baseURL string) (pages []ImportPage, problems []string) {
	for _, m := range maps {
		path, ok := findPageFile(dir, m.File)
		if !ok {
			problems = append(problems, fmt.Sprintf("page %q: file %q not found in %s", m.Slug, m.File, dir))
			continue
		}
		raw, err := os.ReadFile(path) //nolint:gosec // operator-controlled path
		if err != nil {
			problems = append(problems, fmt.Sprintf("page %q: %v", m.Slug, err))
			continue
		}
		p, err := ExtractPage(string(raw), baseURL)
		if err != nil {
			problems = append(problems, fmt.Sprintf("page %q: %v", m.Slug, err))
			continue
		}
		p.Slug, _ = shop.NormalizeSlug(m.Slug)
		p.File = m.File
		p.Position = m.Position
		if m.Title != "" {
			p.Title = m.Title
		}
		if p.Title == "" {
			problems = append(problems, fmt.Sprintf("page %q: no title found, set \"title\" in the mapping", m.Slug))
			continue
		}
		if p.BodyHTML == "" {
			problems = append(problems, fmt.Sprintf("page %q: no content found in %s", m.Slug, m.File))
			continue
		}
		if len(p.BodyHTML) > shop.MaxPageBodyLen {
			problems = append(problems, fmt.Sprintf("page %q: body is %d characters, the shop allows %d", m.Slug, len(p.BodyHTML), shop.MaxPageBodyLen))
			continue
		}
		pages = append(pages, p)
	}
	return pages, problems
}

func findPageFile(dir, name string) (string, bool) {
	cands := []string{name}
	if dec, err := url.PathUnescape(name); err == nil && dec != name {
		cands = append(cands, dec)
	}
	if enc := url.PathEscape(name); enc != name {
		cands = append(cands, enc)
	}
	for _, c := range cands {
		p := filepath.Join(dir, c)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func findNode(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findNode(c, match); f != nil {
			return f
		}
	}
	return nil
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		if x.Type == html.ElementNode && dropTags[x.DataAtom] {
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(collapseSpace(b.String()))
}

// ExtractPage pulls the title and the content out of one rendered Dawn
// page. A policy page keeps its body (shopify-policy__body); a theme page
// keeps everything inside <main> except the decoration. The page's own h1 is
// the title and is not repeated in the body.
func ExtractPage(doc string, baseURL string) (ImportPage, error) {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return ImportPage{}, fmt.Errorf("parse html: %w", err)
	}
	main := findNode(root, func(n *html.Node) bool { return n.DataAtom == atom.Main })
	if main == nil {
		return ImportPage{}, fmt.Errorf("no <main> element")
	}
	var p ImportPage
	if h1 := findNode(main, func(n *html.Node) bool { return n.DataAtom == atom.H1 }); h1 != nil {
		p.Title = nodeText(h1)
		// Detach the title: the storefront prints the title itself.
		if h1.Parent != nil {
			h1.Parent.RemoveChild(h1)
		}
	}
	if p.Title == "" {
		if t := findNode(root, func(n *html.Node) bool { return n.DataAtom == atom.Title }); t != nil {
			p.Title = strings.TrimSpace(strings.SplitN(nodeText(t), " – ", 2)[0])
		}
	}
	scope := main
	if body := findNode(main, func(n *html.Node) bool { return hasClass(n, "shopify-policy__body") }); body != nil {
		scope = body
	}
	opt := cleanOptions{BaseURL: baseURL, DropClasses: pageChrome, KeepImages: true}
	p.BodyHTML = cleanNodes([]*html.Node{scope}, opt)
	p.BodyHTML = dropLeadingTitle(p.BodyHTML, p.Title)
	p.RemoteImages = strings.Count(p.BodyHTML, "<img src=")
	return p, nil
}

var imgSrcPattern = regexp.MustCompile(`(?i)<img[^>]*?\ssrc\s*=\s*"([^"]*)"`)

// shopifyHostedImages returns the sources of the <img> elements of a stored
// text that are still hosted by Shopify: cdn.shopify.com (or another
// *.shopify.com / *.shopifycdn.net host), or the store's own domain under
// /cdn/ (www.einatwinery.com/cdn/shop/files/...). Those stop working when the
// Shopify store is closed, so the pictures have to be re-hosted before the
// domain switch.
func shopifyHostedImages(body string) []string {
	var out []string
	for _, m := range imgSrcPattern.FindAllStringSubmatch(body, -1) {
		raw := html.UnescapeString(m[1])
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		switch {
		case host == "cdn.shopify.com", strings.HasSuffix(host, ".shopify.com"), strings.HasSuffix(host, ".shopifycdn.net"), host == "shopifycdn.net":
			out = append(out, raw)
		case strings.HasPrefix(u.Path, "/cdn/"):
			out = append(out, raw)
		}
	}
	return out
}

// dropLeadingTitle removes a first heading that repeats the page title.
func dropLeadingTitle(body, title string) string {
	for _, tag := range []string{"h2", "h3"} {
		open := "<" + tag + ">" + html.EscapeString(title) + "</" + tag + ">"
		if strings.HasPrefix(body, open) {
			return strings.TrimSpace(strings.TrimPrefix(body, open))
		}
	}
	return body
}
