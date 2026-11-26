package hcatalog

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestKeepSellerDisplayKeys(t *testing.T) {
	stored := json.RawMessage(`{"hosted_page":{"enabled":true,"show_seller":true,"public_contact":{"email":"tickets@x.example"}}}`)
	cases := []struct {
		name         string
		next, stored json.RawMessage
		want         string
	}{
		{"caller value replaced by stored",
			json.RawMessage(`{"hosted_page":{"enabled":true,"show_seller":false,"public_contact":{"email":"attacker@evil.example"}},"x":1}`), stored,
			`{"hosted_page":{"enabled":true,"show_seller":true,"public_contact":{"email":"tickets@x.example"}},"x":1}`},
		{"stored kept when caller omits hosted_page",
			json.RawMessage(`{"x":1}`), stored,
			`{"hosted_page":{"show_seller":true,"public_contact":{"email":"tickets@x.example"}},"x":1}`},
		{"new channel drops the keys",
			json.RawMessage(`{"hosted_page":{"enabled":true,"show_seller":true,"public_contact":{"email":"a@b.example"}}}`), nil,
			`{"hosted_page":{"enabled":true}}`},
		{"seller_hidden cannot be set or cleared by the caller",
			json.RawMessage(`{"hosted_page":{"enabled":true,"seller_hidden":false}}`),
			json.RawMessage(`{"hosted_page":{"enabled":true,"seller_hidden":true}}`),
			`{"hosted_page":{"enabled":true,"seller_hidden":true}}`},
		{"seller_hidden dropped on a new channel",
			json.RawMessage(`{"hosted_page":{"enabled":true,"seller_hidden":true}}`), nil,
			`{"hosted_page":{"enabled":true}}`},
		{"consent_required cannot be switched on by the generic channel route",
			json.RawMessage(`{"hosted_page":{"enabled":true,"consent_required":true}}`),
			json.RawMessage(`{"hosted_page":{"enabled":true}}`),
			`{"hosted_page":{"enabled":true}}`},
		{"consent_required cannot be switched off by the generic channel route",
			json.RawMessage(`{"hosted_page":{"enabled":true,"consent_required":false}}`),
			json.RawMessage(`{"hosted_page":{"enabled":true,"consent_required":true}}`),
			`{"hosted_page":{"enabled":true,"consent_required":true}}`},
		{"thank_you_posters is kept from the stored value",
			json.RawMessage(`{"site":{"thank_you_posters":true,"origins":["https://a.example"]}}`),
			json.RawMessage(`{"site":{"thank_you_posters":false}}`),
			`{"site":{"thank_you_posters":false,"origins":["https://a.example"]}}`},
		{"thank_you_posters dropped on a new channel",
			json.RawMessage(`{"site":{"thank_you_posters":false,"origins":["https://a.example"]}}`), nil,
			`{"site":{"origins":["https://a.example"]}}`},
		{"nothing to do",
			json.RawMessage(`{"hosted_page":{"enabled":true}}`), json.RawMessage(`{"hosted_page":{"enabled":false}}`),
			`{"hosted_page":{"enabled":true}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got, want any
			if err := json.Unmarshal(keepSellerDisplayKeys(c.next, c.stored), &got); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal([]byte(c.want), &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}
