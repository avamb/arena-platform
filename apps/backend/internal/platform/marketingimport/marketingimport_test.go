package marketingimport

import (
	"strings"
	"testing"
	"time"
)

const brevoCSV = "EMAIL,FIRSTNAME,LASTNAME,EMAILBLACKLISTED,UNSUBSCRIBED,consent_date,consent_source\n" +
	"Anna@Example.com,Anna,K,0,0,2025-03-04,popup\n" +
	"bob@example.com,Bob,,1,0,2024-01-01,popup\n" +
	"carol@example.com,Carol,,0,1,,\n" +
	"dave@example.com,Dave,,0,0,,\n" +
	"erin@example.com,Erin,,0,0,not-a-date,popup\n" +
	"broken,Frank,,0,0,2025-01-01,popup\n"

func TestParseBrevo_ClassifiesRows(t *testing.T) {
	recs, probs, err := ParseBrevo(strings.NewReader(brevoCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 6 {
		t.Fatalf("records = %d, want 6", len(recs))
	}
	want := []Kind{KindSubscribe, KindSuppress, KindSuppress, KindNoConsent, KindNoConsent, KindSubscribe}
	for i, k := range want {
		if recs[i].Kind != k {
			t.Errorf("row %d kind = %s, want %s", i, recs[i].Kind, k)
		}
	}
	if recs[0].Name != "Anna K" {
		t.Errorf("name = %q", recs[0].Name)
	}
	if len(probs) != 1 || !strings.Contains(probs[0].Reason, "not-a-date") {
		t.Errorf("problems = %+v", probs)
	}
}

func TestParseElementor_DateIsConsent(t *testing.T) {
	nl := string(rune(10))
	in := string([]byte{0xEF, 0xBB, 0xBF}) + "date;name;email;phone" + nl +
		"19.11.2025 10:15;Ira;ira@example.com;+972" + nl +
		"bad;Oleg;oleg@example.com;" + nl
	recs, probs, err := ParseElementor(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Kind != KindSubscribe || !recs[0].ConsentAt.Equal(time.Date(2025, 11, 19, 10, 15, 0, 0, time.UTC)) {
		t.Errorf("first = %+v", recs[0])
	}
	if recs[1].Kind != KindNoConsent || len(probs) != 1 {
		t.Errorf("second = %+v probs=%v", recs[1], probs)
	}
}

func TestMerge_SuppressionWinsAndEarliestConsentStays(t *testing.T) {
	d1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	recs := []Record{
		{File: "brevo", Line: 2, Email: "A@x.com", Kind: KindSubscribe, ConsentAt: d2, ConsentSource: "popup", Source: "brevo_import"},
		{File: "elementor", Line: 2, Email: "a@x.com ", Kind: KindSubscribe, ConsentAt: d1, ConsentSource: "club form", Source: "elementor_club_form"},
		{File: "brevo", Line: 3, Email: "b@x.com", Kind: KindSubscribe, ConsentAt: d1, ConsentSource: "popup", Source: "brevo_import"},
		{File: "brevo", Line: 4, Email: "b@x.com", Kind: KindSuppress, Source: "brevo_import"},
		{File: "brevo", Line: 5, Email: "nope", Kind: KindSubscribe},
	}
	var c Counts
	got := Merge(recs, &c)
	if len(got) != 2 || c.InvalidEmail != 1 || c.DuplicatesMerged != 2 {
		t.Fatalf("merged=%d counts=%+v", len(got), c)
	}
	if got[0].consent == nil || !got[0].consent.ConsentAt.Equal(d1) || got[0].consent.Source != "elementor_club_form" {
		t.Errorf("a: %+v", got[0].consent)
	}
	if got[1].suppress == nil {
		t.Errorf("b must be a suppression")
	}
}
