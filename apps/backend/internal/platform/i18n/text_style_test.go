// text_style_test.go is the text-style check described in
// docs/text_style_standard_ru.md (principles borrowed from ASD-STE100).
//
// One rule is strict (owner decision 2026-10-13): a forbidden glossary
// synonym (section 4 of the standard) FAILS the test. Every other finding
// (long sentence, semicolon, dash, passive, hidden command) is advisory: it
// goes to the test log, and the count is compared with
// testdata/text_style_baseline.txt so a reviewer sees whether a change made
// the catalog better or worse. A fixed phrase that contains a forbidden word
// but is a term of its own goes into styleAllowed, with a reason.
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

// styleScopePrefixes lists the message-ID prefixes under check: the bot,
// the API error texts (error.*), the Bil24 gateway descriptions a selling
// site may show to buyers (bil24.*), the price-breakdown labels and the
// welcome text. E-mail templates and openapi.yaml come later.
var styleScopePrefixes = []string{"bot.", "error.", "bil24.", "pricing.", "server."}

// styleLocales are the catalogs under check.
var styleLocales = []string{"en", "ru"}

var (
	reHTMLTag     = regexp.MustCompile(`<[^>]+>`)
	reTmplAction  = regexp.MustCompile(`\{\{[^}]*\}\}`)
	reSentenceEnd = regexp.MustCompile(`[.!?…]+(\s+|$)|\n+|•`)
	reDash        = regexp.MustCompile(`\s[—–]\s`)

	// Glossary: a synonym the standard forbids. Keep in step with section 4
	// of docs/text_style_standard_ru.md. A match fails the test.
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
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])баннер\p{L}*`),
			// hold = «бронь» (decision 2026-10-13)
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])удерж(?:ан|иваем)\p{L}*`), // удержание, удержано, удерживаемые

			regexp.MustCompile(`(?i)(?:^|[^\p{L}])блокировк\p{L}*`),
			// venue = «площадка»; «схема зала» is allowed below
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])зал(?:а|у|е|ом|ы|ов|ам|ами|ах)?(?:[^\p{L}]|$)`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])мест\p{L}*\s+проведения`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])кресл\p{L}*`),
			// sales end = «конец продаж», doors open = «вход с»
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])закрыти\p{L}*\s+продаж\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])дедлайн\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])открыти\p{L}*\s+двер\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])тариф\p{L}*`),
			regexp.MustCompile(`(?i)(?:^|[^\p{L}])тестов\p{L}*\s+билет\p{L}*`),
		},
	}

	// styleAllowed are fixed phrases that contain a forbidden word but are a
	// term of their own. They are removed before the glossary scan. Keep the
	// list short and give a reason for each entry (section 4 of the standard).
	styleAllowed = map[string][]*regexp.Regexp{
		"ru": {
			// «схема зала» is the seating plan, not the venue.
			regexp.MustCompile(`(?i)схем\p{L}*\s+зала`),
		},
	}

	// styleStrictRules are the rules whose findings fail the test.
	styleStrictRules = map[string]bool{"glossary": true}

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
	glossaryText := plain
	for _, re := range styleAllowed[locale] {
		glossaryText = re.ReplaceAllString(glossaryText, " ")
	}
	for _, re := range styleForbidden[locale] {
		if m := re.FindString(glossaryText); m != "" {
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

// strictFindings returns the findings that must fail the test.
func strictFindings(fs []styleFinding) []styleFinding {
	var out []styleFinding
	for _, f := range fs {
		if styleStrictRules[f.Rule] {
			out = append(out, f)
		}
	}
	return out
}

// TestTextStyle_Catalogs scans the catalogs and reports findings. It fails
// on a forbidden glossary synonym and only logs every other finding.
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
			findings := checkText(locale, k, texts[k])
			for _, f := range findings {
				counts[f.Locale+"/"+f.Rule]++
				t.Log(f.String())
			}
			for _, f := range strictFindings(findings) {
				t.Errorf("%s: use the glossary term (docs/text_style_standard_ru.md, section 4)", f)
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
		// hold = «бронь» (decision 2026-10-13)
		{"ru", "Продано 3, в брони 2.", ""},
		{"ru", "Удержание мест истекло.", "glossary"},
		{"ru", "Продано 3, удержано 2.", "glossary"},
		{"ru", "Удержанных мест нет.", "glossary"},
		{"ru", "Места удерживаемые заказом.", "glossary"},
		{"ru", "Блокировка мест снята.", "glossary"},
		// venue = «площадка», but «схема зала» is a term of its own
		{"ru", "У сеанса есть схема зала.", ""},
		{"ru", "Откройте схему зала.", ""},
		{"ru", "Все делят места зала.", "glossary"},
		{"ru", "Укажите место проведения.", "glossary"},
		{"ru", "Кресло осталось в продаже.", "glossary"},
		{"ru", "Залог не нужен.", ""},
		// sales end, doors open, category, poster, sample ticket
		{"ru", "Конец продаж: 21:00. Вход с 19:30.", ""},
		{"ru", "Закрытие продаж в 21:00.", "glossary"},
		{"ru", "Дедлайн завтра.", "glossary"},
		{"ru", "Открытие дверей в 19:30.", "glossary"},
		{"ru", "Выберите тариф.", "glossary"},
		{"ru", "Пришлите баннер.", "glossary"},
		{"ru", "Отправьте тестовый билет.", "glossary"},
		{"ru", "Отправьте образец билета.", ""},
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

// TestTextStyle_StrictRules guards which findings fail the catalog test: a
// forbidden glossary synonym does, every other rule stays advisory.
func TestTextStyle_StrictRules(t *testing.T) {
	fs := checkText("ru", "k", "Удержание мест истекло; сессия закрыта — ждите.")
	strict := strictFindings(fs)
	if len(strict) != 2 {
		t.Fatalf("strict findings = %v, want two glossary findings", strict)
	}
	for _, f := range strict {
		if f.Rule != "glossary" {
			t.Errorf("strict finding with rule %q, want only glossary", f.Rule)
		}
	}
	advisory := checkText("en", "k", "Send the poster; then — wait. The file was deleted.")
	if len(advisory) == 0 {
		t.Fatal("expected advisory findings")
	}
	if s := strictFindings(advisory); len(s) != 0 {
		t.Errorf("advisory rules must not fail the test, got %v", s)
	}
}
