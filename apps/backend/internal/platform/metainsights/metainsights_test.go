package metainsights

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

func TestCurrencyExponent(t *testing.T) {
	for cur, want := range map[string]int{
		"EUR": 2, "usd": 2, "ILS": 2, "JPY": 0, "KRW": 0, "ISK": 0, "CLP": 0,
		"BHD": 3, "KWD": 3, "OMR": 3, "JOD": 3, "TND": 3, "": 2,
	} {
		if got := CurrencyExponent(cur); got != want {
			t.Errorf("CurrencyExponent(%q) = %d, want %d", cur, got, want)
		}
	}
}

func TestDecimalToMinor(t *testing.T) {
	cases := []struct {
		in   string
		exp  int
		want int64
		bad  bool
	}{
		{"12.34", 2, 1234, false},
		{"12.3", 2, 1230, false},
		{"12", 2, 1200, false},
		{"0.01", 2, 1, false},
		{".5", 2, 50, false},
		{"5.", 2, 500, false},
		{"0.125", 2, 13, false},
		{"0.124", 2, 12, false},
		{"-1.255", 2, -126, false},
		{"1234", 0, 1234, false},
		{"1234.5", 0, 1235, false},
		{"1234.4", 0, 1234, false},
		{"1.234", 3, 1234, false},
		{"1.2", 3, 1200, false},
		{"0.0005", 3, 1, false},
		{"", 2, 0, false},
		{"92233720368547758.07", 2, 9223372036854775807, false},
		{"92233720368547758.08", 2, 0, true},
		{"1e5", 2, 0, true},
		{"abc", 2, 0, true},
		{"1.2.3", 2, 0, true},
		{".", 2, 0, true},
		{"-", 2, 0, true},
		{"1,5", 2, 0, true},
	}
	for _, c := range cases {
		got, err := DecimalToMinor(c.in, c.exp)
		if c.bad {
			if err == nil {
				t.Errorf("DecimalToMinor(%q, %d) = %d, want an error", c.in, c.exp, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("DecimalToMinor(%q, %d) = %d, %v; want %d", c.in, c.exp, got, err, c.want)
		}
	}
}

func TestFormatMinor(t *testing.T) {
	for _, c := range []struct {
		minor int64
		cur   string
		want  string
	}{
		{123456, "EUR", "1234.56 EUR"},
		{5, "EUR", "0.05 EUR"},
		{-5, "eur", "-0.05 EUR"},
		{1500, "JPY", "1500 JPY"},
		{1234, "KWD", "1.234 KWD"},
	} {
		if got := FormatMinor(c.minor, c.cur); got != c.want {
			t.Errorf("FormatMinor(%d, %q) = %q, want %q", c.minor, c.cur, got, c.want)
		}
	}
}

func TestPickPurchase(t *testing.T) {
	av := func(kv ...string) []ActionValue {
		var out []ActionValue
		for i := 0; i+1 < len(kv); i += 2 {
			out = append(out, ActionValue{ActionType: kv[i], Value: kv[i+1]})
		}
		return out
	}
	// omni_purchase wins and is never summed with the others.
	n, v := PickPurchase(
		av("link_click", "40", "purchase", "3", "omni_purchase", "4", "offsite_conversion.fb_pixel_purchase", "3"),
		av("purchase", "30.00", "omni_purchase", "41.50", "offsite_conversion.fb_pixel_purchase", "30.00"), 2)
	if n != 4 || v != 4150 {
		t.Errorf("omni first: %d %d", n, v)
	}
	n, v = PickPurchase(av("offsite_conversion.fb_pixel_purchase", "2", "purchase", "1"), av("purchase", "10", "offsite_conversion.fb_pixel_purchase", "25.5"), 2)
	if n != 1 || v != 1000 {
		t.Errorf("purchase before pixel: %d %d", n, v)
	}
	n, v = PickPurchase(av("offsite_conversion.fb_pixel_purchase", "2"), av("offsite_conversion.fb_pixel_purchase", "25.5"), 2)
	if n != 2 || v != 2550 {
		t.Errorf("pixel only: %d %d", n, v)
	}
	n, v = PickPurchase(nil, av("purchase", "1500"), 0)
	if n != 0 || v != 1500 {
		t.Errorf("value only: %d %d", n, v)
	}
	n, v = PickPurchase(av("link_click", "3"), nil, 2)
	if n != 0 || v != 0 {
		t.Errorf("no purchase: %d %d", n, v)
	}
}

func TestPlanRange(t *testing.T) {
	done := time.Now()
	today := time.Date(2026, 11, 23, 22, 30, 0, 0, time.FixedZone("x", -8*3600))
	cases := []struct {
		name      string
		st        State
		selected  string
		mode      string
		wantMode  string
		wantSince string
	}{
		{"never synced", State{}, "act_1", ModeManual, ModeBackfill, "2026-08-26"},
		{"backfill not finished", State{AdAccountID: "act_1"}, "act_1", ModeScheduled, ModeBackfill, "2026-08-26"},
		{"account changed", State{AdAccountID: "act_1", BackfillDoneAt: &done}, "act_2", ModeScheduled, ModeBackfill, "2026-08-26"},
		{"scheduled", State{AdAccountID: "act_1", BackfillDoneAt: &done}, "act_1", ModeScheduled, ModeScheduled, "2026-11-17"},
		{"interval", State{AdAccountID: "act_1", BackfillDoneAt: &done}, "act_1", ModeInterval, ModeInterval, "2026-11-17"},
		{"manual", State{AdAccountID: "act_1", BackfillDoneAt: &done}, "act_1", ModeManual, ModeManual, "2026-11-21"},
		{"backfill asked but done", State{AdAccountID: "act_1", BackfillDoneAt: &done}, "act_1", ModeBackfill, ModeScheduled, "2026-11-17"},
	}
	for _, c := range cases {
		mode, since, until := PlanRange(c.st, c.selected, c.mode, today)
		if mode != c.wantMode || since.Format("2006-01-02") != c.wantSince || until.Format("2006-01-02") != "2026-11-23" {
			t.Errorf("%s: %s %s..%s, want %s %s..2026-11-23", c.name, mode, since.Format("2006-01-02"), until.Format("2006-01-02"), c.wantMode, c.wantSince)
		}
		if c.wantMode == ModeBackfill && int(until.Sub(since).Hours()/24)+1 != BackfillDays {
			t.Errorf("%s: backfill is not %d days", c.name, BackfillDays)
		}
	}
}

func TestBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{
		-1: time.Hour, 0: time.Hour, 1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour,
		4: 8 * time.Hour, 5: 16 * time.Hour, 6: 24 * time.Hour, 7: 24 * time.Hour, 1000: 24 * time.Hour,
	} {
		if got := Backoff(n); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestMetaTotalsAddNeverMixesCurrencies(t *testing.T) {
	m := MetaTotals{Currency: "EUR"}
	if !m.add(MetaTotals{Currency: "eur", SpendMinor: 100, Impressions: 5}) {
		t.Fatal("same currency refused")
	}
	if m.add(MetaTotals{Currency: "USD", SpendMinor: 900, Impressions: 50}) {
		t.Fatal("another currency added")
	}
	if m.SpendMinor != 100 || m.Impressions != 5 || m.Currency != "EUR" {
		t.Fatalf("totals = %+v", m)
	}
	var empty MetaTotals
	empty.add(MetaTotals{Currency: "ILS", SpendMinor: 7})
	if empty.Currency != "ILS" || empty.SpendMinor != 7 {
		t.Fatalf("empty totals = %+v", empty)
	}
}

func TestDue(t *testing.T) {
	// 2026-11-23 is winter time in Madrid: UTC+1.
	at := func(h, m int) time.Time { return time.Date(2026, 11, 23, h, m, 0, 0, time.UTC) }
	ago := func(now time.Time, d time.Duration) *time.Time { x := now.Add(-d); return &x }
	night := at(3, 30) // 04:30 Madrid
	day := at(12, 0)   // 13:00 Madrid
	ok := func(now time.Time, success, run time.Duration) State {
		return State{AdAccountID: "act_1", BackfillDoneAt: ago(now, 100*time.Hour), LastSuccessAt: ago(now, success), LastRunAt: ago(now, run)}
	}
	cases := []struct {
		name     string
		st       State
		account  string
		now      time.Time
		every    time.Duration
		wantMode string
	}{
		{"never synced", State{}, "act_1", day, 0, ModeBackfill},
		{"account changed", ok(day, 2*time.Hour, 2*time.Hour), "act_2", day, 0, ModeBackfill},
		{"backfill failed a minute ago", State{LastRunAt: ago(day, time.Minute)}, "act_1", day, 0, ""},
		{"backfill failed two hours ago", State{LastRunAt: ago(day, 2*time.Hour)}, "act_1", day, 0, ModeBackfill},
		{"night, stale", ok(night, 21*time.Hour, 21*time.Hour), "act_1", night, 0, ModeScheduled},
		{"night, fresh", ok(night, 19*time.Hour, 19*time.Hour), "act_1", night, 0, ""},
		{"day, stale", ok(day, 30*time.Hour, 30*time.Hour), "act_1", day, 0, ""},
		{"02:59 Madrid", ok(at(1, 59), 30*time.Hour, 30*time.Hour), "act_1", at(1, 59), 0, ""},
		{"03:00 Madrid", ok(at(2, 0), 30*time.Hour, 30*time.Hour), "act_1", at(2, 0), 0, ModeScheduled},
		{"06:59 Madrid", ok(at(5, 59), 30*time.Hour, 30*time.Hour), "act_1", at(5, 59), 0, ModeScheduled},
		{"07:00 Madrid", ok(at(6, 0), 30*time.Hour, 30*time.Hour), "act_1", at(6, 0), 0, ""},
		{"night, failed 30 minutes ago", ok(night, 25*time.Hour, 30*time.Minute), "act_1", night, 0, ""},
		{"every elapsed", ok(day, 31*time.Minute, 31*time.Minute), "act_1", day, 30 * time.Minute, ModeInterval},
		{"every not elapsed", ok(day, 20*time.Minute, 20*time.Minute), "act_1", day, 30 * time.Minute, ""},
		{"every shortens the gap after a success", ok(day, 20*time.Minute, 20*time.Minute), "act_1", day, 15 * time.Minute, ModeInterval},
		{"a failed run waits 1 h even with every 15m", ok(day, 3*time.Hour, 20*time.Minute), "act_1", day, 15 * time.Minute, ""},
		{"a failed run is retried after 1 h", ok(day, 3*time.Hour, 61*time.Minute), "act_1", day, 15 * time.Minute, ModeInterval},
		{"every off", ok(day, 10*time.Hour, 10*time.Hour), "act_1", day, 0, ""},
	}
	failed := func(n int, run time.Duration) State {
		s := ok(day, 50*time.Hour, run)
		s.LastError, s.Failures = "graph_rate_limited", n
		return s
	}
	cases = append(cases, []struct {
		name     string
		st       State
		account  string
		now      time.Time
		every    time.Duration
		wantMode string
	}{
		{"1 failure, 59 min", failed(1, 59*time.Minute), "act_1", day, 15 * time.Minute, ""},
		{"1 failure, 61 min", failed(1, 61*time.Minute), "act_1", day, 15 * time.Minute, ModeInterval},
		{"3 failures, 3 h 59", failed(3, 239*time.Minute), "act_1", day, 15 * time.Minute, ""},
		{"3 failures, 4 h 01", failed(3, 241*time.Minute), "act_1", day, 15 * time.Minute, ModeInterval},
		{"10 failures, 23 h", failed(10, 23*time.Hour), "act_1", day, 15 * time.Minute, ""},
		{"10 failures, 25 h", failed(10, 25*time.Hour), "act_1", day, 15 * time.Minute, ModeInterval},
		{"failed backfill waits too", State{LastRunAt: ago(day, 30*time.Minute), LastError: "graph_190", Failures: 1}, "act_1", day, 15 * time.Minute, ""},
	}...)
	for _, c := range cases {
		mode, due := Due(c.st, c.account, c.now, c.every)
		if mode != c.wantMode || due != (c.wantMode != "") {
			t.Errorf("%s: Due = %q %v, want %q", c.name, mode, due, c.wantMode)
		}
	}
	// Summer time: Madrid is UTC+2, 03:00 Madrid is 01:00 UTC.
	summer := time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC)
	if mode, _ := Due(ok(summer, 30*time.Hour, 30*time.Hour), "act_1", summer, 0); mode != ModeScheduled {
		t.Errorf("summer 03:00 Madrid: %q", mode)
	}
}

func TestClassify(t *testing.T) {
	ge := func(code int) error { return &metaconnect.GraphError{HTTPStatus: 400, Code: code} }
	cases := []struct {
		err   error
		class ErrorClass
		code  string
	}{
		{ge(190), ClassTokenInvalid, "graph_190"},
		{ge(10), ClassPermission, "graph_10"},
		{ge(100), ClassPermission, "graph_100"},
		{ge(200), ClassPermission, "graph_200"},
		{ge(299), ClassPermission, "graph_299"},
		{ge(4), ClassRateLimited, "graph_rate_limited"},
		{ge(17), ClassRateLimited, "graph_rate_limited"},
		{ge(32), ClassRateLimited, "graph_rate_limited"},
		{ge(613), ClassRateLimited, "graph_rate_limited"},
		{ge(80000), ClassRateLimited, "graph_rate_limited"},
		{ge(80014), ClassRateLimited, "graph_rate_limited"},
		{ge(80015), ClassOther, "graph_80015"},
		{ge(2), ClassOther, "graph_2"},
		{errors.New("metainsights: graph request failed"), ClassOther, "graph_unavailable"},
		{ErrTooManyPages, ClassOther, "insights_too_many_pages"},
	}
	for _, c := range cases {
		class, code := Classify(c.err)
		if class != c.class || code != c.code {
			t.Errorf("Classify(%v) = %d %q, want %d %q", c.err, class, code, c.class, c.code)
		}
	}
}

func TestSummaryTextAndMER(t *testing.T) {
	if m := MER(10000, "EUR", 2500, "eur"); m != "4.00" {
		t.Errorf("MER = %q", m)
	}
	if m := MER(10000, "USD", 2500, "EUR"); m != "" {
		t.Errorf("MER across currencies = %q", m)
	}
	if m := MER(10000, "EUR", 0, "EUR"); m != "" {
		t.Errorf("MER with no spend = %q", m)
	}
	text := SummaryText(Summary{Day: "2026-11-22", Currency: "EUR", SpendMinor: 2500, Impressions: 1200, LinkClicks: 40, Purchases: 3,
		Ours: []OurMoney{{Currency: "EUR", PaidOrders: 2, RevenueNetMin: 9000}, {Currency: "ILS", PaidOrders: 1, RevenueNetMin: 5000}}})
	for _, want := range []string{"Meta ads, 2026-11-22", "Spend: 25.00 EUR", "Impressions: 1200", "Link clicks: 40",
		"Purchases reported by Meta: 3", "Our paid orders from these ads: 2, net 90.00 EUR", "MER (net revenue / spend): 3.60",
		"Our paid orders from these ads: 1, net 50.00 ILS"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary misses %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "MER") != 1 {
		t.Errorf("MER only for the matching currency:\n%s", text)
	}
	empty := SummaryText(Summary{Day: "2026-11-22", Currency: "JPY", SpendMinor: 1500})
	if !strings.Contains(empty, "Spend: 1500 JPY") || !strings.Contains(empty, "Our paid orders from these ads: 0") || strings.Contains(empty, "MER") {
		t.Errorf("empty summary:\n%s", empty)
	}
}

// TestClient_InsightsParsesAndPages: rows are parsed at ad level, the token
// is in the header only, the paging cursor is followed, and a Graph error
// keeps only the codes.
func TestClient_InsightsParsesAndPages(t *testing.T) {
	calls := 0
	var counted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok-secret" || strings.Contains(r.URL.RawQuery, "tok-secret") {
			t.Errorf("token must travel in the header only")
		}
		if r.URL.Path != "/v25.0/act_9/insights" || r.URL.Query().Get("level") != "ad" || r.URL.Query().Get("time_increment") != "1" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.URL.Query().Get("time_range") != `{"since":"2026-11-01","until":"2026-11-03"}` {
			t.Errorf("time_range = %q", r.URL.Query().Get("time_range"))
		}
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(`{"data":[{"ad_id":"a1","ad_name":"Ad 1","adset_id":"s1","campaign_id":"c1","campaign_name":"C 1",
				"spend":"12.34","impressions":"1000","clicks":"20","inline_link_clicks":"15","account_currency":"EUR","date_start":"2026-11-01",
				"actions":[{"action_type":"purchase","value":"2"},{"action_type":"omni_purchase","value":"3"}],
				"action_values":[{"action_type":"omni_purchase","value":"45.5"}]}],
				"paging":{"cursors":{"after":"CUR1"},"next":"https://graph.example/next"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"ad_id":"a2","campaign_id":"c1","spend":"1500","date_start":"2026-11-02","account_currency":"JPY"},
			{"ad_id":"","date_start":"2026-11-02"}],"paging":{"cursors":{"after":"CUR2"}}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "v25.0", nil)
	rows, err := c.Insights(context.Background(), "tok-secret", "act_9", "2026-11-01", "2026-11-03", "EUR", func(ep string, failed bool) {
		if failed {
			ep += ":failed"
		}
		counted = append(counted, ep)
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(counted) != 2 || counted[0] != EndpointInsights {
		t.Fatalf("calls %d, counted %v", calls, counted)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.AdID != "a1" || r.SpendMinor != 1234 || r.Impressions != 1000 || r.Clicks != 20 || r.LinkClicks != 15 ||
		r.Purchases != 3 || r.PurchaseValueMinor != 4550 || r.Currency != "EUR" || r.Day != "2026-11-01" || r.CampaignName != "C 1" {
		t.Errorf("row 1 = %+v", r)
	}
	if rows[1].SpendMinor != 1500 || rows[1].Currency != "JPY" {
		t.Errorf("row 2 = %+v", rows[1])
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Error validating access token: secret text","code":190,"error_subcode":460,"type":"OAuthException"}}`))
	}))
	defer bad.Close()
	failed := 0
	_, err = NewClient(bad.URL, "v25.0", nil).Insights(context.Background(), "tok-secret", "act_9", "2026-11-01", "2026-11-03", "EUR",
		func(_ string, f bool) {
			if f {
				failed++
			}
		})
	if !metaconnect.IsTokenInvalid(err) || failed != 1 {
		t.Fatalf("190: %v, failed %d", err, failed)
	}
	if strings.Contains(err.Error(), "secret text") || strings.Contains(err.Error(), bad.URL) || strings.Contains(err.Error(), "tok-secret") {
		t.Errorf("error leaks: %v", err)
	}
}
