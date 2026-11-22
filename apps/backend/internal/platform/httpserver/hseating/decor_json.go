package hseating

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// Served decor goes through seating.SanitizeDecorSVG. The geometry_checksum
// and the ETag of an answer still describe the STORED geometry: they are
// not recomputed when the served decor differs from the stored one. Decor
// written since the allowlist is stored drawing-only, so for it the two are
// the same.

// decorCacheMaxEntries and decorCacheMaxBytes bound the in-process cache of
// sanitized decor. When either bound would be passed the cache is emptied.
const (
	decorCacheMaxEntries = 512
	decorCacheMaxBytes   = 32 << 20
)

// decorCacheEntry is one sanitized decor. same means the stored decor is
// already drawing only; then clean is empty and the stored string is used.
type decorCacheEntry struct {
	same  bool
	clean string
}

// decorCache keeps sanitized decor keyed by the SHA-256 of the stored decor,
// so the public plan answers (schema, layout.svg, the gateway image) do not
// parse the same decor on every request.
var decorCache = struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]decorCacheEntry
	bytes   int
}{entries: map[[sha256.Size]byte]decorCacheEntry{}}

// sanitizeDecorCached returns seating.SanitizeDecorSVG(decor), from the cache
// when the same decor was sanitized before.
func sanitizeDecorCached(decor string) string {
	if strings.TrimSpace(decor) == "" {
		return decor
	}
	key := sha256.Sum256([]byte(decor))
	decorCache.mu.Lock()
	e, ok := decorCache.entries[key]
	decorCache.mu.Unlock()
	if ok {
		if e.same {
			return decor
		}
		return e.clean
	}
	clean := seating.SanitizeDecorSVG(decor)
	e = decorCacheEntry{same: clean == decor}
	size := 0
	if !e.same {
		e.clean = clean
		size = len(clean)
	}
	if size > decorCacheMaxBytes {
		return clean
	}
	decorCache.mu.Lock()
	if len(decorCache.entries) >= decorCacheMaxEntries || decorCache.bytes+size > decorCacheMaxBytes {
		decorCache.entries = map[[sha256.Size]byte]decorCacheEntry{}
		decorCache.bytes = 0
	}
	if _, dup := decorCache.entries[key]; !dup {
		decorCache.entries[key] = e
		decorCache.bytes += size
	}
	decorCache.mu.Unlock()
	return clean
}

// sanitizedGeometryJSON returns a stored geometry document with its
// decor_svg read back through the decor writer. When the decor does not
// change, the stored bytes are returned as they are. A document that is not
// a JSON object, or has no string decor_svg, is returned as it is.
func sanitizedGeometryJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var probe struct {
		DecorSVG *string `json:"decor_svg"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.DecorSVG == nil {
		return raw
	}
	return sanitizedGeometryJSONWithDecor(raw, *probe.DecorSVG)
}

// sanitizedGeometryJSONWithDecor is sanitizedGeometryJSON for a caller that
// has already decoded the geometry and passes its decor_svg.
func sanitizedGeometryJSONWithDecor(raw json.RawMessage, decor string) json.RawMessage {
	clean := sanitizeDecorCached(decor)
	if clean == decor {
		return raw
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
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
