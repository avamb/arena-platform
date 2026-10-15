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
