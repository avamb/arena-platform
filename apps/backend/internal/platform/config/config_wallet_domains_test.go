package config

import (
	"reflect"
	"testing"
)

func TestWalletPlatformDomains(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		tickets  string
		want     []string
	}{
		{"explicit list wins", "Tickets.Example.test, www.example.test ,tickets.example.test", "https://other.test", []string{"tickets.example.test", "www.example.test"}},
		{"falls back to the tickets host", "", "https://tickets.example.test:8443/", []string{"tickets.example.test"}},
		{"nothing set", "", "", nil},
		{"scheme tolerated in the list", "https://a.example.test/path", "", []string{"a.example.test"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{StripeWalletDomains: parseHostList(c.explicit), PublicTicketsBaseURL: c.tickets}
			if got := cfg.WalletPlatformDomains(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v; want %v", got, c.want)
			}
		})
	}
}
