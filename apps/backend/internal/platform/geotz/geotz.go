// Package geotz is the one table of default IANA time zones for the
// single-zone countries arena sells in.
//
// A venue must always carry a timezone (migration 0118 makes the column NOT
// NULL): every clock time a buyer sees is the session's UTC instant rendered
// in the venue's zone, never in the viewing device's. Every code path that
// creates a venue without an explicit zone — the Telegram bot's wizard, the
// Bil24 catalog import, the 0118 backfill of legacy rows — derives one from
// the country through this table, so the three cannot drift.
//
// Countries with several zones (ES with the Canaries, PT with the Azores, US,
// RU, ...) are deliberately ABSENT: a guess there is wrong for part of the
// country, and the caller must ask. Migration 0118 embeds the same pairs in
// SQL; TestCountryZones_MatchMigration0118 keeps the two identical.
package geotz

import "strings"

// countryZones maps an ISO-3166-1 alpha-2 code to the country's one zone.
var countryZones = map[string]string{
	"CZ": "Europe/Prague", "SK": "Europe/Bratislava", "HU": "Europe/Budapest", "AT": "Europe/Vienna",
	"DE": "Europe/Berlin", "PL": "Europe/Warsaw", "IT": "Europe/Rome", "FR": "Europe/Paris",
	"NL": "Europe/Amsterdam", "BE": "Europe/Brussels", "CH": "Europe/Zurich", "GB": "Europe/London",
	"IE": "Europe/Dublin", "DK": "Europe/Copenhagen", "SE": "Europe/Stockholm", "NO": "Europe/Oslo",
	"FI": "Europe/Helsinki", "EE": "Europe/Tallinn", "LV": "Europe/Riga", "LT": "Europe/Vilnius",
	"IL": "Asia/Jerusalem", "CY": "Asia/Nicosia", "GR": "Europe/Athens", "BG": "Europe/Sofia",
	"RO": "Europe/Bucharest", "HR": "Europe/Zagreb", "SI": "Europe/Ljubljana", "RS": "Europe/Belgrade",
	"TR": "Europe/Istanbul", "GE": "Asia/Tbilisi", "AM": "Asia/Yerevan", "UA": "Europe/Kyiv",
	"MD": "Europe/Chisinau", "BY": "Europe/Minsk", "LU": "Europe/Luxembourg", "MT": "Europe/Malta",
	"AE": "Asia/Dubai", "ME": "Europe/Podgorica", "AL": "Europe/Tirane", "MK": "Europe/Skopje",
	"BA": "Europe/Sarajevo", "IS": "Atlantic/Reykjavik",
}

// ForCountry returns the single IANA zone of the country with the given
// ISO-3166-1 alpha-2 code (any case, surrounding space ignored), or "" for
// a country that is unknown or has several zones.
func ForCountry(iso2 string) string {
	return countryZones[strings.ToUpper(strings.TrimSpace(iso2))]
}

// Countries lists every code the table knows, for tests and diagnostics.
func Countries() []string {
	out := make([]string, 0, len(countryZones))
	for k := range countryZones {
		out = append(out, k)
	}
	return out
}
