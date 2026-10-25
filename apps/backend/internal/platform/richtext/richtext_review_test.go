package richtext

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ---- the one character policy ----

func TestCharPolicyTable(t *testing.T) {
	cases := []struct {
		r    rune
		want charAction
	}{
		{'a', charPlain}, {'я', charPlain}, {'ש', charPlain}, {'\t', charPlain}, {'\n', charPlain},
		{'\r', charPlain}, {'\f', charPlain}, {' ', charPlain}, {0xa0, charPlain}, {0x1f600, charPlain},
		// kept
		{0x200e, charKeep}, {0x200f, charKeep}, {0x061c, charKeep}, {0x200c, charKeep},
		{0x200d, charKeep}, {0x00ad, charKeep},
		// removed silently
		{0x2066, charStrip}, {0x2067, charStrip}, {0x2068, charStrip}, {0x2069, charStrip},
		{0xfeff, charStrip}, {0x200b, charStrip}, {0x2060, charStrip},
		// refused
		{0x00, charRefuse}, {0x01, charRefuse}, {0x0b, charRefuse}, {0x1b, charRefuse}, {0x1f, charRefuse},
		{0x7f, charRefuse}, {0x80, charRefuse}, {0x85, charRefuse}, {0x9f, charRefuse},
		{0x202a, charRefuse}, {0x202b, charRefuse}, {0x202c, charRefuse}, {0x202d, charRefuse}, {0x202e, charRefuse},
		{0xe0000, charRefuse}, {0xe0041, charRefuse}, {0xe007f, charRefuse},
		{0x2061, charRefuse}, {0x2062, charRefuse}, {0x2063, charRefuse}, {0x2064, charRefuse},
		{0x180e, charRefuse}, {0xfff9, charRefuse}, {0xfffa, charRefuse}, {0xfffb, charRefuse},
		// line and paragraph separators
		{0x2028, charLineSep}, {0x2029, charParaSep},
	}
	for _, c := range cases {
		if got := charPolicy(c.r); got != c.want {
			t.Errorf("charPolicy(U+%04X) = %d, want %d", c.r, got, c.want)
		}
	}
}

// TestCharPolicyIsShared checks that Sanitize, Normalize (text, which is
// FromLegacy plus Sanitize) and Render treat one character the same way.
func TestCharPolicyIsShared(t *testing.T) {
	cases := []struct {
		name string
		r    rune
		// refused: Sanitize and Normalize fail and Render drops the character.
		refused bool
		art     string
		text    string
	}{
		{name: "lrm", r: 0x200e, art: "<p>a\u200eb</p>", text: "<p>a\u200eb</p>"},
		{name: "rlm", r: 0x200f, art: "<p>a\u200fb</p>", text: "<p>a\u200fb</p>"},
		{name: "alm", r: 0x061c, art: "<p>a\u061cb</p>", text: "<p>a\u061cb</p>"},
		{name: "zwnj", r: 0x200c, art: "<p>a\u200cb</p>", text: "<p>a\u200cb</p>"},
		{name: "zwj", r: 0x200d, art: "<p>a\u200db</p>", text: "<p>a\u200db</p>"},
		{name: "soft hyphen", r: 0x00ad, art: "<p>a\u00adb</p>", text: "<p>a\u00adb</p>"},
		{name: "lri", r: 0x2066, art: "<p>ab</p>", text: "<p>ab</p>"},
		{name: "rli", r: 0x2067, art: "<p>ab</p>", text: "<p>ab</p>"},
		{name: "fsi", r: 0x2068, art: "<p>ab</p>", text: "<p>ab</p>"},
		{name: "pdi", r: 0x2069, art: "<p>ab</p>", text: "<p>ab</p>"},
		{name: "bom", r: 0xfeff, art: "<p>ab</p>", text: "<p>ab</p>"},
		{name: "zwsp", r: 0x200b, art: "<p>ab</p>", text: "<p>ab</p>"},
		{name: "word joiner", r: 0x2060, art: "<p>ab</p>", text: "<p>ab</p>"},
		{name: "line separator", r: 0x2028, art: "<p>a b</p>", text: "<p>a<br>b</p>"},
		{name: "paragraph separator", r: 0x2029, art: "<p>a b</p>", text: "<p>a</p><p>b</p>"},
		{name: "lre", r: 0x202a, refused: true},
		{name: "rle", r: 0x202b, refused: true},
		{name: "pdf", r: 0x202c, refused: true},
		{name: "lro", r: 0x202d, refused: true},
		{name: "rlo", r: 0x202e, refused: true},
		{name: "tag a", r: 0xe0041, refused: true},
		{name: "tag cancel", r: 0xe007f, refused: true},
		{name: "function application", r: 0x2061, refused: true},
		{name: "invisible plus", r: 0x2064, refused: true},
		{name: "mongolian vowel separator", r: 0x180e, refused: true},
		{name: "interlinear anchor", r: 0xfff9, refused: true},
		{name: "interlinear terminator", r: 0xfffb, refused: true},
		{name: "nul", r: 0x00, refused: true},
		{name: "escape", r: 0x1b, refused: true},
		{name: "del", r: 0x7f, refused: true},
		{name: "c1", r: 0x85, refused: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			artIn := "<p>a" + string(c.r) + "b</p>"
			textIn := "a" + string(c.r) + "b"
			art, artErr := Sanitize(artIn, descr)
			text, textErr := Normalize(textIn, "text", descr)
			renderedArt, _ := Render(artIn, "art", descr)
			renderedText, _ := Render(textIn, "text", descr)
			if c.refused {
				if ReasonOf(artErr) != ReasonControlChars || ReasonOf(textErr) != ReasonControlChars {
					t.Fatalf("not refused: %v / %v", artErr, textErr)
				}
				// Reading never fails on it: the character is dropped.
				if renderedArt != "<p>ab</p>" || renderedText != "<p>ab</p>" {
					t.Fatalf("Render = %q / %q, want the text without it", renderedArt, renderedText)
				}
				return
			}
			if artErr != nil || textErr != nil {
				t.Fatalf("refused: %v / %v", artErr, textErr)
			}
			if art != c.art || text != c.text {
				t.Fatalf("Sanitize = %q, Normalize = %q; want %q and %q", art, text, c.art, c.text)
			}
			// Writing and reading agree, and a stored text is stable.
			wantArt := c.art
			if got, _ := Render(art, "art", descr); got != wantArt {
				t.Fatalf("Render(art) = %q, want %q", got, wantArt)
			}
			if got, _ := Render(textIn, "text", descr); got != c.text {
				t.Fatalf("Render(text) = %q, want %q", got, c.text)
			}
			if again, err := Sanitize(art, descr); err != nil || again != art {
				t.Fatalf("Sanitize is not stable: %q -> %q (%v)", art, again, err)
			}
			if again, err := Normalize(text, "art", descr); err != nil || again != text {
				t.Fatalf("Normalize(art) is not stable: %q -> %q (%v)", text, again, err)
			}
			// What FromLegacy and Sanitize call the words is the same.
			if c.r != 0x2028 && c.r != 0x2029 {
				if !slices.Equal(Words(art), Words(text)) {
					t.Fatalf("Words differ: %q and %q", Words(art), Words(text))
				}
			}
		})
	}
}

func TestPolicyKeepsHebrewDirectionMarks(t *testing.T) {
	// A Vino&Co description: Hebrew with LRM and RLM around Latin words, a
	// price and a link.
	text := "ערב יין\u200f ב-Vino&Co\u200e (Tel Aviv)\u200f\n\nמחיר: 120₪\u200f"
	want := "<p>ערב יין\u200f ב-Vino&amp;Co\u200e (Tel Aviv)\u200f</p><p>מחיר: 120₪\u200f</p>"
	got, err := Normalize(text, "text", descr)
	if err != nil || got != want {
		t.Fatalf("Normalize(text) = %q, %v; want %q", got, err, want)
	}
	art, err := Normalize(want, "art", descr)
	if err != nil || art != want {
		t.Fatalf("Normalize(art) = %q, %v", art, err)
	}
	if r, err := Render(want, "art", descr); err != nil || !strings.Contains(r, "\u200f") || !strings.Contains(r, "\u200e") {
		t.Fatalf("Render lost the marks: %q (%v)", r, err)
	}
	if r, err := Render(text, "text", descr); err != nil || r != want {
		t.Fatalf("Render(text) = %q (%v)", r, err)
	}
	if !slices.Equal(Words(want), Words(FromLegacy(text, descr))) {
		t.Fatal("Words differ")
	}
	// The marks survive the legacy round trip.
	back, err := Normalize(ToLegacy(want), "text", descr)
	if err != nil || back != want {
		t.Fatalf("round trip = %q (%v)", back, err)
	}
}

func TestPolicyInsideHref(t *testing.T) {
	for _, r := range []rune{0x200e, 0x200f, 0x061c, 0x200c, 0x200d, 0x00ad, 0x2066, 0x2069, 0xfeff, 0x200b, 0x2060, 0x2028, 0x2029} {
		in := `<a href="https://e.com/` + string(r) + `x">t</a>`
		if got := mustSanitize(t, in, descr); got != "<p>t</p>" {
			t.Errorf("U+%04X in a link target: %q", r, got)
		}
		in = `<a href="https://e` + string(r) + `.com/">t</a>`
		if got := mustSanitize(t, in, descr); got != "<p>t</p>" {
			t.Errorf("U+%04X in a host: %q", r, got)
		}
		in = `<a href="mailto:a` + string(r) + `@e.com">t</a>`
		if got := mustSanitize(t, in, descr); got != "<p>t</p>" {
			t.Errorf("U+%04X in an address: %q", r, got)
		}
	}
}

// ---- hosts ----

func TestHostNormalisation(t *testing.T) {
	cases := []struct {
		name, href, want string // want is the stored href, "" for an unwrapped link
	}{
		{"fullwidth ipv4", "https://１２７.０.０.１/", ""},
		{"fullwidth ipv4 with a path", "https://１２７.０.０.１/admin", ""},
		{"ideographic full stops", "https://127。0。0。1/", ""},
		{"fullwidth decimal ip", "https://２１３０７０６４３３/", ""},
		{"fullwidth localhost", "https://ｌｏｃａｌｈｏｓｔ/", ""},
		{"fullwidth upper case localhost", "https://ＬＯＣＡＬＨＯＳＴ/x", ""},
		{"subdomain of fullwidth localhost", "https://app.ｌｏｃａｌｈｏｓｔ/", ""},
		{"localhost with a final dot", "https://localhost./", ""},
		{"fullwidth colon", "https://example.com：8080/", ""},
		{"fullwidth at sign", "https://user＠example.com/", ""},
		{"fullwidth letters become ascii", "https://ｅｘａｍｐｌｅ.ｃｏｍ/p", "https://example.com/p"},
		{"upper case", "https://EXAMPLE.Com/P", "https://example.com/P"},
		{"cyrillic", "https://пример.рф/путь", "https://xn--e1afmkfd.xn--p1ai/путь"},
		{"umlaut", "https://münchen.de/x", "https://xn--mnchen-3ya.de/x"},
		{"hebrew", "https://יין.קום/", "https://xn--eebas.xn--9dbq2a/"},
		{"underscore is not a host character", "https://exa_mple.com/", ""},
		{"leading hyphen", "https://-bad.com/", ""},
		{"trailing dot is dropped", "https://example.com./x", "https://example.com/x"},
		{"empty label", "https://a..com/", ""},
		{"two final dots", "https://example.com../", ""},
		{"fuzz regression: numeric host with two dots", "https://0..#", ""},
		{"only a dot", "https://./", ""},
		{"numeric last label", "https://example.123/", ""},
		{"ipv4 octal", "https://0177.0.0.1/", ""},
		{"ascii ipv4 still refused", "https://127.0.0.1/", ""},
		{"public name with digits", "https://8x8.com/", "https://8x8.com/"},
		{"soft hyphen in a host", "https://exam\u00adple.com/", ""},
		{"too long host", "https://" + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + "." + strings.Repeat("e", 60) + ".com/", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mustSanitize(t, `<a href="`+c.href+`">x</a>`, descr)
			want := "<p>x</p>"
			if c.want != "" {
				want = `<p><a href="` + c.want + `">x</a></p>`
			}
			if got != want {
				t.Fatalf("Sanitize = %q, want %q", got, want)
			}
		})
	}
	// The same rule for an address typed into an email note.
	got, _ := Normalize("see https://１２７.０.０.１/ and https://ｌｏｃａｌｈｏｓｔ/x", "text", email)
	if strings.Contains(got, "<a ") {
		t.Fatalf("an auto-linked loopback address: %q", got)
	}
	// An IDN address typed into an email note is linked in its ASCII form.
	got, err := Normalize("see https://пример.рф/x", "text", email)
	if err != nil || got != `<p>see <a href="https://xn--e1afmkfd.xn--p1ai/x">https://пример.рф/x</a></p>` {
		t.Fatalf("auto-linked idn: %q (%v)", got, err)
	}
}

// ---- mailto ----

func TestMailto(t *testing.T) {
	cases := []struct {
		name, href, want string // want is the stored href, "" for an unwrapped link
	}{
		{"plain", "mailto:a@b.com", "mailto:a@b.com"},
		{"query is dropped", "mailto:a@b.com?subject=Hi", "mailto:a@b.com"},
		{"bcc is dropped", "mailto:a@good.com?bcc=x@evil.com&body=hello", "mailto:a@good.com"},
		{"cc and to are dropped", "mailto:a@good.com?cc=x@evil.com&to=y@evil.com", "mailto:a@good.com"},
		{"encoded line break in the query", "mailto:a@b.com?subject=x%0D%0ABcc:x@evil.com", "mailto:a@b.com"},
		{"empty query", "mailto:a@b.com?", "mailto:a@b.com"},
		{"query only", "mailto:?to=x@evil.com", ""},
		{"encoded line break after the address", "mailto:a@b.com%0D%0Abcc:x@evil.com", ""},
		{"encoded line break in the local part", "mailto:a%0D%0A@b.com", ""},
		{"percent in the local part", "mailto:a%40evil.com@b.com", ""},
		{"two addresses with a comma", "mailto:a@b.com,c@d.com", ""},
		{"two addresses with a semicolon", "mailto:a@b.com;c@d.com", ""},
		{"comma in the local part", "mailto:a,b@c.com", ""},
		{"quoted local part", `mailto:"a"@b.com`, ""},
		{"space in the local part", "mailto:a b@c.com", ""},
		{"angle brackets", "mailto:<a@b.com>", ""},
		{"parentheses in the local part", "mailto:a(b)@c.com", ""},
		{"colon in the local part", "mailto:a:b@c.com", ""},
		{"leading dot", "mailto:.a@b.com", ""},
		{"trailing dot", "mailto:a.@b.com", ""},
		{"double dot", "mailto:a..b@c.com", ""},
		{"fragment", "mailto:a@b.com#x", ""},
		{"no at sign", "mailto:abc", ""},
		{"two at signs", "mailto:a@b@c.com", ""},
		{"no local part", "mailto:@b.com", ""},
		{"atext and dots", "mailto:First.Last+tag_1@Example.COM", "mailto:First.Last+tag_1@example.com"},
		{"atext characters", "mailto:a!#$&'*+-/=^_`{|}~b@e.com", "mailto:a!#$&'*+-/=^_`{|}~b@e.com"},
		{"local part too long", "mailto:" + strings.Repeat("a", 65) + "@b.com", ""},
		{"loopback host", "mailto:a@127.0.0.1", ""},
		{"fullwidth loopback host", "mailto:a@１２７.０.０.１", ""},
		{"localhost", "mailto:a@localhost", ""},
		{"ipv6 literal", "mailto:a@[::1]", ""},
		{"idn domain", "mailto:a@пример.рф", "mailto:a@xn--e1afmkfd.xn--p1ai"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mustSanitize(t, `<a href="`+strings.ReplaceAll(c.href, `"`, "&quot;")+`">m</a>`, descr)
			want := "<p>m</p>"
			if c.want != "" {
				want = `<p><a href="` + escapeAttr(c.want) + `">m</a></p>`
			}
			if got != want {
				t.Fatalf("Sanitize = %q, want %q", got, want)
			}
			if c.want != "" {
				if txt := PlainText(got); strings.ContainsAny(txt, "?%") {
					t.Fatalf("PlainText leaks a query: %q", txt)
				}
			}
		})
	}
	if got, _ := Normalize(`<a href="mailto:a@b.com?bcc=x@evil.com">write</a>`, "art", descr); strings.Contains(got, "evil") {
		t.Fatalf("query survived: %q", got)
	}
	if got := ToTelegramHTML(`<p><a href="mailto:a@b.com">write</a></p>`); got != "write (a@b.com)" {
		t.Fatalf("telegram: %q", got)
	}
}

// ---- FromLegacy numbered lists ----

func TestFromLegacyNumberedListsNeedConsecutiveNumbers(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"1 2 3", "1. a\n2. b\n3. c", `<ol><li>a</li><li>b</li><li>c</li></ol>`},
		{"gap to 15", "1. Иванов\n15. Петров", `<p>1. Иванов<br>15. Петров</p>`},
		{"all ones", "1. a\n1. b", `<p>1. a<br>1. b</p>`},
		{"all ones, three", "1. a\n1. b\n1. c", `<p>1. a<br>1. b<br>1. c</p>`},
		{"1 and 3", "1. a\n3. b", `<p>1. a<br>3. b</p>`},
		{"gap after two", "1. a\n2. b\n4. c", `<ol><li>a</li><li>b</li></ol><p>4. c</p>`},
		{"repeat after two", "1. a\n2. b\n2. c", `<ol><li>a</li><li>b</li></ol><p>2. c</p>`},
		{"two lists", "1. a\n2. b\n1. c\n2. d", `<ol><li>a</li><li>b</li></ol><ol><li>c</li><li>d</li></ol>`},
		{"descending", "3. a\n2. b\n1. c", `<p>3. a<br>2. b<br>1. c</p>`},
		{"starts at two", "2. a\n3. b", `<p>2. a<br>3. b</p>`},
		{"text before and after", "Intro\n1. a\n2. b\nOutro", `<p>Intro</p><ol><li>a</li><li>b</li></ol><p>Outro</p>`},
		{"zero start", "0. a\n1. b", `<p>0. a<br>1. b</p>`},
		{"leading zeros are numbers", "01. a\n02. b", `<p>01. a<br>02. b</p>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FromLegacy(c.in, descr); got != c.want {
				t.Fatalf("FromLegacy(%q) = %q, want %q", c.in, got, c.want)
			}
			if clean := mustSanitize(t, c.want, descr); clean != c.want {
				t.Fatalf("not canonical: %q", clean)
			}
		})
	}
}

// ---- Words ----

func TestWordsTokens(t *testing.T) {
	cases := []struct {
		name, art string
		want      []string
	}{
		{"every sign is a token", `<p>Скидка -20%</p>`, []string{"Скидка", "-", "20", "%"}},
		{"comma", `<p>Казнить, нельзя</p>`, []string{"Казнить", ",", "нельзя"}},
		{"decimal comma", `<p>10,5 км</p>`, []string{"10", ",", "5", "км"}},
		{"ordered list shows its numbers", `<ol><li>a</li><li>b c</li></ol>`, []string{"1", ".", "a", "2", ".", "b", "c"}},
		{"bulleted list adds nothing", `<ul><li>a</li><li>b</li></ul>`, []string{"a", "b"}},
		{"nested ordered list numbers each level", `<ol><li>a<ol><li>b</li><li>c</li></ol></li><li>d</li></ol>`,
			[]string{"1", ".", "a", "1", ".", "b", "2", ".", "c", "2", ".", "d"}},
		{"empty items are not numbered", `<ol><li>a</li><li></li><li>b</li></ol>`, []string{"1", ".", "a", "2", ".", "b"}},
		{"email", `<p>info@vino.co.il</p>`, []string{"info", "@", "vino", ".", "co", ".", "il"}},
		{"url", `<p>https://e.com/a-b</p>`, []string{"https", ":", "/", "/", "e", ".", "com", "/", "a", "-", "b"}},
		{"currency and symbols", `<p>5€ &amp; 3$ ©</p>`, []string{"5", "€", "&", "3", "$", "©"}},
		{"marks stay in the word", "<p>école</p>", []string{"école"}},
		{"hebrew points and maqaf", `<p>שָׁלוֹם־עולם</p>`, []string{"שָׁלוֹם", "־", "עולם"}},
		{"direction marks are tokens", "<p>a\u200fb</p>", []string{"a", "\u200f", "b"}},
		{"spaces and markup add nothing", `<p>a <strong>b</strong><br>c</p><p> d </p>`, []string{"a", "b", "c", "d"}},
		{"astral letters", "<p>\U0001d538\U0001d539</p>", []string{"\U0001d538\U0001d539"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			art := mustSanitize(t, c.art, descr)
			if got := Words(art); !slices.Equal(got, c.want) {
				t.Fatalf("Words(%q) = %q, want %q", art, got, c.want)
			}
		})
	}
}

func TestWordsNegative(t *testing.T) {
	// Every pair is two texts that differ in a sign, a number or a marker and
	// must therefore give different words.
	pairs := [][2]string{
		{`<p>Скидка -20%</p>`, `<p>Скидка 20%</p>`},
		{`<p>Скидка -20%</p>`, `<p>Скидка +20%</p>`},
		{`<p>Скидка 20%</p>`, `<p>Скидка 20</p>`},
		{`<p>Казнить, нельзя помиловать</p>`, `<p>Казнить нельзя, помиловать</p>`},
		{`<p>Вход свободный.</p>`, `<p>Вход свободный?</p>`},
		{`<p>Вход свободный.</p>`, `<p>Вход свободный</p>`},
		{`<p>10,5 км</p>`, `<p>10 5 км</p>`},
		{`<p>10.5 км</p>`, `<p>10 5 км</p>`},
		{`<p>10,5 км</p>`, `<p>10.5 км</p>`},
		{`<p>1/2</p>`, `<p>1-2</p>`},
		{`<p>5.000</p>`, `<p>5,000</p>`},
		{`<p>info@vino.co.il</p>`, `<p>info.vino@co.il</p>`},
		{`<p>https://e.com/a-b</p>`, `<p>https://e.com/a/b</p>`},
		{`<p>https://e.com/a-b</p>`, `<p>https://e.com/ab</p>`},
		{`<ol><li>a</li><li>b</li></ol>`, `<ul><li>a</li><li>b</li></ul>`},
		{`<ol><li>a</li></ol>`, `<p>a</p>`},
		{`<ol><li>a</li><li>b</li></ol>`, `<p>a</p><p>b</p>`},
		{`<ul><li>a</li></ul><ol><li>b</li></ol>`, `<ol><li>a</li></ol><ul><li>b</li></ul>`},
		{`<p>a (b)</p>`, `<p>a [b]</p>`},
		{`<p>a — b</p>`, `<p>a - b</p>`},
		{`<p>ab</p>`, `<p>a b</p>`},
		{`<p>5€</p>`, `<p>5$</p>`},
	}
	for _, c := range pairs {
		a, b := mustSanitize(t, c[0], descr), mustSanitize(t, c[1], descr)
		if slices.Equal(Words(a), Words(b)) {
			t.Errorf("the same words for %q and %q: %q", a, b, Words(a))
		}
	}
	// Markup, spaces and bullets do not matter.
	same := [][2]string{
		{`<p>a <strong>b</strong>, c</p>`, `<p>a b, c</p>`},
		{`<p>a<br>b</p>`, `<p>a b</p>`},
		{`<ul><li>a</li><li>b</li></ul>`, `<p>a</p><p>b</p>`},
		{`<p>a  b</p>`, `<p>a b</p>`},
		{`<p><em>-</em>20%</p>`, `<p>-20%</p>`},
	}
	for _, c := range same {
		a, b := mustSanitize(t, c[0], descr), mustSanitize(t, c[1], descr)
		if !slices.Equal(Words(a), Words(b)) {
			t.Errorf("different words for %q and %q: %q and %q", a, b, Words(a), Words(b))
		}
	}
}

func TestWordsOfLegacyEqualWordsOfSanitizedMarkup(t *testing.T) {
	text := "Концерт, в субботу!\nВход -20% (до 10,5 км)\n\n1. Бах\n2. Моцарт\n3. Бетховен\n\n- a/b\n- c-d\n\nПисьмо: info@vino.co.il — https://e.com/a-b; 5.000 и 5,000."
	markup := `<p>Концерт, в <strong>субботу</strong>!<br>Вход <em>-20%</em> (до 10,5 км)</p>` +
		`<ol><li>Бах</li><li><strong>Моцарт</strong></li><li>Бетховен</li></ol>` +
		`<ul><li>a/b</li><li>c-d</li></ul>` +
		`<p>Письмо: info@vino.co.il — https://e.com/a-b; 5.000 и 5,000.</p>`
	want := Words(FromLegacy(text, descr))
	if got := Words(mustSanitize(t, markup, descr)); !slices.Equal(got, want) {
		t.Fatalf("words differ:\n got  %q\n want %q", got, want)
	}
	if len(want) < 40 {
		t.Fatalf("suspiciously few tokens: %q", want)
	}
	// With an auto-linked address the link target is no word.
	linkText := "Билеты: https://e.com/a-b, вопросы: 1. да\n2. нет"
	linked := `<p>Билеты: <a href="https://e.com/a-b">https://e.com/a-b</a>, вопросы: 1. да<br>2. нет</p>`
	if got, want := Words(mustSanitize(t, linked, email)), Words(FromLegacy(linkText, email)); !slices.Equal(got, want) {
		t.Fatalf("words differ with a link:\n got  %q\n want %q", got, want)
	}
	// A changed sign is seen.
	for _, changed := range []string{
		strings.Replace(markup, "-20%", "20%", 1),
		strings.Replace(markup, "Концерт,", "Концерт", 1),
		strings.Replace(markup, "<ol>", "<ul>", 1),
		strings.Replace(markup, "10,5", "10 5", 1),
	} {
		if slices.Equal(Words(mustSanitize(t, changed, descr)), want) {
			t.Errorf("a change was not detected: %q", changed)
		}
	}
}

// ---- no work before the limit, and linear work after ----

// pathologicalBudget is the wall-clock budget of one pathological input.
// The race detector slows this code down by an order of magnitude or more
// (CI's Unit / Race job), so the budget scales there; a quadratic regression
// still blows through it, the linear code does not.
var pathologicalBudget = time.Second * time.Duration(raceSlowdown)

func timed(t *testing.T, name string, fn func()) {
	t.Helper()
	start := time.Now()
	fn()
	d := time.Since(start)
	t.Logf("%s: %v", name, d)
	if d > pathologicalBudget {
		t.Errorf("%s took %v, the budget is %v", name, d, pathologicalBudget)
	}
}

func pathologicalInputs() map[string]string {
	return map[string]string{
		"bold openers":  strings.Repeat("**a ", 250000),
		"https runs":    strings.Repeat("https://", 125000),
		"comment nodes": strings.Repeat("a<!---->", 130000),
	}
}

func TestOversizedInputIsRefusedBeforeAnyWork(t *testing.T) {
	for name, in := range pathologicalInputs() {
		for _, p := range []Profile{descr, email, sess} {
			timed(t, "Normalize text "+name+" "+p.Name, func() {
				if _, err := Normalize(in, "text", p); !errors.Is(err, ErrHTMLTooLong) {
					t.Errorf("err = %v, want html_too_long", err)
				}
			})
			timed(t, "Normalize art "+name+" "+p.Name, func() {
				if _, err := Normalize(in, "art", p); !errors.Is(err, ErrHTMLTooLong) {
					t.Errorf("err = %v, want html_too_long", err)
				}
			})
		}
	}
	// The border: text over MaxHTML characters, art over 4 * MaxHTML characters.
	for _, p := range []Profile{descr, email, sess} {
		if _, err := Normalize(strings.Repeat("я", p.MaxHTML+1), "text", p); !errors.Is(err, ErrHTMLTooLong) {
			t.Errorf("%s: text over the limit: %v", p.Name, err)
		}
		if _, err := Normalize(strings.Repeat("я", p.MaxHTML), "text", p); errors.Is(err, ErrHTMLTooLong) {
			t.Errorf("%s: text at the limit is refused for its size", p.Name)
		}
		if _, err := Sanitize(strings.Repeat("я", 4*p.MaxHTML+1), p); !errors.Is(err, ErrHTMLTooLong) {
			t.Errorf("%s: art over four times the limit: %v", p.Name, err)
		}
		if _, err := Sanitize(strings.Repeat("я", 4*p.MaxHTML), p); errors.Is(err, ErrHTMLTooLong) {
			t.Errorf("%s: art at four times the limit is refused for its size", p.Name)
		}
		// Characters, not bytes: a text of multi-byte characters at the limit is fine.
		if n := utf8.RuneCountInString(strings.Repeat("я", 4*p.MaxHTML)); n != 4*p.MaxHTML {
			t.Fatal("rune count")
		}
	}
	// The size is checked first, before the encoding.
	if _, err := Sanitize("\xff"+strings.Repeat("a", 4*descr.MaxHTML), descr); !errors.Is(err, ErrHTMLTooLong) {
		t.Errorf("err = %v", err)
	}
	if _, err := Normalize("\xff"+strings.Repeat("a", descr.MaxHTML), "text", descr); !errors.Is(err, ErrHTMLTooLong) {
		t.Errorf("err = %v", err)
	}
}

// The readers have no size limit of the profile, so the converters themselves
// must be linear: the same three inputs, at a megabyte, through the paths that
// do not refuse them.
func TestPathologicalInputsAreLinear(t *testing.T) {
	in := pathologicalInputs()
	for _, p := range []Profile{descr, email} {
		timed(t, "FromLegacy bold openers "+p.Name, func() { FromLegacy(in["bold openers"], p) })
		timed(t, "FromLegacy https runs "+p.Name, func() { FromLegacy(in["https runs"], p) })
		timed(t, "Render text bold openers "+p.Name, func() {
			if _, err := Render(in["bold openers"], "text", p); err != nil {
				t.Error(err)
			}
		})
		timed(t, "Render text https runs "+p.Name, func() { _, _ = Render(in["https runs"], "text", p) })
	}
	timed(t, "Sanitize comment nodes (no limits)", func() {
		out, err := Sanitize(in["comment nodes"], descr.readOnly())
		if err != nil || len(out) != len("<p>")+130000+len("</p>") {
			t.Errorf("len = %d, err = %v", len(out), err)
		}
	})
	timed(t, "Render art comment nodes", func() {
		if _, err := Render(in["comment nodes"], "art", descr); err != nil {
			t.Error(err)
		}
	})
	// Some more shapes that are known to hurt a naive converter or parser.
	more := map[string]string{
		"nested b":             strings.Repeat("<b>x", 100000),
		"nested a":             strings.Repeat(`<a href="https://e.com">x`, 40000),
		"misnested formatting": strings.Repeat("<b><i>x</b>", 60000),
		"many li":              strings.Repeat("<li>x", 100000),
		"many paragraphs":      strings.Repeat("<p>x", 150000),
		"many breaks":          strings.Repeat("<br>", 200000),
		"many links":           strings.Repeat(`<a href="https://e.com/a">x</a>`, 30000),
		"many entities":        strings.Repeat("&lt;", 200000),
		"many underscores":     strings.Repeat("_a ", 300000),
		"underscore pairs":     strings.Repeat("_ ", 400000),
		"stars":                strings.Repeat("*", 1000000),
		"star pairs":           strings.Repeat("** ", 300000),
		"bold and italic":      strings.Repeat("**_a_** ", 100000),
		"dot runs":             "https://" + strings.Repeat(".", 1000000),
		"localhost links":      strings.Repeat("https://localhost......", 40000),
		"list lines":           strings.Repeat("- a\n", 200000),
		"numbered lines":       strings.Repeat("1. a\n", 200000),
		"long line":            strings.Repeat("a", 1000000),
	}
	for name, s := range more {
		timed(t, "Sanitize "+name, func() { _, _ = Sanitize(s, descr.readOnly()) })
		timed(t, "FromLegacy "+name, func() { FromLegacy(s, email) })
	}
}

func BenchmarkFromLegacyBoldOpeners(b *testing.B) {
	in := pathologicalInputs()["bold openers"]
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		FromLegacy(in, email)
	}
}

func BenchmarkFromLegacyHTTPSRuns(b *testing.B) {
	in := pathologicalInputs()["https runs"]
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		FromLegacy(in, email)
	}
}

func BenchmarkSanitizeCommentNodes(b *testing.B) {
	in := pathologicalInputs()["comment nodes"]
	p := descr.readOnly()
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		if _, err := Sanitize(in, p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeRefusesOversizedText(b *testing.B) {
	in := pathologicalInputs()["bold openers"]
	for i := 0; i < b.N; i++ {
		if _, err := Normalize(in, "text", email); err == nil {
			b.Fatal("accepted")
		}
	}
}

func BenchmarkSanitizeAtTheLimit(b *testing.B) {
	in := strings.Repeat("<p>Привет, <strong>мир</strong> <a href=\"https://e.com/x\">ссылка</a></p>", 15) + strings.Repeat("<p>Привет, <em>мир</em></p>", 100)
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		if _, err := Sanitize(in, descr); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeTextAtTheLimit(b *testing.B) {
	in := strings.Repeat("**Привет**, _мир_ https://e.com/x\n- a\n- b\n\n", 60)
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		if _, err := Normalize(in, "text", descr); err != nil {
			b.Fatal(err)
		}
	}
}

func TestStripRefused(t *testing.T) {
	flag := "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F"
	for in, want := range map[string]string{
		"plain text":         "plain text",
		"a\tb\nc\u200dd":     "a\tb\nc\u200dd", // tab, line break, joiner kept
		"a\u202eb\u202bc":    "abc",
		"c1\u0085\u009f":     "c1",
		"Match " + flag:      "Match \U0001F3F4",
		"x\u2062y\u2060z":    "xy\u2060z", // an invisible operator goes, a word joiner is Sanitize's
		"\u2028line\u2029ps": "\u2028line\u2029ps",
	} {
		if got := StripRefused(in); got != want {
			t.Errorf("StripRefused(%q) = %q, want %q", in, got, want)
		}
	}
}
