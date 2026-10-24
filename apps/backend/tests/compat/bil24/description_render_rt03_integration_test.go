//go:build integration

// description_render_rt03_integration_test.go - spec 43, RT-03b: the gateway
// sends the sites the RENDERED description. The WordPress plugins print it as
// HTML, so GET_ALL_ACTIONS.description is richtext.Render of the stored text
// with the event's format flag:
//
//   - an ART description keeps its markup, and its links carry
//     rel="noopener noreferrer" target="_blank";
//   - an old text row that was stored with "**bold**" reaches the site as
//     <strong> (the asterisks the sites showed before are gone), a literal tag
//     stays text.
//
// Everything goes through the real server, the event comes from the event
// bundle import like scenario 6 and 527 do.
package compat_bil24_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCompatBil24_RT03_CatalogDescriptionIsRenderedHTML(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)

	poster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nrt03-poster"))
	}))
	defer poster.Close()

	body := bundle527Fixture(t, poster.URL+"/poster.png")
	status, resp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil, body)
	if status != 200 {
		t.Fatalf("POST imports/event-bundle status = %d, want 200 (body %v)", status, resp)
	}
	eventID := sc8UUIDField(t, resp, "event_id")
	sessionID := sc8UUIDField(t, resp, "session_id")
	bundle527RegisterCleanup(t, st, eventID, sessionID)
	compatIDs, ok := resp["compat_ids"].(map[string]interface{})
	if !ok {
		t.Fatalf("event-bundle response has no compat_ids object: %v", resp)
	}
	actionEventID := numberField(t, compatIDs, "action_event_id")

	catalogDescription := func() string {
		t.Helper()
		req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		catalog := postBil24(t, base, req)
		if code := numberField(t, catalog, "resultCode"); code != 0 {
			t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, catalog["description"])
		}
		action := sc1FindActionByEvent(t, catalog, actionEventID)
		d, _ := action["description"].(string)
		return d
	}
	setDescription := func(description, format string) {
		t.Helper()
		if _, err := st.Pool.Exec(context.Background(),
			`UPDATE events SET description = $2, description_format = $3 WHERE id = $1`,
			eventID, description, format); err != nil {
			t.Fatalf("set description: %v", err)
		}
	}

	// ART with a link, a list and an event handler the sanitizer must not pass.
	setDescription(`<p>Join <strong>us</strong> at <a href="https://example.com/tickets">the site</a></p><ul><li>one</li><li>two</li></ul>`, "art")
	got := catalogDescription()
	want := `<p>Join <strong>us</strong> at <a href="https://example.com/tickets" rel="noopener noreferrer" target="_blank">the site</a></p><ul><li>one</li><li>two</li></ul>`
	if got != want {
		t.Errorf("ART description on the wire =\n  %q\nwant\n  %q", got, want)
	}

	// Hostile ART is cleaned again on the way out, whatever the row holds.
	setDescription(`<p onclick="x()">a<script>alert(1)</script><a href="javascript:alert(2)">b</a></p>`, "art")
	got = catalogDescription()
	for _, bad := range []string{"<script", "onclick", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Errorf("description on the wire %q contains %q", got, bad)
		}
	}

	// An old text row: the marks become markup instead of literal asterisks,
	// and a "<" that is not a tag is shown as text.
	setDescription("A **bold** word and a < b\n\n- one\n- two", "text")
	got = catalogDescription()
	want = "<p>A <strong>bold</strong> word and a &lt; b</p><ul><li>one</li><li>two</li></ul>"
	if got != want {
		t.Errorf("text description on the wire =\n  %q\nwant\n  %q", got, want)
	}
	if strings.Contains(got, "**") {
		t.Errorf("the site would still show asterisks: %q", got)
	}

	// HTML a site wrote through source=bil24 before the format flag existed is
	// marked "text". The site gets its HTML back (sanitised), never escaped
	// tags: with <p> as it is, and WordPress-style (blank lines, no <p>) with
	// paragraphs like wpautop.
	setDescription(`<p>Hello <b>site</b> <a href="https://example.com/x">link</a></p><script>alert(1)</script>`, "text")
	got = catalogDescription()
	want = `<p>Hello <strong>site</strong> <a href="https://example.com/x" rel="noopener noreferrer" target="_blank">link</a></p>`
	if got != want {
		t.Errorf("site HTML marked text on the wire =\n  %q\nwant\n  %q", got, want)
	}
	if strings.Contains(got, "&lt;") {
		t.Errorf("the site would show tags as words: %q", got)
	}
	setDescription("First <strong>paragraph</strong>.\r\n\r\nSecond,\r\nsame paragraph.", "text")
	got = catalogDescription()
	want = "<p>First <strong>paragraph</strong>.</p><p>Second,<br>same paragraph.</p>"
	if got != want {
		t.Errorf("WordPress text on the wire =\n  %q\nwant\n  %q", got, want)
	}
}
