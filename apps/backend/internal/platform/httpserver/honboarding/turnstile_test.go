package honboarding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// turnstileStub answers every verification with the given action and host.
func turnstileStub(t *testing.T, success bool, action, host string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ok := "false"
		if success {
			ok = "true"
		}
		_, _ = w.Write([]byte(`{"success":` + ok + `,"action":"` + action + `","hostname":"` + host + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTurnstileVerifier_ActionAndHostname(t *testing.T) {
	for _, c := range []struct {
		name         string
		success      bool
		action, host string
		wantAction   string
		wantHosts    []string
		want         bool
	}{
		{"contact token on the tickets host", true, "contact", "tickets.example", "contact", []string{"tickets.example"}, true},
		{"host compared without case", true, "contact", "Tickets.Example", "contact", []string{"tickets.example"}, true},
		{"onboarding token is refused", true, "onboarding", "tickets.example", "contact", []string{"tickets.example"}, false},
		{"token without an action is refused", true, "", "tickets.example", "contact", []string{"tickets.example"}, false},
		{"token solved on another host is refused", true, "contact", "evil.example", "contact", []string{"tickets.example"}, false},
		{"token without a host is refused", true, "contact", "", "contact", []string{"tickets.example"}, false},
		{"failed token stays refused", false, "contact", "tickets.example", "contact", []string{"tickets.example"}, false},
		{"no action or host configured keeps the old rule", true, "anything", "anywhere", "", nil, true},
	} {
		v := &TurnstileVerifier{Secret: "s", URL: turnstileStub(t, c.success, c.action, c.host), Action: c.wantAction, Hostnames: c.wantHosts}
		got, err := v.Verify(context.Background(), "tok", "")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
