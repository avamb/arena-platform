package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

func sp(s string) *string { return &s }

func TestScoreFacts_NormalizesTextAndSkipsUnknown(t *testing.T) {
	facts := posterread.Facts{
		Name: "  NOCHE de  Música ", Date: "2026-10-24", Time: "19:30", VenueName: "Café Rika", City: "MADRID", Age: "16+",
		Categories: []posterread.Category{{Name: "A", PriceMinor: 2500}, {Name: "B", PriceMinor: 3490}, {Name: "C", PriceMinor: 2500}},
	}
	prices := []float64{34.9, 25}
	truth := Truth{
		Title: sp("noche de musica"), Date: sp("2026-10-24"), Time: sp("19:30"),
		Venue: sp("cafe rika"), City: nil, Prices: &prices, Age: sp("16"),
	}
	s := ScoreFacts(facts, truth)
	if s.Scored != 6 || s.Matched != 6 {
		t.Fatalf("score = %+v", s)
	}
	if s.Fields["city"] != verdictSkip {
		t.Errorf("an unknown truth field must be skipped: %v", s.Fields)
	}
}

func TestScoreFacts_MissesAreNamed(t *testing.T) {
	facts := posterread.Facts{
		Name: "Other show", Date: "2026-10-25", Time: "19:00", VenueName: "Hall", City: "Prague", Age: "12+",
		Categories: []posterread.Category{{Name: "A", PriceMinor: 2500}},
	}
	prices := []float64{25, 30}
	truth := Truth{
		Title: sp("Night show"), Date: sp("2026-10-24"), Time: sp("19:30"), Venue: sp("Hall"), City: sp("Madrid"), Prices: &prices, Age: sp("16+"),
	}
	s := ScoreFacts(facts, truth)
	want := map[string]string{
		"title": verdictMiss, "date": verdictMiss, "time": verdictMiss, "venue": verdictMatch,
		"city": verdictMiss, "prices": verdictMiss, "age": verdictMiss,
	}
	for k, v := range want {
		if s.Fields[k] != v {
			t.Errorf("%s = %s, want %s", k, s.Fields[k], v)
		}
	}
	if s.Matched != 1 || s.Scored != 7 {
		t.Errorf("score = %+v", s)
	}
}

func TestScoreFacts_EmptyAnswerAndNoPrices(t *testing.T) {
	none := []float64{}
	truth := Truth{Title: sp("X"), Prices: &none, Age: sp("0+")}
	s := ScoreFacts(posterread.Facts{}, truth)
	if s.Fields["title"] != verdictMiss || s.Fields["age"] != verdictMiss {
		t.Errorf("an empty read must miss printed fields: %v", s.Fields)
	}
	if s.Fields["prices"] != verdictMatch {
		t.Errorf("no printed prices and none read is a match: %v", s.Fields)
	}
	// Rounding of a major-unit price into minor units.
	p := []float64{19.99}
	got := ScoreFacts(posterread.Facts{Categories: []posterread.Category{{PriceMinor: 1999}}}, Truth{Prices: &p})
	if got.Fields["prices"] != verdictMatch {
		t.Errorf("19.99 must equal 1999 minor: %v", got.Fields)
	}
}

func TestTruthJSON_NullMeansUnknownAndEmptyListMeansNone(t *testing.T) {
	var truths []Truth
	raw := `[{"file":"a.jpg","title":"T","date":null,"prices":[]},{"file":"b.png","prices":null}]`
	if err := json.Unmarshal([]byte(raw), &truths); err != nil {
		t.Fatal(err)
	}
	if truths[0].Date != nil || truths[0].Prices == nil || len(*truths[0].Prices) != 0 {
		t.Errorf("first = %+v", truths[0])
	}
	if truths[1].Prices != nil || truths[1].Title != nil {
		t.Errorf("second = %+v", truths[1])
	}
}

func TestParseSpecs(t *testing.T) {
	got, err := ParseSpecs(" anthropic:claude-sonnet-5-5 , GEMINI:gemini-3.8-flash,")
	if err != nil || len(got) != 2 || got[0] != (Spec{"anthropic", "claude-sonnet-5-5"}) || got[1] != (Spec{"gemini", "gemini-3.8-flash"}) {
		t.Fatalf("specs = %+v err %v", got, err)
	}
	for _, bad := range []string{"", "claude-sonnet-5-5", "openai:gpt", "gemini:", ":x"} {
		if _, err := ParseSpecs(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestKeysReader_NamesTheMissingVariableNotTheValue(t *testing.T) {
	k := Keys{Anthropic: "sk-secret-value"}
	if _, err := k.Reader(Spec{"anthropic", "m"}, time.Second); err != nil {
		t.Fatal(err)
	}
	_, err := k.Reader(Spec{"gemini", "m"}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "GEMINI_KEY") || strings.Contains(err.Error(), "sk-secret-value") {
		t.Fatalf("err = %v", err)
	}
}

type fakeReader struct {
	facts posterread.Facts
	err   error
	types []string
}

func (f *fakeReader) Read(_ context.Context, _ []byte, mediaType string) (posterread.Facts, error) {
	f.types = append(f.types, mediaType)
	return f.facts, f.err
}

func TestRunAllAndReport(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.jpg"), []byte("jpg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	prices := []float64{25}
	truths := []Truth{
		{File: "a.jpg", Title: sp("Show"), Prices: &prices},
		{File: "b.png", Title: sp("Show")},
		{File: "missing.jpg", Title: sp("Show")},
		{File: "c.gif", Title: sp("Show")},
	}
	good := &fakeReader{facts: posterread.Facts{Name: "show", Categories: []posterread.Category{{PriceMinor: 2500}}}}
	bad := &fakeReader{err: errors.New("model answered 500")}
	specs := []Spec{{"anthropic", "m1"}, {"gemini", "m2"}}
	runs := RunAll(context.Background(), dir, truths, specs, []posterread.Reader{good, bad}, time.Second)
	if len(runs) != 8 {
		t.Fatalf("runs = %d", len(runs))
	}
	if strings.Join(good.types, ",") != "image/jpeg,image/png" {
		t.Errorf("media types = %v", good.types)
	}
	if runs[0].Score.Matched != 2 || runs[0].Error != "" {
		t.Errorf("first run = %+v", runs[0])
	}
	if runs[1].Error == "" || runs[4].Error == "" || runs[6].Error == "" {
		t.Errorf("a failed read, a missing file and a gif must be errors: %+v", runs)
	}

	rep := NewReport(specs, runs, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if len(rep.Summary) != 2 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	s0, s1 := rep.Summary[0], rep.Summary[1]
	if s0.Images != 4 || s0.Errors != 2 || s0.Matched != 3 || s0.Scored != 5 {
		t.Errorf("summary 0 = %+v", s0)
	}
	if s1.Errors != 4 || s1.Matched != 0 {
		t.Errorf("summary 1 = %+v", s1)
	}
	md := rep.Markdown()
	for _, want := range []string{"| anthropic:m1 |", "| gemini:m2 |", "## By field", "## By image", "ERROR: model answered 500", "| a.jpg |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Errorf("report does not marshal: %v", err)
	}
}

func TestLoadTruth(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "t.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadTruth(write(`[{"file":"a.jpg","title":"T"}]`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[]`, `{}`, `[{"title":"x"}]`, `[{"file":"../a.jpg"}]`} {
		if _, err := LoadTruth(write(bad)); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
	if _, err := LoadTruth(filepath.Join(dir, "nope.json")); err == nil {
		t.Error("a missing file must be an error")
	}
}
