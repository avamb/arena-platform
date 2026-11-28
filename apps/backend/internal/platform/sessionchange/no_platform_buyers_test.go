package sessionchange

import "testing"

// A partner-sold and an entrance-control session have no buyers on the
// platform: their change is journaled and no letter is queued.
func TestSession_NoPlatformBuyers(t *testing.T) {
	cases := map[string]bool{
		"":             false,
		"platform":     false,
		"external":     true,
		"control_only": true,
	}
	for mode, want := range cases {
		if got := (Session{SalesMode: mode}).NoPlatformBuyers(); got != want {
			t.Errorf("sales_mode %q: NoPlatformBuyers = %v, want %v", mode, got, want)
		}
	}
}
