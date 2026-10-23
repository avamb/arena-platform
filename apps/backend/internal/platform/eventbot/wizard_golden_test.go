package eventbot

// wizard_golden_test.go — golden bundles shared with the web wizard
// (task 6-1b of the event-center plan).
//
// Each file in testdata/wizard_bundles holds ONE set of answers ("input", in
// the typed form the web wizard keeps: "18,90", "19.00", "1 200") and the
// bundles the bot's BuildBundle makes for it, one per date ("bundles"). The
// Go test below rebuilds the bundles from the input and compares them with the
// file; the web's vitest (apps/event-center/src/lib/wizard/bundle.golden.test.ts)
// builds a draft from the SAME input and compares its own bundles with the same
// file. So the bot and the web cannot drift apart: a rule changed in one
// breaks the other's test until the file is regenerated on purpose:
//
//	ARENA_REGEN_WIZARD_GOLDEN=1 go test ./apps/backend/internal/platform/eventbot -run TestWizardGolden
//
// The venue block of the web bundle is not compared (a new venue is only
// described there, creating it is the save of task 6-1c); the web test checks
// that an existing venue is addressed by the same arenaVenueId.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type goldenTickets struct {
	Mode       string `json:"mode"`
	Categories []struct {
		Name   string `json:"name"`
		Price  string `json:"price"`
		Places string `json:"places"`
		Until  string `json:"until"`
		Limit  string `json:"limit"`
	} `json:"categories"`
	Schedule []struct {
		From  string `json:"from"`
		Price string `json:"price"`
	} `json:"schedule"`
}

type goldenDate struct {
	Day      string        `json:"day"`
	Time     string        `json:"time"`
	SalesEnd string        `json:"salesEnd"`
	Doors    string        `json:"doors"`
	Capacity string        `json:"capacity"`
	Tickets  goldenTickets `json:"tickets"`
}

type goldenInput struct {
	Event struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Age         string `json:"age"`
		PromoterID  string `json:"promoterId"`
	} `json:"event"`
	Currency string `json:"currency"`
	Venue    struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Timezone string `json:"timezone"`
		Capacity int    `json:"capacity"`
	} `json:"venue"`
	ExternalRefPrefix string       `json:"externalRefPrefix"`
	Dates             []goldenDate `json:"dates"`
}

type goldenFile struct {
	Description string            `json:"description"`
	Input       goldenInput       `json:"input"`
	Bundles     []json.RawMessage `json:"bundles"`
}

// draftFromGolden is what the bot's questions would have stored for these
// typed answers, read with the bot's own parsers.
func draftFromGolden(t *testing.T, in goldenInput) *Draft {
	t.Helper()
	d := NewDraft()
	d.Publish = false
	d.Event = DraftEvent{Name: in.Event.Name, Age: in.Event.Age, PromoterID: in.Event.PromoterID, Description: in.Event.Description}
	d.Currency = in.Currency
	for i, dt := range in.Dates {
		tm, ok := ParseTime(dt.Time)
		if !ok {
			t.Fatalf("date %d: time %q", i+1, dt.Time)
		}
		capacity := in.Venue.Capacity
		if dt.Capacity != "" {
			n, ok := ParseCount(dt.Capacity)
			if !ok {
				t.Fatalf("date %d: capacity %q", i+1, dt.Capacity)
			}
			capacity = n
		}
		s := DraftSession{
			Date: dt.Day, Time: tm, VenueID: in.Venue.ID, VenueName: in.Venue.Name,
			Timezone: in.Venue.Timezone, Capacity: capacity,
		}
		if dt.SalesEnd != "" {
			typed, ok := ParseTime(dt.SalesEnd)
			if !ok {
				t.Fatalf("date %d: salesEnd %q", i+1, dt.SalesEnd)
			}
			if s.SalesEndMin, ok = salesEndOffset(tm, typed); !ok {
				t.Fatalf("date %d: salesEnd offset %q", i+1, dt.SalesEnd)
			}
		}
		if dt.Doors != "" {
			typed, ok := ParseTime(dt.Doors)
			if !ok {
				t.Fatalf("date %d: doors %q", i+1, dt.Doors)
			}
			if s.DoorsMin, ok = doorsOffset(tm, typed); !ok {
				t.Fatalf("date %d: doors offset %q", i+1, dt.Doors)
			}
		}
		tk := DraftTickets{}
		switch dt.Tickets.Mode {
		case "single":
			tk.Mode = ModeSingle
		case "parallel":
			tk.Mode = ModeParallel
		case "sequence":
			tk.Mode = ModeSequence
		default:
			t.Fatalf("date %d: ticket mode %q", i+1, dt.Tickets.Mode)
		}
		for _, c := range dt.Tickets.Categories {
			price, ok := ParsePriceMinor(c.Price)
			if !ok {
				t.Fatalf("date %d: price %q", i+1, c.Price)
			}
			cat := DraftCategory{Name: c.Name, PriceMinor: price, SellUntil: c.Until}
			if c.Places != "" {
				if cat.Places, ok = ParseCount(c.Places); !ok {
					t.Fatalf("date %d: places %q", i+1, c.Places)
				}
			}
			if c.Limit != "" {
				if cat.SellLimit, ok = ParseCount(c.Limit); !ok {
					t.Fatalf("date %d: limit %q", i+1, c.Limit)
				}
			}
			tk.Categories = append(tk.Categories, cat)
		}
		for _, st := range dt.Tickets.Schedule {
			price, ok := ParsePriceMinor(st.Price)
			if !ok {
				t.Fatalf("date %d: step price %q", i+1, st.Price)
			}
			tk.Schedule = append(tk.Schedule, DraftPriceStep{From: st.From, PriceMinor: price})
		}
		s.Tickets = &tk
		d.Sessions = append(d.Sessions, s)
	}
	return d
}

func marshalBundle(t *testing.T, d *Draft, idx int, ref string) json.RawMessage {
	t.Helper()
	req := BuildBundle(d, idx, 0, "", ref)
	raw, err := json.MarshalIndent(req, "    ", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func normalizeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWizardGolden_BundlesMatchTheFiles(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "wizard_bundles", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden files: %v", err)
	}
	regen := os.Getenv("ARENA_REGEN_WIZARD_GOLDEN") == "1"
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var g goldenFile
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			d := draftFromGolden(t, g.Input)
			var made []json.RawMessage
			for i := range g.Input.Dates {
				made = append(made, marshalBundle(t, d, i, fmt.Sprintf("%s:%d", g.Input.ExternalRefPrefix, i+1)))
			}
			if regen {
				g.Bundles = made
				var buf bytes.Buffer
				enc := json.NewEncoder(&buf)
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				if err := enc.Encode(g); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			if len(g.Bundles) != len(made) {
				t.Fatalf("%d bundles in the file, the bot makes %d (regenerate with ARENA_REGEN_WIZARD_GOLDEN=1)", len(g.Bundles), len(made))
			}
			for i := range made {
				if !reflect.DeepEqual(normalizeJSON(t, g.Bundles[i]), normalizeJSON(t, made[i])) {
					t.Errorf("date %d: the bot's bundle changed.\nfile: %s\nbot:  %s\nRegenerate with ARENA_REGEN_WIZARD_GOLDEN=1 and check the web test too.", i+1, g.Bundles[i], made[i])
				}
			}
		})
	}
}
