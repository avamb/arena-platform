package hsalelinks

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
)

var utmKeys = map[string]bool{
	"utm_source": true, "utm_medium": true, "utm_campaign": true, "utm_content": true, "utm_term": true,
}

var errInvalidUTM = errors.New("invalid utm")

// parseUTM reads a utm object: only the five utm_* keys, each value a string
// that pagestats.CleanUTM keeps whole. An empty value leaves the key out. An
// unknown key, a non-string value or a value CleanUTM would drop (a character
// outside [A-Za-z0-9._~-], or far too long) is errInvalidUTM: a half-cleaned
// label would count under a name nobody typed. null and {} give an empty map.
func parseUTM(raw json.RawMessage) (map[string]string, error) {
	out := map[string]string{}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return out, nil
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errInvalidUTM
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !utmKeys[k] {
			return nil, errInvalidUTM
		}
		s, ok := in[k].(string)
		if !ok {
			return nil, errInvalidUTM
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		clean := pagestats.CleanUTM(s)
		// CleanUTM cuts an over-long value; a cut value is a different label.
		if clean == "" || clean != s {
			return nil, errInvalidUTM
		}
		out[k] = clean
	}
	return out, nil
}

func marshalUTM(m map[string]string) json.RawMessage {
	if len(m) == 0 {
		return json.RawMessage(`{}`)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
