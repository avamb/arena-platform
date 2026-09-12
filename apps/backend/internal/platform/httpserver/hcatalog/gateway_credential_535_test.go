// gateway_credential_535_test.go — unit tests for feature #535 (W1-S1a, spec
// 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.1): the gateway-credential
// PUT response must hand the operator the gateway ENDPOINTS, not the bare
// origin. Pasting a bare origin into the WordPress plugin's "Bil24 API URL"
// field sends every command to `/` and silently 404s.
package hcatalog

import "testing"

func TestW1S1a_GatewayBaseAndImageURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		public    string
		wantBase  string
		wantImage string
	}{
		{
			name:      "api origin",
			public:    "https://api.example.com",
			wantBase:  "https://api.example.com/compat/bil24",
			wantImage: "https://api.example.com/compat/bil24/image",
		},
		{
			name:      "trailing slash is normalised away",
			public:    "https://api.example.com/",
			wantBase:  "https://api.example.com/compat/bil24",
			wantImage: "https://api.example.com/compat/bil24/image",
		},
		{
			name:      "unset stays empty, never a host-less path",
			public:    "",
			wantBase:  "",
			wantImage: "",
		},
		{
			name:      "blank-only stays empty",
			public:    "   ",
			wantBase:  "",
			wantImage: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := gatewayBaseURL(tc.public); got != tc.wantBase {
				t.Errorf("gatewayBaseURL(%q) = %q, want %q", tc.public, got, tc.wantBase)
			}
			if got := gatewayImageURL(tc.public); got != tc.wantImage {
				t.Errorf("gatewayImageURL(%q) = %q, want %q", tc.public, got, tc.wantImage)
			}
		})
	}
}
