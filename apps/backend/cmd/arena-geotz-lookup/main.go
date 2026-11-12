// Command arena-geotz-lookup resolves a time zone for each (country, city)
// item of a JSON file with the geotz tables. It never guesses: an item the
// tables cannot place gets an empty timezone and the list of the country's zones.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// Rules of a lookup.
const (
	RuleCountry    = "country"
	RuleCity       = "city"
	RuleUnresolved = "unresolved"
)

// Item is one input row and its answer.
type Item struct {
	Key      string   `json:"key"`
	ISO2     string   `json:"iso2"`
	City     string   `json:"city"`
	Timezone string   `json:"timezone"`
	Rule     string   `json:"rule"`
	Zones    []string `json:"zones,omitempty"`
}

// Lookup fills the timezone and rule of one item.
func Lookup(it Item) Item {
	if tz := geotz.ForCountry(it.ISO2); tz != "" {
		it.Timezone, it.Rule = tz, RuleCountry
		return it
	}
	if tz := geotz.ForCity(it.ISO2, it.City); tz != "" {
		it.Timezone, it.Rule = tz, RuleCity
		return it
	}
	it.Timezone, it.Rule = "", RuleUnresolved
	it.Zones = []string{}
	for _, z := range geotz.ZonesFor(it.ISO2) {
		it.Zones = append(it.Zones, z.ID)
	}
	// A country whose city table holds exactly one zone has only that
	// answer: it is the data's own statement, not a guess.
	if len(it.Zones) == 1 {
		it.Timezone, it.Rule = it.Zones[0], "single_zone"
	}
	return it
}

// Run reads the items from in and writes the answers to out.
func Run(in, out string) error {
	raw, err := os.ReadFile(in) //nolint:gosec // path comes from the operator's flag
	if err != nil {
		return err
	}
	var items []Item
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &items); err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	for i := range items {
		items[i] = Lookup(items[i])
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(items); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o600)
}

func main() {
	in := flag.String("in", "", "input JSON file")
	out := flag.String("out", "", "output JSON file")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: arena-geotz-lookup --in <file> --out <file>")
		os.Exit(2)
	}
	if err := Run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
