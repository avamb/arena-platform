package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanHTML_StripsEverythingButTheSubset(t *testing.T) {
	in := `<div class="font_8 wixui" style="color:red"><h1>Big</h1>
	<p onclick="x()">Hello <strong class="a">world</strong> &amp; <b>bold</b><br><br><br><br>tail</p>
	<p>  </p><p><br></p>
	<script>alert(1)</script><style>p{}</style>
	<a href="javascript:alert(1)">bad</a> <a href="//cdn.example.com/x" target="_blank">ok</a>
	<ul> <li>one</li> <li></li> </ul><ul></ul>
	<img src="data:image/png;base64,AAAA"><img src="//cdn.example.com/a.jpg?v=1" alt="Pic" width="9">
	<form><input name="q"></form><span>plain</span></div>`
	got := CleanHTML(in, cleanOptions{KeepImages: true})
	for _, bad := range []string{"script", "style", "onclick", "class=", "javascript:", "data:", "<form", "<div", "<span", "<h1", "target="} {
		if strings.Contains(got, bad) {
			t.Errorf("output still contains %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{
		"<h2>Big</h2>",
		"<strong>world</strong>",
		"<strong>bold</strong>",
		`<a href="https://cdn.example.com/x" rel="noopener">ok</a>`,
		`<img src="https://cdn.example.com/a.jpg?v=1" alt="Pic">`,
		"<ul><li>one</li></ul>",
		"plain",
		"&amp;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<br><br><br>") || strings.Contains(got, "<p></p>") || strings.Contains(got, "<ul></ul>") {
		t.Errorf("empties were kept:\n%s", got)
	}
}

func TestCleanHTML_RelativeLinksAndImageSwitch(t *testing.T) {
	in := `<p><a href="/pages/contact">write</a> <img src="/cdn/x.png" alt=""></p>`
	if got := CleanHTML(in, cleanOptions{BaseURL: "https://shop.example.com"}); !strings.Contains(got, `href="https://shop.example.com/pages/contact"`) || strings.Contains(got, "<img") {
		t.Errorf("base url / KeepImages=false: %s", got)
	}
	if got := CleanHTML(in, cleanOptions{KeepImages: true}); !strings.Contains(got, `href="/pages/contact"`) || !strings.Contains(got, `src="/cdn/x.png"`) {
		t.Errorf("without a base the paths stay relative: %s", got)
	}
}

func TestCleanHTML_CyrillicAndQuotesSurvive(t *testing.T) {
	got := CleanHTML(`<h4>Красное сухое "Lonely Cat" — Каберне</h4>`, cleanOptions{})
	if !strings.Contains(got, "Красное сухое") || !strings.Contains(got, "Каберне") || !strings.HasPrefix(got, "<h4>") {
		t.Errorf("got %s", got)
	}
}

const themePage = `<html><head><title>О нас – Einat</title><style>x{}</style></head><body>
<header>site header</header>
<main id="MainContent">
 <section><div class="banner__media"><img src="//cdn.example.com/banner.webp"></div></section>
 <section><div class="rte"><h1>О нас</h1></div>
  <h2>О нас</h2><p>Привет! Мы <strong>Vino</strong>.</p>
  <h2>Скоро</h2><ul class="grid"><li class="product-card-wrapper">Card</li></ul>
  <h2>Прошлое</h2><a href="/blogs/x"> Посмотреть все </a>
  <h2>Наша история</h2><p>Текст</p><img src="//cdn.example.com/story.webp?v=1" alt="">
  <form><input type="text"></form>
 </section>
</main><footer>footer</footer></body></html>`

const policyPage = `<html><head><title>Политика – Einat</title></head><body><main id="MainContent">
<div class="shopify-policy__container"><div class="shopify-policy__title"><h1>Политика возврата</h1></div>
<div class="shopify-policy__body"><div class="rte"><p>30 days.<br><br>Contact us.</p><br><p><strong>Exchanges</strong> <br>text</p></div></div></div></main></body></html>`

func TestExtractPage_ThemePage(t *testing.T) {
	p, err := ExtractPage(themePage, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "О нас" {
		t.Errorf("title %q", p.Title)
	}
	if strings.Contains(p.BodyHTML, "banner.webp") || strings.Contains(p.BodyHTML, "site header") || strings.Contains(p.BodyHTML, "footer") {
		t.Errorf("theme chrome leaked: %s", p.BodyHTML)
	}
	if strings.HasPrefix(p.BodyHTML, "<h2>О нас</h2>") {
		t.Errorf("the repeated title heading was kept: %s", p.BodyHTML)
	}
	if strings.Contains(p.BodyHTML, "Скоро") || strings.Contains(p.BodyHTML, "Прошлое") {
		t.Errorf("sections without content were kept: %s", p.BodyHTML)
	}
	if !strings.Contains(p.BodyHTML, "<strong>Vino</strong>") || !strings.Contains(p.BodyHTML, "<h2>Наша история</h2>") {
		t.Errorf("content lost: %s", p.BodyHTML)
	}
	if p.RemoteImages != 1 || !strings.Contains(p.BodyHTML, `src="https://cdn.example.com/story.webp?v=1"`) {
		t.Errorf("images: %d %s", p.RemoteImages, p.BodyHTML)
	}
}

func TestExtractPage_Policy(t *testing.T) {
	p, err := ExtractPage(policyPage, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Политика возврата" || !strings.HasPrefix(p.BodyHTML, "<p>30 days.") || !strings.Contains(p.BodyHTML, "<strong>Exchanges</strong>") {
		t.Errorf("%q / %s", p.Title, p.BodyHTML)
	}
}

func TestExtractPage_NoMain(t *testing.T) {
	if _, err := ExtractPage("<html><body><p>x</p></body></html>", ""); err == nil {
		t.Error("a document without <main> must be refused")
	}
}

func TestLoadPages_MappingMatchesEncodedAndDecodedNames(t *testing.T) {
	dir := t.TempDir()
	// The exporter wrote the percent-encoded name; the mapping file may use either.
	if err := os.WriteFile(filepath.Join(dir, "pages__%D0%BE-%D0%BD%D0%B0%D1%81.html"), []byte(themePage), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policies__refund-policy.html"), []byte(policyPage), 0o600); err != nil {
		t.Fatal(err)
	}
	mapping := filepath.Join(dir, "map.json")
	js := `{"pages":[
	 {"file":"pages__о-нас.html","slug":"about","position":10},
	 {"file":"pages__%D0%BE-%D0%BD%D0%B0%D1%81.html","slug":"about-again","title":"Свой заголовок","position":11},
	 {"file":"policies__refund-policy.html","slug":"refund-policy","position":50},
	 {"file":"missing.html","slug":"gone","position":99}]}`
	if err := os.WriteFile(mapping, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	maps, err := LoadPageMapping(mapping)
	if err != nil {
		t.Fatal(err)
	}
	pages, problems := LoadPages(dir, maps, "")
	if len(pages) != 3 || len(problems) != 1 || !strings.Contains(problems[0], "missing.html") {
		t.Fatalf("pages=%d problems=%v", len(pages), problems)
	}
	if pages[0].Slug != "about" || pages[0].Title != "О нас" || pages[0].Position != 10 {
		t.Errorf("page 0: %+v", pages[0])
	}
	if pages[1].Title != "Свой заголовок" {
		t.Errorf("explicit title must win: %+v", pages[1])
	}
	if pages[2].Slug != "refund-policy" || pages[2].Title != "Политика возврата" {
		t.Errorf("page 2: %+v", pages[2])
	}
}

func TestLoadPageMapping_Validation(t *testing.T) {
	dir := t.TempDir()
	write := func(js string) string {
		p := filepath.Join(dir, "m.json")
		if err := os.WriteFile(p, []byte(js), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, js := range map[string]string{
		"empty":        `{"pages":[]}`,
		"no slug":      `{"pages":[{"file":"a.html"}]}`,
		"bad slug":     `{"pages":[{"file":"a.html","slug":"a/b"}]}`,
		"not json":     `nope`,
		"unknown file": ``,
	} {
		if name == "unknown file" {
			continue
		}
		if _, err := LoadPageMapping(write(js)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestShopifyHostedImages(t *testing.T) {
	body := `<p>x</p><img src="https://cdn.shopify.com/s/files/1/a.jpg" alt="a">` +
		`<img alt="b" src="https://www.einatwinery.com/cdn/shop/files/b.jpg?v=1">` +
		`<img src="//cdn.shopify.com/c.png">` +
		`<img src="https://media.example.com/ok.jpg">` +
		`<img src="/files/own.jpg">`
	got := shopifyHostedImages(body)
	if len(got) != 3 {
		t.Fatalf("want 3 Shopify-hosted pictures, got %v", got)
	}
	if len(shopifyHostedImages(`<p>no pictures</p>`)) != 0 {
		t.Error("no pictures, no warning")
	}
}
