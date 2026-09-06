// macs_webhook_test.go — W1-Mb (spec §10 M4): the subscriber URL rule.
//
// A MACS receiver only ever serves `/api/_wh/tickets` (the WordPress plugin's
// class-lops-macs.php mount point). A URL pointing anywhere else answers 200
// with an HTML page, which the outbox reads as "delivered" — the sale is lost
// silently. The PUT handler therefore rejects it with 422 up front; this test
// pins the predicate behind that check.
package hcatalog

import "testing"

func TestValidMACSCallbackURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://vinoandco.hu/api/_wh/tickets", true},
		{"https://vinoandco.hu/api/_wh/tickets/", true},
		{"http://localhost:8080/api/_wh/tickets", true},
		// Wrong path: the pre-W1 examples in the contract doc dropped /api.
		{"https://macs.example.com/_wh/tickets", false},
		{"https://macs.example.com/api/_wh/ticket", false},
		{"https://macs.example.com/api/_wh/tickets/import", false},
		{"https://macs.example.com/", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := validMACSCallbackURL(tc.url); got != tc.want {
			t.Errorf("validMACSCallbackURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
