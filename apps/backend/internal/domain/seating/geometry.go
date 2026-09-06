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

// MaxCategories is the Bil24 ceiling on price categories per plan
// (First..Fifteenth — the 15 swatches the SVG convention carries).
// AB-40 A3 enforces it at import and on the hand-entered GA path.
const MaxCategories = 15

// Category kinds (AB-40 A1). The zero value ("", canonicalised away)
// means seated: seats bind to the category by fill colour, exactly as
// before AB-40. KindGeneralAdmission marks a category that carries a
// bulk Capacity and no coordinate-bearing seats.
const (
	KindSeated           = "seated"
	KindGeneralAdmission = "general_admission"
)

// Canvas is the pixel-space canvas the seats live on. width/height are
// taken from the SVG viewBox (or width/height attributes as a fallback).
type Canvas struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Point is a polygon vertex in canvas space (AB-40 A1: a GA category may
// carry an optional hit-test polygon in combined plans).
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Category is a price-category descriptor derived from the PriceCategory
// SVG group (§6 rule 5) or hand-entered on the GA-only path (AB-40 C1).
// Index is 1-based and matches the swatch order inside the group; Color
// is the lowercase 6-digit hex fill (#rrggbb) which is what
// seat-to-category binding matches on (§6 rule 7).
// PriceHint/CurrencyHint are import hints only; real ticket_tiers binding
// happens per session (SEAT-B2).
//
// AB-40 A1: Kind distinguishes seated categories (empty / KindSeated —
// seats bind by colour) from general-admission categories, which carry
// their own Capacity and an optional Polygon for hit-testing. Both kinds
// live in this one list so the admin renders a single
// `Category | Seats | Starting price` table exactly like Bil24's.
type Category struct {
	Index        int    `json:"index"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	PriceHint    string `json:"price_hint,omitempty"`
	CurrencyHint string `json:"currency_hint,omitempty"`
	// ExternalID is the upstream identity of the category when the plan
	// came from a foreign system (§13.3: the Bil24 `categoryPriceId`
	// carried as `sbt:id` in the sbt-SVG <metadata>). Zero means "no
	// external identity", and is omitted from the canonical JSON so
	// checksums of plans authored inside arena are unaffected.
	ExternalID int64 `json:"external_id,omitempty"`
	// Kind is "" (canonical form of seated) or KindGeneralAdmission.
	Kind string `json:"kind,omitempty"`
	// Capacity is the declared bulk capacity of a GA category. Always 0
	// for seated categories (their seat count is derived from Sections).
	Capacity int `json:"capacity,omitempty"`
	// Polygon is the optional GA hit-test area in canvas space. Present
	// when a combined plan's SVG carries a #GA element; absent on the
	// GA-only hand-entered path. Vertex order is preserved as authored.
	Polygon []Point `json:"polygon,omitempty"`
}

// IsGA reports whether the category is a general-admission category.
func (c Category) IsGA() bool {
	return c.Kind == KindGeneralAdmission
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
	// ExternalID is the upstream identity of the seat when the plan came
	// from a foreign system (§13.3: the Bil24 `seatId` carried as
	// `sbt:id` on the <circle>). Materialisation copies it into
	// session_seats.system_seat_id so the very same integer keeps
	// addressing the seat on the wire after a rebind. Zero means "no
	// external identity" and is omitted from the canonical JSON, so
	// checksums of plans authored inside arena are unaffected.
	ExternalID int64 `json:"external_id,omitempty"`
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

// NOTE (AB-40 A4): the former StandingZone parallel array is retired.
// GA capacity is carried by Category entries with
// Kind == KindGeneralAdmission — one representation, not two. Stored
// geometries that still contain a "standing_zones" field unmarshal
// cleanly (the unknown field is dropped); the only populated instance
// was the widget e2e seed, updated with this change.

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
	SchemaVersion int        `json:"schema_version"`
	Canvas        Canvas     `json:"canvas"`
	Categories    []Category `json:"categories"`
	Sections      []Section  `json:"sections"`
	Tables        []Table    `json:"tables"`
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
//
// External ids (Seat.ExternalID / Category.ExternalID, §13.3) travel
// through canonicalisation untouched and therefore participate in
// CanonicalJSON and Checksum: two imports of the same sbt-SVG hash
// alike, and re-importing a plan whose upstream ids changed produces a
// new geometry version, which is the intended signal.
func Canonicalize(g Geometry) Geometry {
	out := Geometry{
		SchemaVersion: SchemaVersion,
		Canvas:        g.Canvas,
		Categories:    append([]Category(nil), g.Categories...),
		Sections:      make([]Section, len(g.Sections)),
		Tables:        append([]Table(nil), g.Tables...),
		DecorSVG:      g.DecorSVG,
	}
	if out.Categories == nil {
		out.Categories = []Category{}
	}
	if out.Tables == nil {
		out.Tables = []Table{}
	}
	for i := range out.Categories {
		out.Categories[i].Color = normalizeColor(out.Categories[i].Color)
		// Canonical form of the seated kind is the empty string, so
		// pre-AB-40 stored geometries and new ones canonicalise alike.
		if out.Categories[i].Kind == KindSeated {
			out.Categories[i].Kind = ""
		}
		if len(out.Categories[i].Polygon) == 0 {
			out.Categories[i].Polygon = nil
		}
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

// GACategories returns the general-admission categories of g, in Index
// order (assuming g is canonical).
func (g Geometry) GACategories() []Category {
	var out []Category
	for _, c := range g.Categories {
		if c.IsGA() {
			out = append(out, c)
		}
	}
	return out
}

// GACapacity returns the summed declared capacity of every GA category
// (AB-40 A2/B6). This is the derived value stored in
// seating_plan_versions.capacity_standing.
func (g Geometry) GACapacity() int {
	n := 0
	for _, c := range g.Categories {
		if c.IsGA() {
			n += c.Capacity
		}
	}
	return n
}

// ValidateForPlanType enforces which geometry primitives a plan type
// permits (AB-40 B3 + the 0057 column contract). It applies to both the
// SVG import path and the direct-geometry path, so a hand-entered
// GA-only plan obeys the same rules as an imported combined one:
//
//   - every plan: at most MaxCategories categories; GA capacities must
//     be positive; seats must not bind to a GA category.
//   - assigned_seats: at least one seat, no GA categories.
//   - general_admission: at least one GA category, no seats.
//   - mixed: at least one seat AND at least one GA category.
//   - tables: reserved, not validated here.
//
// The returned slice is empty when g is valid for planType.
func ValidateForPlanType(g Geometry, planType string) ValidationErrors {
	var errs ValidationErrors

	if len(g.Categories) > MaxCategories {
		errs = append(errs, ValidationError{
			Code:    ErrTooManyCategories,
			Element: "categories",
			Detail: fmt.Sprintf("%d categories exceed the Bil24 ceiling of %d",
				len(g.Categories), MaxCategories),
		})
	}

	gaIdx := map[int]bool{}
	gaCount := 0
	for _, c := range g.Categories {
		if !c.IsGA() {
			continue
		}
		gaCount++
		gaIdx[c.Index] = true
		if c.Capacity <= 0 {
			errs = append(errs, ValidationError{
				Code:    ErrGACapacityInvalid,
				Element: c.Name,
				Detail:  fmt.Sprintf("GA category %q capacity must be positive, got %d", c.Name, c.Capacity),
			})
		}
	}
	for _, sec := range g.Sections {
		for _, r := range sec.Rows {
			for _, s := range r.Seats {
				if gaIdx[s.CategoryIndex] {
					errs = append(errs, ValidationError{
						Code:    ErrSeatInGACategory,
						Element: s.Key,
						Detail:  "seat binds to a general-admission category",
					})
				}
			}
		}
	}

	seats := g.SeatCount()
	switch planType {
	case "assigned_seats":
		if seats == 0 {
			errs = append(errs, ValidationError{
				Code:   ErrSeatsMissing,
				Detail: "an assigned_seats plan must contain at least one seat",
			})
		}
		if gaCount > 0 {
			errs = append(errs, ValidationError{
				Code:   ErrGAAreaNotAllowed,
				Detail: "an assigned_seats plan must not carry general-admission categories",
			})
		}
	case "general_admission":
		if gaCount == 0 {
			errs = append(errs, ValidationError{
				Code:   ErrGAAreaMissing,
				Detail: "a general_admission plan must declare at least one GA category",
			})
		}
		if seats > 0 {
			errs = append(errs, ValidationError{
				Code:   ErrSeatsNotAllowed,
				Detail: "a general_admission plan must not contain coordinate-bearing seats",
			})
		}
	case "mixed":
		if seats == 0 {
			errs = append(errs, ValidationError{
				Code:   ErrSeatsMissing,
				Detail: "a mixed plan must contain at least one seat",
			})
		}
		if gaCount == 0 {
			errs = append(errs, ValidationError{
				Code:   ErrGAAreaMissing,
				Detail: "a mixed plan must declare at least one GA area (label an element \"#GA <name>\" with a capacity <title>)",
			})
		}
	}
	return errs
}
