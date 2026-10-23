package hpagestats

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestParseRange(t *testing.T) {
	cases := []struct {
		name, query string
		wantFrom    string
		wantTo      string
		wantErr     string
	}{
		{"explicit", "from=2026-05-01&to=2026-05-31", "2026-05-01", "2026-05-31", ""},
		{"default from is 29 days before to", "to=2026-05-30", "2026-05-01", "2026-05-30", ""},
		{"one day", "from=2026-05-05&to=2026-05-05", "2026-05-05", "2026-05-05", ""},
		{"bad from", "from=05/01/2026", "", "", "page_stats.invalid_date"},
		{"bad to", "to=yesterday", "", "", "page_stats.invalid_date"},
		{"impossible date", "from=2026-02-30", "", "", "page_stats.invalid_date"},
		{"backwards", "from=2026-05-10&to=2026-05-01", "", "", "page_stats.invalid_range"},
		{"366 days is allowed", "from=2025-05-01&to=2026-05-01", "2025-05-01", "2026-05-01", ""},
		{"367 days is not", "from=2025-04-30&to=2026-05-01", "", "", "page_stats.range_too_long"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/x?"+c.query, nil)
		from, to, rerr := ParseRange(r, nil)
		if c.wantErr != "" {
			if rerr == nil || rerr.Code != c.wantErr {
				t.Errorf("%s: err = %v, want %s", c.name, rerr, c.wantErr)
			}
			continue
		}
		if rerr != nil {
			t.Errorf("%s: unexpected error %v", c.name, rerr)
			continue
		}
		if DateString(from) != c.wantFrom || DateString(to) != c.wantTo {
			t.Errorf("%s: %s..%s, want %s..%s", c.name, DateString(from), DateString(to), c.wantFrom, c.wantTo)
		}
	}
}

func TestParseRange_DefaultsToTodayInTheVenueZone(t *testing.T) {
	tz := "Pacific/Kiritimati" // UTC+14
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	from, to, rerr := ParseRange(r, &tz)
	if rerr != nil {
		t.Fatal(rerr)
	}
	loc, _ := time.LoadLocation(tz)
	y, m, d := time.Now().In(loc).Date()
	if want := time.Date(y, m, d, 0, 0, 0, 0, time.UTC); !to.Equal(want) {
		t.Errorf("to = %v, want %v", to, want)
	}
	if got := int(to.Sub(from).Hours() / 24); got != DefaultDays-1 {
		t.Errorf("window = %d days apart, want %d", got, DefaultDays-1)
	}
}

func TestBuildResponse_FoldsRowsIntoDaysAndTotals(t *testing.T) {
	day := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	days := []gen.EventPageStatDayRow{
		{Day: day("2026-05-01"), Kind: "page_view", Hits: 10, SuspectHits: 2},
		{Day: day("2026-05-01"), Kind: "video_play", Hits: 3},
		{Day: day("2026-05-03"), Kind: "partner_click", Hits: 1, SuspectHits: 1},
		{Day: day("2026-05-03"), Kind: "page_view", Hits: 5},
		{Day: day("2026-05-03"), Kind: "something_new", Hits: 99}, // ignored, never panics
	}
	src := []gen.EventPageStatSourceRow{{UTMSource: "instagram", UTMCampaign: "spring", Hits: 9, SuspectHits: 1}}
	tz := "Europe/Madrid"
	got := buildResponse(uuid.New(), day("2026-05-01"), day("2026-05-31"), &tz, days, src)

	if got.Totals.PageView != (Counts{15, 2}) || got.Totals.VideoPlay != (Counts{3, 0}) || got.Totals.PartnerClick != (Counts{1, 1}) {
		t.Errorf("totals = %+v", got.Totals)
	}
	if len(got.Days) != 2 || got.Days[0].Day != "2026-05-01" || got.Days[1].Day != "2026-05-03" {
		t.Fatalf("days = %+v", got.Days)
	}
	if got.Days[1].PageView.Hits != 5 || got.Days[1].PartnerClick.SuspectHits != 1 {
		t.Errorf("day 2 = %+v", got.Days[1])
	}
	if len(got.Sources) != 1 || got.Sources[0].UTMSource != "instagram" {
		t.Errorf("sources = %+v", got.Sources)
	}
	empty := buildResponse(uuid.New(), day("2026-05-01"), day("2026-05-31"), nil, nil, nil)
	if empty.Days == nil || empty.Sources == nil {
		t.Error("empty lists must be [] not null")
	}
}

func TestHandleGet_NilQueries_503(t *testing.T) {
	w := httptest.NewRecorder()
	New(nil, nil).HandleGet(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
}
