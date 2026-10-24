// wire_attribution_test.go — CREATE_ORDER_EXT's `attribution` object, the
// arena extension of spec 43 section 4.4. An object is kept as sent, a string
// holding an object is unwrapped, and nothing else may fail the envelope.

package bil24compat

import "testing"

func TestRequest_Attribution_FlexibleObject(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"object", `{"command":"CREATE_ORDER_EXT","attribution":{"ref":"abcdefg"}}`, `{"ref":"abcdefg"}`},
		{"string_object", `{"command":"CREATE_ORDER_EXT","attribution":"{\"ref\":\"abcdefg\"}"}`, `{"ref":"abcdefg"}`},
		{"number", `{"command":"CREATE_ORDER_EXT","attribution":42}`, ``},
		{"array", `{"command":"CREATE_ORDER_EXT","attribution":[1]}`, ``},
		{"string_not_object", `{"command":"CREATE_ORDER_EXT","attribution":"abcdefg"}`, ``},
		{"string_broken", `{"command":"CREATE_ORDER_EXT","attribution":"{\"ref\":"}`, ``},
		{"null", `{"command":"CREATE_ORDER_EXT","attribution":null}`, ``},
		{"absent", `{"command":"CREATE_ORDER_EXT"}`, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeRequest(t, tc.raw)
			if string(got.Attribution) != tc.want {
				t.Fatalf("Attribution = %q, want %q", got.Attribution, tc.want)
			}
			if got.Command != "CREATE_ORDER_EXT" {
				t.Fatalf("Command = %q, the envelope was not decoded", got.Command)
			}
		})
	}
}
