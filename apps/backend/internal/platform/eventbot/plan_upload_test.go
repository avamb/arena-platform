package eventbot

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func TestLooksLikeSVG(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"plain", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`, true},
		{"declaration and comment", "<?xml version=\"1.0\"?>\n<!-- Inkscape -->\n<svg width=\"1\" height=\"1\"></svg>", true},
		{"doctype", "<?xml version=\"1.0\"?><!DOCTYPE svg PUBLIC \"-//W3C//DTD SVG 1.1//EN\" \"http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd\"><svg/>", true},
		{"byte order mark", "\xef\xbb\xbf<svg/>", true},
		{"leading space", "  \n\t<svg/>", true},
		{"prefixed root", `<s:svg xmlns:s="http://www.w3.org/2000/svg"/>`, true},
		{"html page", "<!DOCTYPE html><html><body><svg/></body></html>", false},
		{"other xml", `<?xml version="1.0"?><plan/>`, false},
		{"png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", false},
		{"pdf", "%PDF-1.7\n", false},
		{"text", "this is not a plan", false},
		{"empty", "", false},
		{"blanks", " \n ", false},
	}
	for _, c := range cases {
		if got := looksLikeSVG([]byte(c.data)); got != c.want {
			t.Errorf("%s: looksLikeSVG = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPlanTypeOf(t *testing.T) {
	t.Parallel()
	seats := seating.Geometry{Categories: []seating.Category{{Index: 1, Name: "A"}}, Sections: []seating.Section{{Rows: []seating.Row{{Seats: []seating.Seat{{Key: "1", CategoryIndex: 1}}}}}}}
	ga := seating.Geometry{Categories: []seating.Category{{Index: 1, Name: "Floor", Kind: seating.KindGeneralAdmission, Capacity: 50}}}
	mixed := seating.Geometry{Categories: []seating.Category{{Index: 1, Name: "A"}, {Index: 2, Name: "Floor", Kind: seating.KindGeneralAdmission, Capacity: 50}}, Sections: seats.Sections}
	if got := planTypeOf(seats); got != "assigned_seats" {
		t.Errorf("seats = %s", got)
	}
	if got := planTypeOf(ga); got != "general_admission" {
		t.Errorf("ga = %s", got)
	}
	if got := planTypeOf(mixed); got != "mixed" {
		t.Errorf("mixed = %s", got)
	}
	if got := planTypeOf(seating.Geometry{}); got != "assigned_seats" {
		t.Errorf("empty = %s (the importer then refuses it for having no places)", got)
	}
}

func TestPlanNameFor(t *testing.T) {
	t.Parallel()
	cases := []struct{ file, venue, want string }{
		{"Palac_Akropolis.svg", "Hall", "Palac Akropolis"},
		{"main hall.SVG", "Hall", "main hall"},
		{"C:\\plans\\stage.svg", "Hall", "stage"},
		{"", "Teatro", "Teatro"},
		{".svg", "Teatro", "Teatro"},
		{"", "", "Hall plan"},
	}
	for _, c := range cases {
		if got := planNameFor(c.file, c.venue); got != c.want {
			t.Errorf("planNameFor(%q, %q) = %q, want %q", c.file, c.venue, got, c.want)
		}
	}
	if got := planNameFor(strings.Repeat("x", 300)+".svg", "v"); len([]rune(got)) > planNameMax {
		t.Errorf("name is %d characters", len([]rune(got)))
	}
}

func planTestBot(t *testing.T) *Bot {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	return &Bot{texts: NewTexts(bundle)}
}

// Every importer code that can reach a person has a sentence, in every
// language, and a long list is cut with the rest counted.
func TestPlanIssuesText(t *testing.T) {
	t.Parallel()
	b := planTestBot(t)
	codes := []string{
		seating.ErrInvalidSVG, seating.ErrCanvasMissing, seating.ErrCanvasTooLarge, seating.ErrPriceCategoryMissing,
		seating.ErrSeatNotCircle, seating.ErrRowMissingTitle, seating.ErrSeatMissingNumber, seating.ErrDuplicateSeat,
		seating.ErrSeatColorUnmatched, seating.ErrTooManyCategories, seating.ErrSeatsMissing, seating.ErrGACapacityInvalid,
		"something_new",
	}
	for _, loc := range SupportedLocales {
		for _, code := range codes {
			text := b.planIssuesText(loc, seating.ValidationErrors{{Code: code, Element: "<b>R1</b>"}})
			if text == "" || strings.Contains(text, "bot.wz.") || strings.Contains(text, "<no value>") || strings.Contains(text, "<b>") {
				t.Errorf("%s/%s: %q", loc, code, text)
			}
		}
	}
	var many seating.ValidationErrors
	for i := 0; i < 9; i++ {
		many = append(many, seating.ValidationError{Code: seating.ErrSeatMissingNumber, Element: string(rune('a' + i))})
	}
	text := b.planIssuesText("en", many)
	if got := strings.Count(text, "•"); got != maxPlanIssues || !strings.Contains(text, "4 more") {
		t.Errorf("long list: %d lines\n%s", got, text)
	}
	// the same problem twice reads once
	same := seating.ValidationErrors{{Code: seating.ErrInvalidSVG}, {Code: seating.ErrInvalidSVG}}
	if got := strings.Count(b.planIssuesText("en", same), "•"); got != 1 {
		t.Errorf("duplicate lines: %d", got)
	}
}

func TestPlanReport(t *testing.T) {
	t.Parallel()
	b := planTestBot(t)
	two := PlanRef{Name: "Hall <1>", Seats: 12, Categories: []PlanCategory{{Index: 1, Name: "Parterre", Places: 8}, {Index: 2, Name: "Floor", Places: 4, GA: true}}}
	text := b.planReport("en", two, []string{seating.WarnLegendMissing})
	for _, want := range []string{"Hall &lt;1&gt;", "12 places", "2 categories", "Parterre: 8 places", "Floor: 4 places (standing)", "Legend"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "one category") {
		t.Errorf("two categories must not carry the one-category advice:\n%s", text)
	}
	one := PlanRef{Name: "Hall", Seats: 8, Categories: []PlanCategory{{Index: 1, Name: "Parterre", Places: 8}}}
	for _, loc := range SupportedLocales {
		got := b.planReport(loc, one, []string{"future_warning"})
		if strings.Contains(got, "bot.wz.") || strings.Contains(got, "<no value>") {
			t.Errorf("%s: %s", loc, got)
		}
	}
	if got := b.planReport("en", one, nil); !strings.Contains(got, "one category") {
		t.Errorf("a single category must say the price will be one:\n%s", got)
	}
}
