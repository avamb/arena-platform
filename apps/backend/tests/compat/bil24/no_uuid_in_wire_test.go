// no_uuid_in_wire_test.go — feature #476 (W1-A2b) Step 3 guardrail.
//
// Spec §4 (08_architecture/18_bil24_compat_wave1_specification_ru.md) makes
// int64 the ONE-AND-ONLY external identifier form on the Bil24 wire for
// wave 1 WP-site integrations. UUIDv7 stays platform-internal and MUST NOT
// leak into any wire-side payload:
//
//   - request bodies the site POSTs into /compat/bil24/json
//   - golden response payloads the harness pins the handler output against
//   - wp_receiver callbacks the arena backend POSTs to the site
//
// This test walks every JSON file under testdata/wp/ (goldens, requests,
// wp_receiver callbacks) and fails loudly if any string value matches the
// canonical UUID regex. Placeholder tokens like "{{actionEventId}}" that the
// harness resolves at run time from harnessState are compared literally and
// therefore never match the regex — that is intentional: the resolved value
// substituted at run time comes from harnessState.EventID which #476/#495
// downstream work must switch from UUID to int64 (a follow-up sub-feature).
//
// Scope limitations (deliberate):
//   - testdata/vinoandco_fixtures.json is EXCLUDED. Those fixtures pin the
//     legacy #157/#158 UUID-passthrough contract and are consumed by
//     regression_158_test.go; they are NOT wave-1 wire fixtures.
//   - testdata/wp/bil24_orders_pseudonymized.json is a Bil24-side dump used
//     by feature #520 (customer import) and is not part of the wire contract
//     this test guards.
//
// Failure mode: the test prints every offending file with the exact JSON path
// (e.g. `actionList[0].actionEventList[0].sessionId`) and the offending value
// so the fix is obvious — either the golden is wrong (regenerate against the
// rewired handler) or the request fixture accidentally used a UUID (fix the
// fixture).
package compat_bil24_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// uuidPattern matches the canonical 8-4-4-4-12 hex-with-dashes UUID string in
// any case. Anchored to word boundaries via regex character classes on each
// end is unnecessary because we scan JSON string values, which have their own
// delimiters — a substring match is exactly what we want.
var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// TestCompatBil24_476_NoUUIDInWireFixtures walks testdata/wp/{golden,requests,
// wp_receiver} and asserts that no JSON value (string, number, or key) matches
// a canonical UUID pattern. This is the wave-1 wire invariant per spec §4.
func TestCompatBil24_476_NoUUIDInWireFixtures(t *testing.T) {
	roots := []string{
		filepath.Join("testdata", "wp", "golden"),
		filepath.Join("testdata", "wp", "requests"),
		filepath.Join("testdata", "wp", "wp_receiver"),
	}

	var offenders []string

	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			t.Fatalf("wire-fixture root missing: %s (spec §15.3 requires it)", root)
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			// Cheap first pass: if the raw text has no UUID substring the
			// file is clean regardless of shape. This avoids the JSON parse
			// cost for the common case.
			if !uuidPattern.Match(raw) {
				return nil
			}
			// Something matched — parse and pinpoint the JSON path so the
			// failure message points the operator directly at the offending
			// key.
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				offenders = append(offenders, fmt.Sprintf("%s: UUID-shape substring in file, and it does not parse as JSON (%v)", path, err))
				return nil
			}
			for _, hit := range scanForUUID(doc, "") {
				offenders = append(offenders, fmt.Sprintf("%s: %s = %q", path, hit.path, hit.value))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("UUIDs are forbidden on the wave-1 Bil24 wire (spec §4). %d violation(s):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// uuidHit is one (JSON path, offending string) pair.
type uuidHit struct {
	path  string
	value string
}

// scanForUUID walks a decoded JSON document depth-first and returns every
// string value (including map keys) that matches uuidPattern. Numbers and
// booleans are ignored because they cannot contain a UUID.
func scanForUUID(v any, path string) []uuidHit {
	var out []uuidHit
	switch x := v.(type) {
	case map[string]any:
		for k, sub := range x {
			if uuidPattern.MatchString(k) {
				out = append(out, uuidHit{path: path + "{key}", value: k})
			}
			out = append(out, scanForUUID(sub, joinPath(path, k))...)
		}
	case []any:
		for i, sub := range x {
			out = append(out, scanForUUID(sub, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case string:
		if uuidPattern.MatchString(x) {
			out = append(out, uuidHit{path: pathOrRoot(path), value: x})
		}
	}
	return out
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func pathOrRoot(p string) string {
	if p == "" {
		return "<root>"
	}
	return p
}
