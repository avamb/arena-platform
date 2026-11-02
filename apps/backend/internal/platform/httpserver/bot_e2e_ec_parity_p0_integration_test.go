//go:build integration

// bot_e2e_ec_parity_p0_integration_test.go — BOT-02 of the P0 gates
// (docs/testing/runs/2026-11-01-p0-1/results.md, section 4): the event the
// Telegram wizard created is read back through the very routes the web Event
// Center's screens call, as the same person, and must say what the bot wrote:
//
//   - the events list (useEventsList, GET /v1/organizations/{org}/events),
//   - the event screen's summary (GET .../events/{id}/summary),
//   - the edit wizard's load (GET .../events/{id}/event-bundle).
//
// This is the API half of "the same draft on the EC screen"; the pixels of
// those screens are covered by the event-center smoke and live specs that
// render the same payloads.
package httpserver

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// botEventAsEC is what the bot wrote, in the units the API answers with.
type botEventAsEC struct {
	EventID       uuid.UUID
	Name          string
	Status        string
	Age           string
	StartLocal    string // "2006-01-02 15:04" on the venue clock
	VenueTimezone string
	Capacity      int32
	PriceMinor    int64
	Currency      string
}

func ecGet(t *testing.T, baseURL, token, path string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Client-Channel", "event_center_web")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// assertECSeesTheBotEvent reads the event as the Event Center does, signed in
// as userID, and compares it field by field with want.
func assertECSeesTheBotEvent(t *testing.T, baseURL string, orgID, userID uuid.UUID, want botEventAsEC) {
	t.Helper()
	token, _, err := auth.IssueJWT(botTestJWTSecret, userID, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	org := "/v1/organizations/" + orgID.String()
	tz, err := time.LoadLocation(want.VenueTimezone)
	if err != nil {
		t.Fatal(err)
	}
	local := func(rfc string) string {
		at, err := time.Parse(time.RFC3339, rfc)
		if err != nil {
			t.Fatalf("time %q: %v", rfc, err)
		}
		return at.In(tz).Format("2006-01-02 15:04") // allow:timeformat: test fixture comparison
	}

	// The events list: the row the screen finds the event in.
	var list struct {
		Events []struct {
			ID            string  `json:"id"`
			Name          string  `json:"name"`
			Status        string  `json:"status"`
			AgeRating     *string `json:"age_rating"`
			SessionCount  int     `json:"session_count"`
			NextSessionAt *string `json:"next_session_at"`
		} `json:"events"`
	}
	ecGet(t, baseURL, token, org+"/events", &list)
	found := false
	for _, e := range list.Events {
		if e.ID != want.EventID.String() {
			continue
		}
		found = true
		if e.Name != want.Name || e.Status != want.Status || e.AgeRating == nil || *e.AgeRating != want.Age || e.SessionCount != 1 {
			t.Errorf("EC list row = %s %s age=%v sessions=%d; want %s %s %s 1", e.Name, e.Status, e.AgeRating, e.SessionCount, want.Name, want.Status, want.Age)
		}
		if e.NextSessionAt == nil || local(*e.NextSessionAt) != want.StartLocal {
			t.Errorf("EC list next_session_at = %v; want %s on the venue clock", e.NextSessionAt, want.StartLocal)
		}
	}
	if !found {
		t.Fatalf("the bot's event %s is not in the EC events list", want.EventID)
	}

	// The event screen.
	var sum struct {
		Event struct {
			Name         string `json:"name"`
			Status       string `json:"status"`
			SessionCount int    `json:"session_count"`
		} `json:"event"`
		Tiers []struct {
			PriceAmount int64  `json:"price_amount"`
			Currency    string `json:"currency"`
			IsOpen      bool   `json:"is_open"`
		} `json:"tiers"`
		Sessions []struct {
			StartAt       string  `json:"start_at"`
			CapacityTotal int32   `json:"capacity_total"`
			VenueTimezone *string `json:"venue_timezone"`
		} `json:"sessions"`
	}
	ecGet(t, baseURL, token, org+"/events/"+want.EventID.String()+"/summary", &sum)
	if sum.Event.Name != want.Name || sum.Event.Status != want.Status || sum.Event.SessionCount != 1 {
		t.Errorf("EC event header = %+v; want %s %s 1 session", sum.Event, want.Name, want.Status)
	}
	if len(sum.Tiers) != 1 || sum.Tiers[0].PriceAmount != want.PriceMinor || sum.Tiers[0].Currency != want.Currency {
		t.Errorf("EC categories = %+v; want one at %d %s", sum.Tiers, want.PriceMinor, want.Currency)
	}
	if len(sum.Sessions) != 1 {
		t.Fatalf("EC dates = %d; want 1", len(sum.Sessions))
	}
	s := sum.Sessions[0]
	if local(s.StartAt) != want.StartLocal || s.CapacityTotal != want.Capacity || s.VenueTimezone == nil || *s.VenueTimezone != want.VenueTimezone {
		t.Errorf("EC date = %s cap=%d tz=%v; want %s cap=%d tz=%s", local(s.StartAt), s.CapacityTotal, s.VenueTimezone, want.StartLocal, want.Capacity, want.VenueTimezone)
	}

	// The edit form of the web wizard loads the same event as a bundle.
	var read struct {
		EventID string `json:"event_id"`
		Status  string `json:"status"`
		Bundles []struct {
			Action struct {
				Name string `json:"actionName"`
			} `json:"action"`
			CategoryList []json.RawMessage `json:"categoryList"`
		} `json:"bundles"`
	}
	ecGet(t, baseURL, token, org+"/events/"+want.EventID.String()+"/event-bundle", &read)
	if read.EventID != want.EventID.String() || read.Status != want.Status || len(read.Bundles) != 1 {
		t.Fatalf("EC edit load = %s %s %d bundles; want the event, %s, 1 bundle", read.EventID, read.Status, len(read.Bundles), want.Status)
	}
	if read.Bundles[0].Action.Name != want.Name {
		t.Errorf("EC edit load name = %q; want %q", read.Bundles[0].Action.Name, want.Name)
	}
	if len(read.Bundles[0].CategoryList) != 1 {
		t.Errorf("EC edit load categories = %d; want 1", len(read.Bundles[0].CategoryList))
	}
}
