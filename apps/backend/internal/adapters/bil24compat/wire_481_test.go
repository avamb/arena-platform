// wire_481_test.go — decode tests for feature #481 (W1-A4c, spec §7.3):
// the CREATE_USER / gateway-session fields and the number-or-string
// tolerance Request.UnmarshalJSON provides for `fid` and `userId`.
//
// The WordPress plugins serialise these two keys inconsistently — sometimes
// as JSON numbers, sometimes as strings (class-bil24-client.php) — so the
// envelope must accept both without falling back to resultCode=-2.

package bil24compat

import (
	"encoding/json"
	"testing"
)

func decodeRequest(t *testing.T, raw string) Request {
	t.Helper()
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return req
}

func TestRequest_FID_NumberOrString(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"json_number", `{"command":"CREATE_USER","fid":1271}`, "1271"},
		{"json_string", `{"command":"CREATE_USER","fid":"1271"}`, "1271"},
		{"absent", `{"command":"CREATE_USER"}`, ""},
		{"null", `{"command":"CREATE_USER","fid":null}`, ""},
		{"empty_string", `{"command":"CREATE_USER","fid":""}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeRequest(t, tc.raw)
			if got.FID != tc.want {
				t.Errorf("FID = %q, want %q", got.FID, tc.want)
			}
			if got.Command != "CREATE_USER" {
				t.Errorf("Command = %q, want CREATE_USER (decoding must not be disturbed)", got.Command)
			}
		})
	}
}

func TestRequest_UserID_NumberOrString(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int64
	}{
		{"json_number", `{"command":"RESERVATION","userId":1000000042}`, 1000000042},
		{"json_string", `{"command":"RESERVATION","userId":"1000000042"}`, 1000000042},
		{"padded_string", `{"command":"RESERVATION","userId":" 42 "}`, 42},
		{"absent", `{"command":"RESERVATION"}`, 0},
		{"null", `{"command":"RESERVATION","userId":null}`, 0},
		{"garbage_string", `{"command":"RESERVATION","userId":"abc"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeRequest(t, tc.raw).UserID; got != tc.want {
				t.Errorf("UserID = %d, want %d", got, tc.want)
			}
		})
	}
}

// The CREATE_USER payload (spec §7.3) must round-trip every optional field
// the WordPress sites send, alongside the session pair.
func TestRequest_CreateUserFields(t *testing.T) {
	req := decodeRequest(t, `{
		"command":   "CREATE_USER",
		"fid":       1271,
		"token":     "s3cret",
		"locale":    "ru-RU",
		"email":     "buyer@example.com",
		"firstName": "Anna",
		"lastName":  "Novak",
		"phone":     "+420123456789",
		"sessionId": "abc123",
		"userId":    1000000042
	}`)

	checks := []struct{ field, got, want string }{
		{"Command", req.Command, "CREATE_USER"},
		{"FID", req.FID, "1271"},
		{"Token", req.Token, "s3cret"},
		{"Locale", req.Locale, "ru-RU"},
		{"Email", req.Email, "buyer@example.com"},
		{"FirstName", req.FirstName, "Anna"},
		{"LastName", req.LastName, "Novak"},
		{"Phone", req.Phone, "+420123456789"},
		{"SessionID", req.SessionID, "abc123"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if req.UserID != 1000000042 {
		t.Errorf("UserID = %d, want 1000000042", req.UserID)
	}
}

// The custom UnmarshalJSON must not regress the pre-#481 fields decoded by
// encoding/json's case-insensitive fallback.
func TestRequest_LegacyFieldsStillDecode(t *testing.T) {
	req := decodeRequest(t, `{
		"command":         "CREATE_ORDER_EXT",
		"fid":             "1271",
		"actionEventId":   "evt-1",
		"categoryPriceId": "cat-1",
		"quantity":        3,
		"orderId":         "ord-9",
		"reservationId":   "res-7"
	}`)
	if req.ActionEventID != "evt-1" || req.CategoryPriceID != "cat-1" {
		t.Errorf("legacy id fields lost: %+v", req)
	}
	if req.Quantity != 3 {
		t.Errorf("Quantity = %d, want 3", req.Quantity)
	}
	if req.OrderID != "ord-9" || req.ReservationID != "res-7" {
		t.Errorf("order/reservation ids lost: %+v", req)
	}
}

// A malformed envelope must still surface as a decode error so the handler
// can answer -2 (invalid request) rather than silently seeing a zero value.
func TestRequest_MalformedJSONStillErrors(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"command":`), &req); err == nil {
		t.Fatal("expected a decode error for truncated JSON")
	}
}
