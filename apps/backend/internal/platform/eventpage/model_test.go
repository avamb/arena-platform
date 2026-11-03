package eventpage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMergeModel_FillsOnlyWhatThePageLeftEmpty(t *testing.T) {
	r := Extract(loadPage(t, "og_only.html", "https://klub.example.cz/koncert"), testNow)
	r.MergeModel(ModelFacts{
		Name:      "Something else",
		VenueName: "Klub Lucerna",
		City:      "Praha",
		Age:       "18+",
		Dates:     []ModelDate{{Date: "2026-11-21", Time: "20:00"}, {Date: "2026-11-01", Time: "20:00"}, {Date: "2026-11-28", Time: "8 pm"}},
		Categories: []ModelCategory{
			{Name: "Vstupné", Price: "450", Currency: "czk"},
			{Name: "Bad", Price: "a lot"},
		},
	}, testNow)
	f := r.Facts
	if f.Name != "Podzimní koncert" || r.Sources["name"] != SourceOpenGraph {
		t.Errorf("the page's own name must win: %q from %q", f.Name, r.Sources["name"])
	}
	if f.VenueName != "Klub Lucerna" || f.City != "Praha" || r.Sources["venue"] != SourceModel {
		t.Errorf("venue = %q %q from %q", f.VenueName, f.City, r.Sources["venue"])
	}
	want := []DateFact{{Date: "2026-11-21", Time: "20:00"}, {Date: "2026-11-28"}}
	if len(f.Dates) != 2 || f.Dates[0] != want[0] || f.Dates[1] != want[1] {
		t.Errorf("dates = %+v", f.Dates)
	}
	if len(f.Categories) != 1 || f.Categories[0] != (Category{Name: "Vstupné", PriceMinor: 45000, Currency: "CZK"}) {
		t.Errorf("categories = %+v", f.Categories)
	}
	if _, ok := findWarning(r, WarnReadByModel); !ok {
		t.Errorf("warnings = %v", warningCodes(r))
	}
	for _, gone := range []string{WarnNoDates, WarnNoPrices} {
		if _, ok := findWarning(r, gone); ok {
			t.Errorf("%s must go once the model found it", gone)
		}
	}
	if f.Date != "2026-11-21" || f.Time != "20:00" {
		t.Errorf("first date = %q %q", f.Date, f.Time)
	}
}

func TestMergeModel_NothingUsableAddsNoWarning(t *testing.T) {
	r := Extract(loadPage(t, "og_only.html", "https://klub.example.cz/koncert"), testNow)
	r.MergeModel(ModelFacts{Dates: []ModelDate{{Date: "yesterday"}}}, testNow)
	if _, ok := findWarning(r, WarnReadByModel); ok {
		t.Error("nothing was filled: no 'read by model' warning")
	}
}

func TestAnthropic_ReadText(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" || r.URL.Path != "/v1/messages" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","name":"page_facts","input":{"name":"Koncert","dates":[{"date":"2026-11-21","time":"20:00"}],"categories":[{"name":"Vstupné","price":"450","currency":"CZK"}]}}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()
	a := NewAnthropic("k", "", srv.URL, nil)
	mf, err := a.ReadText(context.Background(), "Koncert 21.11. Vstupné 450 Kč", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Name != "Koncert" || len(mf.Dates) != 1 || mf.Categories[0].Price != "450" {
		t.Errorf("facts = %+v", mf)
	}
	msgs, _ := json.Marshal(got["messages"])
	if !strings.Contains(string(msgs), "Today is 2026-11-03") || !strings.Contains(string(msgs), "between the markers") {
		t.Errorf("prompt = %s", msgs)
	}
	if got["model"] != DefaultModel {
		t.Errorf("model = %v", got["model"])
	}
}

func TestAnthropic_ErrorsNeverQuoteTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"PAGE TEXT QUOTED HERE"}}`))
	}))
	defer srv.Close()
	_, err := NewAnthropic("k", "", srv.URL, nil).ReadText(context.Background(), "text", testNow)
	var apiErr *ModelAPIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || strings.Contains(err.Error(), "QUOTED") {
		t.Errorf("err = %v", err)
	}
	if _, err := NewAnthropic("", "", srv.URL, nil).ReadText(context.Background(), "t", testNow); !errors.Is(err, ErrModelNotConfigured) {
		t.Errorf("no key: %v", err)
	}
}
