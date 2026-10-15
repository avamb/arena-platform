package eventbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// The bot asks the API what an uploaded poster says; the model key is not
// the bot's, so the only things it sends are the user's JWT and the ids.
func TestArenaClient_PosterFacts_CallsTheRouteAndDecodesFacts(t *testing.T) {
	org := uuid.New()
	var gotPath, gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"facts":{"name":"Концерт","date":"2099-12-15","time":"20:00","venue_name":"Зал","city":"","age":"16+","description":"",
			"categories":[{"name":"Партер","price_minor":3490,"currency":"EUR"}]}}`))
	}))
	defer srv.Close()

	c := NewArenaClient(srv.URL, "svc", srv.Client())
	facts, err := c.PosterFacts(context.Background(), "jwt-1", org, "media-7")
	if err != nil {
		t.Fatalf("PosterFacts: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/organizations/"+org.String()+"/media/media-7/poster-facts" || gotAuth != "Bearer jwt-1" {
		t.Errorf("request = %s %s auth=%q", gotMethod, gotPath, gotAuth)
	}
	if facts.Name != "Концерт" || facts.Date != "2099-12-15" || facts.VenueName != "Зал" || len(facts.Categories) != 1 || facts.Categories[0].PriceMinor != 3490 {
		t.Errorf("facts = %+v", facts)
	}
	// The call may take as long as the model: the shared client's 15 s must
	// not apply, and the shared client itself is left as it was.
	if c.http.Timeout == posterFactsCallTimeout {
		t.Error("PosterFacts changed the shared client's timeout")
	}
}

func TestArenaClient_PosterFacts_ANotConfiguredAPIIsToldApart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"media.poster_reader_not_configured","message":"reading posters is not configured on this server"}}`))
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client())
	_, err := c.PosterFacts(context.Background(), "jwt", uuid.New(), "m")
	if err == nil || !posterReaderOff(err) {
		t.Fatalf("err = %v, want the not-configured answer", err)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"media.poster_reads_exhausted","message":"x"}}`))
	}))
	defer other.Close()
	_, err = NewArenaClient(other.URL, "svc", other.Client()).PosterFacts(context.Background(), "jwt", uuid.New(), "m")
	if err == nil || posterReaderOff(err) {
		t.Fatalf("a spent daily cap is a failure, not the \"no key\" case: %v", err)
	}
}
