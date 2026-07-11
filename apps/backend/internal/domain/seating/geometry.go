// geometry.go implements the canonical geometry JSON model (§5.3 of
// 09_autoforge/seating_backlog.md) plus deterministic canonicalisation
// and sha256 checksumming. See doc.go for the layer contract.
package seating

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// SchemaVersion is the canonical geometry schema version emitted by this
// package. It is stored verbatim in geometry.SchemaVersion so downstream
// consumers can detect format upgrades without probing shape.
const SchemaVersion = 1

// MaxCanvasDimension is the Bil24 authoring limit for the seating scheme
// canvas (§6 rule 1). Any width or height above this triggers a
// ValidationError with code ErrCanvasTooLarge.
const MaxCanvasDimension = 2000

// Canvas is the pixel-space canvas the seats live on. width/height are
// taken from the SVG viewBox (or width/height attributes as a fallback).
type Canvas struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Category is a price-category descriptor derived from the PriceCategory
// SVG group (§6 rule 5). Index is 1-based and matches the swatch order
// inside the group; Color is the lowercase 6-digit hex fill (#rrggbb)
// which is what seat-to-category binding matches on (§6 rule 7).
// PriceHint/CurrencyHint are import hints only; real ticket_tiers binding
// happens per session (SEAT-B2).
type Category struct {
	Index        int    `json:"index"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	PriceHint    string `json:"price_hint,omitempty"`
	CurrencyHint string `json:"currency_hint,omitempty"`
}

// Seat is a single reservable seat. Key is the stable identifier
// "<section.key>|<row.key>|<number>" copied verbatim into
// session_seats.seat_key at session-binding time. CategoryIndex is a
// 1-based reference into Geometry.Categories.
type Seat struct {
	Key           string  `json:"key"`
	Number        string  `json:"number"`
	X             float64 `json:"x"`
	Y             float64 `json:"y"`
	Radius        float64 `json:"radius"`
	CategoryIndex int     `json:"category_index"`
	BarcodeHint   *string `json:"barcode_hint"`
}

// Row is a horizontal seat run belonging to a Section. Seats are
// canonicalised in ascending Key order.
type Row struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Seats []Seat `json:"seats"`
}

// Section is a named seating area (Parter, Balcony left, ...). Rows are
// canonicalised in ascending Key order.
type Section struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Rows []Row  `json:"rows"`
}

// StandingZone reserves the shape for plan_type="mixed"/"tables" in
// future waves. It is emitted as an empty slice in this wave.
type StandingZone struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
}

// Table reserves the shape for plan_type="tables" in future waves. It is
// emitted as an empty slice in this wave.
type Table struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
}

// Geometry is the canonical geometry JSON structure stored in
// seating_plan_versions.geometry (§5.3). DecorSVG carries every SVG
// element that is not a seat / category swatch / legend, so the client
// can render the backdrop.
//
// Note on DecorSVG fidelity: the importer re-serialises the decor
// fragment deterministically, and attributes / element names from
// namespaces outside the fixed knownNamespacePrefixes table (svg,
// inkscape, sodipodi — see svg_import.go qname) are intentionally
// dropped to their local names. Round-tripping arbitrary
// author-supplied xmlns prefixes would make the serialisation (and
// therefore geometry_checksum) non-deterministic. Stored checksums
// depend on this behaviour — do not change it.
type Geometry struct {
	SchemaVersion int            `json:"schema_version"`
	Canvas        Canvas         `json:"canvas"`
	Categories    []Category     `json:"categories"`
	Sections      []Section      `json:"sections"`
	StandingZones []StandingZone `json:"standing_zones"`
	Tables        []Table        `json:"tables"`
	// DecorSVG holds the deterministically re-serialised decor fragment;
	// unknown-namespace attributes/elements are intentionally dropped for
	// output determinism (stored checksums) — see svg_import.go qname.
	DecorSVG string `json:"decor_svg"`
}

// SeatKey builds the canonical "<section>|<row>|<number>" identifier
// used to link Seat back to session_seats.seat_key. Kept public so the
// SVG importer and any future editor can share exactly one derivation.
func SeatKey(sectionKey, rowKey, seatNumber string) string {
	return sectionKey + "|" + rowKey + "|" + seatNumber
}

// Canonicalize returns g with categories sorted by Index, sections /
// rows / seats sorted by Key, and all optional string fields normalised
// (lowercase colour hex). The returned Geometry is a deep copy — callers
// may mutate it without affecting the input.
//
// Canonicalisation is the pre-condition for a stable Checksum: two
// imports of the same SVG must produce byte-identical JSON, so seat
// order within a row and row order within a section MUST NOT depend on
// document order in the source SVG.
func Canonicalize(g Geometry) Geometry {
	out := Geometry{
		SchemaVersion: SchemaVersion,
		Canvas:        g.Canvas,
		Categories:    append([]Category(nil), g.Categories...),
		Sections:      make([]Section, len(g.Sections)),
		StandingZones: append([]StandingZone(nil), g.StandingZones...),
		Tables:        append([]Table(nil), g.Tables...),
		DecorSVG:      g.DecorSVG,
	}
	if out.Categories == nil {
		out.Categories = []Category{}
	}
	if out.StandingZones == nil {
		out.StandingZones = []StandingZone{}
	}
	if out.Tables == nil {
		out.Tables = []Table{}
	}
	for i := range out.Categories {
		out.Categories[i].Color = normalizeColor(out.Categories[i].Color)
	}
	sort.SliceStable(out.Categories, func(i, j int) bool {
		return out.Categories[i].Index < out.Categories[j].Index
	})
	for i, sec := range g.Sections {
		rows := make([]Row, len(sec.Rows))
		for j, r := range sec.Rows {
			seats := append([]Seat(nil), r.Seats...)
			sort.SliceStable(seats, func(a, b int) bool {
				return seats[a].Key < seats[b].Key
			})
			rows[j] = Row{Key: r.Key, Name: r.Name, Seats: seats}
		}
		sort.SliceStable(rows, func(a, b int) bool {
			return rows[a].Key < rows[b].Key
		})
		out.Sections[i] = Section{Key: sec.Key, Name: sec.Name, Rows: rows}
	}
	sort.SliceStable(out.Sections, func(a, b int) bool {
		return out.Sections[a].Key < out.Sections[b].Key
	})
	sort.SliceStable(out.StandingZones, func(a, b int) bool {
		return out.StandingZones[a].Key < out.StandingZones[b].Key
	})
	sort.SliceStable(out.Tables, func(a, b int) bool {
		return out.Tables[a].Key < out.Tables[b].Key
	})
	return out
}

// CanonicalJSON encodes g via Canonicalize and returns the byte-stable
// JSON representation. Uses encoding/json (no HTML escape, no trailing
// newline) so the output is safe to sha256 directly.
func CanonicalJSON(g Geometry) ([]byte, error) {
	canonical := Canonicalize(g)
	// json.Marshal orders struct fields by declaration order, which is
	// itself stable, and encodes maps with sorted keys. We deliberately
	// do not use MarshalIndent — determinism-of-bytes requires no
	// pretty-printing.
	buf, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("seating: canonical marshal: %w", err)
	}
	return buf, nil
}

// Checksum returns the sha256 hex digest of the canonical JSON encoding
// of g. This is the value stored in seating_plan_versions.geometry_checksum
// and used as the ETag for schema endpoints.
func Checksum(g Geometry) (string, error) {
	buf, err := CanonicalJSON(g)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// SeatCount returns the total number of Seat entries across every
// Section/Row in g. Useful for capacity_seated on seating_plan_versions.
func (g Geometry) SeatCount() int {
	n := 0
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			n += len(r.Seats)
		}
	}
	return n
}
