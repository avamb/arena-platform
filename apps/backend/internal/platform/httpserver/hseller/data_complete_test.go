package hseller

import "testing"

func TestProfile_DataComplete(t *testing.T) {
	cases := []struct {
		name string
		p    Profile
		want bool
	}{
		{"organization incomplete", Profile{Complete: false}, false},
		{"complete, no channel", Profile{Complete: true}, true},
		{"channel shown", Profile{Complete: true, Channels: []Channel{{SellerShown: true}}}, true},
		{"channel hidden by the operator", Profile{Complete: true, Channels: []Channel{{SellerHidden: true}}}, true},
		{"channel lacks its own data", Profile{Complete: true, Channels: []Channel{{SellerShown: true}, {}}}, false},
	}
	for _, tc := range cases {
		if got := tc.p.DataComplete(); got != tc.want {
			t.Errorf("%s: DataComplete = %v; want %v", tc.name, got, tc.want)
		}
	}
}
