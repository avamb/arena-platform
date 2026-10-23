package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsPageTrackBeacon(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/public/pages/acme/show/track", true},
		{http.MethodGet, "/v1/public/pages/acme/show/track", false},
		{http.MethodPost, "/v1/public/pages/acme/track", false},
		{http.MethodPost, "/v1/public/pages/acme/show", false},
		{http.MethodPost, "/v1/public/pages/acme/show/track/extra", false},
		{http.MethodPost, "/v1/public/organizer-contact/acme", false},
		{http.MethodPost, "/v1/other/acme/show/track", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := isPageTrackBeacon(r); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
