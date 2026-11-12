package seating

import "regexp"

// sbtSectRE finds an sbt:sect attribute: the mark of the Forzum/Bil24 sbt
// dialect (a seat sits under a group carrying sbt:sect).
var sbtSectRE = regexp.MustCompile(`\ssbt:sect\s*=`)

// IsSBTSVG reports whether raw is an sbt-dialect seating plan (a group with
// sbt:sect), as opposed to the authoring dialect (Inkscape, PriceCategory).
func IsSBTSVG(raw []byte) bool {
	return sbtSectRE.Match(raw)
}

// ImportAnySVG imports raw with the importer matching its dialect: ImportSBTSVG
// for an sbt plan, ImportSVG otherwise.
func ImportAnySVG(raw []byte) (Geometry, []ValidationError, ValidationErrors) {
	if IsSBTSVG(raw) {
		plan, warnings, errs := ImportSBTSVG(raw)
		return plan.Geometry, warnings, errs
	}
	return ImportSVG(raw)
}
