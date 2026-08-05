// session_media_test.go — pure unit tests for AB-47b video URL validation
// and gallery-cap constants. Handler-integration behaviour with a real
// database is exercised by session_media_integration_test.go
// (build tag: integration).
package hcatalog

import "testing"

// TestValidateVideoURL_AB47b_HostAllowlist covers the AB-47b decision
// that video links accept only YouTube / VK / RuTube / Vimeo (owner
// call: those are the only hosts organizers actually use, and gating on
// a small allowlist prevents phishing/malicious redirects being served
// through the public widget).
func TestValidateVideoURL_AB47b_HostAllowlist(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"empty_is_rejected", "", true},
		{"whitespace_is_rejected", "   ", true},
		{"http_scheme_rejected", "http://youtube.com/watch?v=abc", true},
		{"ftp_scheme_rejected", "ftp://youtube.com/watch?v=abc", true},
		{"javascript_scheme_rejected", "javascript:alert(1)", true},
		{"data_scheme_rejected", "data:text/html,<script>alert(1)</script>", true},
		{"unknown_host_rejected", "https://example.com/video", true},
		{"tiktok_rejected", "https://www.tiktok.com/@user/video/123", true},
		{"malformed_url_rejected", "https://:::/foo", true},

		{"youtube_watch_ok", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", false},
		{"youtube_no_www_ok", "https://youtube.com/watch?v=dQw4w9WgXcQ", false},
		{"youtu_be_short_ok", "https://youtu.be/dQw4w9WgXcQ", false},
		{"vk_ok", "https://vk.com/video-1234567_456239017", false},
		{"vk_www_ok", "https://www.vk.com/video1_2", false},
		{"rutube_ok", "https://rutube.ru/video/abc123/", false},
		{"vimeo_ok", "https://vimeo.com/123456789", false},
		{"vimeo_uppercase_host_ok", "https://Vimeo.com/123456789", false},
		{"vimeo_with_port_ok", "https://vimeo.com:443/123456789", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, msg := validateVideoURL(tc.url)
			if tc.wantErr {
				if msg == "" {
					t.Errorf("validateVideoURL(%q): expected error, got ok (normalized=%q)", tc.url, got)
				}
			} else {
				if msg != "" {
					t.Errorf("validateVideoURL(%q): expected ok, got error %q", tc.url, msg)
				}
			}
		})
	}
}

// TestSessionMediaConstants_AB47b guards the owner-mandated caps. The
// poster cap (5) is a hard-coded HANDLER constant per AB-47b — moving
// it into the migration would force a schema change to raise the ceiling.
// The total-items cap (20) prevents a video-only spam vector.
func TestSessionMediaConstants_AB47b(t *testing.T) {
	t.Parallel()
	if MaxPostersPerGallery != 5 {
		t.Errorf("MaxPostersPerGallery: got %d, want 5 (AB-47b owner cap)", MaxPostersPerGallery)
	}
	if MaxTotalItems < MaxPostersPerGallery {
		t.Errorf("MaxTotalItems (%d) must be >= MaxPostersPerGallery (%d)",
			MaxTotalItems, MaxPostersPerGallery)
	}
	if PosterOwnerType != "session_poster" {
		t.Errorf("PosterOwnerType: got %q, want session_poster (reuses AB-47 allowlist)", PosterOwnerType)
	}
}

// TestSessionMediaAllowedHosts_AB47b guards the host allowlist. Any host
// added or removed here must be a deliberate product decision.
func TestSessionMediaAllowedHosts_AB47b(t *testing.T) {
	t.Parallel()
	want := []string{"youtube.com", "youtu.be", "vk.com", "rutube.ru", "vimeo.com"}
	if len(allowedVideoHosts) != len(want) {
		t.Fatalf("allowedVideoHosts: got %d entries, want %d (%v)", len(allowedVideoHosts), len(want), want)
	}
	for _, h := range want {
		if !allowedVideoHosts[h] {
			t.Errorf("allowedVideoHosts: %q missing from allowlist", h)
		}
	}
}
