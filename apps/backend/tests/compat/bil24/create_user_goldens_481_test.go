// create_user_goldens_481_test.go — feature #481 (W1-A4c, spec §7.3):
// pins the CREATE_USER wire goldens against the bil24compat envelope
// layout so a drift between the hbil24 handler's response shape and the
// fixtures fails loudly in the Unit CI job (no DB required).
//
// The two cases encode the spec's idempotency rule: CREATE_USER resolves
// the buyer by strong key (email/phone, §12.2), so calling it twice with
// the same email returns the SAME userId — but always a NEW sessionId,
// because the command deliberately mints a fresh gateway_sessions row.
// The live behaviour is exercised by the hbil24 unit tests
// (TestBil24_481_CreateUser_*) and, once seeded state exists, by the
// integration harness; this test is a pure envelope-shape guard.

package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// readCreateUserGolden loads and decodes one CREATE_USER golden file.
func readCreateUserGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	path := filepath.Join("testdata", "wp", "golden", "CREATE_USER", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return got
}

func TestBil24_481_CreateUserGoldens_EnvelopeShape(t *testing.T) {
	// Strict key-set per harness §15.2: every required key must be
	// present and no extras may be silently added.
	wantKeys := []string{"resultCode", "description", "command", "userId", "sessionId"}

	for _, name := range []string{"basic.json", "same_email_new_session.json"} {
		name := name
		t.Run(name, func(t *testing.T) {
			got := readCreateUserGolden(t, name)

			code, _ := got["resultCode"].(float64)
			if int(code) != bil24compat.ResultCodeOK {
				t.Errorf("resultCode = %v, want %d", got["resultCode"], bil24compat.ResultCodeOK)
			}
			if cmd, _ := got["command"].(string); cmd != "CREATE_USER" {
				t.Errorf("command = %q, want CREATE_USER", cmd)
			}
			if desc, _ := got["description"].(string); desc == "" {
				t.Error("description must be non-empty (bil24 envelope guarantee)")
			}
			// Spec §7.3 / §3.1: userId is customers.system_id — a JSON
			// number, never a UUID string.
			if _, ok := got["userId"].(float64); !ok {
				t.Errorf("userId = %#v, want a JSON number (customers.system_id)", got["userId"])
			}
			if sid, _ := got["sessionId"].(string); sid == "" {
				t.Errorf("sessionId = %#v, want a non-empty string", got["sessionId"])
			}

			gotKeys := make(map[string]struct{}, len(got))
			for k := range got {
				gotKeys[k] = struct{}{}
			}
			want := make(map[string]struct{}, len(wantKeys))
			for _, k := range wantKeys {
				want[k] = struct{}{}
				if _, ok := gotKeys[k]; !ok {
					t.Errorf("missing required key %q", k)
				}
			}
			for k := range gotKeys {
				if _, ok := want[k]; !ok {
					t.Errorf("unexpected key %q (spec §15.2 forbids extras)", k)
				}
			}
		})
	}
}

// TestBil24_481_CreateUserGoldens_SameEmailSameUserNewSession encodes the
// §7.3 contract that makes the second fixture worth having: identical
// request payload ⇒ identical userId, distinct sessionId placeholder.
func TestBil24_481_CreateUserGoldens_SameEmailSameUserNewSession(t *testing.T) {
	basic := readCreateUserGolden(t, "basic.json")
	repeat := readCreateUserGolden(t, "same_email_new_session.json")

	if basic["userId"] != repeat["userId"] {
		t.Errorf("userId drifted between the two CREATE_USER goldens: %v vs %v — "+
			"spec §7.3 resolves the same email to the same customer",
			basic["userId"], repeat["userId"])
	}
	if basic["sessionId"] == repeat["sessionId"] {
		t.Errorf("both goldens use the sessionId placeholder %v — CREATE_USER always "+
			"mints a NEW gateway session, so the harness must bind two distinct values",
			basic["sessionId"])
	}

	// The requests must genuinely be the same payload, otherwise the
	// "same email" premise of the second case is not being tested.
	readReq := func(name string) map[string]any {
		path := filepath.Join("testdata", "wp", "requests", "CREATE_USER", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		return out
	}
	reqA := readReq("basic.json")
	reqB := readReq("same_email_new_session.json")
	if reqA["email"] != reqB["email"] || reqA["email"] == "" {
		t.Errorf("request emails differ (%v vs %v); the same_email case is meaningless",
			reqA["email"], reqB["email"])
	}
}
