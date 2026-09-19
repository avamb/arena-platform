// wire_complimentary_test.go — CREATE_ORDER_EXT's `complimentary` flag, the
// arena extension that marks an invitation order. PHP clients serialise the
// boolean in several shapes; every "yes" spelling must decode to true and
// nothing else may.

package bil24compat

import "testing"

func TestRequest_Complimentary_FlexibleBool(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"json_true", `{"command":"CREATE_ORDER_EXT","complimentary":true}`, true},
		{"json_false", `{"command":"CREATE_ORDER_EXT","complimentary":false}`, false},
		{"number_one", `{"command":"CREATE_ORDER_EXT","complimentary":1}`, true},
		{"number_zero", `{"command":"CREATE_ORDER_EXT","complimentary":0}`, false},
		{"string_one", `{"command":"CREATE_ORDER_EXT","complimentary":"1"}`, true},
		{"string_true", `{"command":"CREATE_ORDER_EXT","complimentary":"TRUE"}`, true},
		{"string_zero", `{"command":"CREATE_ORDER_EXT","complimentary":"0"}`, false},
		{"string_empty", `{"command":"CREATE_ORDER_EXT","complimentary":""}`, false},
		{"null", `{"command":"CREATE_ORDER_EXT","complimentary":null}`, false},
		{"absent", `{"command":"CREATE_ORDER_EXT"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeRequest(t, tc.raw)
			if got.Complimentary != tc.want {
				t.Errorf("Complimentary = %v, want %v", got.Complimentary, tc.want)
			}
			if got.Command != "CREATE_ORDER_EXT" {
				t.Errorf("Command = %q, want CREATE_ORDER_EXT", got.Command)
			}
		})
	}
}
