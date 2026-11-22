package hseating

import (
	"encoding/json"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// sanitizedGeometryJSON returns a stored geometry document with its
// decor_svg read back through the decor writer (seating.SanitizeDecorSVG).
// When the decor does not change, the stored bytes are returned as they are,
// so an answer for a plan with drawing-only decor is unchanged. A document
// that is not a JSON object, or has no string decor_svg, is returned as it is.
func sanitizedGeometryJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	decorRaw, ok := doc["decor_svg"]
	if !ok {
		return raw
	}
	var decor string
	if err := json.Unmarshal(decorRaw, &decor); err != nil {
		return raw
	}
	clean := seating.SanitizeDecorSVG(decor)
	if clean == decor {
		return raw
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return raw
	}
	doc["decor_svg"] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}
