// Package seating is the pure-domain layer for the seating-plan bounded
// context (feature #303, wave SEAT-A).
//
// The package owns:
//
//   - Canonical geometry JSON model (§5.3 of
//     09_autoforge/seating_backlog.md) — Canvas / Categories / Sections /
//     Rows / Seats / StandingZones / Tables / DecorSVG. This is the
//     source-of-truth representation stored in the
//     seating_plan_versions.geometry column.
//   - Deterministic canonicalisation + sha256 checksum (Canonicalize,
//     Checksum), used as the geometry_checksum column and as the ETag for
//     schema endpoints. Canonicalisation is achieved by encoding through
//     encoding/json which produces sorted-key object output when structs
//     are marshalled and by writing every slice in a stable order defined
//     by the domain (categories by Index, sections/rows/seats by Key).
//   - SVG parser / validator that reproduces the Bil24 Editor authoring
//     conventions from §6: canvas ≤2000×2000 px, circles-only seats, row
//     groups marked with inkscape:label="#<SectorName>" and a <title>
//     child (row name), each seat <circle> carrying a nested <title>
//     (seat number), a PriceCategory group whose child circles' fill
//     colour indexes categories, a Legend group whose presence is
//     validated as a warning, and a decor_svg capture of every element
//     that is not a seat / category swatch / legend.
//
// The package has NO dependencies beyond the Go standard library, NO
// persistence side effects, and NO HTTP concerns. It is safe to call
// from either the HTTP handler layer (future hseating sub-package) or a
// background importer job.
package seating
