package horders

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestFoldSources_GroupsByLinkUTMAndDirect(t *testing.T) {
	link := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	rows := []gen.SessionSummarySourceRow{
		{Currency: "EUR", Orders: 1, Tickets: 2, Amount: 3000},
		{SalesLinkID: &link, Label: "Poster at the door", Currency: "EUR", Orders: 1, Tickets: 1, Amount: 1500},
		{SalesLinkID: &link, Label: "Poster at the door", Currency: "CZK", Orders: 1, Tickets: 1, Amount: 40000},
		{UTMSource: "instagram", UTMCampaign: "c1", Currency: "EUR", Orders: 2, Tickets: 2, Amount: 2000},
		// a refund in another currency than the orders lands in its own entry
		{UTMSource: "instagram", UTMCampaign: "c1", Currency: "USD", Orders: 0, Tickets: 0, Amount: -100},
	}
	got := foldSources(rows)
	b, _ := json.Marshal(got)
	want := `[` +
		`{"kind":"sales_link","sales_link_id":"11111111-1111-1111-1111-111111111111","label":"Poster at the door","orders":2,"tickets":2,"revenue":[{"currency":"CZK","amount_minor":40000},{"currency":"EUR","amount_minor":1500}]},` +
		`{"kind":"utm","utm_source":"instagram","utm_campaign":"c1","orders":2,"tickets":2,"revenue":[{"currency":"EUR","amount_minor":2000},{"currency":"USD","amount_minor":-100}]},` +
		`{"kind":"direct","orders":1,"tickets":2,"revenue":[{"currency":"EUR","amount_minor":3000}]}` +
		`]`
	if string(b) != want {
		t.Fatalf("foldSources =\n%s\nwant\n%s", b, want)
	}
}

func TestFoldSources_EmptyIsAnEmptyList(t *testing.T) {
	b, _ := json.Marshal(foldSources(nil))
	if string(b) != "[]" {
		t.Fatalf("foldSources(nil) = %s, want []", b)
	}
	s := buildSessionSummary(gen.SessionSummaryHeaderRow{ID: uuid.New()}, summaryRows{})
	b, _ = json.Marshal(s)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["sources"]) != "[]" {
		t.Fatalf("sources = %s, want []", m["sources"])
	}
}
