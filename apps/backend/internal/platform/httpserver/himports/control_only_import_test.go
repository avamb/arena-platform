package himports

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A bundle that addresses a session or an event that only controls the
// entrance is refused with 409 and a neutral message (MA-40). The same errors
// end a real import and a dry run: both go through executeArenaImport.
func TestControlOnlyImportRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"session", errControlOnlySession(), "session.sales_mode_locked"},
		{"event", errControlOnlyEvent(), "event.control_only"},
	}
	for _, c := range cases {
		var ie *importError
		if !errors.As(c.err, &ie) {
			t.Fatalf("%s: not an importError: %v", c.name, c.err)
		}
		if ie.status != http.StatusConflict || ie.code != c.code {
			t.Errorf("%s: status=%d code=%s, want 409 %s", c.name, ie.status, ie.code, c.code)
		}
		if strings.Contains(strings.ToLower(ie.message), "control") {
			t.Errorf("%s: message %q is not neutral", c.name, ie.message)
		}
	}
}
