package richtext

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var (
	descr = ProfileDescription
	email = ProfileEmailNote
	sess  = ProfileSessionMessage
)

func mustSanitize(t *testing.T, in string, p Profile) string {
	t.Helper()
	out, err := Sanitize(in, p)
	if err != nil {
		t.Fatalf("Sanitize(%q) error: %v", in, err)
	}
	return out
}

func TestProfilesTable(t *testing.T) {
	cases := []struct {
		p                      Profile
		name                   string
		visible, html, links   int
		schemes                []string
		headings, quote, autol bool
	}{
		{ProfileDescription, "description", 4000, 16000, 20, []string{"https", "mailto"}, true, true, false},
		{ProfileEmailNote, "email_note", 2000, 8000, 5, []string{"https"}, false, false, true},
		{ProfileSessionMessage, "session_message", 1000, 4000, 3, []string{"https"}, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.p.Name != c.name || c.p.MaxVisible != c.visible || c.p.MaxHTML != c.html || c.p.MaxLinks != c.links {
				t.Fatalf("limits of %s: %+v", c.name, c.p)
			}
			if !slices.Equal(c.p.Schemes, c.schemes) {
				t.Fatalf("schemes = %v", c.p.Schemes)
			}
			if c.p.has("h3") != c.headings || c.p.has("h4") != c.headings || c.p.has("blockquote") != c.quote {
				t.Fatalf("headings/quote wrong: %v", c.p.Tags)
			}
			if c.p.AutoLink != c.autol {
				t.Fatalf("AutoLink = %v", c.p.AutoLink)
			}
			if got, ok := ProfileByName(c.name); !ok || got.Name != c.name {
				t.Fatalf("ProfileByName(%q) = %v, %v", c.name, got.Name, ok)
			}
		})
	}
	if _, ok := ProfileByName("nope"); ok {
		t.Fatal("unknown profile found")
	}
}

type sanitizeCase struct {
	name string
	in   string
	p    Profile
	want string
}

func sanitizeCases() []sanitizeCase {
	long := "https://example.com/" + strings.Repeat("a", 480) // exactly 500 characters
	return []sanitizeCase{
		// basics and aliases
		{"paragraph", `<p>Hi</p>`, descr, `<p>Hi</p>`},
		{"bare text", `Hi there`, descr, `<p>Hi there</p>`},
		{"empty", ``, descr, ``},
		{"b and i", `<b>x</b><i>y</i>`, descr, `<p><strong>x</strong><em>y</em></p>`},
		{"underline", `<u>x</u>`, descr, `<p><u>x</u></p>`},
		{"mark order is canonical", `<u><em><strong>x</strong></em></u>`, descr, `<p><strong><em><u>x</u></em></strong></p>`},
		{"link outside marks", `<strong><a href="https://e.com">x</a></strong>`, descr, `<p><a href="https://e.com"><strong>x</strong></a></p>`},
		{"unclosed tags", `<p>a<strong>b`, descr, `<p>a<strong>b</strong></p>`},
		{"hebrew", `<p>שלום <b>עולם</b></p>`, descr, `<p>שלום <strong>עולם</strong></p>`},
		{"text entities", `&lt;script&gt;alert(1)&lt;/script&gt;`, descr, `<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>`},
		{"numeric entities of tag characters", `<p>&#60;b&#62; 5 &amp; 6</p>`, descr, `<p>&lt;b&gt; 5 &amp; 6</p>`},
		{"nbsp and numeric spaces", "a&nbsp;&nbsp;b&#160;c&#x20;d", descr, `<p>a b c d</p>`},
		{"literal nbsp character", "a\u00a0\u00a0b", descr, `<p>a b</p>`},
		{"newlines are spaces", "<p>a\nb\n\nc</p>", descr, `<p>a b c</p>`},
		{"newlines between blocks", "<p>a</p>\n<p>b</p>\n", descr, `<p>a</p><p>b</p>`},
		{"br kept, border br dropped", `<br>a<br><br>b<br>`, descr, `<p>a<br><br>b</p>`},
		{"spaces around br", `<p>a <br> b</p>`, descr, `<p>a<br>b</p>`},
		{"marks do not span a break", `<strong>a<br>b</strong>`, descr, `<p><strong>a</strong><br><strong>b</strong></p>`},

		// OWASP style vectors
		{"javascript scheme", `<a href="javascript:alert(1)">x</a>`, descr, `<p>x</p>`},
		{"mixed case javascript", `<a href="JaVaScRiPt:alert(1)">x</a>`, descr, `<p>x</p>`},
		{"entity encoded javascript", `<a href="&#106;avascript:alert(1)">x</a>`, descr, `<p>x</p>`},
		{"hex entity encoded javascript", `<a href="&#x6A;avascript:alert(1)">x</a>`, descr, `<p>x</p>`},
		{"newline inside scheme", "<a href=\"java&#x0A;script:alert(1)\">x</a>", descr, `<p>x</p>`},
		{"tab inside scheme", "<a href=\"java\tscript:alert(1)\">x</a>", descr, `<p>x</p>`},
		{"data scheme", `<a href="data:text/html;base64,PHNjcmlwdD4=">x</a>`, descr, `<p>x</p>`},
		{"vbscript scheme", `<a href="vbscript:msgbox(1)">x</a>`, descr, `<p>x</p>`},
		{"protocol relative", `<a href="//evil.com">x</a>`, descr, `<p>x</p>`},
		{"relative path", `<a href="/admin">x</a>`, descr, `<p>x</p>`},
		{"fragment", `<a href="#x">x</a>`, descr, `<p>x</p>`},
		{"backslash host", `<a href="https:\\evil.com">x</a>`, descr, `<p>x</p>`},
		{"https without slashes", `<a href="https:evil.com">x</a>`, descr, `<p>x</p>`},
		{"plain http", `<a href="http://example.com">x</a>`, descr, `<p>x</p>`},
		{"upper case scheme and host", `<a href="HTTPS://Example.com">x</a>`, descr, `<p><a href="https://example.com">x</a></p>`},
		{"leading space in href", `<a href=" https://x">y</a>`, descr, `<p><a href="https://x">y</a></p>`},
		{"trailing newline in href", "<a href=\"https://x.com/p\n\">y</a>", descr, `<p><a href="https://x.com/p">y</a></p>`},
		{"space inside href", `<a href="https://exa mple.com">y</a>`, descr, `<p>y</p>`},
		{"path and query kept", `<a href="https://Example.com/A/b?x=1&y=2#z">y</a>`, descr, `<p><a href="https://example.com/A/b?x=1&amp;y=2#z">y</a></p>`},
		{"href with quote", `<a href="https://example.com/&quot;onmouseover=alert(1)">y</a>`, descr, `<p>y</p>`},
		{"event handler attributes", `<p onclick="alert(1)" style="color:red" class="x" id="y">x</p>`, descr, `<p>x</p>`},
		{"link keeps only href", `<a href="https://e.com" onclick="x()" target="_self" rel="x" style="a:b">y</a>`, descr, `<p><a href="https://e.com">y</a></p>`},
		{"uppercase attribute names", `<A HREF="https://e.com" ONCLICK="x()">y</A>`, descr, `<p><a href="https://e.com">y</a></p>`},
		{"svg onload", `<svg onload=alert(1)><circle/></svg>text`, descr, `<p>text</p>`},
		{"img onerror", `<img src=x onerror=alert(1)>a`, descr, `<p>a</p>`},
		{"iframe", `<iframe src="https://e.com">hello</iframe>x`, descr, `<p>x</p>`},
		{"object and embed", `<object data="x">o</object><embed src="x">y`, descr, `<p>y</p>`},
		{"script with content", `<script>alert(1)</script>ok`, descr, `<p>ok</p>`},
		{"style with content", `<style>p{color:red}</style>ok`, descr, `<p>ok</p>`},
		{"noscript and template", `<noscript>n</noscript><template>t</template>ok`, descr, `<p>ok</p>`},
		{"comment", `<!-- secret --><p>ok</p>`, descr, `<p>ok</p>`},
		{"conditional comment", `<!--[if IE]><script>alert(1)</script><![endif]-->ok`, descr, `<p>ok</p>`},
		{"nested anchors", `<a href="https://a.com">1<a href="https://b.com">2</a></a>`, descr, `<p><a href="https://a.com">1</a><a href="https://b.com">2</a></p>`},
		{"broken script tag", `<scr<script>ipt>alert(1)</scr</script>ipt>`, descr, `<p>ipt&gt;alert(1)ipt&gt;</p>`},
		{"double angle script", `<<script>alert(1)//<</script>`, descr, `<p>&lt;</p>`},
		{"script inside a link", `<a href="https://e.com"><script>alert(1)</script>t</a>`, descr, `<p><a href="https://e.com">t</a></p>`},
		{"attribute without value and junk", `<p ="x" <b>a</p>`, descr, `<p>a</p>`},

		// hosts
		{"userinfo", `<a href="https://user:pw@example.com/">x</a>`, descr, `<p>x</p>`},
		{"userinfo trick", `<a href="https://example.com@evil.com/">x</a>`, descr, `<p>x</p>`},
		{"port", `<a href="https://example.com:8080/">x</a>`, descr, `<p>x</p>`},
		{"empty port", `<a href="https://example.com:/">x</a>`, descr, `<p>x</p>`},
		{"ipv4", `<a href="https://127.0.0.1/">x</a>`, descr, `<p>x</p>`},
		{"public ipv4", `<a href="https://8.8.8.8/">x</a>`, descr, `<p>x</p>`},
		{"ipv6", `<a href="https://[::1]/">x</a>`, descr, `<p>x</p>`},
		{"decimal ip", `<a href="https://2130706433/">x</a>`, descr, `<p>x</p>`},
		{"hex ip", `<a href="https://0x7f.0.0.1/">x</a>`, descr, `<p>x</p>`},
		{"localhost", `<a href="https://localhost/">x</a>`, descr, `<p>x</p>`},
		{"LOCALHOST", `<a href="https://LOCALHOST/x">x</a>`, descr, `<p>x</p>`},
		{"sub localhost", `<a href="https://app.localhost/">x</a>`, descr, `<p>x</p>`},
		{"empty host", `<a href="https:///path">x</a>`, descr, `<p>x</p>`},
		{"percent in host", `<a href="https://exa%6Dple.com/">x</a>`, descr, `<p>x</p>`},
		{"long href", `<a href="` + long + `">x</a>`, descr, `<p><a href="` + long + `">x</a></p>`},
		{"too long href", `<a href="` + long + `a">x</a>`, descr, `<p>x</p>`},
		{"empty href", `<a href="">x</a>`, descr, `<p>x</p>`},
		{"anchor without href", `<a name="x">x</a>`, descr, `<p>x</p>`},
		{"idn host is stored as punycode", `<a href="https://пример.рф/путь">x</a>`, descr, `<p><a href="https://xn--e1afmkfd.xn--p1ai/путь">x</a></p>`},

		// schemes per profile
		{"mailto in description drops the query", `<a href="mailto:Org@Example.com?subject=Hi">m</a>`, descr, `<p><a href="mailto:Org@example.com">m</a></p>`},
		{"mailto uppercase scheme", `<a href="MAILTO:a@b.com">m</a>`, descr, `<p><a href="mailto:a@b.com">m</a></p>`},
		{"mailto in email note", `<a href="mailto:a@b.com">m</a>`, email, `<p>m</p>`},
		{"mailto in session message", `<a href="mailto:a@b.com">m</a>`, sess, `<p>m</p>`},
		{"mailto without address", `<a href="mailto:">m</a>`, descr, `<p>m</p>`},
		{"mailto with two addresses", `<a href="mailto:a@b.com,c@d.com">m</a>`, descr, `<p>m</p>`},

		// headings and blockquote per profile
		{"headings in description", `<h1>A</h1><h2>B</h2><h3>C</h3><h4>D</h4><h5>E</h5><h6>F</h6>`, descr,
			`<h3>A</h3><h3>B</h3><h3>C</h3><h4>D</h4><h4>E</h4><h4>F</h4>`},
		{"heading keeps marks", `<h1>A <em>b</em></h1>`, descr, `<h3>A <em>b</em></h3>`},
		{"heading in email note", `<h1>A</h1><h3>B</h3>`, email, `<p><strong>A</strong></p><p><strong>B</strong></p>`},
		{"heading in session message", `<h2>A</h2>text`, sess, `<p><strong>A</strong></p><p>text</p>`},
		{"blockquote in description", `<blockquote>q</blockquote>`, descr, `<blockquote><p>q</p></blockquote>`},
		{"blockquote with paragraphs", `<blockquote><p>a</p><p>b <b>c</b></p></blockquote>`, descr, `<blockquote><p>a</p><p>b <strong>c</strong></p></blockquote>`},
		{"nested blockquote is flat", `<blockquote><blockquote>q</blockquote></blockquote>`, descr, `<blockquote><p>q</p></blockquote>`},
		{"blockquote in email note", `<blockquote>q</blockquote>`, email, `<p>q</p>`},
		{"blockquote in session message", `<blockquote><p>a</p><p>b</p></blockquote>`, sess, `<p>a</p><p>b</p>`},
		{"list inside blockquote", `<blockquote><ul><li>a</li><li>b</li></ul></blockquote>`, descr, `<blockquote><p>a</p><p>b</p></blockquote>`},

		// blocks
		{"divs", `<div>a</div><div>b</div>`, descr, `<p>a</p><p>b</p>`},
		{"table", `<table><tr><td>a</td><td>b</td></tr><tr><td>c</td></tr></table>`, descr, `<p>a</p><p>b</p><p>c</p>`},
		{"section and article", `<section>a<article>b</article></section>`, descr, `<p>a</p><p>b</p>`},
		{"other tags are unwrapped", `<span style="x">a</span> <font color="red">b</font><center>c</center>`, descr, `<p>a b</p><p>c</p>`},
		{"empty paragraphs", `<p></p><p> </p><p><br></p>x`, descr, `<p>x</p>`},
		{"empty link", `<a href="https://e.com"></a>`, descr, ``},
		{"link with only spaces", `<a href="https://e.com"> </a>x`, descr, `<p>x</p>`},
		{"link with only a break", `a<a href="https://e.com"><br></a>b`, descr, `<p>a<br>b</p>`},
		{"li outside a list", `<li>a</li><li>b</li>`, descr, `<p>a</p><p>b</p>`},
		{"list inside paragraph", `<p>a<ul><li>b</li></ul></p>`, descr, `<p>a</p><ul><li>b</li></ul>`},
		{"empty lists", `<ul></ul><ol><li></li><li> </li></ol>`, descr, ``},
		{"empty items are dropped", `<ul><li>a</li><li></li><li>b</li></ul>`, descr, `<ul><li>a</li><li>b</li></ul>`},
		{"paragraphs in a list item", `<ul><li><p>a</p><p>b</p></li></ul>`, descr, `<ul><li>a<br>b</li></ul>`},
		{"text between list tags", `<ul>x<li>a</li></ul>`, descr, `<ul><li>x</li><li>a</li></ul>`},
		{"ordered list", `<ol><li>a</li><li>b</li></ol>`, descr, `<ol><li>a</li><li>b</li></ol>`},
		{"nesting depth two is kept", `<ul><li>a<ol><li>b</li></ol></li></ul>`, descr, `<ul><li>a<ol><li>b</li></ol></li></ul>`},
		{"nesting depth three is flattened", `<ul><li>a<ul><li>b<ul><li>c<ul><li>d</li></ul></li></ul></li></ul></li></ul>`, descr,
			`<ul><li>a<ul><li>b</li><li>c</li><li>d</li></ul></li></ul>`},
		{"list directly in list", `<ul><li>a</li><ul><li>b</li></ul></ul>`, descr, `<ul><li>a<ul><li>b</li></ul></li></ul>`},
		{"heading inside list item", `<ul><li><h1>a</h1></li></ul>`, descr, `<ul><li><strong>a</strong></li></ul>`},
		{"link in list item", `<ul><li><a href="https://e.com">a</a> b</li></ul>`, descr, `<ul><li><a href="https://e.com">a</a> b</li></ul>`},
		{"zero-width joiner is allowed", "<p>\U0001F468\u200d\U0001F469</p>", descr, "<p>\U0001F468\u200d\U0001F469</p>"},
	}
}

func TestSanitize(t *testing.T) {
	for _, c := range sanitizeCases() {
		t.Run(c.name, func(t *testing.T) {
			got := mustSanitize(t, c.in, c.p)
			if got != c.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
			if again := mustSanitize(t, got, c.p); again != got {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

func TestSanitizeNeverEmitsOtherTags(t *testing.T) {
	allowed := map[string]bool{
		"p": true, "br": true, "strong": true, "em": true, "u": true, "a": true,
		"ul": true, "ol": true, "li": true, "h3": true, "h4": true, "blockquote": true,
	}
	tagRE := regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)([^>]*)>`)
	for _, c := range sanitizeCases() {
		out := mustSanitize(t, c.in, c.p)
		for _, m := range tagRE.FindAllStringSubmatch(out, -1) {
			if !allowed[m[2]] {
				t.Errorf("%s: tag <%s> in %q", c.name, m[2], out)
			}
			if m[2] != "a" && m[3] != "" {
				t.Errorf("%s: attributes on <%s> in %q", c.name, m[2], out)
			}
		}
	}
}

func TestSanitizeRejects(t *testing.T) {
	manyParas := strings.Repeat("<p><strong>a</strong></p>", 700) // 1399 visible, 17500 chars of HTML
	links := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString(`<a href="https://example.com/` + string(rune('a'+i)) + `">x</a> `)
		}
		return sb.String()
	}
	cases := []struct {
		name string
		in   string
		p    Profile
		want Reason
	}{
		{"control character", "<p>a\x01b</p>", descr, ReasonControlChars},
		{"nul", "a\x00b", descr, ReasonControlChars},
		{"escape", "a\x1bb", descr, ReasonControlChars},
		{"form feed is whitespace", "ab", descr, ""},
		{"c1 control", "a\u0085b", descr, ReasonControlChars},
		{"numeric entity control", "<p>a&#1;b</p>", descr, ReasonControlChars},
		{"bidi override", "a\u202eb", descr, ReasonControlChars},
		{"bidi override entity", "a&#x202E;b", descr, ReasonControlChars},
		{"embedding", "a\u202ab", descr, ReasonControlChars},
		{"pop directional formatting", "a\u202cb", descr, ReasonControlChars},
		{"tag character", "a\U000E0041b", descr, ReasonControlChars},
		{"tag character entity", "a&#xE0041;b", descr, ReasonControlChars},
		{"invisible times", "a\u2062b", descr, ReasonControlChars},
		{"function application", "a\u2061b", descr, ReasonControlChars},
		{"mongolian vowel separator", "a\u180eb", descr, ReasonControlChars},
		{"interlinear annotation", "a\ufff9b", descr, ReasonControlChars},
		{"del", "a\x7fb", descr, ReasonControlChars},
		// allowed or removed silently by the policy, never refused
		{"bidi isolate is removed", "a\u2066b", descr, ""},
		{"right-to-left mark is kept", "a\u200fb", descr, ""},
		{"bom is removed", "a\ufeffb", descr, ""},
		{"zero-width space is removed", "a\u200bb", descr, ""},
		{"zero-width space entity is removed", "a&#8203;b", descr, ""},
		{"word joiner is removed", "a\u2060b", descr, ""},
		{"line separator is a space", "a\u2028b", descr, ""},
		{"oversized art is refused first", "\xff" + strings.Repeat("a", 64001), descr, ReasonHTMLTooLong},
		{"bidi in an attribute", `<a href="https://e.com/` + "\u202e" + `">x</a>`, descr, ReasonControlChars},
		{"bidi inside a dropped element", "<script>\u202e</script>", descr, ReasonControlChars},
		{"invalid utf-8", "a\xffb", descr, ReasonBadEncoding},
		{"description at the limit", strings.Repeat("я", 4000), descr, ""},
		{"description over the limit", strings.Repeat("я", 4001), descr, ReasonTooLong},
		{"description limit counts break between paragraphs", strings.Repeat("a", 2000) + "<br>" + strings.Repeat("a", 2000), descr, ReasonTooLong},
		{"email note at the limit", strings.Repeat("a", 2000), email, ""},
		{"email note over the limit", strings.Repeat("a", 2001), email, ReasonTooLong},
		{"session message over the limit", strings.Repeat("a", 1001), sess, ReasonTooLong},
		{"session message at the limit", strings.Repeat("a", 1000), sess, ""},
		{"html too long", manyParas, descr, ReasonHTMLTooLong},
		{"20 links", links(20), descr, ""},
		{"21 links", links(21), descr, ReasonTooManyLinks},
		{"5 links in email note", links(5), email, ""},
		{"6 links in email note", links(6), email, ReasonTooManyLinks},
		{"3 links in session message", links(3), sess, ""},
		{"4 links in session message", links(4), sess, ReasonTooManyLinks},
		{"scheme word in email note", "<p>see data:foo</p>", email, ReasonSchemeWord},
		{"javascript word in email note", "<p>javascript:alert(1)</p>", email, ReasonSchemeWord},
		{"http word in email note", "<p>http://example.com</p>", email, ReasonSchemeWord},
		{"https is not a scheme word", "<p>https://example.com</p>", email, ""},
		{"metadata is not a scheme word", "<p>Metadata: bring a pass</p>", email, ""},
		{"scheme word in description is text", "<p>data:foo and http://x</p>", descr, ""},
		{"scheme word after an underscore", "<p>x_javascript:alert(1)</p>", email, ReasonSchemeWord},
		{"scheme word right after an underscore", "<p>_http://example.com</p>", email, ReasonSchemeWord},
		{"scheme word hidden by a zero-width space", "<p>java\u200bscript:alert(1)</p>", email, ReasonSchemeWord},
		{"scheme word in the middle of a word is not one", "<p>metadata:x and Привет</p>", email, ""},
		{"too deep", strings.Repeat("<div>", 300) + "x", descr, ReasonTooDeep},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Sanitize(c.in, c.p)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want reason %q, got no error", c.want)
			}
			if got := ReasonOf(err); got != c.want {
				t.Fatalf("reason = %q, want %q (%v)", got, c.want, err)
			}
			if !errors.Is(err, &Error{Reason: c.want}) {
				t.Fatalf("errors.Is does not match %q", c.want)
			}
			if err.Error() == "" {
				t.Fatal("empty message")
			}
		})
	}
	if ReasonOf(errors.New("other")) != "invalid" {
		t.Fatal("foreign error must give invalid")
	}
	if !errors.Is(ErrTooLong, ErrTooLong) || errors.Is(ErrTooLong, ErrHTMLTooLong) {
		t.Fatal("sentinel matching is wrong")
	}
}

func FuzzSanitize(f *testing.F) {
	for _, c := range sanitizeCases() {
		f.Add(c.in)
	}
	f.Add("<p>a</p>\x00<b")
	f.Fuzz(func(t *testing.T, in string) {
		for _, p := range []Profile{descr, email, sess} {
			// Whatever the input and the format, what Render returns parses
			// into the white list and nothing else.
			for _, format := range []string{FormatART, FormatText, ""} {
				out, _ := Render(in, format, p)
				assertSafeHTML(t, "Render "+format, out)
			}
			out, err := Sanitize(in, p)
			if err != nil {
				continue
			}
			assertSafeHTML(t, "Sanitize", out)
			again, err := Sanitize(out, p)
			if err != nil || again != out {
				t.Fatalf("not idempotent for %q: %q -> %q (%v)", in, out, again, err)
			}
			// What was stored is what is shown, and the same words.
			if shown, err := Render(out, FormatART, p); err != nil || !slices.Equal(Words(shown), Words(out)) {
				t.Fatalf("Render changes the words of %q: %q (%v)", out, shown, err)
			}
			for _, bad := range []string{"<script", "<img", "<iframe", "<svg", "<style", "<a href=\"javascript", "<a href=\"data"} {
				if strings.Contains(strings.ToLower(out), bad) {
					t.Fatalf("%q leaked in %q", bad, out)
				}
			}
		}
	})
}

type corpusCase struct {
	Name    string `json:"name"`
	Profile string `json:"profile"`
	Legacy  string `json:"legacy"`
	Art     string `json:"art"`
}

func loadCorpus(t *testing.T) []corpusCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/richtext_legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("the golden corpus has %d cases, want at least 40", len(cases))
	}
	return cases
}

func TestFromLegacyGoldenCorpus(t *testing.T) {
	for _, c := range loadCorpus(t) {
		t.Run(c.Name, func(t *testing.T) {
			p, ok := ProfileByName(c.Profile)
			if !ok {
				t.Fatalf("unknown profile %q", c.Profile)
			}
			if got := FromLegacy(c.Legacy, p); got != c.Art {
				t.Fatalf("FromLegacy(%q) =\n %q\nwant\n %q", c.Legacy, got, c.Art)
			}
		})
	}
}

func TestCorpusIsCanonicalAndStable(t *testing.T) {
	for _, c := range loadCorpus(t) {
		t.Run(c.Name, func(t *testing.T) {
			p, _ := ProfileByName(c.Profile)
			// The golden ART is what Sanitize would write.
			if got := mustSanitize(t, c.Art, p); got != c.Art {
				t.Fatalf("Sanitize(art) = %q, want the art itself %q", got, c.Art)
			}
			once, err := Normalize(c.Legacy, "text", p)
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if once != c.Art {
				t.Fatalf("Normalize(text) = %q, want %q", once, c.Art)
			}
			twice, err := Normalize(once, "art", p)
			if err != nil || twice != once {
				t.Fatalf("Normalize(Normalize(x,text),art) = %q (%v), want %q", twice, err, once)
			}
			// Reading never changes a stored text either.
			if got, want := mustRender(t, c.Legacy, "text", p), renderARTForTest(t, c.Art, p); got != want {
				t.Fatalf("Render(text) = %q, Render(art) = %q", got, want)
			}
			// The legacy form of the ART keeps the words.
			if !slices.Equal(Words(FromLegacy(ToLegacy(c.Art), p)), Words(c.Art)) {
				t.Fatalf("ToLegacy changed the words: %q", ToLegacy(c.Art))
			}
		})
	}
}

func renderARTForTest(t *testing.T, art string, p Profile) string {
	t.Helper()
	out, err := Render(art, "art", p)
	if err != nil {
		t.Fatalf("Render(art): %v", err)
	}
	return out
}

// mustRender is Render for a text that must be readable.
func mustRender(t *testing.T, stored, format string, p Profile) string {
	t.Helper()
	out, err := Render(stored, format, p)
	if err != nil {
		t.Fatalf("Render(%q, %q): %v", stored, format, err)
	}
	return out
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name, in, format string
		p                Profile
		want             string
		reason           Reason
	}{
		{"default format is text", "a **b**\n\nc", "", descr, `<p>a <strong>b</strong></p><p>c</p>`, ""},
		{"text", "a < b", "text", descr, `<p>a &lt; b</p>`, ""},
		{"text keeps typed tags as text", "<b>x</b>", "text", descr, `<p>&lt;b&gt;x&lt;/b&gt;</p>`, ""},
		{"art", "<b>x</b>", "art", descr, `<p><strong>x</strong></p>`, ""},
		{"art with a lone less-than", "a < b", "art", descr, `<p>a &lt; b</p>`, ""},
		{"unknown format", "x", "html", descr, "", ReasonInvalidFormat},
		{"upper case format is unknown", "x", "ART", descr, "", ReasonInvalidFormat},
		{"text control characters", "a\x01b", "text", descr, "", ReasonControlChars},
		{"text bidi", "a\u202eb", "text", descr, "", ReasonControlChars},
		{"text bad encoding", "a\xffb", "text", descr, "", ReasonBadEncoding},
		{"text too long", strings.Repeat("a", 4001), "text", descr, "", ReasonTooLong},
		{"text email note auto-links", "see https://example.com/x.", "text", email, `<p>see <a href="https://example.com/x">https://example.com/x</a>.</p>`, ""},
		{"text email note scheme word", "javascript:alert(1)", "text", email, "", ReasonSchemeWord},
		{"text email note http word", "http://example.com", "text", email, "", ReasonSchemeWord},
		{"text description http word is text", "http://example.com", "text", descr, `<p>http://example.com</p>`, ""},
		{"empty text", "  ", "text", descr, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(c.in, c.format, c.p)
			if c.reason != "" {
				if ReasonOf(err) != c.reason {
					t.Fatalf("err = %v, want reason %q", err, c.reason)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("Normalize = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestNormalizeIdempotentForSanitizeCases(t *testing.T) {
	for _, c := range sanitizeCases() {
		once, err := Normalize(c.in, "art", c.p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		twice, err := Normalize(once, "art", c.p)
		if err != nil || twice != once {
			t.Fatalf("%s: %q -> %q (%v)", c.name, once, twice, err)
		}
		// The same raw string read as plain text is also stable.
		if txt, err := Normalize(c.in, "text", c.p); err == nil {
			again, err := Normalize(txt, "art", c.p)
			if err != nil || again != txt {
				t.Fatalf("%s: text %q -> %q (%v)", c.name, txt, again, err)
			}
		}
	}
}

func TestRender(t *testing.T) {
	blank := ` rel="noopener noreferrer" target="_blank"`
	cases := []struct {
		name, stored, format string
		p                    Profile
		want                 string
	}{
		{"art link gets rel and target", `<p><a href="https://e.com/x">l</a></p>`, "art", descr,
			`<p><a href="https://e.com/x"` + blank + `>l</a></p>`},
		{"art mailto gets them too", `<a href="mailto:a@b.com">m</a>`, "art", descr,
			`<p><a href="mailto:a@b.com"` + blank + `>m</a></p>`},
		{"hostile art", `<p onclick="x()">a</p><script>alert(1)</script><img src=x onerror=y><a href="javascript:alert(1)" onclick="z()">b</a>`, "art", descr,
			`<p>a</p><p>b</p>`},
		{"hostile art with an attacker rel", `<a href="https://e.com" rel="opener" target="_top">x</a>`, "art", descr,
			`<p><a href="https://e.com"` + blank + `>x</a></p>`},
		{"text is converted first", "**b**\n\n- a\n- b", "text", descr,
			`<p><strong>b</strong></p><ul><li>a</li><li>b</li></ul>`},
		{"empty format is text", "a\nb", "", descr, `<p>a<br>b</p>`},
		{"unknown format is text, never markup", "<b>x</b>", "html", descr, `<p>&lt;b&gt;x&lt;/b&gt;</p>`},
		{"text with a typed script", "<script>alert(1)</script>", "text", descr, `<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>`},
		{"email note text links", "see https://e.com/x", "text", email,
			`<p>see <a href="https://e.com/x"` + blank + `>https://e.com/x</a></p>`},
		{"art profile restricts headings", `<h3>t</h3>`, "art", email, `<p><strong>t</strong></p>`},
		{"art profile restricts schemes", `<a href="mailto:a@b.com">m</a>`, "art", sess, `<p>m</p>`},
		{"invalid utf-8 is dropped", "a\xffb", "text", descr, `<p>ab</p>`},
		{"empty", "", "art", descr, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mustRender(t, c.stored, c.format, c.p); got != c.want {
				t.Fatalf("Render = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("limits are not enforced when reading", func(t *testing.T) {
		long := strings.Repeat("a", 5000)
		if got := mustRender(t, long, "text", descr); got != "<p>"+long+"</p>" {
			t.Fatalf("a long stored text was cut: %d", len(got))
		}
		if got := mustRender(t, "<p>"+long+"</p>", "art", sess); got != "<p>"+long+"</p>" {
			t.Fatalf("a long stored art was cut: %d", len(got))
		}
	})
	t.Run("too many links still show", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < 8; i++ {
			sb.WriteString(`<a href="https://e.com/` + string(rune('a'+i)) + `">x</a> `)
		}
		if got := mustRender(t, sb.String(), "art", sess); strings.Count(got, "<a ") != 8 {
			t.Fatalf("links lost: %q", got)
		}
	})
	t.Run("a hostile stored value never yields unsafe html", func(t *testing.T) {
		for _, c := range sanitizeCases() {
			for _, f := range []string{"art", "text", ""} {
				out, _ := Render(c.in, f, c.p)
				assertSafeHTML(t, c.name+"/"+f, out)
			}
		}
	})
}

// assertSafeHTML parses out and fails on any element, attribute or link
// target outside what Render may write.
func assertSafeHTML(t *testing.T, name, out string) {
	t.Helper()
	nodes, err := html.ParseFragment(strings.NewReader(out), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	allowed := map[string]bool{
		"p": true, "br": true, "strong": true, "em": true, "u": true, "a": true,
		"ul": true, "ol": true, "li": true, "h3": true, "h4": true, "blockquote": true,
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if !allowed[n.Data] {
				t.Fatalf("%s: element <%s> in %q", name, n.Data, out)
			}
			for _, a := range n.Attr {
				switch {
				case n.Data == "a" && a.Key == "href":
					if !strings.HasPrefix(a.Val, "https://") && !strings.HasPrefix(a.Val, "mailto:") {
						t.Fatalf("%s: href %q in %q", name, a.Val, out)
					}
				case n.Data == "a" && a.Key == "rel" && a.Val == "noopener noreferrer":
				case n.Data == "a" && a.Key == "target" && a.Val == "_blank":
				default:
					t.Fatalf("%s: attribute %s on <%s> in %q", name, a.Key, n.Data, out)
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
}
func TestPlainText(t *testing.T) {
	cases := []struct {
		name, art, want string
	}{
		{"paragraphs", `<p>One</p><p>Two</p>`, "One\n\nTwo"},
		{"marks vanish", `<p>A <strong>b</strong> <em>c</em> <u>d</u></p>`, "A b c d"},
		{"break", `<p>a<br>b</p>`, "a\nb"},
		{"heading is its own paragraph", `<h3>Title</h3><p>text</p><h4>Sub</h4>`, "Title\n\ntext\n\nSub"},
		{"blockquote has no marker", `<blockquote><p>q1</p><p>q2</p></blockquote><p>after</p>`, "q1\n\nq2\n\nafter"},
		{"lists", `<ul><li>a</li><li>b</li></ul><ol><li>x</li><li>y</li></ol>`, "• a\n• b\n\n1. x\n2. y"},
		{"nested list", `<ul><li>a<ol><li>b</li><li>c</li></ol></li><li>d</li></ul>`, "• a\n  1. b\n  2. c\n• d"},
		{"link with text", `<p>See <a href="https://e.com/x">the site</a>.</p>`, "See the site (https://e.com/x)."},
		{"link text equals href", `<p><a href="https://e.com/x">https://e.com/x</a></p>`, "https://e.com/x"},
		{"link text equals href in another case", `<p><a href="https://example.com/x">HTTPS://Example.com/x</a></p>`, "HTTPS://Example.com/x"},
		{"mailto link", `<p><a href="mailto:a@b.com">write</a> <a href="mailto:a@b.com">a@b.com</a></p>`, "write (a@b.com) a@b.com"},
		{"escaped text is text", `<p>a &lt; b &amp; c</p>`, "a < b & c"},
		{"hostile input", `<p>a</p><script>alert(1)</script>`, "a"},
		{"list item with a break", `<ul><li>a<br>b</li><li>c</li></ul>`, "• a b\n• c"},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PlainText(c.art); got != c.want {
				t.Fatalf("PlainText = %q, want %q", got, c.want)
			}
		})
	}
}

func TestToLegacy(t *testing.T) {
	cases := []struct {
		name, art, want string
	}{
		{"marks", `<p>A <strong>bold</strong> and <em>it</em> word</p>`, "A **bold** and _it_ word"},
		{"both", `<p><strong>a <em>b</em></strong></p>`, "**a _b_**"},
		{"underline is dropped", `<p>a <u>b</u> c</p>`, "a b c"},
		{"spaces move out of the marks", `<p><strong>a </strong>b</p>`, "**a** b"},
		{"lists", `<ul><li>a</li><li>b</li></ul><ol><li>x</li><li>y</li></ol>`, "- a\n- b\n\n1. x\n2. y"},
		{"headings and quotes become paragraphs", `<h3>T</h3><blockquote><p>q</p></blockquote><p>p</p>`, "T\n\nq\n\np"},
		{"link", `<p><a href="https://e.com/x">site</a></p>`, "site (https://e.com/x)"},
		{"link that is its address", `<p><a href="https://e.com/x">https://e.com/x</a></p>`, "https://e.com/x"},
		{"break", `<p>a<br>b</p>`, "a\nb"},
		{"typed tags stay text", `<p>&lt;b&gt;x&lt;/b&gt; a &lt; b</p>`, "<b>x</b> a < b"},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToLegacy(c.art); got != c.want {
				t.Fatalf("ToLegacy = %q, want %q", got, c.want)
			}
		})
	}
}

func TestToLegacyRoundTrip(t *testing.T) {
	arts := []string{
		`<p>A <strong>bold</strong> and <em>it</em> word</p>`,
		`<p><strong>bold and <em>both</em> here</strong></p>`,
		`<p>Line-up:</p><ul><li>Anna — <strong>piano</strong></li><li>Boris</li><li>Clara</li></ul>`,
		`<ol><li>a</li><li>b</li><li>c</li></ol><p>after <em>it</em></p>`,
		`<p>a<br>b</p><p>c</p>`,
		`<p>שלום <strong>עולם</strong> <em>טוב</em></p><ul><li>אחד</li><li>שניים</li></ul>`,
		`<p>Привет, <strong>мир</strong>! <em>Курсив</em> тут.</p>`,
		`<p>a &lt; b &amp; <b>no</b></p>`,
	}
	for _, art := range arts {
		clean := mustSanitize(t, art, descr)
		back, err := Normalize(ToLegacy(clean), "text", descr)
		if err != nil {
			t.Fatalf("%q: %v", art, err)
		}
		if back != clean {
			t.Fatalf("round trip of %q:\n legacy %q\n got    %q", clean, ToLegacy(clean), back)
		}
	}
	// A link survives as its address in the email-note profile.
	art := `<p>see <a href="https://e.com/x">https://e.com/x</a></p>`
	back, _ := Normalize(ToLegacy(art), "text", email)
	if back != art {
		t.Fatalf("link round trip: %q", back)
	}
}

func TestToTelegramHTML(t *testing.T) {
	cases := []struct {
		name, art, want string
	}{
		{"marks", `<p>a <strong>b</strong> <em>c</em> <u>d</u></p>`, "a <b>b</b> <i>c</i> <u>d</u>"},
		{"nesting", `<p><strong><em>x</em></strong></p>`, "<b><i>x</i></b>"},
		{"escaping", `<p>a &lt; b &amp; c &gt; d &lt;b&gt;x&lt;/b&gt;</p>`, "a &lt; b &amp; c &gt; d &lt;b&gt;x&lt;/b&gt;"},
		{"link", `<p><a href="https://e.com/?a=1&amp;b=2">l&amp;m</a></p>`, `<a href="https://e.com/?a=1&amp;b=2">l&amp;m</a>`},
		{"paragraphs", `<p>a</p><p>b</p>`, "a\n\nb"},
		{"break", `<p>a<br>b</p>`, "a\nb"},
		{"heading is a bold line", `<h3>T</h3><p>x</p><h4>S <strong>b</strong></h4>`, "<b>T</b>\n\nx\n\n<b>S b</b>"},
		{"blockquote", `<blockquote><p>q1</p><p>q2</p></blockquote><p>x</p>`, "<blockquote>q1\nq2</blockquote>\n\nx"},
		{"lists", `<ul><li>a</li><li><strong>b</strong></li></ul><ol><li>x</li><li>y</li></ol>`, "• a\n• <b>b</b>\n\n1. x\n2. y"},
		{"nested list", `<ul><li>a<ul><li>b</li></ul></li></ul>`, "• a\n  • b"},
		{"mailto is text", `<p><a href="mailto:a@b.com">write</a></p>`, "write (a@b.com)"},
		{"mailto that is its address", `<p><a href="mailto:a@b.com">a@b.com</a></p>`, "a@b.com"},
		{"hostile", `<p>a</p><script>alert(1)</script><img src=x onerror=y>`, "a"},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToTelegramHTML(c.art); got != c.want {
				t.Fatalf("ToTelegramHTML = %q, want %q", got, c.want)
			}
		})
	}
	// Only the tags Telegram accepts, whatever the input.
	tagRE := regexp.MustCompile(`</?([a-zA-Z0-9]+)`)
	for _, c := range sanitizeCases() {
		out := ToTelegramHTML(mustSanitize(t, c.in, c.p))
		for _, m := range tagRE.FindAllStringSubmatch(out, -1) {
			switch m[1] {
			case "b", "i", "u", "a", "blockquote":
			default:
				t.Errorf("%s: <%s> in %q", c.name, m[1], out)
			}
		}
	}
}

func TestWords(t *testing.T) {
	cases := []struct {
		name, art string
		want      []string
	}{
		{"marks and punctuation", `<p>Hello, <strong>wor</strong>ld! It's 5-3.</p>`, []string{"Hello", ",", "world", "!", "It", "'", "s", "5", "-", "3", "."}},
		{"lists and empty items", `<ul><li>a</li><li></li><li>b c</li></ul><ol><li>d</li></ol>`, []string{"a", "b", "c", "1", ".", "d"}},
		{"breaks and paragraphs", `<p>a<br>b</p><p>c</p>`, []string{"a", "b", "c"}},
		{"nfc", "<p>e\u0301 \u00e9</p>", []string{"\u00e9", "\u00e9"}},
		{"hebrew", `<p>שָׁלוֹם עולם־טוב</p>`, []string{"שָׁלוֹם", "עולם", "־", "טוב"}},
		{"links count text only", `<p><a href="https://e.com">site</a></p>`, []string{"site"}},
		{"headings and quotes", `<h3>T t</h3><blockquote><p>q</p></blockquote>`, []string{"T", "t", "q"}},
		{"empty", ``, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Words(c.art)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("Words = %q, want %q", got, c.want)
			}
		})
	}
}

func TestWordsEqualForTextAndFormattedVersion(t *testing.T) {
	text := "Концерт в субботу\nВход свободный\n\nПрограмма:\n- Бах\n- Моцарт\n- Бетховен\n\nПриходите: 5*3 = 15"
	formatted := `<p>Концерт в <strong>субботу</strong><br>Вход <em>свободный</em></p>` +
		`<h3>Программа:</h3><ul><li>Бах</li><li><strong>Моцарт</strong></li><li>Бетховен</li></ul>` +
		`<p>Приходите: 5*3 = 15</p>`
	want := Words(FromLegacy(text, descr))
	got := Words(mustSanitize(t, formatted, descr))
	if !slices.Equal(got, want) {
		t.Fatalf("words differ:\n got  %q\n want %q", got, want)
	}
	// A changed word is seen.
	changed := strings.Replace(formatted, "Бах", "Бахх", 1)
	if slices.Equal(Words(mustSanitize(t, changed, descr)), want) {
		t.Fatal("a changed word was not detected")
	}
}

func TestVisibleLen(t *testing.T) {
	if n := VisibleLen(`<p>абв</p><p>де<br>ж</p><ul><li>з</li></ul>`); n != 3+1+2+1+1+1+1 {
		t.Fatalf("VisibleLen = %d", n)
	}
	if VisibleLen("") != 0 {
		t.Fatal("empty text is not 0")
	}
}

func TestFromLegacyMiscRules(t *testing.T) {
	cases := []struct {
		name, in string
		p        Profile
		want     string
	}{
		{"italic at the start of a bold part", "**_x_ y**", descr, `<p><strong><em>x</em> y</strong></p>`},
		{"underscore pairs with more than one word", "_a b c_", descr, `<p><em>a b c</em></p>`},
		{"two italics", "_a_ and _b_", descr, `<p><em>a</em> and <em>b</em></p>`},
		{"italic with spaces inside is text", "_ a _", descr, `<p>_ a _</p>`},
		{"lone underscore", "a _ b", descr, `<p>a _ b</p>`},
		{"bold across lines is text", "**a\nb**", descr, `<p>**a<br>b**</p>`},
		{"bold followed by list marker line", "**Title**\n- a\n- b", descr, `<p><strong>Title</strong></p><ul><li>a</li><li>b</li></ul>`},
		{"bullet needs a space", "-a\n-b", descr, `<p>-a<br>-b</p>`},
		{"bullet with empty body", "-\n- a", descr, `<p>-<br>- a</p>`},
		{"number list with 3 digits cap", "1000. a\n1001. b", descr, `<p>1000. a<br>1001. b</p>`},
		{"control characters survive for Sanitize to refuse", "a\x01b", descr, "<p>a\x01b</p>"},
		{"no autolink without a scheme", "example.com", email, `<p>example.com</p>`},
		{"autolink with a port is not linked", "https://example.com:8080/x", email, `<p>https://example.com:8080/x</p>`},
		{"autolink stops at a quote", `"https://example.com/x"`, email, `<p>"<a href="https://example.com/x">https://example.com/x</a>"</p>`},
		{"autolink cyrillic punctuation", "«https://example.com/x»", email, `<p>«<a href="https://example.com/x">https://example.com/x</a>»</p>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FromLegacy(c.in, c.p); got != c.want {
				t.Fatalf("FromLegacy(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// FromLegacy writes the canonical form itself: Sanitize must not change it,
// for any input the profile accepts.
func FuzzFromLegacy(f *testing.F) {
	for _, c := range loadCorpusForFuzz() {
		f.Add(c)
	}
	f.Add("**a _b_ c**\n- x\n- y\n\n1. q\n2. w https://e.com/a_b)")
	f.Fuzz(func(t *testing.T, in string) {
		for _, p := range []Profile{descr, email, sess} {
			out := FromLegacy(in, p)
			clean, err := Sanitize(out, p.readOnly())
			if err != nil {
				continue // control characters
			}
			if clean != out {
				t.Fatalf("FromLegacy(%q) = %q, Sanitize changes it to %q", in, out, clean)
			}
			assertSafeHTML(t, "FromLegacy", out)
			// Both sides agree on the words, which is what the AI check relies on.
			if !slices.Equal(Words(clean), Words(out)) {
				t.Fatalf("Words(Sanitize(FromLegacy(%q))) = %q, Words(FromLegacy) = %q", in, Words(clean), Words(out))
			}
			for _, format := range []string{FormatART, FormatText} {
				shown, _ := Render(in, format, p)
				assertSafeHTML(t, "Render "+format, shown)
			}
		}
	})
}

func loadCorpusForFuzz() []string {
	raw, err := os.ReadFile("testdata/richtext_legacy.json")
	if err != nil {
		return nil
	}
	var cases []corpusCase
	if json.Unmarshal(raw, &cases) != nil {
		return nil
	}
	out := make([]string, 0, len(cases))
	for _, c := range cases {
		out = append(out, c.Legacy)
	}
	return out
}
