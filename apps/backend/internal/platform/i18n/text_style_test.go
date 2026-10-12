// text_style_test.go is the advisory text-style check described in
// docs/text_style_standard_ru.md (principles borrowed from ASD-STE100).
//
// It NEVER fails on a style finding: findings go to the test log, and the
// count is compared with testdata/text_style_baseline.txt so a reviewer sees
// whether a change made the catalog better or worse. It fails only when a
// catalog cannot be parsed or when the checker's own unit tests break.
//
// Run:   go test ./internal/platform/i18n -run TestTextStyle -v
// Baseline refresh: ARENA_STYLE_WRITE_BASELINE=1 go test ./internal/platform/i18n -run TestTextStyle_Catalogs
package i18n

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const (
	// styleMaxWords is the longest sentence we accept without a warning.
	// The standard's target is 20 for instructions and 25 for descriptions.
	styleMaxWords = 25

	styleBaselineFile = "testdata/text_style_baseline.txt"
)

// styleScopePrefixes lists the message-ID prefixes under check. Widen it as
// the rollout plan moves on (e-mail templates and API errors come later).
var styleScopePrefixes = []string{"bot."}

// styleLocales are the catalogs under check.
var styleLocales = []string{"en", "ru"}

var (
	reHTMLTag     = regexp.MustCompile(`<[^>]+>`)
	reTmplAction  = regexp.MustCompile(`\{\{[^}]*\}\}`)
	reSentenceEnd = regexp.MustCompile(`[.!?…]+(\s+|$)|\n+|•`)
	reDash        = regexp.MustCompile(`\s[—–]\s`)

	// Glossary: a synonym the standard forbids. Keep in step with section 4
	// of docs/text_style_standard_ru.md.
	styleForbidden = map[string][]*regexp.Regexp{
		"en": {
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])clients?(?:[^\p{L}]|$)`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])organisations?(?:[^\p{L}]|$)`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])cancell(?:ed|ing)(?:[^\p{L}]|$)`), // American: canceled, canceling
		},
		"ru": {
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])сесси[яиюей]\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])событи\p{L}*`),                           // event = «мероприятие» (decision 2026-10-12)
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])ивент(?:а|ы|ов|е|у|ом)?(?:[^-\p{L}]|$)`), // "ивент-центр" stays allowed
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])клиент\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])юзер\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])возмещени\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])рефанд\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])постер\p{L}*`),
		},
	}

	// Passive voice with no agent, English heuristic (noisy by design).
	reEnPassive = regexp.MustCompile(`(?i)\b(is|are|was|were|be|been|being)\s+\w+ed\b`)
	// "It is necessary to ..." style: an order hidden in a description.
	reRuNecessary = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(необходимо|следует)(?:[^\p{L}]|$)`)
)

// styleFinding is one advisory warning.
type styleFinding struct {
	Locale, Key, Rule, Detail string
}

func (f styleFinding) String() string {
	return fmt.Sprintf("[%s] %s: %s: %s", f.Locale, f.Key, f.Rule, f.Detail)
}

// plainText removes markup and template actions so only words remain.
func plainText(s string) string {
	s = reHTMLTag.ReplaceAllString(s, "")
	s = reTmplAction.ReplaceAllString(s, "X")
	return s
}

// splitSentences breaks text into sentences. Line breaks and list bullets
// also end a sentence, because a list item is its own sentence.
func splitSentences(s string) []string {
	parts := reSentenceEnd.Split(plainText(s), -1)
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkText returns the advisory findings for one message text.
func checkText(locale, key, text string) []styleFinding {
	var res []styleFinding
	add := func(rule, detail string) {
		res = append(res, styleFinding{Locale: locale, Key: key, Rule: rule, Detail: detail})
	}
	plain := plainText(text)

	for _, s := range splitSentences(text) {
		if n := len(strings.Fields(s)); n > styleMaxWords {
			add("long-sentence", fmt.Sprintf("%d words (limit %d): %.60s…", n, styleMaxWords, s))
		}
	}
	if strings.Contains(plain, ";") {
		add("semicolon", "use two sentences or a list")
	}
	if hasThoughtDash(locale, plain) {
		add("dash", "a dash joins two thoughts: write two sentences")
	}
	for _, re := range styleForbidden[locale] {
		if m := re.FindString(plain); m != "" {
			add("glossary", fmt.Sprintf("term %q is not in the glossary", strings.Trim(m, " \n.,;:!?«»\"()")))
		}
	}
	switch locale {
	case "en":
		if m := reEnPassive.FindString(plain); m != "" {
			add("passive", fmt.Sprintf("possible passive voice: %q", m))
		}
	case "ru":
		if m := reRuNecessary.FindString(plain); m != "" {
			add("command", fmt.Sprintf("%q hides a command: use the imperative", m))
		}
	}
	return res
}

// hasThoughtDash reports a dash that joins two thoughts in running text.
// Russian is exempt: the dash (тире) is required punctuation there. A dash
// between two template values ("{{.Code}} — {{.Role}}") is a field separator
// and a text of six words or fewer is a label, so neither counts.
func hasThoughtDash(locale, plain string) bool {
	if locale == "ru" {
		return false
	}
	if len(strings.Fields(plain)) <= 6 {
		return false
	}
	for _, loc := range reDash.FindAllStringIndex(plain, -1) {
		before, after := plain[:loc[0]], plain[loc[1]:]
		if strings.HasSuffix(before, "X") && strings.HasPrefix(after, "X") {
			continue
		}
		return true
	}
	return false
}

func inStyleScope(key string) bool {
	for _, p := range styleScopePrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// loadCatalogTexts returns key -> "other" text for one embedded locale file.
func loadCatalogTexts(t *testing.T, locale string) map[string]string {
	t.Helper()
	data, err := localesFS.ReadFile("locales/" + locale + ".toml")
	if err != nil {
		t.Fatalf("read %s catalog: %v", locale, err)
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		t.Fatalf("parse %s catalog: %v", locale, err)
	}
	out := map[string]string{}
	for key, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["other"].(string); ok {
			out[key] = s
		}
	}
	return out
}

// TestTextStyle_Catalogs scans the catalogs and reports findings. It never
// fails on a style finding.
func TestTextStyle_Catalogs(t *testing.T) {
	counts := map[string]int{} // "locale/rule" -> n
	for _, locale := range styleLocales {
		texts := loadCatalogTexts(t, locale)
		keys := make([]string, 0, len(texts))
		for k := range texts {
			if inStyleScope(k) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, f := range checkText(locale, k, texts[k]) {
				counts[f.Locale+"/"+f.Rule]++
				t.Log(f.String())
			}
		}
		counts[locale+"/messages"] = len(keys)
	}

	lines := make([]string, 0, len(counts))
	for k, n := range counts {
		lines = append(lines, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(lines)
	current := strings.Join(lines, "\n") + "\n"

	if os.Getenv("ARENA_STYLE_WRITE_BASELINE") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(styleBaselineFile, []byte(current), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("baseline written to %s", styleBaselineFile)
		return
	}

	base, err := os.ReadFile(styleBaselineFile)
	if err != nil {
		t.Logf("no baseline (%v): current counts\n%s", err, current)
		return
	}
	baseCounts := map[string]int{}
	for _, ln := range strings.Split(strings.TrimSpace(string(base)), "\n") {
		var k string
		var n int
		if _, err := fmt.Sscanf(ln, "%s %d", &k, &n); err == nil {
			baseCounts[k] = n
		}
	}
	for _, ln := range lines {
		var k string
		var n int
		_, _ = fmt.Sscanf(ln, "%s %d", &k, &n)
		if strings.HasSuffix(k, "/messages") {
			continue
		}
		if b := baseCounts[k]; n > b {
			t.Logf("STYLE NOTE: %s grew from %d to %d warnings", k, b, n)
		}
	}
}

// TestTextStyle_CheckerRules guards the checker itself.
func TestTextStyle_CheckerRules(t *testing.T) {
	rules := func(fs []styleFinding) string {
		var r []string
		for _, f := range fs {
			r = append(r, f.Rule)
		}
		return strings.Join(r, ",")
	}
	cases := []struct {
		locale, text, want string
	}{
		{"en", "Type a whole number, 1 or more. Example: 80.", ""},
		{"en", "Too many messages and then — please wait a minute.", "dash"},
		{"en", "Manager — events only", ""},
		{"en", "• {{.Email}} — {{.Role}}{{.Marks}}", ""},
		{"ru", "Владелец может всё — приглашать и убирать людей.", ""},
		{"en", "Send the poster; then wait.", "semicolon"},
		{"en", "The file {{.Name}} was deleted.", "passive"},
		{"en", "Contact your client.", "glossary"},
		{"en", "<b>Warning</b>\n• Press Back.\n• Wait.", ""},
		{"ru", "Введите целое число. Пример: 80.", ""},
		{"ru", "Сессия отменена.", "glossary"},
		{"ru", "Откройте ивент-центр.", ""},
		{"ru", "Создайте мероприятие.", ""},
		{"ru", "Создайте событие.", "glossary"},
		{"ru", "Новый ивент.", "glossary"},
		{"ru", "Необходимо ввести число.", "command"},
	}
	for _, c := range cases {
		if got := rules(checkText(c.locale, "k", c.text)); got != c.want {
			t.Errorf("%s %q: rules = %q, want %q", c.locale, c.text, got, c.want)
		}
	}
	long := strings.Repeat("word ", styleMaxWords+1) + "."
	if got := rules(checkText("en", "k", long)); got != "long-sentence" {
		t.Errorf("long sentence: rules = %q", got)
	}
	if n := len(splitSentences("One. Two!\nThree")); n != 3 {
		t.Errorf("splitSentences = %d, want 3", n)
	}
}
