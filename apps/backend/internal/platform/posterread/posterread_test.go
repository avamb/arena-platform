package posterread

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The reader posts the poster as a base64 image with the version header
// and the key, forces the poster_facts tool, and normalizes the tool input:
// prices become minor units, the age one of the five ratings, an unreal
// date or time is dropped rather than passed on.
func TestAnthropic_Read_ForcesTheToolAndNormalizes(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "sk-test" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("request: %s %s key=%q version=%q", r.Method, r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"))
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use","content":[
			{"type":"text","text":"Reading."},
			{"type":"tool_use","id":"tu_1","name":"poster_facts","input":{
				"name":"  Ночь открытой сцены ","date":"2026-10-24","time":"19:0","venue_name":"Test Hall Rika","city":"Madrid",
				"age":"16","description":"Открытый микрофон для всех.",
				"categories":[{"name":"Партер","price":34.9,"currency":"eur"},{"name":"","price":10},{"name":"Free","price":0}]}}]}`))
	}))
	defer srv.Close()

	r := NewAnthropic("sk-test", "", srv.URL, srv.Client())
	facts, err := r.Read(context.Background(), []byte("\xff\xd8fake"), "image/jpeg")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got["model"] != DefaultModel {
		t.Errorf("model = %v", got["model"])
	}
	if tc, _ := got["tool_choice"].(map[string]any); tc["type"] != "tool" || tc["name"] != "poster_facts" {
		t.Errorf("tool_choice = %v", got["tool_choice"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", got["messages"])
	}
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	img, _ := content[0].(map[string]any)
	src, _ := img["source"].(map[string]any)
	if img["type"] != "image" || src["media_type"] != "image/jpeg" || src["data"] == "" {
		t.Errorf("image block = %v", img)
	}

	want := Facts{Name: "Ночь открытой сцены", Date: "2026-10-24", Time: "19:00", VenueName: "Test Hall Rika", City: "Madrid",
		Age: "16+", Description: "Открытый микрофон для всех.",
		Categories: []Category{{Name: "Партер", PriceMinor: 3490, Currency: "EUR"}}}
	if facts.Name != want.Name || facts.Date != want.Date || facts.Time != want.Time || facts.VenueName != want.VenueName ||
		facts.City != want.City || facts.Age != want.Age || facts.Description != want.Description {
		t.Fatalf("facts = %+v, want %+v", facts, want)
	}
	if len(facts.Categories) != 1 || facts.Categories[0] != want.Categories[0] {
		t.Fatalf("categories = %+v, want %+v", facts.Categories, want.Categories)
	}
}

func TestAnthropic_Read_DropsWhatDoesNotParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","name":"poster_facts","input":{"date":"24.10.2026","time":"25:00","age":"21+","categories":[]}}]}`))
	}))
	defer srv.Close()
	facts, err := NewAnthropic("k", "", srv.URL, srv.Client()).Read(context.Background(), []byte("x"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Empty() {
		t.Fatalf("facts should be empty, got %+v", facts)
	}
}

func TestAnthropic_Read_ErrorsAreNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	_, err := NewAnthropic("bad", "", srv.URL, srv.Client()).Read(context.Background(), []byte("x"), "image/png")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewAnthropic("", "", srv.URL, nil).Read(context.Background(), []byte("x"), "image/png"); err != ErrNotConfigured {
		t.Fatalf("unconfigured reader err = %v", err)
	}
}
